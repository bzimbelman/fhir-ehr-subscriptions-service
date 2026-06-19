// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_VendorChangeFeedEmitsResourceChange covers the Vendor
// API Client's change-feed path. Many vendors expose a change feed
// (e.g. Cerner Bulk Data, Epic FHIR Subscription topics). The binary's
// vendor adapter polls that feed; when it sees an updated resource it
// emits a resource_changes row.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_VendorChangeFeedEmitsResourceChange(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/vendor-change-feed", tag))
	_ = subID

	// The realstack uses HAPI as the change-feed backend in lieu of a
	// real vendor sandbox; the vendor adapter under test must be
	// configured at boot to read from HAPI.
	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "vendor-feed-1",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-vendor"},
	})

	got := s.waitForRestHookNotifications(tag, 1, 120*time.Second)
	if len(got) == 0 {
		t.Fatalf("VendorChangeFeedEmits: rest-hook subscriber received 0 notifications; vendor change-feed adapter not wired against real upstream")
	}
}
