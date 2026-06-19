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

// blockingActivator holds in ActivateSubscription until ctx cancels.
type blockingActivator struct {
	startedWG *sync.WaitGroup
}

func newBlockingActivator(startedWG *sync.WaitGroup) *blockingActivator {
	return &blockingActivator{startedWG: startedWG}
}

func (b *blockingActivator) ActivateSubscription(ctx context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if b.startedWG != nil {
		b.startedWG.Done()
	}
	<-ctx.Done()
	return handlers.HandshakeFailed, ctx.Err()
}

// TestAPIActivate_ShutdownCancelsHandshake verifies that POSTing a
// Subscription with a slow channel handshake does NOT leave a goroutine
// pinned through process shutdown. The lifecycle ctx is canceled and
// the WaitGroup unwinds within a bounded budget; the row flips to
// `error` instead of remaining stuck `requested`. (B-10)
func TestAPIActivate_ShutdownCancelsHandshake(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resetPipelineTables(t, ctx, h)

	startedWG := &sync.WaitGroup{}
	startedWG.Add(1)
	bc := newBlockingActivator(startedWG)

	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}

	clientID := "client-shutdown-" + uuid.New().String()[:8]
	api, err := hpipe.StartAPIServer(ctx, hpipe.APIServerConfig{
		Pool:                h.DB,
		ClientID:            clientID,
		ExtraChannels:       handlers.ChannelRegistry{"rest-hook": bc},
		LifecycleCtx:        lifecycleCtx,
		ActivationTimeout:   30 * time.Second,
		ActivationWaitGroup: wg,
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

	startedWG.Wait()

	// Trigger lifecycle shutdown while the handshake is still
	// blocked. The bare `go s.activate(...)` regression ignored this
	// signal; the fixed path joins via WaitGroup.
	lifecycleCancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("activation goroutine did not unwind within 5s of lifecycle cancel")
	}

	// Row must NOT be stuck in `requested`.
	row, err := getSubRow(ctx, h.DB, subID)
	if err != nil {
		t.Fatalf("get sub row: %v", err)
	}
	if row.Status == repos.SubRequested {
		t.Fatalf("row still in `requested` after shutdown — goroutine leaked")
	}
}
