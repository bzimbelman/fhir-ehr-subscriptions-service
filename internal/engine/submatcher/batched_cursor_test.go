// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestFanoutBatchesCursorAdvance asserts story #56's contract: regardless
// of how many candidate subscriptions a topic has, the per-tx
// `events_since_subscription_start` advance is issued as exactly ONE
// batched UPDATE keyed by `unnest($1::uuid[], $2::bigint[])`.
//
// Pre-#56 (the streaming fanout from #55) issued one UPDATE per Match,
// so an N-subscriber topic paid N UPDATE round-trips inside the fanout
// transaction. This test fails on that path because pgxmock would see
// N inline UPDATE statements where it expects one batched form.
//
// We program pgxmock with the per-match nextEventNumber+INSERT pair only
// (no inline UPDATE per match) and a single tail UPDATE that contains
// the literal `unnest(` token. Any per-row UPDATE attempt against
// `subscriptions ... events_since_subscription_start` will trip the
// "unexpected query" failure.
func TestFanoutBatchesCursorAdvance(t *testing.T) {
	t.Parallel()

	const N = 64
	rows := makeRows(N, "http://example.org/batched-cursor")
	lister := &streamFakeLister{rows: rows}

	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()

	mockPool.ExpectBegin()
	// Per-match: nextEventNumber UPDATE..RETURNING, then INSERT delivery.
	// NO per-match UPDATE of events_since_subscription_start.
	for i := 0; i < N; i++ {
		mockPool.ExpectQuery(`UPDATE subscriptions\s+SET next_event_number`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"next_event_number"}).AddRow(int64(i + 1)))
		mockPool.ExpectQuery(`INSERT INTO deliveries`).
			WithArgs(anyArgsN(8)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	}
	// Single batched cursor advance after streaming completes.
	mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start.*unnest\(\$1::uuid\[\], \$2::bigint\[\]\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 64"))
	// MarkProcessed.
	mockPool.ExpectExec(`UPDATE ehr_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mockPool.ExpectCommit()

	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	metrics := newStubMetrics()
	w := newWorkerWithLister(t, lister, metrics)

	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		EventNumber:   42,
		TopicURL:      "http://example.org/batched-cursor",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation","id":"o1"}`),
		CorrelationID: uuid.New(),
		OccurredAt:    time.Unix(1700000000, 0).UTC(),
		CreatedMonth:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := w.fanoutOne(context.Background(), tx, row); err != nil {
		t.Fatalf("fanoutOne: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if metrics.outcomes[FanoutMatch] != N {
		t.Fatalf("Match metric: got %d, want %d", metrics.outcomes[FanoutMatch], N)
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock expectations: %v", err)
	}
}

// TestFanoutBatchedCursorRespectsBatchCap asserts the BatchSize cap:
// when N exceeds the configured cap, the worker emits ceil(N/cap)
// batched UPDATE statements rather than one giant one. This guards
// against `bind message has too many parameters` (Postgres caps each
// extended-protocol bind at 65535 parameters; with two arrays of
// scalars per row the practical safe cap is well below that, but we
// want a configurable knob with a sane default).
func TestFanoutBatchedCursorRespectsBatchCap(t *testing.T) {
	t.Parallel()

	const N = 250
	const cap = 100 // -> 3 batches: 100, 100, 50
	rows := makeRows(N, "http://example.org/batched-cap")
	lister := &streamFakeLister{rows: rows}

	mockPool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mockPool.Close()

	mockPool.ExpectBegin()
	for i := 0; i < N; i++ {
		mockPool.ExpectQuery(`UPDATE subscriptions\s+SET next_event_number`).
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"next_event_number"}).AddRow(int64(i + 1)))
		mockPool.ExpectQuery(`INSERT INTO deliveries`).
			WithArgs(anyArgsN(8)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		// As each batch fills, expect one batched UPDATE. We'll add
		// expectations for two full batches (i==99, i==199) and one
		// trailing flush below.
		if i == cap-1 || i == 2*cap-1 {
			mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start.*unnest`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgconn.NewCommandTag("UPDATE 100"))
		}
	}
	// Trailing flush for the remaining 50.
	mockPool.ExpectExec(`UPDATE subscriptions\s+SET events_since_subscription_start.*unnest`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 50"))
	mockPool.ExpectExec(`UPDATE ehr_events`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))
	mockPool.ExpectCommit()

	tx, err := mockPool.BeginTx(context.Background(), pgx.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	metrics := newStubMetrics()
	w := newWorkerWithLister(t, lister, metrics)
	w.cfg.CursorAdvanceBatchSize = cap

	row := &repos.EhrEventRow{
		ID:            uuid.New(),
		TopicURL:      "http://example.org/batched-cap",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"Observation"}`),
		CorrelationID: uuid.New(),
	}
	if err := w.fanoutOne(context.Background(), tx, row); err != nil {
		t.Fatalf("fanoutOne: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := mockPool.ExpectationsWereMet(); err != nil {
		t.Fatalf("pgxmock expectations: %v", err)
	}
}
