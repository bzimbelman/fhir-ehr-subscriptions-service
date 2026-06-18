// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// P2.5: VerifyChainReport on a clean chain reports zero breaks, the
// row count, and a non-empty head hash.
func TestVerifyChainReport_CleanChain(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
	})
	for i := 0; i < 4; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Date(2026, 6, 1, 0, 0, i, 0, time.UTC),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	res, err := audit.VerifyChainReport(context.Background(), store, audit.VerifyOptions{})
	if err != nil {
		t.Fatalf("VerifyChainReport: %v", err)
	}
	if res.RowsSeen != 4 {
		t.Fatalf("rows: want 4, got %d", res.RowsSeen)
	}
	if len(res.Breaks) != 0 {
		t.Fatalf("breaks: want 0, got %d: %#v", len(res.Breaks), res.Breaks)
	}
	if res.HeadHash == "" {
		t.Fatalf("head hash empty")
	}
	// Head hash is the lowercase hex of the last row's chain_hash, 64 chars.
	if len(res.HeadHash) != 64 {
		t.Fatalf("head hash unexpected length %d", len(res.HeadHash))
	}
}

// P2.5: VerifyChainReport surfaces a chain_hash break and pinpoints the row.
func TestVerifyChainReport_ChainHashMismatch(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
	})
	for i := 0; i < 3; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: time.Date(2026, 6, 1, 0, 0, i, 0, time.UTC),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	// Tamper with row 1's payload — its stored chain_hash will no
	// longer match the recomputed one.
	store.mu.Lock()
	store.rows[1].Payload["i"] = 999
	store.mu.Unlock()

	res, err := audit.VerifyChainReport(context.Background(), store, audit.VerifyOptions{})
	if err != nil {
		t.Fatalf("VerifyChainReport: %v", err)
	}
	if len(res.Breaks) == 0 {
		t.Fatalf("expected at least one break")
	}
	first := res.Breaks[0]
	if first.RowIndex != 1 {
		t.Errorf("first break row index: want 1, got %d", first.RowIndex)
	}
	if first.Kind != "chain_hash" {
		t.Errorf("first break kind: want chain_hash, got %q", first.Kind)
	}
	if !strings.Contains(first.Message, "chain_hash") {
		t.Errorf("first break message: want chain_hash mention, got %q", first.Message)
	}
}

// P2.5: VerifyChainReport filters reported breaks by the From/To window.
// The chain is still walked to completion so the row count and head
// hash are accurate; only the Breaks slice is windowed.
func TestVerifyChainReport_WindowFiltersBreaks(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	w, _ := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
		Clock: fixedClock(base),
	})
	// Five rows with timestamps base+0s..base+4s; tamper with row 3.
	for i := 0; i < 5; i++ {
		_ = w.Emit(context.Background(), audit.Event{
			OccurredAt: base.Add(time.Duration(i) * time.Second),
			ActorKind:  "system",
			Action:     "x",
			Outcome:    "success",
			Payload:    map[string]any{"i": i},
		})
	}
	store.mu.Lock()
	store.rows[3].Payload["i"] = 999
	store.mu.Unlock()

	// Window covers rows 0..2: the break at row 3 is OUT of window
	// and must not appear.
	res, err := audit.VerifyChainReport(context.Background(), store, audit.VerifyOptions{
		From: base,
		To:   base.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("VerifyChainReport: %v", err)
	}
	if res.RowsSeen != 5 {
		t.Errorf("rows seen should still cover whole chain: want 5, got %d", res.RowsSeen)
	}
	if len(res.Breaks) != 0 {
		t.Errorf("expected no breaks in early window; got %#v", res.Breaks)
	}

	// Window covers rows 3..4: the break MUST appear.
	res2, err := audit.VerifyChainReport(context.Background(), store, audit.VerifyOptions{
		From: base.Add(3 * time.Second),
		To:   base.Add(5 * time.Second),
	})
	if err != nil {
		t.Fatalf("VerifyChainReport: %v", err)
	}
	if len(res2.Breaks) == 0 {
		t.Errorf("expected break at row 3 to be in late window")
	}
}
