// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// smoke_listener_ack
//
// Goal: prove the v1 listener stub accepts an MLLP-framed message and
// returns an ACK^AA carrying the source message control id.
//
// Stimulus: scenario control-plane POST /scenarios/admit_patient.
// Assertion: the EHR-side mock receives an ACK^AA back from the stub
// listener (visible via the control plane's response payload).

func TestScenario_SmokeListenerACK(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The control plane in the EHR mock is wired to dial the stub
	// listener's MLLPTarget. Triggering the admit_patient scenario
	// emits one ADT^A01 frame and reads back an ACK.
	resp := postScenario(t, ctx, h, "/scenarios/admit_patient", map[string]any{
		"patient_id":  "MRN-SMOKE-1",
		"message_id":  "SMOKE-ACK-1",
		"trigger":     "A01",
		"family_name": "Smoke",
		"given_name":  "Test",
	})

	if !bytes.Contains(resp, []byte("MSA|AA|SMOKE-ACK-1")) {
		t.Fatalf("control-plane response missing MSA|AA|SMOKE-ACK-1; got: %s", resp)
	}
}
