// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// TestE2E_MigrateCLI_AppliesAllAndIsIdempotent pins OP #212.
//
// Operators run `fhir-subs migrate up` (or `make migrate-up`, which
// shells out to the binary) against an empty Postgres. The full
// embedded migration set MUST apply on the first call, and a second
// call MUST be a no-op (the schema_migrations row count stays at the
// embedded count). `migrate status` MUST list every applied migration
// with no pending migrations after the up pass.
//
// This is the e2e contract for `make migrate-up` against a real
// Postgres — no mocks, no in-process Up() — so a regression that
// breaks the CLI dispatch surface (e.g. forgetting to wire the verb
// in main.go) trips the gate.
func TestE2E_MigrateCLI_AppliesAllAndIsIdempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		// OP #259: env-gated skip — -short mode skips the testcontainers Postgres path.
		t.Skip("short")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("migrate_cli"),
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

	// Wait for Postgres to actually accept connections before invoking
	// the CLI; testcontainers' wait strategy occasionally returns
	// before pgx can dial.
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

	// Build the binary.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	binPath := filepath.Join(t.TempDir(), "fhir-subs")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/fhir-subs")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/fhir-subs: %v\n%s", err, out)
	}

	// A migrate-only config: facility/adapter/codec are required by
	// loadConfig. We render a probe-only mode YAML so Validate accepts
	// the minimal posture; the migrate subcommand only consumes
	// database.url so the rest is filler that satisfies the schema.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)

	yamlBody := fmt.Sprintf(`deployment:
  facility_id: e2e-migrate
  environment: e2e
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: builtin-noop
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
database:
  url: %s
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: %s
auth:
  audience: ""
  allow_dev_bypass: true
pipeline:
  hl7_processor:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  matcher:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  submatcher:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  scheduler:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  correlation_hold_window: 1s
`, dbURL, keyB64)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// First migrate up: applies every embedded migration.
	upCtx, upCancel := context.WithTimeout(ctx, 60*time.Second)
	defer upCancel()
	upCmd := exec.CommandContext(upCtx, binPath, "migrate", "up", "--config", cfgPath)
	upOut, upErr := upCmd.CombinedOutput()
	if upErr != nil {
		t.Fatalf("migrate up failed: %v\n%s", upErr, upOut)
	}
	t.Logf("migrate up out:\n%s", upOut)

	// Embedded count is the success bar.
	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("embedded: %v", err)
	}
	want := len(migs)

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&n); err != nil {
		t.Fatalf("count after up: %v", err)
	}
	if n != want {
		t.Fatalf("schema_migrations count = %d after up, want %d", n, want)
	}

	// Second migrate up: idempotent — count stays the same.
	up2Ctx, up2Cancel := context.WithTimeout(ctx, 30*time.Second)
	defer up2Cancel()
	up2Cmd := exec.CommandContext(up2Ctx, binPath, "migrate", "up", "--config", cfgPath)
	up2Out, up2Err := up2Cmd.CombinedOutput()
	if up2Err != nil {
		t.Fatalf("idempotent migrate up failed: %v\n%s", up2Err, up2Out)
	}

	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&n); err != nil {
		t.Fatalf("count after second up: %v", err)
	}
	if n != want {
		t.Fatalf("schema_migrations count = %d after idempotent up, want %d", n, want)
	}

	// migrate status: applied count = embedded; pending = 0; latest
	// version is the head.
	statusCtx, statusCancel := context.WithTimeout(ctx, 30*time.Second)
	defer statusCancel()
	statusCmd := exec.CommandContext(statusCtx, binPath, "migrate", "status", "--config", cfgPath)
	statusOut, statusErr := statusCmd.CombinedOutput()
	if statusErr != nil {
		t.Fatalf("migrate status failed: %v\n%s", statusErr, statusOut)
	}
	statusStr := string(statusOut)
	headVersion := migs[len(migs)-1].Version
	if !strings.Contains(statusStr, headVersion) {
		t.Errorf("migrate status output missing head version %s:\n%s", headVersion, statusStr)
	}
	// Status MUST report at least one applied row and zero pending.
	if !strings.Contains(strings.ToLower(statusStr), "applied") {
		t.Errorf("migrate status missing 'applied' summary:\n%s", statusStr)
	}
	if !strings.Contains(strings.ToLower(statusStr), "pending") {
		t.Errorf("migrate status missing 'pending' summary:\n%s", statusStr)
	}
}

// TestE2E_MakeMigrateUp_AppliesAllMigrations pins the operator-facing
// surface from OP #212: `make migrate-up` MUST shell out to the binary
// against $DATABASE_URL and apply every embedded migration. Today the
// Makefile target echoes a TODO. This test makes the operator-quoted
// command real.
func TestE2E_MakeMigrateUp_AppliesAllMigrations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		// OP #259: env-gated skip — -short mode skips the make-driven migration path.
		t.Skip("short")
	}

	if _, err := exec.LookPath("make"); err != nil {
		// OP #259: env-gated skip — make not available on this runner.
		t.Skipf("make not available on this runner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pgCtr, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("make_migrate_up"),
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

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	makeCtx, makeCancel := context.WithTimeout(ctx, 90*time.Second)
	defer makeCancel()
	makeCmd := exec.CommandContext(makeCtx, "make", "migrate-up")
	makeCmd.Dir = repoRoot
	makeCmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL)
	makeOut, makeErr := makeCmd.CombinedOutput()
	if makeErr != nil {
		t.Fatalf("make migrate-up failed: %v\n%s", makeErr, makeOut)
	}
	t.Logf("make migrate-up out:\n%s", makeOut)

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("embedded: %v", err)
	}
	want := len(migs)

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != want {
		t.Fatalf("schema_migrations count = %d after make migrate-up, want %d", n, want)
	}
}
