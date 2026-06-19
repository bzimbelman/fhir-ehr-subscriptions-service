// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// runMigrateSubcommand dispatches the `migrate <verb>` family from
// realMain. It mirrors runAuditSubcommand: usage on a missing verb,
// "unknown verb" diagnostics on a stranger, exit 2 in both cases.
//
// The supported verbs are:
//   - up:     apply every pending embedded migration to head and exit 0.
//   - status: print applied + pending migrations and exit 0.
//   - down:   intentionally rejected (see #212 out-of-scope note); exits
//     non-zero with a "not supported" diagnostic. The verb is recognized
//     so an operator can discover that down semantics are not provided.
func runMigrateSubcommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fhir-subs migrate <up|down|status> [--config PATH]")
		return 2
	}
	switch args[0] {
	case "up":
		return runMigrateUp(args[1:], stdout, stderr)
	case "status":
		return runMigrateStatus(args[1:], stdout, stderr)
	case "down":
		fmt.Fprintln(stderr, "error: migrate down is not supported (forward-only schema discipline; see migrations/ docs)")
		return 2
	default:
		fmt.Fprintf(stderr, "error: unknown migrate verb %q (expected: up, down, status)\n", args[0])
		return 2
	}
}

// migrateOptions captures the parsed flag set for the migrate subcommand.
type migrateOptions struct {
	ConfigPath string
}

// parseMigrateFlags is the shared flag parser for every migrate verb.
// It is split out so unit tests can drive it deterministically. The
// only flag today is --config; the verb itself is parsed by the
// dispatch function.
func parseMigrateFlags(args []string, stderr io.Writer) (*migrateOptions, error) {
	fs := flag.NewFlagSet("fhir-subs migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var configPath string
	fs.StringVar(&configPath, "config", defaultConfigPath, "path to the config file")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: fhir-subs migrate <up|status> [--config PATH]\n\n")
		fmt.Fprintf(stderr, "Subcommands:\n")
		fmt.Fprintf(stderr, "  up      apply every pending embedded migration\n")
		fmt.Fprintf(stderr, "  status  list applied + pending migrations\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelpRequested
		}
		return nil, err
	}
	return &migrateOptions{ConfigPath: configPath}, nil
}

// migrateOpenPool loads the binary's config and opens a pgx pool against
// the configured database URL. It is shared by the up and status verbs
// so they take the same code path through Validate + buildPoolConfig
// the production server takes.
func migrateOpenPool(ctx context.Context, configPath string, stderr io.Writer) (*pgxpool.Pool, int) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: load config:", err)
		return nil, 1
	}
	if cfg.Database.URL == "" {
		fmt.Fprintln(stderr, "error: database.url is required")
		return nil, 1
	}
	poolCfg, err := buildPoolConfig(cfg.Database.URL, defaultPgxConnectTimeout)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 1
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		fmt.Fprintln(stderr, "error: open pool:", err)
		return nil, 1
	}
	return pool, 0
}

// runMigrateUp is the production entry point for `fhir-subs migrate up`.
// It applies every embedded migration whose version is not yet recorded
// in schema_migrations. Idempotent — a second invocation against an
// already-migrated DB is a no-op (migrate.Up's checksum check guards
// against drift).
func runMigrateUp(args []string, stdout, stderr io.Writer) int {
	opts, err := parseMigrateFlags(args, stderr)
	switch {
	case errors.Is(err, errHelpRequested):
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, rc := migrateOpenPool(ctx, opts.ConfigPath, stderr)
	if rc != 0 {
		return rc
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		fmt.Fprintln(stderr, "error: migrate up:", err)
		return 1
	}
	fmt.Fprintln(stdout, "migrate up: ok")
	return 0
}

// runMigrateStatus prints a stable, line-oriented report of applied and
// pending migrations:
//
//	applied: <count>
//	pending: <count>
//	  <version> <state>
//	  ...
//
// `state` is "applied" or "pending". Operators read the count lines for
// quick checks; the per-row block is the audit trail.
func runMigrateStatus(args []string, stdout, stderr io.Writer) int {
	opts, err := parseMigrateFlags(args, stderr)
	switch {
	case errors.Is(err, errHelpRequested):
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	pool, rc := migrateOpenPool(ctx, opts.ConfigPath, stderr)
	if rc != 0 {
		return rc
	}
	defer pool.Close()

	migs, err := migrate.Embedded()
	if err != nil {
		fmt.Fprintln(stderr, "error: embedded:", err)
		return 1
	}

	// Pull applied versions. If schema_migrations does not exist,
	// every embedded migration is pending.
	applied := map[string]bool{}
	{
		var exists bool
		row := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = 'schema_migrations'
				  AND n.nspname = ANY (current_schemas(false))
				  AND c.relkind IN ('r','p'))`)
		if err := row.Scan(&exists); err != nil {
			fmt.Fprintln(stderr, "error: probe schema_migrations:", err)
			return 1
		}
		if exists {
			rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
			if err != nil {
				fmt.Fprintln(stderr, "error: select schema_migrations:", err)
				return 1
			}
			defer rows.Close()
			for rows.Next() {
				var v string
				if err := rows.Scan(&v); err != nil {
					fmt.Fprintln(stderr, "error: scan:", err)
					return 1
				}
				applied[v] = true
			}
			if rerr := rows.Err(); rerr != nil {
				fmt.Fprintln(stderr, "error: rows:", rerr)
				return 1
			}
		}
	}

	type row struct {
		version string
		state   string
	}
	rowsOut := make([]row, 0, len(migs))
	pendingCount := 0
	for _, m := range migs {
		state := "pending"
		if applied[m.Version] {
			state = "applied"
		} else {
			pendingCount++
		}
		rowsOut = append(rowsOut, row{version: m.Version, state: state})
	}
	sort.Slice(rowsOut, func(i, j int) bool { return rowsOut[i].version < rowsOut[j].version })

	fmt.Fprintf(stdout, "applied: %d\n", len(migs)-pendingCount)
	fmt.Fprintf(stdout, "pending: %d\n", pendingCount)
	for _, r := range rowsOut {
		fmt.Fprintf(stdout, "  %s %s\n", r.version, r.state)
	}
	return 0
}
