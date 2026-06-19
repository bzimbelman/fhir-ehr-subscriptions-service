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

func TestResourceChangesInsertReturnsID(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	id := uuid.New()
	corr := uuid.New()

	pool.ExpectBegin()
	pool.ExpectQuery("INSERT INTO resource_changes").
		WithArgs(anyArgs(10)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "sequence", "created_month"}).
			AddRow(id, int64(101), time.Now()))
	pool.ExpectCommit()

	tx, _ := pool.Begin(ctx)
	repo := repos.NewResourceChangesRepo(newCodec(t))
	got, _, err := repo.Insert(ctx, tx, repos.ResourceChangeRow{
		AdapterID:     "epic",
		CorrelationID: corr,
		ResourceType:  "ServiceRequest",
		ChangeKind:    repos.ChangeCreate,
		Resource:      []byte(`{"resourceType":"ServiceRequest"}`),
		OccurredAt:    time.Now(),
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

func TestResourceChangesClaimUnprocessed(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	c := newCodec(t)
	body := []byte(`{"resourceType":"Observation"}`)
	id := uuid.New()
	enc, _, _ := c.Encrypt(body, repos.AADResourceChanges(id, c.ActiveVersion(), "resource"))
	corr := uuid.New()
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	pool.ExpectBegin()
	pool.ExpectQuery("resource_changes").
		WithArgs(int32(20)).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "sequence", "adapter_id", "correlation_id", "resource_type",
			"change_kind", "resource", "previous_resource", "key_version",
			"occurred_at", "event_code", "processed", "created_month", "created_at",
		}).AddRow(id, int64(99), "default", corr, "Observation",
			"create", enc, []byte(nil), int32(1), now, "", false, month, now))
	pool.ExpectCommit()

	tx, _ := pool.Begin(ctx)
	repo := repos.NewResourceChangesRepo(c)
	rows, err := repo.ClaimUnprocessed(ctx, tx, 20)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row got %d", len(rows))
	}
	if string(rows[0].Resource) != string(body) {
		t.Errorf("resource decrypted incorrectly: got %q", rows[0].Resource)
	}
	if rows[0].ChangeKind != repos.ChangeCreate {
		t.Errorf("expected create, got %s", rows[0].ChangeKind)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
