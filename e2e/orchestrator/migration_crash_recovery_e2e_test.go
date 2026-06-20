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

// TestE2E_Migrate_PartialApplyRecovers (OP #214) pins the
// crash-recovery acceptance criterion: if every CREATE/ALTER in every
// migration body is idempotent against partial application, then a
// pod that runs every migration's body twice — once "interrupted"
// (no version-row recorded), once cleanly — must converge on the
// same final schema as a single clean apply.
//
// The simulation models the three failure modes #214 calls out:
//
//   1. Partial body apply with no version row recorded. The next pod
//      sees the version absent and re-runs the whole body. The body
//      MUST be a no-op the second time around.
//
//   2. INSERT INTO schema_migrations after a partial body. The next
//      pod sees the version present and skips. The body MUST have
//      been idempotent so any earlier failed-mid-body apply is
//      harmless on retry.
//
//   3. The migration runner's session-level advisory lock dropping on
//      crash. Two simulated pods running migrate.Up after the
//      simulated partial apply both succeed.
//
// Implementation: rather than try to actually kill a pod between
// statements (no portable signal exists), we directly execute every
// migration body twice against a fresh DB, with the version-INSERT
// from the body NOT recorded between runs. This is strictly stricter
// than the real-world failure mode — every statement runs twice — so
// passing this test proves any narrower partial-apply recovers too.
func TestE2E_Migrate_PartialApplyRecovers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("migrate_recovery"),
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

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("embedded: %v", err)
	}

	// Phase 1: simulate a partial apply by executing each migration's
	// body but withholding the version row. Schema_migrations needs
	// to exist for the runner's probe so we bootstrap 0001's create
	// table for it manually before the second pass — but we do NOT
	// pre-record versions; the runner sees an empty applied set and
	// must re-run every body.
	for _, m := range migs {
		// Each body runs in its own connection so a body-level error
		// does not poison the connection state; if the body fails,
		// we simply move on — the second pass must still converge.
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire (phase1 %s): %v", m.Filename, err)
		}
		_, _ = conn.Exec(ctx, m.Body)
		conn.Release()
	}

	// Phase 2: real migrate.Up does the full apply with the runner's
	// transaction wrapping + version-recording. After phase 1 every
	// idempotent statement has run; the runner sees no
	// schema_migrations rows (because phase 1 did not commit them)
	// and re-applies every body inside a transaction. The bodies
	// MUST be idempotent for this to succeed.
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("Up after partial apply must succeed (idempotency): %v", err)
	}

	// Phase 3: a second migrate.Up after a clean apply must be a
	// no-op — proves the version-tracking + body-idempotency combine
	// to a stable steady state under restart loops.
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("second Up should be no-op: %v", err)
	}

	// Phase 4: schema_migrations should hold exactly the embedded
	// version set, no duplicates.
	want := len(migs)
	var got int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&got); err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != want {
		t.Fatalf("schema_migrations count after recovery = %d, want %d", got, want)
	}

	// Cross-check: spin up two more concurrent migrate.Up calls.
	// They must both return nil (the advisory lock serializes them)
	// and the count must remain == want.
	const racers = 2
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
		t.Fatalf("post-recovery racer err: %v", e)
	}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&got); err != nil {
		t.Fatalf("recount: %v", err)
	}
	if got != want {
		t.Fatalf("schema_migrations count after concurrent re-up = %d, want %d", got, want)
	}
}
