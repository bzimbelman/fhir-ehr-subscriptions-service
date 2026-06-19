// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestAPIActivate_PanicIsRecovered verifies that a panic inside the
// channel adapter's ActivateSubscription does NOT crash the server.
// The panic is recovered, the metric is incremented, and the row flips
// to `error` instead of staying `requested`. (B-10)
func TestAPIActivate_PanicIsRecovered(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	rec := &panicMetrics{}
	wg := &sync.WaitGroup{}

	clientID := "client-panic-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:                h.DB,
		ClientID:            clientID,
		ExtraChannels:       handlers.ChannelRegistry{"rest-hook": panickingActivator{}},
		LifecycleCtx:        ctx,
		ActivationTimeout:   5 * time.Second,
		ActivationWaitGroup: wg,
		Metrics:             rec,
	})
	if err != nil {
		t.Fatalf("api start: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })

	if err := seedHL7Topic(ctx, h.DB); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	subBody, _ := json.Marshal(map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        "http://example.org/topics/hl7-passthrough",
		"channelType":  map[string]any{"code": "rest-hook"},
		"endpoint":     "https://example.org/wh",
		"content":      "id-only",
		"channel":      map[string]any{"type": "rest-hook", "endpoint": "https://example.org/wh"},
	})
	subID, err := hpipe.PostSubscription(ctx, api, api.Client(), subBody)
	if err != nil {
		t.Fatalf("POST subscription: %v", err)
	}

	// Wait for the (recovered) goroutine to unwind.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("activation goroutine did not unwind within 5s")
	}

	if got := rec.ActivatePanics(); got != 1 {
		t.Fatalf("activate panic metric = %d, want 1", got)
	}

	row, err := getSubRow(ctx, h.DB, subID)
	if err != nil {
		t.Fatalf("get sub row: %v", err)
	}
	if row.Status != repos.SubError {
		t.Fatalf("row.Status = %s, want error (panic should have flipped it)", row.Status)
	}
}
