// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/audit"
)

// JCS canonicalization (RFC 8785, ADR 0010 #3) — sort keys, no whitespace.
func TestJCS_SortsKeys(t *testing.T) {
	t.Parallel()
	got, err := audit.CanonicalizeJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := `{"a":1,"b":2}`
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// JCS handles nested objects.
func TestJCS_NestedObjects(t *testing.T) {
	t.Parallel()
	got, err := audit.CanonicalizeJSON([]byte(`{"x":{"b":2,"a":1}}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != `{"x":{"a":1,"b":2}}` {
		t.Fatalf("got %s", got)
	}
}

// Conformance with one of the RFC 8785 vectors: integer
func TestJCS_RFCVector_NumberFormatting(t *testing.T) {
	t.Parallel()
	// RFC 8785 §3.2.2.3: trailing zeros stripped from decimal.
	got, err := audit.CanonicalizeJSON([]byte(`{"a":1.0}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("got %s", got)
	}
}

// AuditWriter writes a row through the AuditStore and computes the chain.
func TestEmit_ChainsToPriorRow(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for i := 0; i < 3; i++ {
		err := w.Emit(context.Background(), audit.Event{
			OccurredAt:    time.Now(),
			ActorKind:     "system",
			ActorID:       "test",
			Action:        "subscription.create",
			TargetKind:    "Subscription",
			TargetID:      "sub-1",
			Outcome:       "success",
			CorrelationID: uuid.New(),
			Payload:       map[string]any{"i": i},
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	rows := store.snapshot()
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	// Row 0's prior_hash is the genesis hash.
	if !bytesEqual(rows[0].PriorHash, audit.GenesisHash()) {
		t.Fatalf("row 0 prior_hash != genesis")
	}
	// Each subsequent row's prior_hash equals the prior row's chain_hash.
	for i := 1; i < len(rows); i++ {
		if !bytesEqual(rows[i].PriorHash, rows[i-1].ChainHash) {
			t.Fatalf("row %d prior_hash != row %d chain_hash", i, i-1)
		}
	}
}

// Hash chain detects mutation.
func TestVerifyChain_DetectsMutation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	for i := 0; i < 3; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Now(),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}

	// Mutate row 1 (simulates an attacker tampering with a prior row).
	store.mu.Lock()
	store.rows[1].Payload["i"] = 999
	store.mu.Unlock()


	if err := audit.VerifyChain(context.Background(), store); err == nil {
		t.Fatalf("expected verification failure")
	}
}

// Hash chain succeeds on intact rows.
func TestVerifyChain_Success(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	for i := 0; i < 5; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Now(),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	if err := audit.VerifyChain(context.Background(), store); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// Sink fan-out: stdout-default sink receives a JSON line per event.
func TestStdoutSink_WritesJSON(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	var mu sync.Mutex
	sink := audit.NewWriterSink("stdout", &mu, &sb)

	err := sink.Emit(context.Background(), audit.Event{
		OccurredAt: time.Now(),
		ActorKind:  "system",
		Action:     "test",
		Outcome:    "success",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(sb.String(), `"action":"test"`) {
		t.Fatalf("got %q", sb.String())
	}
}

// Sink emit errors are observable but do NOT unwind the durable row.
func TestEmit_DurableSucceedsEvenIfSinkFails(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	failingSink := stubSink{err: errors.New("boom")}
	failures := 0
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  failingSink,
		Clock: fixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		OnSinkFailure: func(string) {
			failures++
		},
	})
	err := w.Emit(context.Background(), audit.Event{
		OccurredAt: time.Now(),
		ActorKind:  "system",
		Action:     "x",
		Outcome:    "success",
	})
	if err != nil {
		t.Fatalf("emit returned err; durable should succeed: %v", err)
	}
	if len(store.rows) != 1 {
		t.Fatalf("expected 1 durable row")
	}
	if failures != 1 {
		t.Fatalf("expected sink failure callback")
	}
}

// Concurrent emits serialize through the AcquireLock contract: rows are
// chained correctly under concurrency.
func TestEmit_SerializesUnderConcurrency(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})

	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = w.Emit(context.Background(), audit.Event{
				OccurredAt: time.Now(),
				ActorKind:  "system",
				Action:     "x",
				Outcome:    "success",
				Payload:    map[string]any{"i": i},
			})
		}(i)
	}
	wg.Wait()

	rows := store.snapshot()
	if len(rows) != N {
		t.Fatalf("got %d rows; want %d", len(rows), N)
	}
	if err := audit.VerifyChain(context.Background(), store); err != nil {
		t.Fatalf("chain verify under concurrency: %v", err)
	}
}

// fakeStore implements audit.Store for tests. The AcquireChainLock
// returns a release that frees `chain`; while it is held, only one
// emitter mutates rows, modeling the advisory-lock contract. mu is a
// separate, finer-grained lock that keeps slice reads safe under
// concurrent IterateRows / snapshot.
type fakeStore struct {
	chain sync.Mutex
	mu    sync.Mutex
	rows  []*audit.Row
}

func newFakeStore() *fakeStore { return &fakeStore{} }

func (s *fakeStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := row
	s.rows = append(s.rows, &r)
	return nil
}

func (s *fakeStore) LastChainHash(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *fakeStore) AcquireChainLock(_ context.Context) (func() error, error) {
	s.chain.Lock()
	return func() error {
		s.chain.Unlock()
		return nil
	}, nil
}

func (s *fakeStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	s.mu.Lock()
	rows := append([]*audit.Row(nil), s.rows...)
	s.mu.Unlock()
	for _, r := range rows {
		if err := fn(*r); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeStore) snapshot() []audit.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Row, len(s.rows))
	for i, r := range s.rows {
		out[i] = *r
	}
	return out
}

type stubSink struct{ err error }

func (s stubSink) Emit(context.Context, audit.Event) error { return s.err }

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
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

// Sanity: the genesis hash is exactly SHA-256("fhir-subscriptions-foss audit chain genesis").
func TestGenesisHash(t *testing.T) {
	t.Parallel()
	want := sha256.Sum256([]byte("fhir-subscriptions-foss audit chain genesis"))
	got := audit.GenesisHash()
	if hex.EncodeToString(got) != hex.EncodeToString(want[:]) {
		t.Fatalf("genesis hash mismatch: %x vs %x", got, want[:])
	}
}
