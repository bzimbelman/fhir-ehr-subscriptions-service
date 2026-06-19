// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_cancel_and_replace_scan covers the LLD's
// `cancel_and_replace_scan` merge-gate scenario: the FHIR Scan Runner
// observes an updated ServiceRequest on the upstream HAPI FHIR server,
// the diff against its prior snapshot collapses the (cancel, replace)
// pair into one resource_changes row, and the rest-hook test subscriber
// receives exactly ONE delivery.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_cancel_and_replace_scan(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	// Seed the upstream FHIR server with the original ServiceRequest.
	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "scan-c2r-orig",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-c2r"},
	})

	// Subscribe via the prod binary's HTTP API to the
	// resource-changed-on-scan topic. The topic URL matches the binary's
	// shipped catalog; if not yet wired, POST will 4xx and the test
	// fails RED.
	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/service-request-scan-changed", tag))
	_ = subID

	// Replace the resource: cancel + new in one upstream update.
	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "scan-c2r-orig",
		"status":       "revoked",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-c2r"},
	})
	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "scan-c2r-new",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-c2r"},
	})

	got := s.waitForRestHookNotifications(tag, 1, 90*time.Second)
	if len(got) != 1 {
		t.Fatalf("cancel_and_replace_scan: expected exactly 1 delivery to rest-hook subscriber; got %d", len(got))
	}
}
