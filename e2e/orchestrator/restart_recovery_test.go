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

// TestScenario_restart_recovery drives a message through the pipeline,
// stops it after delivery, drives a SECOND message via MLLP, then
// starts a fresh pipeline against the same DB. The fresh pipeline
// claims the unprocessed row and delivers it. Total notifications for
// the test tag must be exactly 2 — no duplicates from the first run
// and no losses across the restart boundary.
func TestScenario_restart_recovery(t *testing.T) {
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

	tag := shortTag("restart")
	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	// First message — drives end-to-end delivery before shutdown.
	driveAdmit(t, ctx, h, "RR-1-"+tag, "MRN-RR-1", "A01")
	if _, err := WaitForNotification(ctx, h, tag, 30*time.Second); err != nil {
		dumpAndFail(t, ctx, h, subID, "wait notification 1: %v", err)
	}

	// Stop the pipeline.
	fx.pipeline().Stop()

	// Drive the second message AFTER the pipeline is stopped. The
	// MLLP listener and the persister are part of the test harness
	// (not the pipeline), so the message lands in hl7_message_queue
	// without being processed.
	driveAdmit(t, ctx, h, "RR-2-"+tag, "MRN-RR-2", "A01")

	// Brief sleep so any lingering claim attempts settle.
	time.Sleep(500 * time.Millisecond)

	// Start a fresh pipeline against the same DB. It should claim the
	// unprocessed row and deliver.
	pipe2, err := hpipe.NewPipeline(h.DB, hpipe.PipelineConfig{
		AdapterID: "default",
		Channels:  map[string]channel.Channel{"rest-hook": restCh},
	})
	if err != nil {
		t.Fatalf("pipeline2 new: %v", err)
	}
	if err := pipe2.SeedTopic(ctx, hpipe.TopicFixture{
		URL:     "http://example.org/topics/hl7-passthrough",
		Version: "1.0.0",
		Title:   "HL7 passthrough",
		Body:    []byte(passthroughTopicJSON),
	}); err != nil {
		t.Fatalf("pipeline2 seed topic: %v", err)
	}
	if err := pipe2.Start(ctx); err != nil {
		t.Fatalf("pipeline2 start: %v", err)
	}
	t.Cleanup(pipe2.Stop)

	// Wait for the delivery (count == 2 in journal).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got := h.MockSub.RestHook.Received(tag)
		if len(got) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := h.MockSub.RestHook.Received(tag)
	if len(got) != 2 {
		dumpAndFail(t, ctx, h, subID, "expected 2 notifications across restart, got %d", len(got))
	}
}
