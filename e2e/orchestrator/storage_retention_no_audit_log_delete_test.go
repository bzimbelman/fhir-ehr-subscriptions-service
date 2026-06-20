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
// to "delete everything older than 1ns", and asserts NONE of the
// seeded rows were deleted.
//
// OP #339: the harness is shared across t.Parallel() tests that all
// write audit_log rows. A whole-table count comparison (before/after)
// races with concurrent writers and produces both spurious increases
// (other test inserts) and decreases (other test resets). The
// assertion that actually pins B-32 is "every row this test inserted
// is still present after Tick"; we tag each row with a unique actor_id
// and count exactly that subset, which is unaffected by other tests.
func TestE2E_Retention_DoesNotDeleteAuditLog(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	// Unique actor_id per test invocation so concurrent tests writing
	// audit rows do not mutate the count we are watching.
	actor := "retention-test-" + uuid.NewString()

	const N = 100
	for i := 0; i < N; i++ {
		corr := uuid.New()
		if _, err := h.DB.Exec(ctx, `
			INSERT INTO audit_log
				(occurred_at, actor_kind, actor_id, action, target_kind,
				 target_id, outcome, correlation_id, chain_input, chain_hash, prior_hash)
			VALUES (now() - interval '90 days', 'system', $1, 'noop',
			        'subscription', $2, 'success', $3,
			        $4, $5, $6)`,
			actor, uuid.NewString(), corr,
			[]byte(`{"a":1}`), []byte("chain-hash"), []byte("prior-hash"),
		); err != nil {
			t.Fatalf("insert audit row %d: %v", i, err)
		}
	}

	var before int
	if err := h.DB.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE actor_id = $1`, actor,
	).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before != N {
		t.Fatalf("expected exactly %d rows in audit_log before sweep, got %d", N, before)
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
	if err := h.DB.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE actor_id = $1`, actor,
	).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("audit_log row count for actor %q changed: before=%d after=%d (sweep MUST NOT delete audit rows)", actor, before, after)
	}
}
