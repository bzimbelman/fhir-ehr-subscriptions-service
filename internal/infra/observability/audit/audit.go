// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package audit implements the append-only, hash-chained audit log per
// LLD §8 and ADR 0010 #7.
//
// The audit log has two destinations:
//
//   - The Postgres audit_log table — durable source of truth, hash-chained,
//     reached through the Store interface. The Store implementation
//     serializes appenders through a Postgres advisory lock (see the
//     pgStore in this package); fakes in tests serialize through a
//     sync.Mutex.
//
//   - A configurable real-time sink (stdout default, file/syslog/otlp
//     supported per ADR 0008 #11). Sink failures do NOT unwind the
//     durable row; they are observable through the OnSinkFailure
//     callback so the metrics layer can increment
//     fhir_subs_audit_sink_failures_total{sink}.
//
// The hash chain links rows by prior_hash = SHA-256(JCS(prior_row's
// canonical input)). Genesis is the SHA-256 of the literal string
// "fhir-ehr-subscriptions-service audit chain genesis" so the chain is
// reproducible across deployments.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// genesisLiteral is the human-readable string hashed to seed the chain.
const genesisLiteral = "fhir-ehr-subscriptions-service audit chain genesis"

// GenesisHash returns the SHA-256 of the genesis literal (LLD §8.1).
func GenesisHash() []byte {
	h := sha256.Sum256([]byte(genesisLiteral))
	out := make([]byte, len(h))
	copy(out, h[:])
	return out
}

// Event is the per-action audit event. PHI fields must be redacted before
// the event reaches Emit; the audit module does not re-redact bodies.
type Event struct {
	OccurredAt    time.Time
	ActorKind     string // "subscriber" | "operator" | "system"
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string // "success" | "failure" | "denied"
	CorrelationID uuid.UUID
	Payload       map[string]any
}

// Row is the persisted audit row including the chain bookkeeping fields.
type Row struct {
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID uuid.UUID
	Payload       map[string]any
	PriorHash     []byte
	ChainInput    []byte
	ChainHash     []byte
}

// Store is the durable backing store contract.
type Store interface {
	// InsertAuditRow persists a row inside the lock acquired by AcquireChainLock.
	InsertAuditRow(ctx context.Context, row Row) error
	// LastChainHash returns the chain_hash of the most recent row, or
	// nil when the table is empty.
	LastChainHash(ctx context.Context) ([]byte, error)
	// AcquireChainLock returns a release function. The caller holds an
	// advisory lock across InsertAuditRow so concurrent appenders see a
	// linear chain.
	AcquireChainLock(ctx context.Context) (release func() error, err error)
	// IterateRows visits rows in insertion order for chain verification.
	IterateRows(ctx context.Context, fn func(Row) error) error
}

// Sink is the real-time forwarding contract.
type Sink interface {
	Emit(ctx context.Context, evt Event) error
}

// WriterOptions configures NewWriter.
type WriterOptions struct {
	Store         Store
	Sink          Sink
	Clock         func() time.Time
	OnSinkFailure func(sink string)
	// SinkName is the label passed to OnSinkFailure. Defaults to "stdout".
	SinkName string
}

// Writer is the audit-log front door.
type Writer struct {
	store         Store
	sink          Sink
	clock         func() time.Time
	onSinkFailure func(string)
	sinkName      string
}

// NewWriter constructs a Writer.
func NewWriter(opts WriterOptions) (*Writer, error) {
	if opts.Store == nil {
		return nil, errors.New("audit: store is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	sink := opts.Sink
	if sink == nil {
		sink = NewStdoutSink()
	}
	sinkName := opts.SinkName
	if sinkName == "" {
		sinkName = "stdout"
	}
	return &Writer{
		store:         opts.Store,
		sink:          sink,
		clock:         clock,
		onSinkFailure: opts.OnSinkFailure,
		sinkName:      sinkName,
	}, nil
}

// Emit appends evt to the durable store and to the configured sink. The
// durable write is the source of truth; sink failures are observable via
// OnSinkFailure but do not unwind the durable row.
//
// The durable path runs under the chain advisory lock. A `defer recover`
// wraps the lock holder so a panic in InsertAuditRow / LastChainHash /
// canonicalChainInput releases the lock before the panic propagates
// (B-34). Without this, a panic between AcquireChainLock and the manual
// release leaks the lock forever and every subsequent Emit blocks.
func (w *Writer) Emit(ctx context.Context, evt Event) (retErr error) {
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = w.clock()
	}

	release, err := w.store.AcquireChainLock(ctx)
	if err != nil {
		return fmt.Errorf("audit: acquire chain lock: %w", err)
	}
	released := false
	releaseOnce := func() {
		if released {
			return
		}
		released = true
		_ = release()
	}
	defer func() {
		if r := recover(); r != nil {
			releaseOnce()
			retErr = fmt.Errorf("audit: panic in durable write path: %v", r)
		}
	}()

	prior, err := w.store.LastChainHash(ctx)
	if err != nil {
		releaseOnce()
		return fmt.Errorf("audit: read prior chain hash: %w", err)
	}
	if prior == nil {
		prior = GenesisHash()
	}

	chainInput, err := canonicalChainInput(evt, prior)
	if err != nil {
		releaseOnce()
		return fmt.Errorf("audit: canonicalize: %w", err)
	}
	sum := sha256.Sum256(chainInput)
	chainHash := make([]byte, len(sum))
	copy(chainHash, sum[:])

	row := Row{
		OccurredAt:    evt.OccurredAt,
		ActorKind:     evt.ActorKind,
		ActorID:       evt.ActorID,
		Action:        evt.Action,
		TargetKind:    evt.TargetKind,
		TargetID:      evt.TargetID,
		Outcome:       evt.Outcome,
		CorrelationID: evt.CorrelationID,
		Payload:       evt.Payload,
		PriorHash:     prior,
		ChainInput:    chainInput,
		ChainHash:     chainHash,
	}
	if err := w.store.InsertAuditRow(ctx, row); err != nil {
		releaseOnce()
		return fmt.Errorf("audit: insert: %w", err)
	}
	if err := release(); err != nil {
		released = true
		return fmt.Errorf("audit: release lock: %w", err)
	}
	released = true

	// Sink emit is best-effort; durable row already committed.
	if err := w.sink.Emit(ctx, evt); err != nil && w.onSinkFailure != nil {
		w.onSinkFailure(w.sinkName)
	}
	return nil
}

// canonicalChainInput builds the JCS-canonical bytes that go into the
// SHA-256 hash. The shape is the public, persisted-event view augmented
// with prior_hash so a prior-row mutation breaks every subsequent hash.
func canonicalChainInput(evt Event, prior []byte) ([]byte, error) {
	obj := map[string]any{
		"ts":             evt.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_kind":     evt.ActorKind,
		"actor_id":       evt.ActorID,
		"action":         evt.Action,
		"target_kind":    evt.TargetKind,
		"target_id":      evt.TargetID,
		"outcome":        evt.Outcome,
		"correlation_id": evt.CorrelationID.String(),
		"payload":        evt.Payload,
		"prior_hash":     fmt.Sprintf("%x", prior),
	}
	enc, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(enc)
}

// VerifyChain walks rows in insertion order and verifies each row's chain
// links to the prior row's chain_hash. Returns the first mismatch.
func VerifyChain(ctx context.Context, store Store) error {
	prior := GenesisHash()
	idx := 0
	return store.IterateRows(ctx, func(row Row) error {
		if !bytesEqual(row.PriorHash, prior) {
			return fmt.Errorf("audit: chain break at row %d: prior_hash mismatch", idx)
		}
		evt := Event{
			OccurredAt:    row.OccurredAt,
			ActorKind:     row.ActorKind,
			ActorID:       row.ActorID,
			Action:        row.Action,
			TargetKind:    row.TargetKind,
			TargetID:      row.TargetID,
			Outcome:       row.Outcome,
			CorrelationID: row.CorrelationID,
			Payload:       row.Payload,
		}
		expected, err := canonicalChainInput(evt, prior)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(expected)
		if !bytesEqual(sum[:], row.ChainHash) {
			return fmt.Errorf("audit: chain break at row %d: chain_hash mismatch", idx)
		}
		prior = row.ChainHash
		idx++
		return nil
	})
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stdoutSink writes one JSON event per line to os.Stdout.
type stdoutSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutSink returns the default audit sink.
func NewStdoutSink() Sink { return &stdoutSink{w: os.Stdout} }

// NewWriterSink returns a sink that writes to w under mu. Used by tests
// (and by the file-sink wiring) to forward audit events to an arbitrary
// stream.
func NewWriterSink(_ string, mu *sync.Mutex, w io.Writer) Sink {
	return &writerSink{mu: mu, w: w}
}

type writerSink struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *writerSink) Emit(_ context.Context, evt Event) error {
	if s.mu != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	b, err := json.Marshal(map[string]any{
		"ts":             evt.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_kind":     evt.ActorKind,
		"actor_id":       evt.ActorID,
		"action":         evt.Action,
		"target_kind":    evt.TargetKind,
		"target_id":      evt.TargetID,
		"outcome":        evt.Outcome,
		"correlation_id": evt.CorrelationID.String(),
		"payload":        evt.Payload,
	})
	if err != nil {
		return err
	}
	_, err = s.w.Write(append(b, '\n'))
	return err
}

func (s *stdoutSink) Emit(_ context.Context, evt Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(map[string]any{
		"ts":             evt.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_kind":     evt.ActorKind,
		"actor_id":       evt.ActorID,
		"action":         evt.Action,
		"target_kind":    evt.TargetKind,
		"target_id":      evt.TargetID,
		"outcome":        evt.Outcome,
		"correlation_id": evt.CorrelationID.String(),
		"payload":        evt.Payload,
	})
	if err != nil {
		return err
	}
	_, err = s.w.Write(append(b, '\n'))
	return err
}

// CanonicalizeJSON returns RFC 8785 (JCS) canonical bytes of the input.
//
// Implementation: parse to a generic value with json.Decoder using
// UseNumber to preserve numeric form fidelity, then re-emit with sorted
// object keys and the JCS number formatting rule (json.Number's
// canonical form via strconv).
func CanonicalizeJSON(input []byte) ([]byte, error) {
	dec := json.NewDecoder(newReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("audit: decode: %w", err)
	}
	return canonicalEncode(v)
}

// canonicalEncode emits the JCS-canonical form of v.
func canonicalEncode(v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if t {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return jsonString(t), nil
	case json.Number:
		return canonicalNumber(t)
	case float64:
		return canonicalNumber(json.Number(fmt.Sprintf("%g", t)))
	case []any:
		return canonicalArray(t)
	case map[string]any:
		return canonicalObject(t)
	default:
		return nil, fmt.Errorf("audit: unsupported value %T", v)
	}
}

func canonicalArray(arr []any) ([]byte, error) {
	out := []byte{'['}
	for i, e := range arr {
		if i > 0 {
			out = append(out, ',')
		}
		b, err := canonicalEncode(e)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	out = append(out, ']')
	return out, nil
}

func canonicalObject(obj map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, jsonString(k)...)
		out = append(out, ':')
		b, err := canonicalEncode(obj[k])
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	out = append(out, '}')
	return out, nil
}

// jsonString serializes s as a canonical JSON string. encoding/json's
// default Marshal already escapes per RFC 8259 (a strict superset of
// what JCS requires for strings); we use it here to keep the
// implementation small and verified against the standard library.
func jsonString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// canonicalNumber renders n per RFC 8785 §3.2.2: drop a trailing ".0" so
// "1.0" -> "1", and pass through integer forms unchanged. For the audit
// chain the only numeric values are integers and bounded floats from
// payload maps; full ECMA-262 number formatting is not needed for chain
// determinism because both writer and verifier walk the same code path.
func canonicalNumber(n json.Number) ([]byte, error) {
	s := string(n)
	// Strip trailing zeros after a decimal point: "1.0" -> "1", "1.50" -> "1.5".
	if i := indexByte(s, '.'); i >= 0 {
		// Walk from end stripping zeros.
		end := len(s)
		for end > i+1 && s[end-1] == '0' {
			end--
		}
		if end > 0 && s[end-1] == '.' {
			end--
		}
		s = s[:end]
	}
	return []byte(s), nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// newReader wraps a byte slice into a tiny io.Reader; avoids importing bytes
// just for this one site (keeps the dep graph quiet).
type byteReader struct {
	b   []byte
	pos int
}

func newReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
