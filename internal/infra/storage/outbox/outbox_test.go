// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pashagolub/pgxmock/v3"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/outbox"
)

func TestRunOutboxCommitsOnSuccess(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("mock pool: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectBegin()
	pool.ExpectExec("INSERT INTO test_table").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	out, err := outbox.RunPool(ctx, pool, func(ctx context.Context, tx outbox.Tx) error {
		_, execErr := tx.Exec(ctx, "INSERT INTO test_table VALUES (1)")
		return execErr
	})
	if err != nil {
		t.Fatalf("RunPool: %v", err)
	}
	if out.Err != nil {
		t.Errorf("expected nil err in outcome, got %v", out.Err)
	}
	if out.RowsWritten != 1 {
		t.Errorf("expected 1 row written, got %d", out.RowsWritten)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRunOutboxRollsBackOnError(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("mock pool: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	wantErr := errors.New("boom")
	pool.ExpectBegin()
	pool.ExpectRollback()

	_, runErr := outbox.RunPool(ctx, pool, func(ctx context.Context, tx outbox.Tx) error {
		return wantErr
	})
	if !errors.Is(runErr, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, runErr)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestRunOutboxCountsMultipleWrites(t *testing.T) {
	t.Parallel()

	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("mock pool: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	pool.ExpectBegin()
	pool.ExpectExec("UPDATE input").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec("INSERT output").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectExec("INSERT output").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	pool.ExpectCommit()

	out, err := outbox.RunPool(ctx, pool, func(ctx context.Context, tx outbox.Tx) error {
		if _, execErr := tx.Exec(ctx, "UPDATE input SET processed=true"); execErr != nil {
			return execErr
		}
		if _, execErr := tx.Exec(ctx, "INSERT output VALUES (1)"); execErr != nil {
			return execErr
		}
		_, execErr := tx.Exec(ctx, "INSERT output VALUES (2)")
		return execErr
	})
	if err != nil {
		t.Fatalf("RunPool: %v", err)
	}
	if out.RowsWritten != 3 {
		t.Errorf("expected 3 row writes counted, got %d", out.RowsWritten)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOutboxOutcomeAlreadyProcessed(t *testing.T) {
	t.Parallel()

	o := outbox.Outcome{AlreadyProcessed: true}
	if !o.IsAlreadyProcessed() {
		t.Error("expected IsAlreadyProcessed to be true")
	}
}
