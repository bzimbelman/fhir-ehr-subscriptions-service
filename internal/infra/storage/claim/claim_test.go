// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package claim_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v3"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/claim"
)

type fakeRow struct {
	ID   int64
	Name string
}

func decodeFake(scan claim.Scanner) (fakeRow, error) {
	var r fakeRow
	if err := scan(&r.ID, &r.Name); err != nil {
		return r, err
	}
	return r, nil
}

func TestUnprocessedReturnsRows(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectBegin()
	pool.ExpectQuery("SELECT id, name FROM widgets").
		WithArgs(int64(10)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name"}).
			AddRow(int64(1), "first").
			AddRow(int64(2), "second"))
	pool.ExpectCommit()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := claim.Unprocessed(ctx, tx, decodeFake,
		`SELECT id, name FROM widgets WHERE processed = false LIMIT $1 FOR UPDATE SKIP LOCKED`,
		int64(10))
	if err != nil {
		t.Fatalf("ClaimUnprocessed: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].ID != 1 || rows[0].Name != "first" {
		t.Errorf("unexpected row[0]: %+v", rows[0])
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestUnprocessedRejectsSQLWithoutSkipLocked(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	// No expectations set: function should fail before issuing SQL.
	pool.ExpectBegin()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, claimErr := claim.Unprocessed(ctx, tx, decodeFake,
		`SELECT id, name FROM widgets WHERE processed = false LIMIT $1`,
		int64(10))
	if claimErr == nil {
		t.Fatal("expected error for SQL without SKIP LOCKED")
	}
	if !strings.Contains(claimErr.Error(), "SKIP LOCKED") {
		t.Errorf("expected error to mention SKIP LOCKED, got %v", claimErr)
	}
}

func TestUnprocessedPropagatesScanError(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := context.Background()

	want := errors.New("decode failed")
	pool.ExpectBegin()
	pool.ExpectQuery("SELECT id, name").
		WillReturnRows(pgxmock.NewRows([]string{"id", "name"}).
			AddRow(int64(1), "x"))

	tx, _ := pool.Begin(ctx)
	bad := func(_ claim.Scanner) (fakeRow, error) {
		return fakeRow{}, want
	}
	_, err = claim.Unprocessed(ctx, tx, bad,
		`SELECT id, name FROM widgets FOR UPDATE SKIP LOCKED`)
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped %v, got %v", want, err)
	}
}
