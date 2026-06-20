// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// TestE2E_PartitionTrigger_UsesNewCreatedAt (OP #215, finding #139)
// pins the partition trigger contract: a row inserted with an
// explicit historical created_at MUST land in the partition for that
// month, not in the partition for the current wallclock month.
//
// Pre-fix: 0001_init.sql's set_resource_changes_created_month and
// set_ehr_events_created_month both set
//   NEW.created_month := date_trunc('month', now())::date
// which silently re-stamps every backfill/replay row to the current
// month — schema invariant
//   created_month = date_trunc('month', created_at)
// is violated and any read path that filters by month sees ghosts.
//
// Post-fix: 0015_partition_trigger_use_new_created_at.sql replaces the
// trigger function with date_trunc('month', NEW.created_at)::date so
// the application-supplied created_at is authoritative.
//
// We exercise both partitioned tables (resource_changes and
// ehr_events) and assert the row materializes in the correctly-named
// monthly partition. The target partition is pre-created so the
// trigger has somewhere to route the row; partition.Run is
// responsible for ensuring historical partitions exist before
// backfill (covered separately in partition tests).
func TestE2E_PartitionTrigger_UsesNewCreatedAt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		// OP #215: env-gated skip — -short mode skips the testcontainers Postgres path.
		t.Skip("short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("partition_trigger"),
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

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate.Up: %v", err)
	}

	// Pick a created_at safely in the past — six months back, set to
	// a stable day-of-month so the partition naming is deterministic.
	now := time.Now().UTC()
	histAnchor := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC).
		AddDate(0, -6, 0)
	histMonth := time.Date(histAnchor.Year(), histAnchor.Month(), 1, 0, 0, 0, 0, time.UTC)
	histMonthEnd := histMonth.AddDate(0, 1, 0)
	suffix := histMonth.Format("2006_01")

	// Pre-create the historical partitions for both partitioned
	// parents. The trigger only sets created_month; the partition
	// router needs a child partition for that month to exist or the
	// INSERT fails with "no partition of relation found".
	for _, parent := range []string{"resource_changes", "ehr_events"} {
		ddl := "CREATE TABLE IF NOT EXISTS " + parent + "_" + suffix +
			" PARTITION OF " + parent +
			" FOR VALUES FROM ('" + histMonth.Format("2006-01-02") + "')" +
			" TO ('" + histMonthEnd.Format("2006-01-02") + "')"
		if _, err := pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("create historical partition %s: %v", parent+"_"+suffix, err)
		}
	}

	// Seed an auth_clients row so ehr_events can satisfy its
	// client_id FK (added in 0008).
	clientID := "tenant-" + uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth_clients(id, display_name) VALUES ($1, $1)`,
		clientID,
	); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	// Insert via the same SQL shape the production repos use — the
	// repos set created_at + created_month from a single source of
	// truth (COALESCE($N, now())) so partition routing has the right
	// value before the BEFORE-INSERT trigger fires. Postgres routes
	// partitions BEFORE the parent's row-level BEFORE triggers fire
	// (a documented limitation), so the application is responsible
	// for supplying created_month; the trigger is belt-and-suspenders.
	// OP #215's fix is two-part: (a) repo SQL must derive
	// created_month from the row's created_at, (b) the trigger body
	// (0015) must do the same so the suspenders don't re-stamp the
	// row to the current month if someone INSERTs without supplying
	// created_month.
	rcID := uuid.New()
	correlationID := uuid.New()
	var rcInsertedMonth time.Time
	if err := pool.QueryRow(ctx,
		`INSERT INTO resource_changes
			(id, adapter_id, correlation_id, resource_type, change_kind,
			 resource, occurred_at, created_at, created_month)
		 VALUES ($1, 'test-adapter', $2, 'Patient', 'create',
			 '\x00'::bytea, $3, $3,
			 date_trunc('month', $3::timestamptz)::date)
		 RETURNING created_month`,
		rcID, correlationID, histAnchor,
	).Scan(&rcInsertedMonth); err != nil {
		t.Fatalf("insert resource_changes (historical, anchor=%s, expected partition=%s): %v",
			histAnchor.Format(time.RFC3339), suffix, err)
	}

	eventID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO ehr_events
			(id, topic_url, focus, change_kind, resource,
			 correlation_id, occurred_at, resource_change_id,
			 created_at, client_id, created_month)
		 VALUES ($1, 'http://example.org/topic', 'Patient/x', 'create',
			 '\x00'::bytea, $2, $3, $4, $3, $5,
			 date_trunc('month', $3::timestamptz)::date)`,
		eventID, correlationID, histAnchor, rcID, clientID,
	); err != nil {
		t.Fatalf("insert ehr_events (historical): %v", err)
	}

	// Independently exercise the trigger: insert with NULL
	// created_month so the trigger MUST set it. This pins #215's
	// trigger-body fix — pre-fix the trigger used now() and the row
	// landed in the current-month partition; post-fix the trigger
	// uses NEW.created_at and the row lands in the historical
	// partition. The partition router runs BEFORE this trigger on
	// the parent, so for this assertion to pass we must INSERT
	// directly into the partition (bypassing the parent's routing)
	// — the trigger fires there too because trigger inheritance
	// cascades to attached partitions. If the trigger body is
	// regressed to now(), the assertion below catches it.
	rcID2 := uuid.New()
	correlationID2 := uuid.New()
	var rcTriggerMonth time.Time
	insertDirect := "INSERT INTO resource_changes_" + suffix +
		` (id, adapter_id, correlation_id, resource_type, change_kind,
			 resource, occurred_at, created_at)
		 VALUES ($1, 'test-adapter-trig', $2, 'Patient', 'create',
			 '\x00'::bytea, $3, $3)
		 RETURNING created_month`
	if err := pool.QueryRow(ctx, insertDirect, rcID2, correlationID2, histAnchor).Scan(&rcTriggerMonth); err != nil {
		t.Fatalf("insert into historical partition directly: %v", err)
	}
	wantTriggerMonth := time.Date(histAnchor.Year(), histAnchor.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !rcTriggerMonth.Equal(wantTriggerMonth) {
		t.Errorf("trigger-set created_month=%s, want %s — set_resource_changes_created_month appears to use now() instead of NEW.created_at",
			rcTriggerMonth.Format("2006-01-02"), wantTriggerMonth.Format("2006-01-02"))
	}

	// Assertion 1: created_month must equal date_trunc('month', created_at).
	for _, tc := range []struct {
		table, idCol string
		id           uuid.UUID
	}{
		{"resource_changes", "id", rcID},
		{"ehr_events", "id", eventID},
	} {
		var createdMonth, createdAt time.Time
		q := "SELECT created_month, created_at FROM " + tc.table + " WHERE " + tc.idCol + " = $1"
		if err := pool.QueryRow(ctx, q, tc.id).Scan(&createdMonth, &createdAt); err != nil {
			t.Fatalf("read %s row: %v", tc.table, err)
		}
		want := time.Date(createdAt.Year(), createdAt.Month(), 1, 0, 0, 0, 0, time.UTC)
		if !createdMonth.Equal(want) {
			t.Errorf("%s: created_month=%s, want %s (date_trunc(month, created_at)). Trigger using now() instead of NEW.created_at?",
				tc.table, createdMonth.Format("2006-01-02"), want.Format("2006-01-02"))
		}
	}

	// Assertion 2: every row physically lives in the historical
	// partition, not the current-month one. A row routed to the
	// wrong partition would not be returned by a query that targets
	// the historical partition by name. resource_changes has 2 rows
	// (one routed via parent-INSERT, one inserted directly into the
	// partition by the trigger-bypass test above); ehr_events has 1.
	for _, tc := range []struct {
		parent string
		want   int
	}{
		{"resource_changes", 2},
		{"ehr_events", 1},
	} {
		var n int
		q := "SELECT count(*) FROM " + tc.parent + "_" + suffix
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("count %s_%s: %v", tc.parent, suffix, err)
		}
		if n != tc.want {
			t.Errorf("%s_%s rowcount=%d, want %d (row landed in the wrong partition; created_month derived from now() instead of created_at)",
				tc.parent, suffix, n, tc.want)
		}
	}
}
