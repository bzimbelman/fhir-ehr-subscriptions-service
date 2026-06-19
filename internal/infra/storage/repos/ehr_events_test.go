// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

func TestEhrEventsInsertReturnsID(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	corr := uuid.New()
	rcID := uuid.New()

	pool.ExpectBegin()
	// 12 args: id (app-generated for AAD #109) + client_id + 10 originals.
	pool.ExpectQuery("INSERT INTO ehr_events").
		WithArgs(anyArgs(12)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "event_number", "created_month"}).
			AddRow(id, int64(7), time.Now()))
	pool.ExpectCommit()

	tx, _ := pool.Begin(ctx)
	repo := repos.NewEhrEventsRepo(newCodec(t))
	got, _, err := repo.Insert(ctx, tx, repos.EhrEventRow{
		ClientID:         "client-test",
		TopicURL:         "http://example.org/order-changed",
		ChangeKind:       repos.ChangeUpdate,
		Focus:            "ServiceRequest/abc",
		Resource:         []byte(`{"resourceType":"ServiceRequest"}`),
		CorrelationID:    corr,
		ResourceChangeID: rcID,
		OccurredAt:       time.Now(),
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
