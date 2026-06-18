// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
)

// TestE2E_Scheduler_RecoverySweepResetsStuckRows pins B-31's recovery
// sweep behavior. A delivery row marked status='delivering' with an
// updated_at older than StuckThreshold is reclaimed back to 'pending'
// when the sweep ticks. Pre-fix such rows would be pinned forever
// because no code path reset them.
func TestE2E_Scheduler_RecoverySweepResetsStuckRows(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-recovery-sweep")

	deliveryID := uuid.New()
	stuckSince := time.Now().Add(-10 * time.Minute)
	if _, err := h.DB.Exec(ctx, `
		INSERT INTO deliveries
			(id, subscription_id, ehr_event_id, event_number, status,
			 attempts, next_attempt_at, correlation_id, updated_at)
		VALUES ($1, $2, $3, 1, 'delivering', 0, now(), $4, $5)`,
		deliveryID, subID, uuid.New(), uuid.New(), stuckSince,
	); err != nil {
		t.Fatalf("insert stuck row: %v", err)
	}

	w := scheduler.NewWorker(
		h.DB,
		nil, nil, nil, nil, nil, nil,
		scheduler.Config{
			StuckThreshold:   5 * time.Minute,
			RecoveryInterval: time.Hour,
		},
		scheduler.Options{},
	)
	w.RecoverStuckForTest(ctx)

	var status string
	var attempts int32
	if err := h.DB.QueryRow(ctx,
		`SELECT status, attempts FROM deliveries WHERE id=$1`, deliveryID,
	).Scan(&status, &attempts); err != nil {
		t.Fatalf("read after sweep: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want 'pending' after recovery sweep", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 after recovery sweep", attempts)
	}
}

// TestE2E_Scheduler_RecoverySweepLeavesFreshDeliveringAlone pins the
// negative case of B-31: a 'delivering' row whose updated_at is more
// recent than StuckThreshold must NOT be reset. Otherwise the sweep
// would race with a healthy worker mid-dispatch.
func TestE2E_Scheduler_RecoverySweepLeavesFreshDeliveringAlone(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-recovery-sweep-fresh")

	deliveryID := uuid.New()
	if _, err := h.DB.Exec(ctx, `
		INSERT INTO deliveries
			(id, subscription_id, ehr_event_id, event_number, status,
			 attempts, next_attempt_at, correlation_id, updated_at)
		VALUES ($1, $2, $3, 1, 'delivering', 0, now(), $4, now())`,
		deliveryID, subID, uuid.New(), uuid.New(),
	); err != nil {
		t.Fatalf("insert fresh row: %v", err)
	}

	w := scheduler.NewWorker(
		h.DB, nil, nil, nil, nil, nil, nil,
		scheduler.Config{
			StuckThreshold:   5 * time.Minute,
			RecoveryInterval: time.Hour,
		},
		scheduler.Options{},
	)
	w.RecoverStuckForTest(ctx)

	var status string
	if err := h.DB.QueryRow(ctx,
		`SELECT status FROM deliveries WHERE id=$1`, deliveryID,
	).Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "delivering" {
		t.Fatalf("status = %q, want 'delivering' (fresh row, sweep should not touch)", status)
	}
}
