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

// TestScenario_single_event_to_resthook covers the LLD's
// `single_event_to_resthook`: one ADT in, one rest-hook POST out.
func TestScenario_single_event_to_resthook(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires HL7 Message Processor + Topic Matcher + Subscriptions Engine + rest-hook channel + Subscriptions API")
}

// TestScenario_cancel_and_replace_hl7 covers the LLD's
// `cancel_and_replace_hl7`: ORM + ORC-3 cancellation pair collapses to
// one resource_changes row, one notification.
func TestScenario_cancel_and_replace_hl7(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires HL7 Message Processor + pending_pairs correlation + Topic Matcher + Subscriptions Engine + rest-hook channel")
}

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

// TestScenario_subscription_filter_drop covers the LLD's
// `subscription_filter_drop`: a non-matching change does not produce a
// delivery; a parallel matching subscription receives normally.
func TestScenario_subscription_filter_drop(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Topic Matcher filterBy + Subscriptions Engine + rest-hook channel")
}

// TestScenario_events_replay covers the LLD's `events_replay`:
// `Subscription/$events?eventsSinceNumber=N` returns the same Bundle
// idempotently; ehr_events.event_number is monotonic.
func TestScenario_events_replay(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Subscriptions API $events + Subscriptions Engine + storage event_number monotonicity")
}

// TestScenario_wss_delivery_and_reconnect covers the LLD's
// `wss_delivery_and_reconnect`: WSS subscriber receives a notification,
// orchestrator forces a server-side disconnect, subscriber re-handshakes
// with the same binding token, missed events are replayed in order.
func TestScenario_wss_delivery_and_reconnect(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires WebSocket channel + binding-token reissue + Subscriptions Engine replay-on-reconnect")
}

// TestScenario_email_v1_smtp covers the LLD's `email_v1_smtp`: an
// email-channel subscription delivers the notification Bundle as an
// SMTP message; the IMAP inbox shows one message with the right MIME
// type, subject, and notification body.
func TestScenario_email_v1_smtp(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Email channel (SMTP-only per ADR 0010) + Subscriptions Engine")
}

// TestScenario_graceful_shutdown covers the LLD's `graceful_shutdown`:
// a SIGTERM mid-scenario drains in-flight messages, ACKs the EHR for
// everything persisted, refuses new connections during drain, exits
// within the configured grace period; the journal shows no
// half-delivered notifications.
func TestScenario_graceful_shutdown(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires lifecycle SIGTERM handler + MLLP listener drain + every adapter + every channel")
}

// TestScenario_restart_recovery covers the LLD's `restart_recovery`:
// the SUT is killed mid-scenario; on restart, every persisted row
// resumes processing exactly once; no duplicate notifications appear;
// audit-log hash chain re-walks clean.
func TestScenario_restart_recovery(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires lifecycle restart-recovery + storage idempotency + every adapter + every channel + audit-log chain re-walk")
}

// TestScenario_backpressure covers the LLD's `backpressure`: the
// subscriber's rest-hook receiver returns 503 for the first N attempts;
// the SUT retries with the configured backoff, eventually succeeds, and
// the journal shows exactly one successful delivery in the right
// position. NACK / drop semantics on the EHR side are exercised when
// the persistor is held under load.
func TestScenario_backpressure(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires Subscriptions Engine retry curve + rest-hook channel + MLLP listener inflight-cap + NACK/drop semantics")
}

// TestScenario_auth_revocation covers the LLD's `auth_revocation`: a
// subscription whose bearer token is revoked mid-scenario receives a
// 401 from the subscriber, the SUT surfaces the failure on
// Subscription.$status, and stops attempting deliveries until the
// operator re-enables it.
func TestScenario_auth_revocation(t *testing.T) {
	// requireHarness is intentionally NOT called: skip-stubs should
	// report SKIP regardless of Docker availability, so the merge-gate
	// manifest is stable across local/CI/no-docker environments.
	t.Skip("requires rest-hook channel auth + Subscriptions Engine error-status transition + Subscriptions API $status")
}

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
