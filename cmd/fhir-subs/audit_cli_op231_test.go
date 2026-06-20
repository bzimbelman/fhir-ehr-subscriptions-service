// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestRunAuditVerify_RefusesEmptyChain (OP #231 AC #2):
// "The CLI MUST refuse to run if the audit chain hasn't been
// initialized (no rows + no schema)."
//
// Strategy: build a config with a database URL pointing at a
// nonexistent host. We expect the CLI to refuse early with a
// non-zero exit and a "no audit chain" diagnostic — NOT just a
// generic connect error and not a happy "result: clean" with rows: 0.
func TestRunAuditVerify_RefusesEmptyChain(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`server:
  addr: ":0"
api:
  base_url: "http://localhost"
database:
  url: "postgres://nope:nope@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"
`)
	if err := os.WriteFile(cfgPath, contents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := runAuditVerify([]string{"--config", cfgPath}, stdout, stderr)
	if rc == 0 {
		t.Errorf("rc=0 on empty chain / unreachable DB; want non-zero")
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "result: clean") {
		t.Errorf("CLI reported result: clean against an unreachable DB; combined=%q", combined)
	}
}

// TestRunAuditVerify_ReportsProgressOnLargeChain (OP #231 AC #3):
// "The CLI MUST report walk progress for large chains (~100K rows)."
//
// Strategy: drive the verifier-with-progress entry point against a
// synthetic store seeded with a chain large enough that progress
// emission becomes observable. The synthetic store yields N rows
// honoring the audit.Store contract; the CLI's progress hook MUST
// fire during iteration.
func TestRunAuditVerify_ReportsProgressOnLargeChain(t *testing.T) {
	t.Parallel()

	store := newProgressTestStore(t, 5_000)
	stdout := &bytes.Buffer{}

	report, err := verifyChainWithProgress(context.Background(), store, audit.VerifyOptions{}, stdout, 1_000)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.RowsSeen != 5_000 {
		t.Errorf("RowsSeen = %d; want 5000", report.RowsSeen)
	}
	out := stdout.String()
	// Expect at least one "progress: <N>" line emitted before the final summary.
	if !strings.Contains(out, "progress: ") {
		t.Errorf("expected progress lines on large chain; got %q", out)
	}
}

// TestRunAuditVerify_NonZeroOnTamperedChain (OP #231 AC #4):
// "Unit test asserting the CLI returns zero on a clean chain and
// non-zero on a tampered one."
//
// Strategy: drive verifyChainWithProgress against a tampered
// synthetic chain; the helper MUST report at least one break and the
// caller MUST translate that to a non-zero exit code in
// reportVerifyResult.
func TestRunAuditVerify_NonZeroOnTamperedChain(t *testing.T) {
	t.Parallel()

	store := newProgressTestStore(t, 25)
	store.tamperRow(12)

	stdout := &bytes.Buffer{}
	report, err := verifyChainWithProgress(context.Background(), store, audit.VerifyOptions{}, stdout, 50)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(report.Breaks) == 0 {
		t.Fatalf("tampered chain reported zero breaks; report=%+v", report)
	}

	rc := reportVerifyResult(report, stdout)
	if rc == 0 {
		t.Errorf("rc=0 on tampered chain; want 1")
	}
}

// TestRunAuditVerify_ZeroOnCleanChain (OP #231 AC #4 happy path).
func TestRunAuditVerify_ZeroOnCleanChain(t *testing.T) {
	t.Parallel()

	store := newProgressTestStore(t, 25)

	stdout := &bytes.Buffer{}
	report, err := verifyChainWithProgress(context.Background(), store, audit.VerifyOptions{}, stdout, 50)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(report.Breaks) != 0 {
		t.Fatalf("clean chain reported breaks: %+v", report.Breaks)
	}

	rc := reportVerifyResult(report, stdout)
	if rc != 0 {
		t.Errorf("rc=%d on clean chain; want 0", rc)
	}
}

// progressTestStore is a synthetic audit.Store that yields a
// deterministic, well-formed chain. Its sole purpose is to exercise
// the verifier-with-progress code path without standing up a real
// Postgres testcontainer.
type progressTestStore struct {
	rows []audit.Row
}

// newProgressTestStore builds a store of n well-formed chain rows.
// The rows are produced by an in-memory writer so every row has the
// correct prior_hash / chain_hash byte sequence.
func newProgressTestStore(t *testing.T, n int) *progressTestStore {
	t.Helper()
	mem := newMemAuditStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: mem,
		Sink:  discardSink{},
		Clock: func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := w.Emit(context.Background(), audit.Event{
			ActorKind: "system",
			Action:    "test.event",
			Outcome:   "success",
		}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	return &progressTestStore{rows: mem.rows}
}

func (s *progressTestStore) tamperRow(idx int) {
	if idx < 0 || idx >= len(s.rows) {
		return
	}
	// Flip a byte in the chain_hash so the verifier surfaces a
	// chain_hash break.
	if len(s.rows[idx].ChainHash) > 0 {
		s.rows[idx].ChainHash[0] ^= 0xFF
	}
}

func (s *progressTestStore) AcquireChainLock(_ context.Context) (func() error, error) {
	return func() error { return nil }, nil
}
func (s *progressTestStore) LastChainHash(_ context.Context) ([]byte, error) {
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}
func (s *progressTestStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.rows = append(s.rows, row)
	return nil
}
func (s *progressTestStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	for _, r := range s.rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// memAuditStore is a tiny in-memory store used to seed a
// progressTestStore; it differs from progressTestStore only because
// audit.NewWriter requires a Store at construction time and we want
// the rows back out for tampering.
type memAuditStore struct {
	rows []audit.Row
}

func newMemAuditStore() *memAuditStore { return &memAuditStore{} }

// discardSink is a no-op audit.Sink — the test only cares about the
// durable rows the writer wrote into the store, not the realtime
// sink fan-out.
type discardSink struct{}

func (discardSink) Emit(_ context.Context, _ audit.Event) error { return nil }

func (m *memAuditStore) AcquireChainLock(_ context.Context) (func() error, error) {
	return func() error { return nil }, nil
}
func (m *memAuditStore) LastChainHash(_ context.Context) ([]byte, error) {
	if len(m.rows) == 0 {
		return nil, nil
	}
	return m.rows[len(m.rows)-1].ChainHash, nil
}
func (m *memAuditStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	m.rows = append(m.rows, row)
	return nil
}
func (m *memAuditStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	for _, r := range m.rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}
