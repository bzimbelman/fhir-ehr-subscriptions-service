//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestIntegrationRecoverStuck_NoDoubleIncrementUnderConcurrentSweeps
// covers OP #200. Two scheduler workers running the recovery sweep
// against the same shared Postgres MUST NOT double-increment
// deliveries.attempts. The fix uses `FOR UPDATE SKIP LOCKED` inside
// the targeting subquery so each row is claimed by exactly one
// sweep — a row that one sweep is updating is invisible to the
// other for the duration of that statement.
//
// Without the lock, both sweeps' UPDATE statements race on the same
// candidate rows; either Postgres serializes them and increments
// twice, or one fails. Either way the AC's "increment by exactly 1"
// invariant breaks.
func TestIntegrationRecoverStuck_NoDoubleIncrementUnderConcurrentSweeps(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Seed a delivery row, advance it to `delivering`, and back-date
	// updated_at so it is past the StuckThreshold.
	_, delID, _ := seedFanout(t, s, "rest-hook-fake")
	if _, err := s.Pool().Pgx().Exec(ctx, `
		UPDATE deliveries
		   SET status = 'delivering', updated_at = now() - interval '1 hour'
		 WHERE id = $1
	`, delID); err != nil {
		t.Fatalf("seed delivering+old: %v", err)
	}

	// Two workers, both pointed at the same Postgres pool, with a
	// short StuckThreshold so the seeded back-dated row is "stuck".
	reg := scheduler.NewMapRegistry()
	mkWorker := func() *scheduler.Worker {
		return newScheduler(t, s, reg, scheduler.RetryConfig{MaxAttempts: 5})
	}
	w1 := mkWorker()
	w2 := mkWorker()

	// The default StuckThreshold (5m) is fine — we backdated by 1h.
	var wg sync.WaitGroup
	var results [2]int64
	wg.Add(2)
	start := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		results[0] = w1.RecoverStuckForTest(ctx)
	}()
	go func() {
		defer wg.Done()
		<-start
		results[1] = w2.RecoverStuckForTest(ctx)
	}()
	close(start)
	wg.Wait()

	// Exactly one sweep MUST report it reset 1 row; the other MUST
	// report 0. With FOR UPDATE SKIP LOCKED, the row is claimed by
	// the first arrival and invisible to the second for the duration
	// of the statement.
	total := results[0] + results[1]
	if total != 1 {
		t.Errorf("recoverStuck reset count = %d (workers reported %d + %d); want exactly 1", total, results[0], results[1])
	}

	// Re-read and assert attempts incremented by exactly 1.
	dlv, err := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if err != nil || dlv == nil {
		t.Fatalf("get delivery: %v", err)
	}
	if dlv.Status != repos.DeliveryPending {
		t.Errorf("status: got %q want pending after recovery sweep", dlv.Status)
	}
	if dlv.Attempts != 1 {
		t.Errorf("attempts: got %d want 1 (OP #200 — concurrent recovery sweeps must increment by exactly 1)", dlv.Attempts)
	}
}

// TestIntegrationRecoverStuck_RepeatedSweepsAreIdempotent confirms
// that once a stuck row has been reclaimed (status flipped back to
// pending), a subsequent recovery sweep does not re-touch it. The
// existing predicate `status = 'delivering'` already enforces this;
// the test pins the invariant against future regressions.
func TestIntegrationRecoverStuck_RepeatedSweepsAreIdempotent(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	_, delID, _ := seedFanout(t, s, "rest-hook-fake")
	if _, err := s.Pool().Pgx().Exec(ctx, `
		UPDATE deliveries SET status = 'delivering', updated_at = now() - interval '1 hour'
		 WHERE id = $1
	`, delID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := scheduler.NewMapRegistry()
	w := newScheduler(t, s, reg, scheduler.RetryConfig{MaxAttempts: 5})

	if got := w.RecoverStuckForTest(ctx); got != 1 {
		t.Fatalf("first sweep reset = %d; want 1", got)
	}
	// Second sweep finds the row already pending and reports 0.
	if got := w.RecoverStuckForTest(ctx); got != 0 {
		t.Errorf("second sweep reset = %d; want 0 (row is no longer 'delivering')", got)
	}

	dlv, err := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if err != nil || dlv == nil {
		t.Fatalf("get delivery: %v", err)
	}
	if dlv.Attempts != 1 {
		t.Errorf("attempts after 2 sweeps: got %d want 1", dlv.Attempts)
	}
	// Avoid unused "time" import when this is the only function.
	_ = time.Now
}
