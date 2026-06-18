// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

func TestDeliveriesInsert(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	subID := uuid.New()
	eventID := uuid.New()
	corr := uuid.New()

	pool.ExpectBegin()
	pool.ExpectQuery("INSERT INTO deliveries").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(id))
	pool.ExpectCommit()

	tx, _ := pool.Begin(ctx)
	repo := repos.NewDeliveriesRepo()
	got, err := repo.Insert(ctx, tx, repos.DeliveryRow{
		SubscriptionID: subID,
		EhrEventID:     eventID,
		EventNumber:    1,
		Status:         repos.DeliveryPending,
		NextAttemptAt:  time.Now(),
		CorrelationID:  corr,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got != id {
		t.Fatalf("expected %v got %v", id, got)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDeliveriesClaimPending(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	subID := uuid.New()
	eventID := uuid.New()
	corr := uuid.New()
	now := time.Now()

	pool.ExpectBegin()
	pool.ExpectQuery("FOR UPDATE SKIP LOCKED").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "subscription_id", "ehr_event_id", "event_number",
			"status", "attempts", "next_attempt_at", "last_error",
			"key_version", "correlation_id", "created_at", "updated_at",
		}).AddRow(id, subID, eventID, int64(1), "pending", int32(0), now, "",
			int32(1), corr, now, now))
	pool.ExpectExec("UPDATE deliveries").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	tx, _ := pool.Begin(ctx)
	repo := repos.NewDeliveriesRepo()
	rows, err := repo.ClaimPending(ctx, tx, 50, now)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].ID != id {
		t.Errorf("expected id %v got %v", id, rows[0].ID)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
