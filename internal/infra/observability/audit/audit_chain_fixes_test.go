// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestCanonicalInput_OnDiskFieldsRecomputeIdenticalBytes asserts the
// invariant from story #107: the bytes hashed into chain_hash MUST equal
// the bytes produced when an external auditor recomputes the canonical
// input from the on-disk row. If application-hashed timestamp differs
// from on-disk timestamp (or any other field), chain verification is
// structurally impossible.
//
// Today the writer hashes evt.OccurredAt with RFC3339Nano formatting and
// also hashes evt.CorrelationID.String(); a verifier that recomputes the
// bytes from a stored Row and feeds them through the same canonicalizer
// should reproduce row.ChainInput byte-for-byte.
func TestCanonicalInput_OnDiskFieldsRecomputeIdenticalBytes(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 6, 19, 10, 11, 12, 13_000_000, time.UTC)),
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	const N = 10
	for i := 0; i < N; i++ {
		evt := audit.Event{
			OccurredAt:    time.Date(2026, 6, 19, 10, 11, 12, 13_000_000, time.UTC).Add(time.Duration(i) * time.Second),
			ActorKind:     "system",
			ActorID:       "test",
			Action:        "subscription.create",
			TargetKind:    "Subscription",
			TargetID:      fmt.Sprintf("sub-%d", i),
			Outcome:       "success",
			CorrelationID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
			Payload:       map[string]any{"i": i},
		}
		if err := w.Emit(context.Background(), evt); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	rows := store.snapshot()
	if len(rows) != N {
		t.Fatalf("rows: want %d got %d", N, len(rows))
	}
	for i, row := range rows {
		recomputed, err := recomputeCanonicalInputForTest(row)
		if err != nil {
			t.Fatalf("row %d: recompute: %v", i, err)
		}
		if !bytes.Equal(recomputed, row.ChainInput) {
			t.Errorf("row %d: recomputed canonical bytes do not match on-disk ChainInput\n  on-disk:    %s\n  recomputed: %s",
				i, string(row.ChainInput), string(recomputed))
		}
		// And the chain_hash must equal SHA-256 of the on-disk chain_input.
		sum := sha256.Sum256(row.ChainInput)
		if !bytes.Equal(sum[:], row.ChainHash) {
			t.Errorf("row %d: SHA-256(on-disk ChainInput) != ChainHash", i)
		}
	}
}

// TestCanonicalInput_PriorHashHashedAsRawBytes asserts story #108's
// "prior_hash hashed as hex string" finding. The LLD spec says the chain
// is over the prior row's raw hash bytes; current code stringifies via
// fmt.Sprintf("%x", prior) and hashes the hex form, defeating any
// external verifier that follows the spec.
//
// The test computes the genesis hash, drives ONE emit, then asserts the
// canonical bytes encode the prior hash either as raw bytes (e.g.
// base64) or as a typed binary marker — but specifically NOT as the
// 64-character lowercase-hex literal that today's code produces.
func TestCanonicalInput_PriorHashHashedAsRawBytes(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Emit(context.Background(), audit.Event{
		OccurredAt: time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC),
		ActorKind:  "system",
		Action:     "x",
		Outcome:    "success",
		Payload:    map[string]any{"i": 1},
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("rows: %d", len(rows))
	}
	row := rows[0]
	genesis := audit.GenesisHash()
	hexForm := fmt.Sprintf("%x", genesis)
	if bytes.Contains(row.ChainInput, []byte(hexForm)) {
		t.Errorf("ChainInput contains hex-encoded prior_hash %q; spec requires raw-bytes hashing of prior, not hex stringification.\nChainInput=%s", hexForm, string(row.ChainInput))
	}
}

// TestVerifyChainReport_DoesNotReanchor asserts story #108's
// re-anchor finding. After a mid-chain corruption, the verifier MUST NOT
// silently re-align with the on-disk chain_hash; downstream rows are
// still chained to a bad prior and must surface as breaks too.
//
// Layout: 10 rows, corrupt row 5's payload. We expect at minimum:
//   - row 5: chain_hash break (recomputed != stored)
//   - rows 6..9: prior_hash break (their stored prior_hash is row N-1's
//     stored chain_hash, but the verifier walks with its own
//     application-computed prior — which diverges starting at row 6).
//
// Concretely: at least 5 breaks (the 6 affected rows minus the
// possibility that a chain_hash break also subsumes the prior_hash
// check on the same row).
func TestVerifyChainReport_DoesNotReanchor(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(base),
	})
	const N = 10
	for i := 0; i < N; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: base.Add(time.Duration(i) * time.Second),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}

	// Corrupt row 5's payload so its stored chain_hash no longer
	// matches the recomputed value.
	const tamperedRow = 5
	store.mu.Lock()
	store.rows[tamperedRow].Payload["i"] = 999
	store.mu.Unlock()

	res, err := audit.VerifyChainReport(context.Background(), store, audit.VerifyOptions{})
	if err != nil {
		t.Fatalf("VerifyChainReport: %v", err)
	}

	// Every row from tamperedRow onward must surface as a break (either
	// the corrupted row itself flags chain_hash, or downstream rows
	// flag prior_hash because the verifier's application-computed
	// prior diverges from each row's on-disk stored prior_hash).
	wantMinBreaks := N - tamperedRow // 5
	if len(res.Breaks) < wantMinBreaks {
		t.Fatalf("verifier silently re-anchored: got %d breaks, want at least %d (rows %d..%d)\nbreaks=%#v",
			len(res.Breaks), wantMinBreaks, tamperedRow, N-1, res.Breaks)
	}

	// Make sure the corrupted row itself is reported.
	sawTampered := false
	for _, b := range res.Breaks {
		if b.RowIndex == tamperedRow {
			sawTampered = true
			break
		}
	}
	if !sawTampered {
		t.Errorf("expected a break at the corrupted row %d; got %#v", tamperedRow, res.Breaks)
	}

	// All breaks AFTER the corrupted row should be reported as
	// prior_hash breaks (downstream rows are correctly chained to a
	// bad prior, so the application-computed prior won't match).
	cascadeFound := false
	for _, b := range res.Breaks {
		if b.RowIndex > tamperedRow && b.Kind == "prior_hash" {
			cascadeFound = true
			break
		}
	}
	if !cascadeFound {
		t.Errorf("expected at least one downstream prior_hash break (cascade); got %#v", res.Breaks)
	}
}

// recomputeCanonicalInputForTest mirrors the canonicalChainInput rules
// using only the on-disk Row fields plus the prior hash. It is a
// stand-in for the third-party verifier reference implementation
// required by story #108 AC #3.
//
// Once the implementation is GREEN, the bytes produced here for each
// row MUST equal row.ChainInput exactly. The function is intentionally
// independent of the production canonicalChainInput so the test would
// fail if the production code drifts.
func recomputeCanonicalInputForTest(row audit.Row) ([]byte, error) {
	obj := map[string]any{
		"ts":          row.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor_kind":  row.ActorKind,
		"actor_id":    row.ActorID,
		"action":      row.Action,
		"target_kind": row.TargetKind,
		"target_id":   row.TargetID,
		"outcome":     row.Outcome,
		// correlation_id is captured as the on-disk UUID's canonical
		// string form. If the writer substituted a random UUID
		// server-side and the on-disk row still holds that random
		// UUID, this still re-canonicalizes deterministically.
		"correlation_id": row.CorrelationID.String(),
		"payload":        row.Payload,
		// prior_hash MUST be encoded the way the spec calls for. We
		// encode raw bytes via base64 (RFC 4648 stdlib) — once Phase B
		// chooses raw-bytes encoding, this test pins the format.
		"prior_hash": row.PriorHash, // placeholder — Phase B picks the encoding
	}
	// JCS-canonicalize: sort keys, no whitespace.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		// Re-canonicalize the value through the audit package's exported
		// canonicalizer so encoding rules match production.
		raw, _ := json.Marshal(obj[k])
		canon, err := audit.CanonicalizeJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("canonicalize %s: %w", k, err)
		}
		buf.Write(canon)
	}
	buf.WriteByte('}')
	return []byte(buf.String()), nil
}
