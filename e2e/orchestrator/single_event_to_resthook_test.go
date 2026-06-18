// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// passthroughTopic is the SubscriptionTopic the harness seeds for any
// scenario that needs a topic that matches every Bundle the default
// adapter emits. The default adapter wraps raw HL7 in a
// `Bundle/collection`, which the matcher's resourceTrigger sees as
// `resourceType=Bundle`.
const passthroughTopicJSON = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topics/hl7-passthrough",
  "version": "1.0.0",
  "title": "HL7 passthrough",
  "status": "active",
  "resourceTrigger": [{
    "resource": "Bundle",
    "supportedInteraction": ["create", "update", "delete"]
  }]
}`

// TestScenario_single_event_to_resthook drives the full pipeline:
// MLLP -> hl7processor -> matcher -> submatcher -> builder -> scheduler
// -> rest-hook channel -> mock subscriber. The assertion is that the
// mocksub journal records exactly one POST under the subscription's
// configured /hook/{tag} path.
func TestScenario_single_event_to_resthook(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	// Build TLS rest-hook listener that wraps the mocksub receiver.
	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("start tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })

	// Real rest-hook channel pointed at our self-signed TLS server.
	restCh, err := resthook.New(resthook.Options{
		HTTPClient: tlsSrv.Client(),
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

	// Pipeline: every stage running, rest-hook registered.
	pipe, err := hpipe.NewPipeline(h.DB, hpipe.PipelineConfig{
		AdapterID: "default",
		Channels: map[string]channel.Channel{
			"rest-hook": restCh,
		},
	})
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	if err := pipe.SeedTopic(ctx, hpipe.TopicFixture{
		URL:     "http://example.org/topics/hl7-passthrough",
		Version: "1.0.0",
		Title:   "HL7 passthrough",
		Body:    []byte(passthroughTopicJSON),
	}); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	if err := pipe.Start(ctx); err != nil {
		t.Fatalf("pipeline start: %v", err)
	}
	t.Cleanup(pipe.Stop)

	// API server with the rest-hook activator that auto-succeeds.
	clientID := "client-single-event-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:     h.DB,
		ClientID: clientID,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	// Subscriber endpoint: deterministic tag the mocksub uses as
	// SubscriptionID for journal lookups.
	tag := "single-event-" + uuid.New().String()[:8]
	endpoint := fmt.Sprintf("%s/hook/%s", tlsSrv.URL, tag)

	subBody, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     endpoint,
		"content":      "full-resource",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": endpoint},
	})

	subID, err := hpipe.PostSubscription(ctx, api, http.DefaultClient, subBody)
	if err != nil {
		t.Fatalf("POST subscription: %v", err)
	}

	// Mark the row active synchronously to remove the race against
	// the activate goroutine.
	if err := hpipe.MarkSubscriptionActive(ctx, h.DB, subID); err != nil {
		t.Fatalf("mark active: %v", err)
	}

	// Drive an MLLP message via the existing EHR scenario control plane.
	postScenario(t, ctx, h, "/scenarios/admit_patient", map[string]any{
		"patient_id":  "MRN-S2RH-1",
		"message_id":  "S2RH-1-" + tag,
		"trigger":     "A01",
		"family_name": "Doe",
		"given_name":  "Jane",
	})

	// Wait for the rest-hook journal to record one delivery for our tag.
	got, err := WaitForNotification(ctx, h, tag, 30*time.Second)
	if err != nil {
		// On timeout, dump the pipeline state so we can see where it
		// stalled.
		dumpPipelineState(t, ctx, h, subID)
		t.Fatalf("wait notification: %v", err)
	}
	if got.SubscriptionID != tag {
		t.Errorf("journal subscription id: got %q want %q", got.SubscriptionID, tag)
	}
	if len(got.Body) == 0 {
		t.Errorf("journal body is empty")
	}

	// The X-Subscription-Id header is the *real* subscription id, set by
	// the rest-hook channel. Verify it matches our subID.
	hdr := got.Header.Get("X-Subscription-Id")
	if hdr != subID.String() {
		t.Errorf("X-Subscription-Id header: got %q want %q", hdr, subID.String())
	}
}
