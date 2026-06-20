// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// runAuditSubcommand dispatches the `audit <verb>` family. Today only
// `audit verify` is implemented (P2.5 audit chain verifier CLI).
//
// Unknown verbs print usage to stderr and exit 2 (matching the binary's
// flag-parsing convention). The subcommand intentionally bypasses the
// full server runtime: it loads the config (so it can resolve the
// Postgres URL and the audit chain genesis literal) but does NOT start
// the lifecycle module, the HTTP listener, or any pipeline workers.
func runAuditSubcommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fhir-subs audit verify [--config PATH] [--from RFC3339] [--to RFC3339]")
		return 2
	}
	switch args[0] {
	case "verify":
		return runAuditVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown audit verb %q (expected: verify)\n", args[0])
		return 2
	}
}

// auditVerifyOptions captures the parsed flag set for `audit verify`.
type auditVerifyOptions struct {
	ConfigPath string
	From       time.Time
	To         time.Time
}

// parseAuditVerifyFlags is parseFlags for `audit verify`. It is split out
// so unit tests can drive it deterministically.
func parseAuditVerifyFlags(args []string, stderr io.Writer) (*auditVerifyOptions, error) {
	fs := flag.NewFlagSet("fhir-subs audit verify", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		configPath string
		fromStr    string
		toStr      string
	)
	fs.StringVar(&configPath, "config", defaultConfigPath, "path to the config file")
	fs.StringVar(&fromStr, "from", "", "report only breaks at or after this RFC3339 timestamp (e.g. 2026-06-01T00:00:00Z)")
	fs.StringVar(&toStr, "to", "", "report only breaks at or before this RFC3339 timestamp (e.g. 2026-06-30T23:59:59Z)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: fhir-subs audit verify [--config PATH] [--from RFC3339] [--to RFC3339]\n\n")
		fmt.Fprintf(stderr, "Walks the audit_log chain end-to-end, recomputes each row's chain_hash from\n")
		fmt.Fprintf(stderr, "the JCS-canonicalized event bytes, and reports any mismatch.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelpRequested
		}
		return nil, err
	}

	out := &auditVerifyOptions{ConfigPath: configPath}
	if fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return nil, fmt.Errorf("invalid --from: %w", err)
		}
		out.From = t
	}
	if toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return nil, fmt.Errorf("invalid --to: %w", err)
		}
		out.To = t
	}
	if !out.From.IsZero() && !out.To.IsZero() && out.To.Before(out.From) {
		return nil, fmt.Errorf("--to must not be before --from")
	}
	return out, nil
}

// defaultAuditVerifyProgressInterval is how often (in rows) the
// progress reporter emits a heartbeat line. 1000 keeps the noise
// below ~100 lines for typical 100K-row chains while remaining
// responsive enough that an operator can tell the walk is moving.
const defaultAuditVerifyProgressInterval = 1000

// runAuditVerify is the production entry point for `fhir-subs audit verify`.
// It returns an exit code: 0 on a verified-clean chain, 1 on any reported
// break (or an operational error), 2 on flag-parsing problems.
func runAuditVerify(args []string, stdout, stderr io.Writer) int {
	opts, err := parseAuditVerifyFlags(args, stderr)
	switch {
	case errors.Is(err, errHelpRequested):
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	cfg, err := loadConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: load config:", err)
		return 1
	}
	if cfg.Database.URL == "" {
		fmt.Fprintln(stderr, "error: database.url is required to verify the audit chain")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	poolCfg, err := buildPoolConfig(cfg.Database.URL, defaultPgxConnectTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		fmt.Fprintln(stderr, "error: open pool:", err)
		return 1
	}
	defer pool.Close()

	// OP #231 AC #2: refuse to run if the audit chain hasn't been
	// initialized. We probe with a short-deadline context so an
	// unreachable DB fails loudly here instead of silently returning
	// `result: clean` after a zero-row IterateRows. The probe runs
	// against a pool-side connection (no transaction) so it doesn't
	// need the chain advisory lock.
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	rows, probeErr := auditChainPrecheck(probeCtx, pool)
	probeCancel()
	if probeErr != nil {
		fmt.Fprintln(stderr, "error: audit chain pre-check:", probeErr)
		return 1
	}
	if rows == 0 {
		fmt.Fprintln(stderr, "error: audit_log is empty — chain not initialized; refusing to verify")
		return 1
	}

	// Empty schema means "use the connection's default search_path", which
	// is what the production wiring at observability.Start uses today.
	// If the deployment ever moves the audit_log table to a non-default
	// schema, this will need to be threaded through cfg.Database (and the
	// production writer wiring updated to match).
	store, err := audit.NewPgStore(pool, audit.PgStoreOptions{})
	if err != nil {
		fmt.Fprintln(stderr, "error: open audit store:", err)
		return 1
	}

	// OP #231 AC #3: report walk progress so an operator running
	// against a 100K-row chain can see the verifier is making
	// progress instead of staring at a silent terminal for minutes.
	res, err := verifyChainWithProgress(ctx, store, audit.VerifyOptions{
		From: opts.From,
		To:   opts.To,
	}, stdout, defaultAuditVerifyProgressInterval)
	if err != nil {
		fmt.Fprintln(stderr, "error: verify:", err)
		return 1
	}
	return reportVerifyResult(res, stdout)
}

// auditChainPrecheck returns the row count of audit_log. It is the
// OP #231 AC #2 refusal pre-check: if no rows exist (or the table
// doesn't exist at all) the CLI must fail closed rather than report
// `result: clean`.
//
// The probe uses to_regclass so a missing table surfaces as
// `audit_log: not initialized` instead of a generic "relation does
// not exist" error — operators reading the line should be able to
// tell whether they pointed the CLI at the wrong DB or whether
// migrations haven't run.
func auditChainPrecheck(ctx context.Context, pool auditCountQuerier) (int64, error) {
	var oid *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('audit_log')::text`).Scan(&oid); err != nil {
		return 0, fmt.Errorf("to_regclass: %w", err)
	}
	if oid == nil {
		return 0, fmt.Errorf("audit_log: not initialized (table does not exist; run migrations)")
	}
	var n int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit_log: %w", err)
	}
	return n, nil
}

// auditCountQuerier is the minimal pgxpool surface auditChainPrecheck
// uses. Defined here so tests can stub it without a real pool.
type auditCountQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// verifyChainWithProgress wraps audit.VerifyChainReport with periodic
// progress emission. The verifier itself remains in the audit
// package; we install a counting Store wrapper that ticks every
// `interval` rows and emits a `progress: <N>` line to stdout. The
// final summary is emitted by reportVerifyResult.
//
// `interval` <= 0 disables progress (used by short-chain unit tests).
func verifyChainWithProgress(
	ctx context.Context,
	store audit.Store,
	opts audit.VerifyOptions,
	stdout io.Writer,
	interval int,
) (audit.VerifyResult, error) {
	if interval > 0 {
		store = &progressEmittingStore{inner: store, interval: interval, w: stdout}
	}
	return audit.VerifyChainReport(ctx, store, opts)
}

// progressEmittingStore wraps an audit.Store and writes a
// `progress: <N>` line to its writer every `interval` rows during
// IterateRows. AcquireChainLock / LastChainHash / InsertAuditRow are
// passed through verbatim — the verifier never calls those, but
// keeping the contract intact lets the wrapper substitute for a
// real store in mixed call paths.
type progressEmittingStore struct {
	inner    audit.Store
	interval int
	w        io.Writer
}

func (s *progressEmittingStore) AcquireChainLock(ctx context.Context) (func() error, error) {
	return s.inner.AcquireChainLock(ctx)
}
func (s *progressEmittingStore) LastChainHash(ctx context.Context) ([]byte, error) {
	return s.inner.LastChainHash(ctx)
}
func (s *progressEmittingStore) InsertAuditRow(ctx context.Context, row audit.Row) error {
	return s.inner.InsertAuditRow(ctx, row)
}
func (s *progressEmittingStore) IterateRows(ctx context.Context, fn func(audit.Row) error) error {
	count := 0
	return s.inner.IterateRows(ctx, func(r audit.Row) error {
		count++
		if s.interval > 0 && count%s.interval == 0 {
			fmt.Fprintf(s.w, "progress: %d\n", count)
		}
		return fn(r)
	})
}

// reportVerifyResult emits the final summary lines and returns the
// CLI exit code: 0 on a clean chain, 1 on any break.
func reportVerifyResult(res audit.VerifyResult, stdout io.Writer) int {
	fmt.Fprintf(stdout, "rows: %d\n", res.RowsSeen)
	fmt.Fprintf(stdout, "head_hash: %s\n", res.HeadHash)
	if len(res.Breaks) == 0 {
		fmt.Fprintln(stdout, "result: clean")
		return 0
	}
	fmt.Fprintf(stdout, "result: %d break(s)\n", len(res.Breaks))
	for _, b := range res.Breaks {
		fmt.Fprintf(stdout, "  row %d at %s: %s (%s)\n",
			b.RowIndex, b.OccurredAt.UTC().Format(time.RFC3339Nano), b.Kind, b.Message)
	}
	return 1
}
