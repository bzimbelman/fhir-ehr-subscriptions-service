// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/outbox"
)

func TestStorageConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "postgres://localhost/x",
		KeyVersions: map[int32][]byte{1: make32()},
		ActiveKey:   1,
	}
	cfg.ApplyDefaults()
	if cfg.Pool.MaxConnections == 0 {
		t.Error("pool defaults not applied")
	}
	if cfg.Retention.Hl7MessageQueue == 0 {
		t.Error("retention defaults not applied")
	}
	if cfg.Partitioning.RunInterval == 0 {
		t.Error("partition defaults not applied")
	}
}

func TestStorageStartReturnsErrorWithBadURL(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "this-is-bogus://nope",
		KeyVersions: map[int32][]byte{1: make32()},
		ActiveKey:   1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := storage.Start(ctx, cfg, storage.Context{}); err == nil {
		t.Fatal("expected error from bad URL")
	}
}

func TestStorageStartRequiresKeys(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "postgres://localhost/x",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := storage.Start(ctx, cfg, storage.Context{}); err == nil {
		t.Fatal("expected error when no keys configured")
	}
}

// TestStorageConfigSixRetentionWindows asserts the production config
// surfaces all six retention windows the architecture doc requires:
// four row-deletion sweeps (hl7_message_queue, deliveries, dead_letters,
// audit_log accepted-for-back-compat) plus two partition-rotation
// retentions (resource_changes, ehr_events). Story #95 acceptance
// criterion: "Default storage.RetentionConfig MUST be parsed from
// storage.retention.* in YAML (six retention windows per the
// architecture doc)."
func TestStorageConfigSixRetentionWindows(t *testing.T) {
	t.Parallel()

	cfg := storage.Config{
		PostgresURL: "postgres://localhost/x",
		KeyVersions: map[int32][]byte{1: make32()},
		ActiveKey:   1,
	}
	cfg.ApplyDefaults()

	// All four row-deletion windows must have non-zero defaults.
	if cfg.Retention.Hl7MessageQueue <= 0 {
		t.Errorf("Retention.Hl7MessageQueue default = %v, want >0", cfg.Retention.Hl7MessageQueue)
	}
	if cfg.Retention.Deliveries <= 0 {
		t.Errorf("Retention.Deliveries default = %v, want >0", cfg.Retention.Deliveries)
	}
	if cfg.Retention.DeadLetters <= 0 {
		t.Errorf("Retention.DeadLetters default = %v, want >0", cfg.Retention.DeadLetters)
	}
	if cfg.Retention.AuditLog <= 0 {
		t.Errorf("Retention.AuditLog default = %v, want >0", cfg.Retention.AuditLog)
	}
	// Two partition-rotation windows must have non-zero defaults.
	if cfg.Partitioning.ResourceChangesRetention <= 0 {
		t.Errorf("Partitioning.ResourceChangesRetention default = %v, want >0", cfg.Partitioning.ResourceChangesRetention)
	}
	if cfg.Partitioning.EhrEventsRetention <= 0 {
		t.Errorf("Partitioning.EhrEventsRetention default = %v, want >0", cfg.Partitioning.EhrEventsRetention)
	}
}

// TestStorageExposesOutboxAndClaimAccessors asserts the storage package
// re-exports outbox.Run via a non-generic Storage method and re-exports
// claim.Unprocessed via a generic package-level helper, so the four
// sub-packages (outbox, claim, partition, retention) reach production
// callers via a single import. Story #95 acceptance criterion: "All
// four sub-packages MUST be reachable from a single
// go list -deps ./cmd/fhir-subs."
func TestStorageExposesOutboxAndClaimAccessors(t *testing.T) {
	t.Parallel()

	// Compile-time check: Storage.Outbox is callable with an outbox.Tx
	// closure (which forces the outbox package into the package's
	// dependency graph). The runtime call site is exercised by the
	// integration suite with a real pool. Wrapped in a func value so the
	// closure is well-typed at compile time without ever being invoked.
	_ = func(s *storage.Storage) (outbox.Outcome, error) {
		return s.Outbox(context.Background(), func(_ context.Context, _ outbox.Tx) error { return nil })
	}

	// ClaimUnprocessed is a generic package-level re-export (Go does not
	// allow generic methods on a concrete struct, so a package function
	// is the canonical shape). Reference it to fail compilation if the
	// symbol is removed.
	_ = storage.ClaimUnprocessed[int]
}

func make32() []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}
