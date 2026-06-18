// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_events_replay drives an event through the pipeline,
// observes the rest-hook delivery, then calls
// GET /Subscription/{id}/$events?eventsSinceNumber=0 and asserts that
// the response Bundle includes the same eventNumber the rest-hook
// notification carried.
//
// This proves the API's $events op is reading the same ehr_events log
// the engine wrote — the foundation of idempotent replay.
func TestScenario_events_replay(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 90*time.Second)
	defer cancel()

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
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	tag := shortTag("evt-replay")
	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	driveAdmit(t, ctx, h, "EVT-REPLAY-1-"+tag, "MRN-EVT-1", "A01")

	// Wait for the delivery.
	got, err := WaitForNotification(ctx, h, tag, 30*time.Second)
	if err != nil {
		dumpAndFail(t, ctx, h, subID, "wait notification: %v", err)
	}
	deliveredEventNum := got.Header.Get("X-Subscription-Event-Number")
	if deliveredEventNum == "" {
		t.Fatalf("delivery did not carry X-Subscription-Event-Number")
	}

	// Now call $events.
	url := fmt.Sprintf("%s/Subscription/%s/$events?eventsSinceNumber=0", fx.API().URL, subID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET $events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET $events: status %d body=%s", resp.StatusCode, body)
	}
	var bundle map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if rt, _ := bundle["resourceType"].(string); rt != "Bundle" {
		t.Errorf("resourceType: got %q", rt)
	}
	if bt, _ := bundle["type"].(string); bt != "subscription-notification" {
		t.Errorf("Bundle.type: got %q", bt)
	}

	// The status resource inside the bundle should mention the
	// delivered eventNumber. Walk the entry[0].resource.notificationEvent.
	entries, _ := bundle["entry"].([]any)
	if len(entries) == 0 {
		t.Fatalf("bundle has no entries")
	}
	entry0, _ := entries[0].(map[string]any)
	resource, _ := entry0["resource"].(map[string]any)
	notifs, _ := resource["notificationEvent"].([]any)
	if len(notifs) == 0 {
		t.Fatalf("status resource has no notificationEvent entries")
	}
	first, _ := notifs[0].(map[string]any)
	gotEventNum := fmt.Sprintf("%v", first["eventNumber"])
	if gotEventNum != deliveredEventNum {
		t.Errorf("$events eventNumber=%q vs delivered=%q", gotEventNum, deliveredEventNum)
	}
}
