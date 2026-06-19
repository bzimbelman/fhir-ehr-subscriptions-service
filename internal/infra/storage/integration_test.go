//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the storage module. Requires Docker. Skips
// gracefully (via t.Skip) if a Postgres container cannot be started.
//
// Run with: go test -race -tags integration ./internal/infra/storage/...

package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/outbox"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/partition"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/retention"
)

// startPostgres returns a connection URL for a Postgres 16 container,
// or t.Skip if Docker isn't available.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("storage_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		tcpostgres.WithSQLDriver("pgx/v5"),
	)
	if err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		// Use a fresh context — t.Cleanup may run after the test ctx is canceled.
		_ = container.Terminate(context.Background())
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}
	return url
}

func newTestStorage(t *testing.T, url string) *storage.Storage {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cfg := storage.Config{
		PostgresURL: url,
		KeyVersions: map[int32][]byte{1: key},
		ActiveKey:   1,
	}
	cfg.Partitioning.AutoDrop = false
	// Push the background tickers far away so they don't fire during
	// the test.
	cfg.Partitioning.RunInterval = time.Hour
	cfg.Retention.RunInterval = time.Hour
	cfg.Retention.Hl7MessageQueue = 0 // disable sweep

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := storage.Start(ctx, cfg, storage.Context{})
	if err != nil {
		t.Fatalf("storage.Start: %v", err)
	}
	t.Cleanup(func() {
		shctx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		defer sc()
		_ = s.Shutdown(shctx)
	})
	return s
}

func TestIntegrationMigrationsApplyCleanly(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	var version string
	if err := pool.QueryRow(ctx,
		`SELECT version FROM schema_migrations ORDER BY version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "0001" {
		t.Errorf("expected first migration '0001', got %q", version)
	}

	// Re-running is a no-op.
	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("MigrateUp idempotent run failed: %v", err)
	}
}

func TestIntegrationHl7MessageQueueRoundTrip(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)

	ctx := context.Background()
	corr := uuid.New()
	plaintext := []byte("MSH|^~\\&|EHR|FAC|RCV|FAC|20260617120000||ORM^O01|MSGID|P|2.5\r")

	id, err := s.Hl7MessageQueue().Insert(ctx, s.Pool().Pgx(), repos.Hl7MessageQueueRow{
		ListenerEndpoint: "adt-feed",
		PeerAddr:         "10.0.0.1:5000",
		MllpMessageID:    "MSGID",
		CorrelationID:    corr,
		RawBody:          plaintext,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Hl7MessageQueue().GetByID(ctx, s.Pool().Pgx(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("nil row")
	}
	if string(got.RawBody) != string(plaintext) {
		t.Errorf("raw_body mismatch:\ngot  %q\nwant %q", got.RawBody, plaintext)
	}
	if got.CorrelationID != corr {
		t.Errorf("correlation_id mismatch: got %v want %v", got.CorrelationID, corr)
	}

	// Verify the on-disk bytea is NOT plaintext.
	var raw []byte
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT raw_body FROM hl7_message_queue WHERE id=$1`, id,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+len(plaintext) <= len(raw); i++ {
		if string(raw[i:i+len(plaintext)]) == string(plaintext) {
			t.Fatalf("plaintext leaked into bytea column at offset %d", i)
		}
	}
}

func TestIntegrationClaimUnprocessedSkipLocked(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Insert N rows.
	const N = 6
	for i := 0; i < N; i++ {
		_, err := s.Hl7MessageQueue().Insert(ctx, s.Pool().Pgx(), repos.Hl7MessageQueueRow{
			ListenerEndpoint: "adt-feed",
			PeerAddr:         "10.0.0.1:5000",
			MllpMessageID:    uuid.New().String(),
			CorrelationID:    uuid.New(),
			RawBody:          []byte("MSH|test"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Two concurrent claimants. They must claim disjoint sets and the
	// union must equal the total.
	var wg sync.WaitGroup
	results := make([][]uuid.UUID, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			tx, err := s.Pool().Pgx().BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errs[i] = err
				return
			}
			defer tx.Rollback(ctx)
			rows, err := s.Hl7MessageQueue().ClaimUnprocessed(ctx, tx, N/2)
			if err != nil {
				errs[i] = err
				return
			}
			ids := make([]uuid.UUID, len(rows))
			for j, r := range rows {
				ids[j] = r.ID
			}
			results[i] = ids
			// Hold the lock briefly so the other goroutine cannot see the
			// rows we claimed.
			time.Sleep(200 * time.Millisecond)
			_ = tx.Commit(ctx)
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatalf("worker err: %v", e)
		}
	}

	seen := map[uuid.UUID]int{}
	for _, set := range results {
		for _, id := range set {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %v claimed %d times (expected 1)", id, n)
		}
	}
	if len(seen) > N {
		t.Errorf("more rows claimed (%d) than inserted (%d)", len(seen), N)
	}
}

func TestIntegrationOutboxRollback(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Insert a hl7 message inside the outbox closure, then return an
	// error. The transaction must roll back, leaving zero rows.
	want := errors.New("induced rollback")
	_, runErr := outbox.Run(ctx, s.Pool().Pgx(), func(ctx context.Context, tx outbox.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO hl7_message_queue (listener_endpoint, peer_addr, correlation_id, raw_body)
			 VALUES ('x', 'y', $1, $2)`,
			uuid.New(), []byte("doomed"))
		if err != nil {
			return err
		}
		return want
	})
	if !errors.Is(runErr, want) {
		t.Fatalf("expected wrapped %v, got %v", want, runErr)
	}
	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM hl7_message_queue`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected zero rows after rollback, got %d", n)
	}
}

func TestIntegrationCodecEncryptOnWriteDecryptOnRead(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Sanity: codec is the same one wired up by Storage.
	if _, ok := any(s.Codec()).(*codec.Codec); !ok {
		t.Fatal("Storage.Codec() should return *codec.Codec")
	}

	plaintext := []byte(`{"resourceType":"ServiceRequest","id":"abc-123"}`)
	tx, err := s.Pool().Pgx().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.ResourceChanges().Insert(ctx, tx, repos.ResourceChangeRow{
		AdapterID:     "default",
		CorrelationID: uuid.New(),
		ResourceType:  "ServiceRequest",
		ChangeKind:    repos.ChangeCreate,
		Resource:      plaintext,
		OccurredAt:    time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Now read back via claim path.
	tx2, _ := s.Pool().Pgx().Begin(ctx)
	defer tx2.Rollback(ctx)
	rows, err := s.ResourceChanges().ClaimUnprocessed(ctx, tx2, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
			if string(r.Resource) != string(plaintext) {
				t.Errorf("decrypted mismatch: got %q want %q", r.Resource, plaintext)
			}
		}
	}
	if !found {
		t.Errorf("inserted row not found via claim")
	}

	// Direct SQL read shows ciphertext.
	var raw []byte
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT resource FROM resource_changes WHERE id=$1`, id,
	).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) == string(plaintext) {
		t.Error("plaintext stored on disk")
	}
}

func TestIntegrationPartitionMaintainerCreatesNextMonth(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Force a tick.
	if err := partition.Tick(ctx, s.Pool().Pgx(), partition.Config{
		AutoDrop: false,
		Now:      time.Now,
	}); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Verify next-month partition exists for resource_changes.
	next := time.Now().AddDate(0, 1, 0)
	suffix := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006_01")
	target := "resource_changes_" + suffix

	var exists bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`, target,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("expected partition %s to exist", target)
	}
}

// TestIntegrationStorageStartLaunchesPartitionRunner asserts that
// storage.Start spawns a partition.Run goroutine that performs a
// CREATE-IF-NOT-EXISTS for next-month resource_changes / ehr_events
// partitions on its first tick, without an explicit caller invoking
// Tick. Story #95 acceptance criterion: partition maintainer goroutine
// starts and creates partitions on cadence.
func TestIntegrationStorageStartLaunchesPartitionRunner(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)

	// The maintainer's first Tick fires synchronously inside the goroutine
	// at startup, so the next-month partition should appear quickly.
	next := time.Now().AddDate(0, 1, 0)
	suffix := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006_01")
	target := "resource_changes_" + suffix

	deadline := time.Now().Add(5 * time.Second)
	var exists bool
	for time.Now().Before(deadline) {
		if err := s.Pool().Pgx().QueryRow(context.Background(),
			`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`, target,
		).Scan(&exists); err != nil {
			t.Fatalf("query: %v", err)
		}
		if exists {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !exists {
		t.Errorf("expected next-month partition %q after storage.Start; not found", target)
	}
}

// TestIntegrationPartitionMaintainerDoesNotClobberExisting asserts that
// the partition maintainer's CREATE TABLE IF NOT EXISTS path is
// idempotent: a second call (or a concurrent call from another pod)
// must not error out, drop the existing partition, or empty its rows.
// Story #95 negative case.
//
// We insert a row via the production INSERT path (which lands in the
// current-month partition that migration 0001 seeds) and confirm that
// running partition.Tick repeatedly leaves the row + partition object
// untouched.
func TestIntegrationPartitionMaintainerDoesNotClobberExisting(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	corr := uuid.New()
	plaintext := []byte(`{"resourceType":"ServiceRequest","id":"abc"}`)
	tx, err := s.Pool().Pgx().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResourceChanges().Insert(ctx, tx, repos.ResourceChangeRow{
		AdapterID:     "default",
		CorrelationID: corr,
		ResourceType:  "ServiceRequest",
		ChangeKind:    repos.ChangeCreate,
		Resource:      plaintext,
		OccurredAt:    time.Now(),
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert via repo: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Run partition Tick twice. CREATE TABLE IF NOT EXISTS makes both
	// calls no-ops on the current-month partition the migration seeded;
	// each call also creates the next-month partition (the second call
	// is a no-op against the partition the first call created).
	for i := 0; i < 2; i++ {
		if terr := partition.Tick(ctx, s.Pool().Pgx(), partition.Config{
			AutoDrop: false,
			Now:      time.Now,
		}); terr != nil {
			t.Fatalf("Tick %d: %v", i, terr)
		}
	}

	// Row must still be present.
	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM resource_changes WHERE correlation_id = $1`, corr,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("re-running partition Tick clobbered existing partition: row count = %d, want 1", n)
	}

	// Current-month partition table must still exist (Tick must not
	// detach or drop it).
	thisMonth := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC)
	currentSuffix := thisMonth.Format("2006_01")
	var exists bool
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`,
		"resource_changes_"+currentSuffix,
	).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("current-month partition resource_changes_%s missing after Tick", currentSuffix)
	}
}

// TestIntegrationStorageShutdownDrainsRunners asserts that Storage.Shutdown
// cancels the partition + retention goroutines and waits for them to
// exit. After Shutdown returns, attempting to use the pool must surface
// the closed-pool error, confirming the runners no longer hold connections.
// Story #95 acceptance criterion: both shut down cleanly on lifecycle
// drain.
func TestIntegrationStorageShutdownDrainsRunners(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cfg := storage.Config{
		PostgresURL: url,
		KeyVersions: map[int32][]byte{1: key},
		ActiveKey:   1,
	}
	cfg.Partitioning.RunInterval = 50 * time.Millisecond
	cfg.Retention.RunInterval = 50 * time.Millisecond
	cfg.Retention.Hl7MessageQueue = 24 * time.Hour
	cfg.Retention.Deliveries = 24 * time.Hour
	cfg.Retention.DeadLetters = 24 * time.Hour
	cfg.Lifecycle.ShutdownGracePeriod = 5 * time.Second

	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startCancel()
	s, err := storage.Start(startCtx, cfg, storage.Context{})
	if err != nil {
		t.Fatalf("storage.Start: %v", err)
	}

	// Let the runners spin a bit so we know they're really live.
	time.Sleep(200 * time.Millisecond)

	// Shutdown must return well before the lifecycle grace period
	// expires when nothing is stuck.
	shCtx, shCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shCancel()
	t0 := time.Now()
	if err := s.Shutdown(shCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(t0); elapsed > 5*time.Second {
		t.Errorf("Shutdown took too long: %v (runners did not respect ctx cancel)", elapsed)
	}

	// After Shutdown the pool is closed; any subsequent Acquire
	// must fail rather than block.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer probeCancel()
	if perr := s.Probe(probeCtx, time.Second); perr == nil {
		t.Errorf("expected Probe to fail after Shutdown; got nil")
	}
}

// TestIntegrationRetentionSweeperLeavesUnexpiredRows asserts the negative
// case: a sweeper Tick at the configured retention window must NOT
// remove rows whose age is below the window. Story #95 negative case.
func TestIntegrationRetentionSweeperLeavesUnexpiredRows(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Insert a fresh dead-letter (created_at = now()).
	young := uuid.New()
	if _, err := s.Pool().Pgx().Exec(ctx,
		`INSERT INTO dead_letters
		 (kind, source_table, source_id, reason)
		 VALUES ('hl7_unparseable', 'hl7_message_queue', $1, 'fresh')`,
		young,
	); err != nil {
		t.Fatal(err)
	}

	// Run a sweep with a 30-day retention. Fresh row must survive.
	if err := retention.Tick(ctx, s.Pool().Pgx(), retention.Config{
		BatchSize:   100,
		BatchPause:  time.Millisecond,
		DeadLetters: 30 * 24 * time.Hour,
		Now:         time.Now,
	}); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM dead_letters WHERE source_id = $1`, young,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("retention swept an unexpired row: count = %d, want 1", n)
	}
}

func TestIntegrationRetentionSweeperDeletesOldRows(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Insert a dead-letter, manually backdate created_at.
	src := uuid.New()
	if _, err := s.Pool().Pgx().Exec(ctx,
		`INSERT INTO dead_letters
		 (kind, source_table, source_id, reason, created_at)
		 VALUES ('hl7_unparseable', 'hl7_message_queue', $1, 'test', now() - interval '2 years')`,
		src,
	); err != nil {
		t.Fatal(err)
	}
	// Insert a young one too — it must survive.
	young := uuid.New()
	if _, err := s.Pool().Pgx().Exec(ctx,
		`INSERT INTO dead_letters
		 (kind, source_table, source_id, reason)
		 VALUES ('hl7_unparseable', 'hl7_message_queue', $1, 'fresh')`,
		young,
	); err != nil {
		t.Fatal(err)
	}

	// Run a sweep with a 30d retention.
	if err := retention.Tick(ctx, s.Pool().Pgx(), retention.Config{
		BatchSize:   100,
		BatchPause:  time.Millisecond,
		DeadLetters: 30 * 24 * time.Hour,
		Now:         time.Now,
	}); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM dead_letters WHERE source_id = $1`, src,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected old row deleted, got %d", n)
	}
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM dead_letters WHERE source_id = $1`, young,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected fresh row to survive, got %d", n)
	}
}
