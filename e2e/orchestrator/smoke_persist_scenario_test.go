// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"
)

// smoke_persist
//
// Goal: prove the persistence-then-ACK contract: when the stub listener
// commits a row to hl7_message_queue, the inserted row is visible in
// the contract table with mllp_message_id == MSH-10.
//
// Stimulus: scenario control-plane POST /scenarios/admit_patient.
// Assertion:
//   * The hl7_message_queue row count grows by exactly 1 across the
//     scenario.
//   * The new row has the source message control id in
//     mllp_message_id and the listener_endpoint configured on the stub.

func TestScenario_SmokePersist(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	before, err := h.QueueSize(ctx)
	if err != nil {
		t.Fatalf("queue size before: %v", err)
	}

	postScenario(t, ctx, h, "/scenarios/admit_patient", map[string]any{
		"patient_id": "MRN-SMOKE-2",
		"message_id": "SMOKE-PERSIST-1",
		"trigger":    "A01",
	})

	// Poll for the row — the persist transaction commits before the ACK
	// is written, so by the time the control-plane response is back the
	// row should be visible. Allow a small grace window for clock skew.
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		after, err = h.QueueSize(ctx)
		if err != nil {
			t.Fatalf("queue size after: %v", err)
		}
		if after == before+1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if after != before+1 {
		t.Fatalf("expected hl7_message_queue to grow by 1, got before=%d after=%d", before, after)
	}

	var (
		mllpMsgID        string
		listenerEndpoint string
	)
	err = h.DB.QueryRow(ctx, `
		select mllp_message_id, listener_endpoint
		  from hl7_message_queue
		 where mllp_message_id = $1
	`, "SMOKE-PERSIST-1").Scan(&mllpMsgID, &listenerEndpoint)
	if err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if mllpMsgID != "SMOKE-PERSIST-1" {
		t.Fatalf("mllp_message_id: got %q want SMOKE-PERSIST-1", mllpMsgID)
	}
	if listenerEndpoint != "stub-feed" {
		t.Fatalf("listener_endpoint: got %q want stub-feed", listenerEndpoint)
	}
}
