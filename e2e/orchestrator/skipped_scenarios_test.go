// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import "testing"

// Scenario stubs.
//
// Each scenario below is a placeholder for a test that will be wired up
// once its required component lands. v1 of the e2e harness ships only
// the smoke_listener_ack and smoke_persist scenarios as runnable; the
// remainder are committed with t.Skip so the harness exposes the full
// scenario inventory and CI prints a manifest of pending coverage.
//
// When a scenario is wired up, drop the t.Skip line and add the test
// body. Do not move the function into a different file — keep one
// scenario per `t.Run`-like name to make the manifest readable.

func TestScenario_AdaptHL7ToFHIR(t *testing.T) {
	t.Skip("requires HL7 Message Processor implementation")
}

func TestScenario_FHIRScanRunnerEmitsResourceChange(t *testing.T) {
	t.Skip("requires FHIR Scan Runner implementation")
}

func TestScenario_VendorChangeFeedEmitsResourceChange(t *testing.T) {
	t.Skip("requires Vendor API Client implementation")
}

func TestScenario_TopicMatcherFanout(t *testing.T) {
	t.Skip("requires Topic Matcher implementation")
}

func TestScenario_SubscriptionEngineDeliversRestHook(t *testing.T) {
	t.Skip("requires Subscriptions Engine implementation")
}

func TestScenario_SubscriptionEngineDeliversWebSocket(t *testing.T) {
	t.Skip("requires Subscriptions Engine + WebSocket channel implementation")
}

func TestScenario_SubscriptionEngineDeliversEmail(t *testing.T) {
	t.Skip("requires Subscriptions Engine + Email channel implementation")
}

func TestScenario_CancelAndReplaceMergesIntoOneUpdate(t *testing.T) {
	t.Skip("requires HL7 Message Processor + pending_pairs cancel-and-replace implementation")
}

func TestScenario_CancelAndReplaceWindowExpires(t *testing.T) {
	t.Skip("requires HL7 Message Processor + pending_pairs reaper implementation")
}

func TestScenario_BurstHandledWithoutDrop(t *testing.T) {
	t.Skip("requires MLLP listener inflight cap + persist-then-ACK implementation")
}

func TestScenario_DeliveryRetryThenSuccess(t *testing.T) {
	t.Skip("requires Subscriptions Engine retry curve implementation")
}

func TestScenario_DeliveryDeadLetterAfterMax(t *testing.T) {
	t.Skip("requires Subscriptions Engine + dead_letters write implementation")
}

func TestScenario_SubscriptionStatusOperation(t *testing.T) {
	t.Skip("requires Subscriptions API $status implementation")
}

func TestScenario_SubscriptionEventsReplay(t *testing.T) {
	t.Skip("requires Subscriptions API $events implementation")
}

func TestScenario_AuditChainIsValid(t *testing.T) {
	t.Skip("requires audit_log emitter + chain verifier implementation")
}
