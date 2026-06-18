// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// blockingChannel is an activator that holds in ActivateSubscription
// until either ctx cancels or the test releases it.
type blockingChannel struct {
	released  chan struct{}
	startedWG *sync.WaitGroup
	canceled  atomic.Bool
}

func newBlockingChannel(startedWG *sync.WaitGroup) *blockingChannel {
	return &blockingChannel{released: make(chan struct{}), startedWG: startedWG}
}

func (b *blockingChannel) ActivateSubscription(ctx context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if b.startedWG != nil {
		b.startedWG.Done()
	}
	select {
	case <-ctx.Done():
		b.canceled.Store(true)
		return handlers.HandshakeFailed, ctx.Err()
	case <-b.released:
		return handlers.HandshakeSucceeded, nil
	}
}

// panicChannel is an activator that panics inside ActivateSubscription.
type panicChannel struct{}

func (panicChannel) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	panic(errors.New("synthetic activator panic"))
}

func postSubscriptionWithChannel(t *testing.T, srv string, channelType string) string {
	t.Helper()
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "` + channelType + `"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "` + channelType + `"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(b))
	}
	loc := resp.Header.Get("Location")
	return strings.TrimPrefix(loc, "/Subscription/")
}

// TestActivate_ContextTimeout_FlipsToError verifies that the handler
// derives a per-activation ctx from ActivationTimeout, so a slow vendor
// cannot pin a goroutine indefinitely. After the timeout fires the row
// must be marked `error`, not stuck `requested`.
func TestActivate_ContextTimeout_FlipsToError(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	startedWG := &sync.WaitGroup{}
	startedWG.Add(1)
	bc := newBlockingChannel(startedWG)
	deps.Channels = handlers.ChannelRegistry{"rest-hook": bc}
	deps.ActivationTimeout = 50 * time.Millisecond
	deps.LifecycleCtx = context.Background()
	deps.ActivationWaitGroup = &sync.WaitGroup{}

	srv := newTestServer(t, defaultPrincipal(), deps)
	id := postSubscriptionWithChannel(t, srv.URL, "rest-hook")

	startedWG.Wait()
	deps.ActivationWaitGroup.Wait()

	if !bc.canceled.Load() {
		t.Fatalf("activator was not canceled by timeout")
	}

	subID, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("parse id: %v", err)
	}
	subs := deps.Subscriptions.(*memSubs)
	row, _ := subs.GetByID(context.Background(), subID)
	if row == nil {
		t.Fatalf("row missing")
	}
	if row.Status != repos.SubError {
		t.Fatalf("status = %s, want error", row.Status)
	}
}

// TestActivate_LifecycleCancel_FlipsToError verifies that canceling the
// lifecycle context propagates to in-flight activation goroutines.
func TestActivate_LifecycleCancel_FlipsToError(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	startedWG := &sync.WaitGroup{}
	startedWG.Add(1)
	bc := newBlockingChannel(startedWG)
	deps.Channels = handlers.ChannelRegistry{"rest-hook": bc}
	deps.ActivationTimeout = 5 * time.Second
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	deps.LifecycleCtx = lifecycleCtx
	deps.ActivationWaitGroup = &sync.WaitGroup{}

	srv := newTestServer(t, defaultPrincipal(), deps)
	postSubscriptionWithChannel(t, srv.URL, "rest-hook")

	startedWG.Wait()
	cancel()

	deps.ActivationWaitGroup.Wait()

	if !bc.canceled.Load() {
		t.Fatalf("activator was not canceled by lifecycle ctx")
	}
}

// TestActivate_PanicRecovered records a panic metric and does NOT
// crash the test process. The row is left in `error`.
func TestActivate_PanicRecovered(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.Channels = handlers.ChannelRegistry{"rest-hook": panicChannel{}}
	deps.ActivationTimeout = 1 * time.Second
	deps.LifecycleCtx = context.Background()
	deps.ActivationWaitGroup = &sync.WaitGroup{}
	rec := newRecordingHandlerMetrics()
	deps.Metrics = rec

	srv := newTestServer(t, defaultPrincipal(), deps)
	id := postSubscriptionWithChannel(t, srv.URL, "rest-hook")

	deps.ActivationWaitGroup.Wait()

	if got := rec.activatePanics(); got != 1 {
		t.Fatalf("activate panic metric = %d, want 1", got)
	}

	subID, _ := uuid.Parse(id)
	subs := deps.Subscriptions.(*memSubs)
	row, _ := subs.GetByID(context.Background(), subID)
	if row == nil || row.Status != repos.SubError {
		t.Fatalf("row.Status = %v, want error", row)
	}
}
