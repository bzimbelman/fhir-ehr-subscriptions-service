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

// TestE2E_Scheduler_RunDrainsThenReturns pins the shutdown-grace half
// of B-31. Run must return promptly when ctx is canceled; in-flight
// dispatches get up to ShutdownGrace to commit. Without anything in
// flight the loop returns immediately, but the recovery sweep tick
// must also exit cleanly without leaking goroutines.
func TestE2E_Scheduler_RunDrainsThenReturns(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)

	w := scheduler.NewWorker(
		h.DB, nil, nil, nil, nil, nil, nil,
		scheduler.Config{
			ClaimBatchSize:   1,
			IdlePollInterval: 50 * time.Millisecond,
			ShutdownGrace:    500 * time.Millisecond,
			RecoveryInterval: 100 * time.Millisecond,
			StuckThreshold:   time.Hour,
		},
		scheduler.Options{},
	)

	// We can't usefully run tickOnce against a worker missing repos, so
	// drive Run only long enough to confirm it starts and exits cleanly
	// when ctx is canceled. The objective is the shutdown contract.
	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Let the recovery sweep tick at least once.
	time.Sleep(250 * time.Millisecond)
	runCancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}

// TestE2E_Scheduler_RecoverySweepRunsPeriodically combines the Run
// shutdown contract with the periodic recovery sweep — a stuck row
// inserted after Run starts should be reclaimed within one
// RecoveryInterval, then Run returns when ctx is canceled.
func TestE2E_Scheduler_RecoverySweepRunsPeriodically(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-recovery-periodic")
	deliveryID := uuid.New()
	stuckSince := time.Now().Add(-10 * time.Minute)
	if _, err := h.DB.Exec(ctx, `
		INSERT INTO deliveries
			(id, subscription_id, ehr_event_id, event_number, status,
			 attempts, next_attempt_at, correlation_id, updated_at)
		VALUES ($1, $2, $3, 1, 'delivering', 0, now(), $4, $5)`,
		deliveryID, subID, uuid.New(), uuid.New(), stuckSince,
	); err != nil {
		t.Fatalf("insert stuck: %v", err)
	}

	w := scheduler.NewWorker(
		h.DB, nil, nil, nil, nil, nil, nil,
		scheduler.Config{
			ClaimBatchSize:   1,
			IdlePollInterval: 50 * time.Millisecond,
			ShutdownGrace:    500 * time.Millisecond,
			RecoveryInterval: 100 * time.Millisecond,
			StuckThreshold:   5 * time.Minute,
		},
		scheduler.Options{},
	)

	runCtx, runCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	// Wait long enough for at least two recovery ticks to fire.
	deadline := time.Now().Add(2 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		_ = h.DB.QueryRow(ctx, `SELECT status FROM deliveries WHERE id=$1`, deliveryID).Scan(&status)
		if status == "pending" {
			break
		}
		time.Sleep(75 * time.Millisecond)
	}
	if status != "pending" {
		t.Fatalf("recovery sweep did not flip status; got %q", status)
	}

	runCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
