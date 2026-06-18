// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// resetPipelineTables truncates every table the pipeline reads or
// writes so a scenario starts from a clean slate even though the test
// package shares one Postgres container. Call this at the top of every
// scenario test that constructs its own Pipeline.
//
// Order matters: subscriptions has a foreign key to auth_clients;
// deliveries has FKs to subscriptions and ehr_events; ws_binding_tokens
// has an FK to subscriptions; pending_pairs has an FK to
// hl7_message_queue. We use TRUNCATE ... CASCADE to resolve in one
// statement.
func resetPipelineTables(t *testing.T, ctx context.Context, h *Harness) {
	t.Helper()
	tables := []string{
		"hl7_message_queue",
		"pending_pairs",
		"resource_changes",
		"ehr_events",
		"subscriptions",
		"deliveries",
		"dead_letters",
		"ws_binding_tokens",
		"subscription_topics",
		"auth_clients",
		"adapter_state",
		"audit_log",
	}
	stmt := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	if _, err := h.DB.Exec(ctx, stmt); err != nil {
		t.Fatalf("resetPipelineTables: %v", err)
	}
	// Reset standalone sequences not reached by RESTART IDENTITY (they
	// are owned-but-shared and TRUNCATE wouldn't touch them).
	for _, seq := range []string{
		"ehr_events_event_number_seq",
		"resource_changes_sequence_seq",
	} {
		if _, err := h.DB.Exec(ctx, "ALTER SEQUENCE "+seq+" RESTART WITH 1"); err != nil {
			t.Fatalf("resetPipelineTables: reset seq %s: %v", seq, err)
		}
	}
}
