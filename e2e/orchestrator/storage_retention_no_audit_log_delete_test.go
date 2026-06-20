// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/retention"
)

// TestE2E_Retention_DoesNotDeleteAuditLog pins B-32. The retention
// sweeper had a row for audit_log that physically deleted hash-chained
// rows, breaking chain integrity. The fix removes audit_log from the
// allow-listed targets entirely. This test seeds 100 rows in audit_log
// with very old timestamps, runs Tick with all retention windows set
// to "delete everything older than 1ns", and asserts the audit_log
// count is unchanged.
func TestE2E_Retention_DoesNotDeleteAuditLog(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	const N = 100
	for i := 0; i < N; i++ {
		corr := uuid.New()
		if _, err := h.DB.Exec(ctx, `
			INSERT INTO audit_log
				(occurred_at, actor_kind, actor_id, action, target_kind,
				 target_id, outcome, correlation_id, chain_input, chain_hash, prior_hash)
			VALUES (now() - interval '90 days', 'system', 'retention-test', 'noop',
			        'subscription', $1, 'success', $2,
			        $3, $4, $5)`,
			uuid.NewString(), corr,
			[]byte(`{"a":1}`), []byte("chain-hash"), []byte("prior-hash"),
		); err != nil {
			t.Fatalf("insert audit row %d: %v", i, err)
		}
	}

	var before int
	if err := h.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before < N {
		t.Fatalf("expected at least %d rows in audit_log before sweep, got %d", N, before)
	}

	if err := retention.Tick(ctx, h.DB, retention.Config{
		BatchSize:       int32(N + 10),
		BatchPause:      time.Millisecond,
		Hl7MessageQueue: time.Nanosecond,
		Deliveries:      time.Nanosecond,
		DeadLetters:     time.Nanosecond,
		AuditLog:        time.Nanosecond, // must be ignored
	}); err != nil {
		t.Fatalf("retention.Tick: %v", err)
	}

	var after int
	if err := h.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("audit_log row count changed: before=%d after=%d (sweep MUST NOT delete audit rows)", before, after)
	}
}
