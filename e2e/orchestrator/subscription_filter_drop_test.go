// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_subscription_filter_drop creates two subscriptions on the
// same topic: one with a filterBy clause that does NOT match the
// scripted body and one with no filter. Drives a single message and
// asserts only the no-filter subscription receives a notification.
func TestScenario_subscription_filter_drop(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 90*time.Second)
	defer cancel()

	// Scripted adapter that emits a body with status=active.
	adapter := &hpipe.ScriptedAdapter{
		ResourceType: "ServiceRequest",
		BodyFn: func(_ []byte) []byte {
			return []byte(`{"resourceType":"ServiceRequest","id":"sr-1","status":"active"}`)
		},
	}

	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })
	restCh, err := resthook.New(resthook.Options{HTTPClient: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		preBuiltTLS: tlsSrv,
		pipelineConfig: hpipe.PipelineConfig{
			Adapter:   adapter,
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/order-changed",
			Version: "1.0.0",
			Title:   "Order changed",
			Body:    []byte(serviceRequestTopicJSON),
		}},
	})

	matchingTag := shortTag("match")
	droppedTag := shortTag("drop")

	// Matching subscription: no filterBy.
	subMatch := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/order-changed", tlsSrv.URL, matchingTag, nil))

	// Dropped subscription: filterBy status=cancelled (won't match
	// status=active body).
	subDrop := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/order-changed", tlsSrv.URL, droppedTag,
			[]map[string]any{
				{"filterParameter": "status", "value": "cancelled"},
			}))

	// Drive one message.
	driveAdmit(t, ctx, h, "FILT-1-"+matchingTag, "MRN-FILT-1", "A01")

	// The matching sub should receive.
	if _, err := WaitForNotification(ctx, h, matchingTag, 30*time.Second); err != nil {
		dumpAndFail(t, ctx, h, subMatch, "matching sub: wait notification: %v", err)
	}

	// The dropped sub should NOT receive — we wait briefly to give the
	// pipeline time to attempt-and-skip, then assert the journal stays
	// empty for that tag.
	time.Sleep(2 * time.Second)
	got := h.MockSub.RestHook.Received(droppedTag)
	if len(got) != 0 {
		dumpAndFail(t, ctx, h, subDrop, "filter-drop sub got %d notifications, expected 0", len(got))
	}
}
