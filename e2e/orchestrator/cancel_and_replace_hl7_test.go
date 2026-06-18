// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_cancel_and_replace_hl7 drives an ORM cancel + replacement
// pair through the existing scenario control plane. The hl7processor's
// pending-pairs correlator collapses the (cancel, replace) pair into a
// single resource_changes row reflecting the post-replacement state, so
// the rest-hook subscriber observes ONE delivery — not two.
func TestScenario_cancel_and_replace_hl7(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 90*time.Second)
	defer cancel()

	// Scripted adapter that recognizes the test-suite's cancel + new
	// control codes and propagates a single correlation key so the
	// pending-pairs correlator pairs them.
	const corrKey = "ORC-2:placeholder-1234"
	adapter := &hpipe.ScriptedAdapter{
		ResourceType: "ServiceRequest",
		ClassifyFn: func(raw []byte) spi.Classification {
			// Inspect the HL7 control code. Test fixtures emit the
			// control code in the ORC|<code>| segment. The scenario's
			// cancel_and_replace control plane uses ORC-1 = "CA" for
			// cancel and "NW" for new.
			if containsBytes(raw, []byte("ORC|CA|")) {
				return spi.Classification{Kind: spi.ChangeDelete, CorrelationKey: corrKey}
			}
			return spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: corrKey}
		},
		BodyFn: func(raw []byte) []byte {
			if containsBytes(raw, []byte("ORC|CA|")) {
				return []byte(`{"resourceType":"ServiceRequest","id":"old","status":"revoked"}`)
			}
			return []byte(`{"resourceType":"ServiceRequest","id":"new","status":"active"}`)
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
			Adapter:               adapter,
			AdapterID:             "default",
			CorrelationHoldWindow: 30 * time.Second, // long enough to avoid reaper
			Channels: map[string]channel.Channel{
				"rest-hook": restCh,
			},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/order-changed",
			Version: "1.0.0",
			Title:   "Order changed",
			Body:    []byte(serviceRequestTopicJSON),
		}},
	})
	_ = fx

	// Subscribe to the topic.
	tag := shortTag("c2r-hl7")
	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/order-changed", tlsSrv.URL, tag, nil))

	// Drive the (cancel, replace) pair through the existing EHR control plane.
	postScenario(t, ctx, h, "/scenarios/cancel_and_replace_order", map[string]any{
		"placer_order_id":        "PO-1",
		"filler_order_id":        "FO-1",
		"patient_id":             "MRN-c2r-hl7",
		"cancel_message_id":      "CANCEL-1-" + tag,
		"replacement_message_id": "REPLACE-1-" + tag,
	})

	got, err := WaitForNotification(ctx, h, tag, 60*time.Second)
	if err != nil {
		dumpAndFail(t, ctx, h, subID, "wait notification: %v", err)
	}
	if got.SubscriptionID != tag {
		t.Errorf("journal subscription id: got %q want %q", got.SubscriptionID, tag)
	}
	// Assertion: only ONE delivery in the journal for this tag.
	all := h.MockSub.RestHook.Received(tag)
	if len(all) != 1 {
		t.Errorf("expected exactly 1 notification, got %d", len(all))
	}
	_ = uuid.Nil // silence unused-import guard if removed
}

// containsBytes is a tiny helper: bytes.Contains imported via stdlib
// would be cleaner but we want this file self-contained at the
// orchestrator scope.
func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

const serviceRequestTopicJSON = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/order-changed",
  "version": "1.0.0",
  "title": "Order changed",
  "status": "active",
  "resourceTrigger": [{
    "resource": "ServiceRequest",
    "supportedInteraction": ["create", "update", "delete"]
  }]
}`
