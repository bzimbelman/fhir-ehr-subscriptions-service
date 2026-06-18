// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// dumpPipelineState logs counts at every pipeline-stage table for the
// given subscription so a failing scenario tells us exactly which stage
// dropped the message. Read with the failure message: a non-zero
// hl7_message_queue but zero resource_changes means the HL7 processor
// stalled; non-zero ehr_events but zero deliveries means the submatcher
// did not see a matching active subscription; etc.
func dumpPipelineState(t *testing.T, ctx context.Context, h *Harness, subID uuid.UUID) {
	t.Helper()
	rows := []struct {
		name string
		sql  string
	}{
		{"hl7_message_queue total", `SELECT count(*) FROM hl7_message_queue`},
		{"hl7_message_queue processed", `SELECT count(*) FROM hl7_message_queue WHERE processed`},
		{"resource_changes total", `SELECT count(*) FROM resource_changes`},
		{"resource_changes processed", `SELECT count(*) FROM resource_changes WHERE processed`},
		{"ehr_events total", `SELECT count(*) FROM ehr_events`},
		{"ehr_events processed", `SELECT count(*) FROM ehr_events WHERE processed`},
		{"subscriptions total", `SELECT count(*) FROM subscriptions`},
		{"subscriptions active", `SELECT count(*) FROM subscriptions WHERE status='active'`},
		{"subscription_topics active", `SELECT count(*) FROM subscription_topics WHERE status='active'`},
		{"deliveries total", `SELECT count(*) FROM deliveries`},
		{"deliveries pending", `SELECT count(*) FROM deliveries WHERE status='pending'`},
		{"deliveries delivered", `SELECT count(*) FROM deliveries WHERE status='delivered'`},
		{"deliveries failed", `SELECT count(*) FROM deliveries WHERE status='failed'`},
		{"deliveries dead", `SELECT count(*) FROM deliveries WHERE status='dead'`},
		{"dead_letters total", `SELECT count(*) FROM dead_letters`},
	}
	for _, r := range rows {
		var n int
		if err := h.DB.QueryRow(ctx, r.sql).Scan(&n); err != nil {
			t.Logf("dump %s: query err: %v", r.name, err)
			continue
		}
		t.Logf("dump %-32s = %d", r.name, n)
	}

	var subStatus, subTopic, subError string
	if err := h.DB.QueryRow(ctx,
		`SELECT status, topic_url, COALESCE(error,'') FROM subscriptions WHERE id=$1`,
		subID).Scan(&subStatus, &subTopic, &subError); err != nil {
		t.Logf("dump subscription %s: %v", subID, err)
	} else {
		t.Logf("dump subscription %s status=%s topic=%s error=%q",
			subID, subStatus, subTopic, subError)
	}

	rowsq, err := h.DB.Query(ctx, `
		SELECT id, subscription_id, status, attempts, last_error
		  FROM deliveries
		 WHERE subscription_id=$1
		 ORDER BY created_at DESC
		 LIMIT 5`, subID)
	if err == nil {
		defer rowsq.Close()
		for rowsq.Next() {
			var id, ssub uuid.UUID
			var status, lastErr string
			var attempts int
			if err := rowsq.Scan(&id, &ssub, &status, &attempts, &lastErr); err != nil {
				t.Logf("scan delivery: %v", err)
				continue
			}
			t.Logf("dump delivery id=%s status=%s attempts=%d last_err=%q",
				id, status, attempts, lastErr)
		}
	}

	dlRows, err := h.DB.Query(ctx, `
		SELECT id, kind, source_table, reason
		  FROM dead_letters
		 ORDER BY created_at DESC
		 LIMIT 5`)
	if err == nil {
		defer dlRows.Close()
		for dlRows.Next() {
			var id uuid.UUID
			var kind, srcTable, reason string
			if err := dlRows.Scan(&id, &kind, &srcTable, &reason); err != nil {
				t.Logf("scan dead_letter: %v", err)
				continue
			}
			t.Logf("dump dead_letter id=%s kind=%s src=%s reason=%q",
				id, kind, srcTable, reason)
		}
	}
}
