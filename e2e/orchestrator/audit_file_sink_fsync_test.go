// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestB34_AuditFileSinkFsyncs (B-34) starts the observability module
// with the file audit sink in default (every_write) mode, emits a few
// audit events, and verifies the file contains those events on disk.
//
// In every_write mode the sink fsyncs after each write — so once Emit
// returns, the row is on persistent storage. This test asserts the
// durability surface: post-Emit, the file content is observable.
func TestB34_AuditFileSinkFsyncs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")

	store := newE2EAuditStore()
	mod, handles, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
		Audit: observability.AuditConfig{
			Sink:         "file",
			FilePath:     path,
			FileSyncMode: "every_write",
		},
	}, observability.Context{
		StoragePool: store,
		Clock:       func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	for i := 0; i < 3; i++ {
		if err := handles.Audit.Emit(context.Background(), observability.AuditEvent{
			ActorKind: "system",
			Action:    "b34.fsync.test",
			Outcome:   "success",
		}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}

	// File must exist with three lines visible immediately. Pre-fix
	// the file existed but a power-loss between Emit return and the
	// kernel page-cache flush would lose recent rows; the durability
	// claim was silently broken.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	count := strings.Count(string(data), "\n")
	if count != 3 {
		t.Fatalf("expected 3 audit rows on disk; got %d (%q)", count, string(data))
	}
	if !strings.Contains(string(data), `"action":"b34.fsync.test"`) {
		t.Fatalf("expected action line on disk; got %q", string(data))
	}
}

// e2eAuditStore is a tiny in-memory Store used by these tests; the
// production observability.Start requires a non-nil StoragePool but the
// file-sink test does not need real Postgres.
type e2eAuditStore struct {
	rows []audit.Row
}

func newE2EAuditStore() *e2eAuditStore { return &e2eAuditStore{} }

func (s *e2eAuditStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.rows = append(s.rows, row)
	return nil
}

func (s *e2eAuditStore) LastChainHash(_ context.Context) ([]byte, error) {
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *e2eAuditStore) AcquireChainLock(_ context.Context) (func() error, error) {
	return func() error { return nil }, nil
}

func (s *e2eAuditStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	for _, r := range s.rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}
