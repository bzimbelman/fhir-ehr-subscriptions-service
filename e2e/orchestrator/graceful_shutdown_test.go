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

// TestScenario_graceful_shutdown drives a delivery, then calls
// pipeline.Stop and asserts the worker goroutines drain in a bounded
// amount of time without leaking.
//
// The full LLD scenario also exercises SIGTERM handling against the
// fhir-subs binary; that path requires the lifecycle module's signal
// handler against an external process. The harness's Pipeline runs in
// the same process, so we exercise the equivalent context-cancel-and-
// drain semantics that the lifecycle module's PhaseStopAccepting
// phase wraps.
func TestScenario_graceful_shutdown(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 60*time.Second)
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

	tag := shortTag("shutdown")
	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	driveAdmit(t, ctx, h, "SHUT-1-"+tag, "MRN-SHUT-1", "A01")
	if _, err := WaitForNotification(ctx, h, tag, 30*time.Second); err != nil {
		dumpAndFail(t, ctx, h, subID, "wait notification before shutdown: %v", err)
	}

	// Stop the pipeline. Stop blocks until every worker returns or 15s
	// has elapsed. Measure the actual drain time.
	start := time.Now()
	fx.pipeline().Stop()
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Errorf("graceful drain exceeded 10s: %v", elapsed)
	}

	// A second Stop must be a no-op (idempotent).
	fx.pipeline().Stop()
}
