// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
)

// TestE2E_EventNumber_NoDuplicatesUnderConcurrency pins B-26.
//
// Fires N concurrent transactions that each acquire the next
// per-subscription event_number via the same UPDATE ... RETURNING
// pattern the submatcher worker uses, then INSERT a deliveries row
// stamped with that number. Asserts the resulting deliveries set is
// monotonic from 1..N with no gaps and no duplicates.
//
// Pre-fix this would either drop a delivery (ON CONFLICT DO UPDATE
// hides the duplicate) or pin every worker on the same MAX(...)+1
// value; the row-level lock on subscriptions.next_event_number is what
// serializes them.
func TestE2E_EventNumber_NoDuplicatesUnderConcurrency(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-eventnum-race")
	const N = 50

	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			tx, err := h.DB.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			var n int64
			if err := tx.QueryRow(ctx, `
				UPDATE subscriptions
				   SET next_event_number = next_event_number + 1,
				       updated_at = now()
				 WHERE id = $1
				 RETURNING next_event_number`, subID,
			).Scan(&n); err != nil {
				errs <- err
				return
			}
			ehrID := uuid.New()
			if _, err := tx.Exec(ctx, `
				INSERT INTO deliveries
					(subscription_id, ehr_event_id, event_number, status,
					 attempts, next_attempt_at, correlation_id)
				VALUES ($1, $2, $3, 'pending', 0, now(), $4)`,
				subID, ehrID, n, uuid.New(),
			); err != nil {
				errs <- err
				return
			}
			if err := tx.Commit(ctx); err != nil {
				errs <- err
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("worker err: %v", e)
	}

	rows, err := h.DB.Query(ctx,
		`SELECT event_number FROM deliveries WHERE subscription_id=$1 ORDER BY event_number`, subID,
	)
	if err != nil {
		t.Fatalf("select deliveries: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if len(got) != N {
		t.Fatalf("expected %d deliveries, got %d", N, len(got))
	}
	for i, n := range got {
		want := int64(i + 1)
		if n != want {
			t.Fatalf("event_number[%d] = %d, want %d (gap or duplicate)", i, n, want)
		}
	}
}

// TestE2E_EventNumber_ContinuesAfterRetention pins B-27.
//
// 5 deliveries are written, retention then deletes some of them, then 5
// more are written. The new event_numbers must continue from 6 — they
// must NOT reuse low numbers that subscribers have already replayed.
func TestE2E_EventNumber_ContinuesAfterRetention(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	subID := h.mustRegisterSubscription(t, ctx, "client-eventnum-after-retention")

	// Phase 1: write 5 deliveries.
	for i := 0; i < 5; i++ {
		h.advanceAndInsert(t, ctx, subID)
	}

	// Simulate retention deleting old deliveries. The retention sweeper
	// is excluded from running deliberately to keep this test
	// deterministic; we just DELETE directly.
	if _, err := h.DB.Exec(ctx,
		`DELETE FROM deliveries WHERE subscription_id=$1 AND event_number IN (3, 4, 5)`,
		subID,
	); err != nil {
		t.Fatalf("simulate retention: %v", err)
	}

	// Phase 2: write 5 more deliveries.
	for i := 0; i < 5; i++ {
		h.advanceAndInsert(t, ctx, subID)
	}

	// The cursor on subscriptions must now be 10. event_numbers 6..10
	// must exist on deliveries even though the MAX(deliveries) before
	// phase 2 dropped to 2.
	var cursor int64
	if err := h.DB.QueryRow(ctx,
		`SELECT next_event_number FROM subscriptions WHERE id=$1`, subID,
	).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 10 {
		t.Fatalf("subscriptions.next_event_number = %d, want 10", cursor)
	}

	rows, err := h.DB.Query(ctx,
		`SELECT event_number FROM deliveries WHERE subscription_id=$1 ORDER BY event_number`,
		subID,
	)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	want := []int64{1, 2, 6, 7, 8, 9, 10}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// mustRegisterSubscription creates a fresh client+subscription via the
// real /Subscription HTTP API (story #150 — no SQL bypass) and returns
// the subscription id. Fails the test on error. Each call stands up a
// throwaway harness API server so the helper does not require a
// long-lived prod binary.
func (h *Harness) mustRegisterSubscription(t *testing.T, ctx context.Context, clientID string) uuid.UUID {
	t.Helper()
	uniqueClient := clientID + "-" + uuid.NewString()[:8]
	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: uniqueClient,
	})
	if err != nil {
		t.Fatalf("start api: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })
	id, err := RegisterSubscriber(ctx, h, RegisterSubscriberOptions{
		ClientID:    uniqueClient,
		TopicURL:    "http://example.org/topics/hl7-passthrough",
		ChannelType: "rest-hook",
		Endpoint:    "https://sub.example.org/notif",
		APIBaseURL:  api.URL,
	})
	if err != nil {
		t.Fatalf("register subscriber: %v", err)
	}
	return uuid.MustParse(id)
}

// advanceAndInsert advances next_event_number once and writes a
// matching deliveries row. Mirrors what the submatcher does at fanout
// time. Used to drive deterministic test sequences.
func (h *Harness) advanceAndInsert(t *testing.T, ctx context.Context, subID uuid.UUID) int64 {
	t.Helper()
	tx, err := h.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var n int64
	if err := tx.QueryRow(ctx, `
		UPDATE subscriptions
		   SET next_event_number = next_event_number + 1,
		       updated_at = now()
		 WHERE id = $1
		 RETURNING next_event_number`, subID).Scan(&n); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deliveries
			(subscription_id, ehr_event_id, event_number, status,
			 attempts, next_attempt_at, correlation_id)
		VALUES ($1, $2, $3, 'pending', 0, now(), $4)`,
		subID, uuid.New(), n, uuid.New(),
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Best-effort space the inserts so timestamps differ; not required
	// for correctness.
	_ = time.Millisecond
	return n
}
