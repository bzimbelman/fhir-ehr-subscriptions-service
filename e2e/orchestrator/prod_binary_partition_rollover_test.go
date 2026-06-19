// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"
)

// TestE2E_ProdBinary_PartitionRollover proves the production binary's
// startup wires storage.Start, which spawns the partition maintainer
// goroutine. Without that wiring (the bug story #95 fixes), migration
// 0001 only ever seeds 3 partitions; a binary that ran for >3 months
// without the maintainer would lose every insert at "no partition for
// value." This test confirms that booting cmd/fhir-subs:
//   - creates the next-month resource_changes / ehr_events partitions
//     on its own (proving the production wiring drives the runner);
//   - the runner-created partition is attached and writable — a
//     synthetic INSERT routed to next-month via created_month succeeds.
//
// Story #95 acceptance criterion: "boot binary, simulate 3-month
// wall-clock advance via the runner's clock injection, write a
// synthetic resource_change in the next month, assert insert
// succeeds." The literal +3-month wall-clock advance through the
// binary requires plumbing a clock seam through cmd/fhir-subs's
// argv/env (the binary today wires the runner with the default
// time.Now). That follow-on is captured in the runner-level
// integration test
// internal/infra/storage/integration_test.go::
//   TestIntegrationPartitionRunnerCreatesPartitionAfterFourMonthAdvance
// which uses cfg.Partitioning.Now to advance 4 months in-process.
// This e2e confirms the production binary path wires the runner so
// that property carries over end-to-end.
func TestE2E_ProdBinary_PartitionRollover(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "rollover-e2e",
		AdapterID:             "default",
		AuthAudience:          "https://api.test.local",
		Insecure:              true,
		AuthAllowInsecureJWKS: true,
		GracePeriod:           5 * time.Second,
	})
	t.Cleanup(func() { bin.Stop(t, 5*time.Second) })

	// Compute the suffix the partition maintainer's first Tick must
	// produce: next-month-relative-to-now. partition.createNextMonth
	// rounds today to the first of the month, then adds one month.
	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := thisMonth.AddDate(0, 1, 0)
	suffix := nextMonth.Format("2006_01")

	// Poll the harness DB. The runner's first Tick fires inside
	// storage.Start's spawned goroutine, so the partition appears
	// shortly after /healthz=200 (the gate startProdBinary blocks on).
	// We give it a generous budget to absorb scheduling jitter on
	// loaded CI hosts.
	deadline := time.Now().Add(20 * time.Second)
	var rcExists, ehrExists bool
	for time.Now().Before(deadline) {
		if err := h.DB.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`,
			"resource_changes_"+suffix,
		).Scan(&rcExists); err != nil {
			t.Fatalf("query resource_changes partition: %v", err)
		}
		if err := h.DB.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`,
			"ehr_events_"+suffix,
		).Scan(&ehrExists); err != nil {
			t.Fatalf("query ehr_events partition: %v", err)
		}
		if rcExists && ehrExists {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !rcExists {
		t.Fatalf("expected partition resource_changes_%s after binary startup; not found — storage.Start did not wire the partition maintainer", suffix)
	}
	if !ehrExists {
		t.Fatalf("expected partition ehr_events_%s after binary startup; not found — storage.Start did not wire the partition maintainer", suffix)
	}

	// Write a synthetic resource_change row routed to the *next-month*
	// partition the runner just created. Acceptance criterion: "write a
	// synthetic resource_change in the next month, assert insert
	// succeeds." The v0 BEFORE-INSERT trigger overrides created_month
	// to date_trunc('month', now()), which simulates "today" routing.
	// Once production rolls past the month boundary the trigger will
	// naturally produce the next-month value, so for the test we
	// disable the trigger for this single statement and write directly
	// with created_month=nextMonth — exercising the runner-attached
	// partition as the real wall-clock-advanced production path will.
	partTable := "resource_changes_" + suffix
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatalf("disable trigger for tx: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO `+partTable+`
		 (adapter_id, correlation_id, resource_type, change_kind,
		  resource, occurred_at, key_version, created_month)
		 VALUES ('default', gen_random_uuid(), 'ServiceRequest',
		         'create', $1, now(), 1, $2)`,
		[]byte(`{"resourceType":"ServiceRequest","id":"e2e-rollover"}`),
		nextMonth,
	); err != nil {
		t.Fatalf("insert into next-month partition %s: %v — runner-created partition is not writable",
			partTable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Confirm the row is visible from the parent partitioned relation
	// — proves the runner-created partition is properly attached, not
	// just a freestanding table.
	var n int
	if err := h.DB.QueryRow(ctx,
		`SELECT count(*) FROM resource_changes
		 WHERE created_month = $1::date AND resource_type = 'ServiceRequest'`,
		nextMonth,
	).Scan(&n); err != nil {
		t.Fatalf("count next-month rows: %v", err)
	}
	if n < 1 {
		t.Errorf("expected the synthetic row to be visible via parent at created_month=%s; got count=%d (runner-created partition not properly attached?)",
			nextMonth.Format("2006-01-02"), n)
	}
}
