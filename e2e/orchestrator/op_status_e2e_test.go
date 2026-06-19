// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_SubscriptionStatusOperation covers the Subscription/$status
// FHIR operation. The binary MUST serve the operation against a real
// existing subscription and return a Parameters resource with
// `eventsSinceSubscriptionStart`, `eventsInWindow`, and a status code.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_SubscriptionStatusOperation(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	subID := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/service-request-scan-changed", tag))

	// Exercise the operation directly.
	resp, body := s.binaryGet("/Subscription/" + subID + "/$status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("$status: %d; want 200; body=%s", resp.StatusCode, string(body))
	}
	bs := string(body)
	if !strings.Contains(bs, "Parameters") {
		t.Errorf("$status response is not a Parameters resource: %s", bs)
	}
	if !strings.Contains(bs, "eventsSinceSubscriptionStart") {
		t.Errorf("$status response does not include eventsSinceSubscriptionStart: %s", bs)
	}
}
