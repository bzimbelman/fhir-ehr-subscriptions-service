// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// N-1: Emit takes a defensive copy of evt.Payload so a caller mutating
// the map after Emit returns cannot corrupt the persisted row.
func TestN1_EmitDefensiveCopiesPayload(t *testing.T) {
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

	caller := map[string]any{"k": "original"}
	if err := w.Emit(context.Background(), audit.Event{
		ActorKind: "system",
		Action:    "test.action",
		Outcome:   "success",
		Payload:   caller,
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	caller["k"] = "MUTATED"

	rows := store.snapshot()
	if len(rows) != 1 {
		t.Fatalf("got %d rows; want 1", len(rows))
	}
	if got := rows[0].Payload["k"]; got != "original" {
		t.Fatalf("post-Emit caller mutation leaked into row: got %v want \"original\"", got)
	}
}

// N-1: AuditChainAdvisoryLockID is documented and stable.
func TestN1_AuditChainAdvisoryLockIDIsDocumented(t *testing.T) {
	t.Parallel()
	// FNV-1a("audit_chain_serial") truncated to int64 — the value the
	// pre-N-1 NewPgStore computed at runtime. Keeping the constant
	// pinned here so a future careless change is caught at test time.
	const want int64 = -1971033306967946433
	if got := audit.AuditChainAdvisoryLockID; got != want {
		t.Fatalf("AuditChainAdvisoryLockID changed: got %d want %d", got, want)
	}
}
