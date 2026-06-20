//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// OP #342 — production scheduler nil-pointer panic. The orchestrator
// e2e suite leaks a Worker constructed with dl==nil into the shared
// pgxpool: drain_test.go:29 builds a probe-only Worker with all-nil
// repos, then runs Run() against the harness DB. Run's claim loop
// successfully picks up a delivery row left over from a parallel
// prod_binary test. Inside dispatchOne the missing ehr_event causes
// requeueWithReason → applyBailoutDecision → w.dl.Insert(...) which
// panics on a nil receiver because dl was never wired.
//
// Two contracts are pinned here:
//
//  1. applyBailoutDecision is defensive against w.dl == nil. Production
//     wiring always passes a real DeadLettersRepo, but the e2e harness
//     and any future Worker that wires only the recovery sweep MUST
//     not crash a goroutine just because the dispatch-bailout path
//     happens to be reached. The deliveries row still gets MarkDead'd
//     so retention/recovery do not pin the row in 'delivering' forever.
//
//  2. Pure unit-level guard: applyBailoutDecision does not panic when
//     w.dl is nil even outside the integration test (the integration
//     half pins the contract end-to-end against a real Postgres).

package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestOP342_NilDeadLettersDoesNotPanic — pins the Phase B fix. A Worker
// constructed with dl=nil but a delivery row whose ehr_event is missing
// must NOT crash on the requeueWithReason → applyBailoutDecision →
// dl.Insert path. Pre-fix this test panics with a nil-pointer
// dereference at internal/infra/storage/repos/dead_letters.go:99.
//
// We use real repos for subs/ehr/dlv (so the dispatch path can read
// state and call MarkDead) and pass nil for dl to reproduce the e2e
// drain-test wiring shape. The delivery row points at a non-existent
// ehr_event_id, which is exactly the loadEhrEvent → (nil, nil)
// condition that triggered the original panic.
func TestOP342_NilDeadLettersDoesNotPanic(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Seed a real subscription so loadSubscription returns non-nil and
	// dispatchOne advances to loadEhrEvent. Without the auth_clients
	// row, the FK on subscriptions.client_id rejects the insert.
	seedAuthClient(t, s, "client-OP342")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-OP342",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/op342",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/op342",
		Content:     "id-only",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}

	// Insert a delivery row whose ehr_event_id refers to a row that
	// does NOT exist. loadEhrEvent will return (nil, nil) — the
	// "permanent / not-found" branch — which routes through
	// requeueWithReason → applyBailoutDecision. With MaxAttempts=1 the
	// classifier escalates straight to ActionDeadLetter so the
	// dl.Insert call site fires.
	corr := uuid.New()
	missingEhrID := uuid.New()
	delID, err := s.Deliveries().Insert(ctx, s.Pool().Pgx(), repos.DeliveryRow{
		SubscriptionID: subID,
		EhrEventID:     missingEhrID,
		EventNumber:    1,
		Status:         repos.DeliveryPending,
		Attempts:       0,
		NextAttemptAt:  time.Now().UTC().Add(-time.Second),
		CorrelationID:  corr,
	})
	if err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	// Construct the Worker with dl=nil — the bug. registry/builder are
	// real but unused on this path because dispatch bails on the
	// missing ehr_event before ever reaching the channel.
	w := scheduler.NewWorker(
		s.Pool().Pgx(),
		s.Subscriptions(), s.EhrEvents(), s.Deliveries(),
		nil, // <-- the e2e drain-test wiring shape; dl deliberately nil
		scheduler.NewMapRegistry(),
		builder.New(builder.Config{}),
		scheduler.Config{
			ClaimBatchSize: 16,
			Retry:          scheduler.RetryConfig{MaxAttempts: 1},
		},
		scheduler.Options{RNG: scheduler.DeterministicRNG(1)},
	)

	// Pre-fix: TickOnce panics inside the dispatch goroutine.
	// Post-fix: TickOnce returns processed=true and the deliveries
	// row reaches the 'dead' terminal status without dl.Insert being
	// called.
	processed, terr := w.TickOnce(ctx)
	if terr != nil {
		t.Fatalf("TickOnce: %v", terr)
	}
	if !processed {
		t.Fatal("expected processed=true (one delivery row was claimed)")
	}

	// Confirm the row reached terminal status. Without the fix the
	// goroutine panics before MarkDead commits, so the row would be
	// pinned in 'delivering'. With the fix the deliveries.MarkDead
	// commit lands in the same tx that would have written dl.Insert,
	// so the row IS dead.
	dlv, derr := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if derr != nil || dlv == nil {
		t.Fatalf("get delivery after tick: %v", derr)
	}
	if dlv.Status != repos.DeliveryDead {
		t.Errorf("status: got %q want %q", dlv.Status, repos.DeliveryDead)
	}
}
