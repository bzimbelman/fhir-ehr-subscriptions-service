// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// TestE2E_Migrate_AdvisoryLockSerializesParallelRunners pins B-33. Two
// processes calling migrate.Up against the same database concurrently
// must serialize via pg_advisory_lock; both must succeed and the final
// schema_migrations row count must match the embedded migration set
// exactly (no duplicate inserts, no missing entries).
func TestE2E_Migrate_AdvisoryLockSerializesParallelRunners(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		// OP #259: env-gated skip — -short mode skips the testcontainers Postgres path.
		t.Skip("short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Stand up a fresh postgres so this test does not interact with the
	// shared harness DB (which already has migrations applied).
	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("migrate_race"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		dockerGate(t, err, allowNoDocker)
		return
	}
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = pgCtr.Terminate(stopCtx)
	}()

	dbURL, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	// Wait for the container's TCP port to actually accept connections.
	// pgxpool.New is lazy on first connect; the racer goroutines below
	// otherwise hit "connection refused" if Postgres is mid-startup.
	deadline := time.Now().Add(30 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		pingCtx, c := context.WithTimeout(ctx, 2*time.Second)
		pingErr = pool.Ping(pingCtx)
		c()
		if pingErr == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("waiting for postgres: %v", pingErr)
	}

	// Embedded migration count is the success bar.
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("embedded: %v", err)
	}
	want := len(migs)

	const racers = 3
	var wg sync.WaitGroup
	wg.Add(racers)
	errs := make(chan error, racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			if err := migrate.Up(ctx, pool); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("racer err: %v", e)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != want {
		t.Fatalf("schema_migrations count = %d, want %d (advisory lock failed to serialize)", n, want)
	}
}
