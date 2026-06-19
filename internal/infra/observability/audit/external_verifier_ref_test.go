// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestExternalVerifierRef_CleanSyntheticChain pins story #108 AC #3:
// the third-party verifier reference implementation must accept a
// synthetic chain produced by the production writer. CI runs this on
// every build so the reference impl's hashing rules cannot drift from
// the production code without being noticed.
func TestExternalVerifierRef_CleanSyntheticChain(t *testing.T) {
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
	const N = 10
	for i := 0; i < N; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Date(2026, 6, 19, 10, 0, i, 0, time.UTC),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	rows := make([]audit.ExternalVerifierRow, 0, N)
	for _, r := range store.snapshot() {
		rows = append(rows, audit.ExternalVerifierRow{
			OccurredAt:    r.OccurredAt,
			ActorKind:     r.ActorKind,
			ActorID:       r.ActorID,
			Action:        r.Action,
			TargetKind:    r.TargetKind,
			TargetID:      r.TargetID,
			Outcome:       r.Outcome,
			CorrelationID: r.CorrelationID,
			Payload:       r.Payload,
			PriorHash:     r.PriorHash,
			ChainHash:     r.ChainHash,
		})
	}
	firstBad, breaks := audit.VerifyChainExternal(rows, "")
	if firstBad != -1 {
		t.Errorf("clean chain reported first-bad-idx=%d (want -1); breaks=%d", firstBad, breaks)
	}
	if breaks != 0 {
		t.Errorf("clean chain reported %d breaks (want 0)", breaks)
	}
}

// TestExternalVerifierRef_TamperedRowCascades pins story #108 AC #4:
// corrupt row 5 of a 10-row chain; the external verifier must surface
// row 5 as the first break AND include rows 6..9 in the break count
// (downstream rows are correctly chained to a bad prior).
func TestExternalVerifierRef_TamperedRowCascades(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)),
	})
	const N = 10
	for i := 0; i < N; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Date(2026, 6, 19, 10, 0, i, 0, time.UTC),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	const tampered = 5
	store.mu.Lock()
	store.rows[tampered].Payload["i"] = 999
	store.mu.Unlock()

	rows := make([]audit.ExternalVerifierRow, 0, N)
	for _, r := range store.snapshot() {
		rows = append(rows, audit.ExternalVerifierRow{
			OccurredAt:    r.OccurredAt,
			ActorKind:     r.ActorKind,
			ActorID:       r.ActorID,
			Action:        r.Action,
			TargetKind:    r.TargetKind,
			TargetID:      r.TargetID,
			Outcome:       r.Outcome,
			CorrelationID: r.CorrelationID,
			Payload:       r.Payload,
			PriorHash:     r.PriorHash,
			ChainHash:     r.ChainHash,
		})
	}
	firstBad, breaks := audit.VerifyChainExternal(rows, "")
	if firstBad != tampered {
		t.Errorf("first-bad-idx: want %d, got %d", tampered, firstBad)
	}
	wantMin := N - tampered // 5 rows from tampered onward
	if breaks < wantMin {
		t.Errorf("expected at least %d breaks (cascade), got %d", wantMin, breaks)
	}
}
