//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// S-2.14 integration: a synthetically-slow query running against
// pg_stores' Pool with a tight per-query deadline must surface
// handlers.ErrQueryTimeout, not a generic context error nor a raw pgx
// error string. The test induces slowness by holding an ACCESS
// EXCLUSIVE lock on the `subscriptions` table from a sibling
// connection so the store's ListByClient SELECT has to wait. With a
// 100ms read deadline, the deadline must fire and the typed error
// must surface.
func TestPgSubscriptionsStore_ListByClient_DeadlineYieldsTypedError(t *testing.T) {
	url := startPostgres(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Hold an ACCESS EXCLUSIVE lock on subscriptions in a separate tx so
	// the store's SELECT has to wait. The lock auto-releases on
	// rollback after the test.
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(holder.Release)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `LOCK TABLE subscriptions IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock: %v", err)
	}

	store := handlers.NewPgSubscriptionsStoreWithTimeouts(pool, handlers.QueryTimeouts{
		Read:  100 * time.Millisecond,
		Write: 1 * time.Second,
	})

	start := time.Now()
	_, gotErr := store.ListByClient(ctx, "any-client")
	elapsed := time.Since(start)

	if gotErr == nil {
		t.Fatal("expected ErrQueryTimeout, got nil")
	}
	if !errors.Is(gotErr, handlers.ErrQueryTimeout) {
		t.Fatalf("expected errors.Is(err, ErrQueryTimeout); got %v (%T)", gotErr, gotErr)
	}
	// Sanity: deadline should fire close to the 100ms knob, not the
	// outer 60s test deadline.
	if elapsed > 5*time.Second {
		t.Errorf("query waited %v — far longer than the per-query deadline; deadline did not fire", elapsed)
	}
}
