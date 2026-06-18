// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_backpressure exercises the scheduler's transient retry
// curve. The mock subscriber returns 503 for the first two attempts,
// then forwards to the real journal. The SUT's scheduler must retry
// with backoff and eventually succeed; the journal must show exactly
// one successful delivery.
func TestScenario_backpressure(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 120*time.Second)
	defer cancel()

	tag := shortTag("backpressure")

	var attempt atomic.Int32
	delegate := h.MockSub.RestHook.Handler()
	switching := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/hook/"+tag) {
			delegate.ServeHTTP(w, r)
			return
		}
		// Reject the first 2 attempts on this tag.
		if attempt.Add(1) <= 2 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		delegate.ServeHTTP(w, r)
	})

	tlsSrv, err := hpipe.StartTLSRestHookServer(switching)
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

	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	driveAdmit(t, ctx, h, "BP-1-"+tag, "MRN-BP", "A01")

	// The default scheduler retry curve has Initial=500ms in the
	// harness Pipeline config, so two transient failures retry in
	// ~0.5s + ~1s = ~1.5s. Allow extra slack.
	if _, err := WaitForNotification(ctx, h, tag, 60*time.Second); err != nil {
		dumpAndFail(t, ctx, h, subID, "wait notification: %v (attempts=%d)", err, attempt.Load())
	}

	// Exactly one successful delivery in the journal.
	got := h.MockSub.RestHook.Received(tag)
	if len(got) != 1 {
		dumpAndFail(t, ctx, h, subID,
			"expected exactly 1 successful delivery; got %d (attempts=%d)",
			len(got), attempt.Load())
	}
	if attempt.Load() < 3 {
		t.Errorf("expected at least 3 attempts (2 transient + 1 success), got %d",
			attempt.Load())
	}
}
