// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_TopicMatcherFanout covers the Topic Matcher's match +
// fanout path: a single upstream resource_change matched by a topic
// must fan out to every subscriber whose Subscription targets that
// topic. Two subscribers are registered against the same topic; both
// should observe the delivery.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_TopicMatcherFanout(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tagA := shortTagFor(t) + "-a"
	tagB := shortTagFor(t) + "-b"

	subA := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/service-request-scan-changed", tagA))
	subB := s.postSubscription(restHookSubscriptionJSON(s.stack,
		"http://example.org/topics/service-request-scan-changed", tagB))
	_, _ = subA, subB

	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "fanout-1",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-fanout"},
	})

	gotA := s.waitForRestHookNotifications(tagA, 1, 90*time.Second)
	gotB := s.waitForRestHookNotifications(tagB, 1, 90*time.Second)
	if len(gotA) == 0 || len(gotB) == 0 {
		t.Fatalf("TopicMatcherFanout: subscribers A=%d B=%d; matcher must fan out to every subscriber on the topic", len(gotA), len(gotB))
	}
}
