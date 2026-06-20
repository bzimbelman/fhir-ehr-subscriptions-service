// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command audit-chain-walker is the standalone, real-DB chain verifier
// CLI required by OP story #257 (H2 AuditChainWalker). It opens a
// connection to a Postgres database holding the audit_log table, walks
// every row in (occurred_at, seq) order, recomputes each row's
// chain_hash from the on-disk fields using the audit package's
// reference verifier (audit.VerifyChainExternal), and reports any
// mismatch.
//
// Exit codes:
//
//   - 0  clean chain. Stdout reports `rows: <N>` and `result: clean`.
//   - 1  any chain break OR operational error. Stdout reports `rows:
//     <N>` and one line per break with the offending seq, occurred_at,
//     and a `break` indicator.
//   - 2  flag-parse error.
//
// The tool is intentionally decoupled from cmd/fhir-subs config: it
// takes a `--database-url` flag (or DATABASE_URL env) so an external
// auditor can run it against any deployment without dragging in the
// service config.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// errHelpRequested is returned by parseFlags when the operator passed
// `-h`/`--help`; main treats it as exit 0 with usage already written.
var errHelpRequested = errors.New("help requested")

// walkerOptions captures the parsed flag set.
type walkerOptions struct {
	DatabaseURL string
	From        time.Time
	To          time.Time
	Genesis     string
}

// parseFlags parses the audit-chain-walker flag set. It is split out so
// unit tests can drive it deterministically.
func parseFlags(args []string, stderr io.Writer) (*walkerOptions, error) {
	fs := flag.NewFlagSet("audit-chain-walker", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		dbURL   string
		fromStr string
		toStr   string
		genesis string
	)
	fs.StringVar(&dbURL, "database-url", "", "Postgres connection URL (falls back to DATABASE_URL env)")
	fs.StringVar(&fromStr, "from", "", "report only breaks at or after this RFC3339 timestamp (e.g. 2026-06-01T00:00:00Z)")
	fs.StringVar(&toStr, "to", "", "report only breaks at or before this RFC3339 timestamp (e.g. 2026-06-30T23:59:59Z)")
	fs.StringVar(&genesis, "genesis", "", "override the chain genesis literal; empty uses the default 'fhir-ehr-subscriptions-service audit chain genesis'")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: audit-chain-walker --database-url URL [--from RFC3339] [--to RFC3339] [--genesis LITERAL]\n\n")
		fmt.Fprintf(stderr, "Walks the audit_log chain end-to-end against a real Postgres connection,\n")
		fmt.Fprintf(stderr, "recomputes each row's chain_hash via the audit reference verifier, and\n")
		fmt.Fprintf(stderr, "reports any chain break with the offending seq and occurred_at.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelpRequested
		}
		return nil, err
	}

	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		return nil, fmt.Errorf("--database-url is required (or set DATABASE_URL)")
	}

	out := &walkerOptions{DatabaseURL: dbURL, Genesis: genesis}
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

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	switch {
	case errors.Is(err, errHelpRequested):
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	hasBreaks, err := walk(ctx, *opts, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if hasBreaks {
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
