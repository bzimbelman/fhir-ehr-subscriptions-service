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

	// The production writer at observability.Start does not override
	// WriterOptions.GenesisLiteral, so the chain seed is always the
	// package default. Verify with the same default to match what the
	// rows were written under.
	res, err := audit.VerifyChainReport(ctx, store, audit.VerifyOptions{
		From: opts.From,
		To:   opts.To,
	})
	if err != nil {
		fmt.Fprintln(stderr, "error: verify:", err)
		return 1
	}

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
