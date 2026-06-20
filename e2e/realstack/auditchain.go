// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AuditChainResult is the parsed output of a cmd/audit-chain-walker run.
//
// Tests assert on the structured fields rather than re-parsing Stdout
// strings. ExitCode follows the walker contract:
//
//	0 — chain clean
//	1 — at least one chain break detected
//	2 — flag-parse / operational error
//
// Breaks is populated from `break at row <i>: seq=<n> occurred_at=<ts>`
// lines and is non-empty exactly when ExitCode == 1.
type AuditChainResult struct {
	ExitCode int
	Rows     int
	Clean    bool
	Breaks   []AuditChainBreak
	Stdout   string
}

// AuditChainBreak describes a single mismatch row reported by the
// walker. Index is the row's position in the (occurred_at, seq) walk;
// Seq is the on-disk audit_log.seq value.
type AuditChainBreak struct {
	Index      int
	Seq        int64
	OccurredAt string
	Reason     string
}

// VerifyAuditChain builds and runs cmd/audit-chain-walker against the
// realstack Postgres (s.Postgres.URL) as a real OS subprocess and
// returns the parsed result. No fakes: the binary is built from source
// every call (cached in t.TempDir), connects via real pgx, and walks
// the same audit_log table the production fhir-subs binary writes to.
//
// fromTo, when non-zero, is forwarded as --from / --to to the walker.
// The two-element form keeps the caller signature small for the common
// "no time window" case (pass time.Time{} pairs).
func (s *Stack) VerifyAuditChain(ctx context.Context, t *testing.T, from, to time.Time) AuditChainResult {
	t.Helper()
	if s == nil || s.Postgres.URL == "" {
		t.Fatalf("VerifyAuditChain: stack has no Postgres URL — was Boot() called?")
	}

	bin := buildAuditChainWalker(ctx, t)

	args := []string{"--database-url", s.Postgres.URL}
	if !from.IsZero() {
		args = append(args, "--from", from.UTC().Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		args = append(args, "--to", to.UTC().Format(time.RFC3339Nano))
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	out, runErr := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		t.Fatalf("VerifyAuditChain: walker did not run: %v\noutput:\n%s", runErr, out)
	}

	res := AuditChainResult{
		ExitCode: cmd.ProcessState.ExitCode(),
		Stdout:   string(out),
	}
	res.Rows, res.Clean, res.Breaks = parseAuditChainStdout(res.Stdout)

	// Exit code 2 means the walker itself failed (not a chain break);
	// surface that as a hard test failure rather than letting tests
	// assert on Clean=false.
	if res.ExitCode == 2 {
		t.Fatalf("VerifyAuditChain: walker reported operational error (exit 2)\nargs: %v\noutput:\n%s",
			args, res.Stdout)
	}
	// Exit code 1 must come paired with at least one parsed break line;
	// exit 0 must come paired with a clean banner. If those invariants
	// don't hold the walker output drifted from its contract — fail
	// loud rather than letting downstream tests guess.
	switch res.ExitCode {
	case 0:
		if !res.Clean {
			t.Fatalf("VerifyAuditChain: walker exited 0 but stdout missing 'result: clean'\noutput:\n%s", res.Stdout)
		}
	case 1:
		if len(res.Breaks) == 0 {
			t.Fatalf("VerifyAuditChain: walker exited 1 but stdout reported no break lines\noutput:\n%s", res.Stdout)
		}
	default:
		t.Fatalf("VerifyAuditChain: unexpected walker exit code %d\noutput:\n%s", res.ExitCode, res.Stdout)
	}
	return res
}

// buildAuditChainWalker compiles cmd/audit-chain-walker once per test
// run into t.TempDir() and returns the absolute path. Each test gets
// its own TempDir so concurrent VerifyAuditChain calls across tests do
// not race on the binary file.
func buildAuditChainWalker(ctx context.Context, t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	binPath := filepath.Join(t.TempDir(), "audit-chain-walker")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./cmd/audit-chain-walker")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build ./cmd/audit-chain-walker: %v\n%s", err, out)
	}
	return binPath
}

// parseAuditChainStdout reads the walker's stdout and pulls out the
// row count, clean flag, and structured break list. The walker's
// output contract (see cmd/audit-chain-walker/walker.go):
//
//	rows: <N>
//	result: clean                                 -- clean case
//	  break at row <i>: seq=<n> occurred_at=<ts> reason=<...>   -- per break
//	result: <N> break(s)                          -- break case
//
// Unknown lines are ignored; the parser is permissive on whitespace
// and case so future cosmetic changes to the walker output do not
// silently break this wrapper.
func parseAuditChainStdout(stdout string) (rows int, clean bool, breaks []AuditChainBreak) {
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "rows:"):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "rows:"))
			if n, err := scanInt(rest); err == nil {
				rows = n
			}
		case strings.EqualFold(line, "result: clean"):
			clean = true
		case strings.HasPrefix(line, "break at row "):
			if b, ok := parseBreakLine(line); ok {
				breaks = append(breaks, b)
			}
		}
	}
	return rows, clean, breaks
}

// parseBreakLine matches one of:
//
//	break at row 17: seq=42 occurred_at=2026-06-19T20:11:02Z reason=chain_hash mismatch
//
// Returns (zero value, false) if the line doesn't match. The function
// extracts each `key=value` pair via simple field-splitting rather
// than a regex so additional fields appended to the walker's break
// line in future do not have to be reflected here.
func parseBreakLine(line string) (AuditChainBreak, bool) {
	rest := strings.TrimPrefix(line, "break at row ")
	colonIdx := strings.IndexByte(rest, ':')
	if colonIdx < 0 {
		return AuditChainBreak{}, false
	}
	idxStr := strings.TrimSpace(rest[:colonIdx])
	idx, err := scanInt(idxStr)
	if err != nil {
		return AuditChainBreak{}, false
	}
	b := AuditChainBreak{Index: idx, Reason: "unknown"}
	tail := strings.TrimSpace(rest[colonIdx+1:])

	for _, tok := range splitOnSpaces(tail) {
		eq := strings.IndexByte(tok, '=')
		if eq <= 0 {
			continue
		}
		k, v := tok[:eq], tok[eq+1:]
		switch k {
		case "seq":
			if n, err := scanInt64(v); err == nil {
				b.Seq = n
			}
		case "occurred_at":
			b.OccurredAt = v
		case "reason":
			b.Reason = v
		}
	}
	return b, true
}

func splitOnSpaces(s string) []string {
	// Walker emits `reason=chain_hash mismatch` with a literal space
	// inside the value. Treat the substring after `reason=` as the
	// rest-of-line so we don't mis-split it. Other fields are space-
	// separated key=value pairs with no spaces inside values.
	if i := strings.Index(s, "reason="); i >= 0 {
		head := strings.Fields(strings.TrimSpace(s[:i]))
		head = append(head, "reason="+strings.TrimSpace(s[i+len("reason="):]))
		return head
	}
	return strings.Fields(s)
}

func scanInt(s string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func scanInt64(s string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// AssertChainClean is a convenience for tests that expect the walker
// to report a clean chain. It fails the test with the walker's full
// stdout on any non-clean result, including operational errors.
func (s *Stack) AssertChainClean(ctx context.Context, t *testing.T) AuditChainResult {
	t.Helper()
	res := s.VerifyAuditChain(ctx, t, time.Time{}, time.Time{})
	if !res.Clean {
		t.Fatalf("AssertChainClean: walker reported %d break(s)\noutput:\n%s",
			len(res.Breaks), res.Stdout)
	}
	return res
}

// AssertChainBroken is a convenience for tamper-evidence tests. It
// asserts the walker exited non-zero AND reported at least one break
// AND, when wantSeq != 0, that one of the reported breaks names that
// audit_log.seq value.
func (s *Stack) AssertChainBroken(ctx context.Context, t *testing.T, wantSeq int64) AuditChainResult {
	t.Helper()
	res := s.VerifyAuditChain(ctx, t, time.Time{}, time.Time{})
	if res.Clean {
		t.Fatalf("AssertChainBroken: walker reported clean chain; expected break\noutput:\n%s", res.Stdout)
	}
	if len(res.Breaks) == 0 {
		t.Fatalf("AssertChainBroken: walker exited %d but reported no breaks\noutput:\n%s", res.ExitCode, res.Stdout)
	}
	if wantSeq != 0 {
		var found bool
		for _, b := range res.Breaks {
			if b.Seq == wantSeq {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("AssertChainBroken: walker did not report seq=%d as broken; reported %v\noutput:\n%s",
				wantSeq, res.Breaks, res.Stdout)
		}
	}
	return res
}

// errMissingPostgres is exported only so package-internal tests can
// assert on the precise error path; tests under e2e/realstack/ call
// VerifyAuditChain via *Stack so they never see this error directly.
var errMissingPostgres = errors.New("realstack: stack has no Postgres URL")
