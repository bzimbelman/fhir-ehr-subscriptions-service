// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package main_test exercises the cmd/audit-chain-walker binary
// end-to-end against a real Postgres 16 testcontainer with a real
// production-shaped audit_log chain seeded by the production audit
// writer (audit.Writer + audit.PgStore).
//
// Story OP #257 (H2 AuditChainWalker) — Phase A RED tests. The
// cmd/audit-chain-walker binary does NOT exist yet at this commit;
// these tests therefore fail at the `go build` step. That is the
// intended Red state.
package main_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// chainSeedRowCount is the number of real chain rows seeded by the
// production audit writer prior to invoking the walker. The number
// is deliberately above 25 (>= 50) so a corruption injected into a
// "middle" row leaves a meaningful tail of downstream rows that
// must be reported as broken.
const chainSeedRowCount = 50

// startTestPostgres returns a *pgxpool.Pool and the connection URL
// pointing at a freshly-started Postgres 16 testcontainer. The
// container is torn down via t.Cleanup. If Docker isn't available
// the test is skipped — matching the convention already established
// in cmd/fhir-subs/wiring_storage_integration_test.go.
func startTestPostgres(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("audit_walker_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		tcpostgres.WithSQLDriver("pgx/v5"),
	)
	if err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, url
}

// repoRoot resolves the worktree root by walking up from the current
// test source file. Used to scope `go build ./cmd/audit-chain-walker`
// to the right module.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	// file == .../cmd/audit-chain-walker/walker_real_e2e_test.go
	// repoRoot is three levels up.
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// buildWalkerBinary builds cmd/audit-chain-walker into a temporary
// path under t.TempDir() and returns the absolute path. A failure
// here is the intended RED for Phase A: the package does not exist.
func buildWalkerBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	binPath := filepath.Join(t.TempDir(), "audit-chain-walker")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/audit-chain-walker")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/audit-chain-walker failed (RED-state expected until Phase B): %v\n%s", err, out)
	}
	return binPath
}

// seedRealChain seeds n rows through the production audit writer +
// PgStore, hashing each row through the same canonicalization the
// production binary uses. No INSERT shortcut, no in-memory store.
// Returns the writer for reuse if the caller wants to extend the chain.
func seedRealChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) {
	t.Helper()
	store, err := audit.NewPgStore(pool, audit.PgStoreOptions{})
	if err != nil {
		t.Fatalf("audit.NewPgStore: %v", err)
	}
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
	})
	if err != nil {
		t.Fatalf("audit.NewWriter: %v", err)
	}
	for i := 0; i < n; i++ {
		evt := audit.Event{
			ActorKind:     pickActorKind(i),
			ActorID:       fmt.Sprintf("actor-%d", i),
			Action:        pickAction(i),
			TargetKind:    "Subscription",
			TargetID:      fmt.Sprintf("sub-%04d", i),
			Outcome:       pickOutcome(i),
			CorrelationID: uuid.New(),
			Payload: map[string]any{
				"i":    i,
				"note": fmt.Sprintf("seed row %d", i),
				"meta": map[string]any{"phase": "A", "round": i % 7},
			},
		}
		if err := w.Emit(ctx, evt); err != nil {
			t.Fatalf("Emit(%d): %v", i, err)
		}
	}
}

func pickActorKind(i int) string {
	switch i % 3 {
	case 0:
		return "system"
	case 1:
		return "operator"
	default:
		return "subscriber"
	}
}

func pickAction(i int) string {
	switch i % 4 {
	case 0:
		return "subscription.create"
	case 1:
		return "subscription.update"
	case 2:
		return "event.deliver"
	default:
		return "auth.bind"
	}
}

func pickOutcome(i int) string {
	switch i % 5 {
	case 0:
		return "failure"
	case 1:
		return "denied"
	default:
		return "success"
	}
}

// TestAuditChainWalker_CleanChain_ReturnsZero asserts the Phase B
// walker binary, given a real audit_log chain produced by the
// production writer, exits 0 and reports a clean verification.
//
// Phase A RED expectation: this test fails at buildWalkerBinary
// because cmd/audit-chain-walker does not exist yet.
func TestAuditChainWalker_CleanChain_ReturnsZero(t *testing.T) {
	t.Parallel()

	pool, dsn := startTestPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate.Up: %v", err)
	}

	seedRealChain(t, ctx, pool, chainSeedRowCount)

	bin := buildWalkerBinary(t)

	cmd := exec.CommandContext(ctx, bin, "--database-url", dsn)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("walker exited non-zero on a clean chain: %v\nstdout/stderr:\n%s", err, out)
	}
	got := string(out)

	wantRowsPhrase := fmt.Sprintf("rows: %d", chainSeedRowCount)
	if !strings.Contains(got, wantRowsPhrase) {
		t.Errorf("walker output missing %q\noutput:\n%s", wantRowsPhrase, got)
	}
	if !strings.Contains(strings.ToLower(got), "clean") {
		t.Errorf("walker output did not declare a clean result\noutput:\n%s", got)
	}
}

// TestAuditChainWalker_TamperedRow_ReturnsNonZero_PrintsRow asserts
// that after a real pgx UPDATE corrupts the chain_hash bytes of a
// known middle row, the walker exits non-zero AND identifies the
// corrupted row by index or by occurred_at, AND uses a "break" /
// "mismatch" indicator in its output.
//
// Phase A RED expectation: this test fails at buildWalkerBinary
// because cmd/audit-chain-walker does not exist yet.
func TestAuditChainWalker_TamperedRow_ReturnsNonZero_PrintsRow(t *testing.T) {
	t.Parallel()

	pool, dsn := startTestPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate.Up: %v", err)
	}

	seedRealChain(t, ctx, pool, chainSeedRowCount)

	// Pick a middle row and capture its occurred_at BEFORE corruption
	// so we can match against the walker's output.
	const tamperedSeq = 25
	var occurredAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT occurred_at FROM audit_log WHERE seq = $1`, tamperedSeq,
	).Scan(&occurredAt); err != nil {
		t.Fatalf("read occurred_at for seq=%d: %v", tamperedSeq, err)
	}

	// Real pgx UPDATE: flip the first byte of chain_hash to 0x00 so
	// the on-disk hash no longer matches SHA-256(chain_input).
	tag, err := pool.Exec(ctx,
		`UPDATE audit_log
		    SET chain_hash = decode('00','hex') || substring(chain_hash from 2)
		  WHERE seq = $1`, tamperedSeq)
	if err != nil {
		t.Fatalf("corrupt chain_hash: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("expected to corrupt 1 row, got %d", tag.RowsAffected())
	}

	bin := buildWalkerBinary(t)

	cmd := exec.CommandContext(ctx, bin, "--database-url", dsn)
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatalf("walker did not run; output:\n%s", out)
	}
	if cmd.ProcessState.ExitCode() == 0 {
		t.Fatalf("walker exited 0 on a tampered chain; expected non-zero\noutput:\n%s", out)
	}

	got := string(out)
	gotLower := strings.ToLower(got)

	// Must identify the corrupted row by either its seq/index or
	// occurred_at timestamp.
	indexMarker := fmt.Sprintf("%d", tamperedSeq)
	tsMarker := occurredAt.UTC().Format(time.RFC3339)
	tsMarker2 := occurredAt.Format(time.RFC3339Nano)
	if !strings.Contains(got, indexMarker) &&
		!strings.Contains(got, tsMarker) &&
		!strings.Contains(got, tsMarker2) {
		t.Errorf("walker output did not identify the corrupted row by seq=%d or occurred_at=%s\noutput:\n%s",
			tamperedSeq, tsMarker, got)
	}

	// Must use a chain-break indicator.
	if !strings.Contains(gotLower, "break") &&
		!strings.Contains(gotLower, "mismatch") &&
		!strings.Contains(gotLower, "broken") {
		t.Errorf("walker output missing 'break'/'mismatch'/'broken' indicator\noutput:\n%s", got)
	}
}
