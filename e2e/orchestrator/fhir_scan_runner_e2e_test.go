// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_FHIRScanRunnerEmitsResourceChange covers the FHIR Scan
// Runner's emit path. The binary's scan runner polls the upstream HAPI
// FHIR server on its configured cadence; when it observes a
// previously-unseen resource it emits a resource_changes row, which the
// pipeline fans out to the subscriber.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_FHIRScanRunnerEmitsResourceChange(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/service-request-scan-changed", tag))
	_ = subID

	// Seed a NEW resource on HAPI after subscription is live so the scan
	// runner observes a delta.
	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "scan-emit-1",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-scan"},
	})

	got := s.waitForRestHookNotifications(tag, 1, 120*time.Second)
	if len(got) == 0 {
		t.Fatalf("FHIRScanRunnerEmits: rest-hook subscriber received 0 notifications; scan runner not emitting resource_changes against real HAPI server")
	}
}
