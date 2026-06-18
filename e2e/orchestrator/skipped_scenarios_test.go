// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import "testing"

// Scenario stubs.
//
// The 13 scenarios named in the e2e-harness LLD's "Scenario taxonomy"
// table are the merge gate. Each scenario name maps to a Go test
// function on this package — that way `go test ./e2e/orchestrator/...`
// reports the manifest as a single list, and a future component PR
// unblocks one or more scenarios by deleting `t.Skip()` and asserting.
//
// LLD-named scenarios (13). Two are runnable in v1 (smoke pair); 11
// remain skip-stubs until their owning components land. The skip
// message names the specific component(s) the scenario depends on so
// component-PR authors can grep for "requires" and find their hooks.
//
//   smoke_listener_ack         — runnable; see smoke_listener_ack_scenario_test.go
//   smoke_persist              — runnable; see smoke_persist_scenario_test.go
//   single_event_to_resthook
//   cancel_and_replace_hl7
//   cancel_and_replace_scan
//   subscription_filter_drop
//   events_replay
//   wss_delivery_and_reconnect
//   email_v1_smtp
//   graceful_shutdown
//   restart_recovery
//   backpressure
//   auth_revocation
//
// Extra non-LLD partition-by-component stubs (kept for component PR
// hooks): AdaptHL7ToFHIR, FHIRScanRunnerEmitsResourceChange,
// VendorChangeFeedEmitsResourceChange, TopicMatcherFanout,
// CancelAndReplaceWindowExpires, DeliveryRetryThenSuccess,
// DeliveryDeadLetterAfterMax, SubscriptionStatusOperation,
// AuditChainIsValid. These are not part of the merge gate but they
// help component PRs land partial proof of behavior before their
// owning LLD scenario goes green.

// --- LLD-named scenarios (the 13-scenario merge-gate manifest) -----------

// TestScenario_single_event_to_resthook lives in
// single_event_to_resthook_test.go now that the pipeline + channels +
// API are on main and the harness wires them end-to-end.

// TestScenario_cancel_and_replace_hl7 lives in
// cancel_and_replace_hl7_test.go now that the HL7 processor's pending-
// pairs correlator is on main and the harness can drive a (cancel,
// replace) pair through the production pipeline.

// TestScenario_cancel_and_replace_scan covers the LLD's
// `cancel_and_replace_scan`: FHIR Scan Runner sees an updated
// ServiceRequest; diff against prior snapshot collapses to one
// resource_changes row.
func TestScenario_cancel_and_replace_scan(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires FHIR Scan Runner snapshot diff + Topic Matcher + Subscriptions Engine + rest-hook channel")
}

// TestScenario_subscription_filter_drop lives in
// subscription_filter_drop_test.go.

// TestScenario_events_replay lives in events_replay_test.go.

// TestScenario_wss_delivery_and_reconnect lives in
// wss_delivery_and_reconnect_test.go.

// TestScenario_email_v1_smtp lives in email_v1_smtp_test.go.

// TestScenario_graceful_shutdown lives in graceful_shutdown_test.go.

// TestScenario_restart_recovery lives in restart_recovery_test.go.

// TestScenario_backpressure lives in backpressure_test.go.

// TestScenario_auth_revocation lives in auth_revocation_test.go.

// --- Extra non-LLD partition-by-component stubs --------------------------

// TestScenario_AdaptHL7ToFHIR — partition-by-component placeholder for
// the HL7 Message Processor's translate-and-emit path. Not part of the
// LLD's 13-scenario gate; useful as a hook for component-level e2e.
func TestScenario_AdaptHL7ToFHIR(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires HL7 Message Processor implementation (extra non-LLD stub)")
}

// TestScenario_FHIRScanRunnerEmitsResourceChange — partition-by-component
// placeholder for the FHIR Scan Runner's emit path.
func TestScenario_FHIRScanRunnerEmitsResourceChange(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires FHIR Scan Runner implementation (extra non-LLD stub)")
}

// TestScenario_VendorChangeFeedEmitsResourceChange — partition-by-
// component placeholder for the Vendor API Client's change-feed path.
func TestScenario_VendorChangeFeedEmitsResourceChange(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Vendor API Client implementation (extra non-LLD stub)")
}

// TestScenario_TopicMatcherFanout — partition-by-component placeholder
// for the Topic Matcher's match + fanout path.
func TestScenario_TopicMatcherFanout(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Topic Matcher implementation (extra non-LLD stub)")
}

// TestScenario_CancelAndReplaceWindowExpires — partition-by-component
// placeholder for the pending_pairs reaper.
func TestScenario_CancelAndReplaceWindowExpires(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires HL7 Message Processor + pending_pairs reaper implementation (extra non-LLD stub)")
}

// TestScenario_DeliveryRetryThenSuccess — partition-by-component
// placeholder for the engine's retry curve.
func TestScenario_DeliveryRetryThenSuccess(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Subscriptions Engine retry curve implementation (extra non-LLD stub)")
}

// TestScenario_DeliveryDeadLetterAfterMax — partition-by-component
// placeholder for engine + dead_letters writes.
func TestScenario_DeliveryDeadLetterAfterMax(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Subscriptions Engine + dead_letters write implementation (extra non-LLD stub)")
}

// TestScenario_SubscriptionStatusOperation — partition-by-component
// placeholder for Subscription.$status.
func TestScenario_SubscriptionStatusOperation(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Subscriptions API $status implementation (extra non-LLD stub)")
}

// TestScenario_AuditChainIsValid — partition-by-component placeholder
// for audit_log + chain verifier.
func TestScenario_AuditChainIsValid(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires audit_log emitter + chain verifier implementation (extra non-LLD stub)")
}
