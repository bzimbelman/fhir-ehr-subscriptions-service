// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// ctxObservingSubs wraps memSubs but captures the ctx every UpdateStatus
// call sees so the test can assert the bookkeeping write is bound by a
// CANCELLABLE ctx and NOT context.Background().
//
// OP #187 RED: today subscription_handlers.go line ~390 falls back to
// context.Background() for the bookkeeping UpdateStatus when the
// per-call ctx is dead. The AC requires the bookkeeping to derive from
// Deps.LifecycleCtx with a 30s deadline so a binary shutdown can
// abort an unbounded-by-design write.
type ctxObservingSubs struct {
	*memSubsForBookkeeping

	mu                sync.Mutex
	updateStatusCtxes []context.Context
	updateStatusGate  chan struct{} // unbuffered: gate to hold UpdateStatus until released
	updateStatusFired atomic.Int32
}

// memSubsForBookkeeping is a minimal SubscriptionsStore — we deliberately
// do NOT reuse package-level memSubs because that file is in the same
// package_test and we want a separate observer that doesn't interfere
// with other tests.
type memSubsForBookkeeping struct {
	mu   sync.Mutex
	rows map[uuid.UUID]repos.SubscriptionRow
}

func newMemSubsForBookkeeping() *memSubsForBookkeeping {
	return &memSubsForBookkeeping{rows: map[uuid.UUID]repos.SubscriptionRow{}}
}

func (m *memSubsForBookkeeping) Insert(_ context.Context, row repos.SubscriptionRow) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.New()
	row.ID = id
	row.CreatedAt = time.Now().UTC()
	row.UpdatedAt = row.CreatedAt
	m.rows[id] = row
	return id, nil
}

func (m *memSubsForBookkeeping) GetByID(_ context.Context, id uuid.UUID) (*repos.SubscriptionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, nil
	}
	r2 := r
	return &r2, nil
}

func (m *memSubsForBookkeeping) ListByClient(_ context.Context, clientID string) ([]repos.SubscriptionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []repos.SubscriptionRow{}
	for _, r := range m.rows {
		if r.ClientID == clientID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memSubsForBookkeeping) FindByClientAndCriteria(_ context.Context, _ string, _ handlers.SubscriptionMatchCriteria) ([]repos.SubscriptionRow, error) {
	return nil, nil
}

func (m *memSubsForBookkeeping) ListByClientPage(_ context.Context, _ string, _ *handlers.SubscriptionCursor, _ int) ([]repos.SubscriptionRow, error) {
	return nil, nil
}

func (m *memSubsForBookkeeping) UpdateResource(_ context.Context, id uuid.UUID, row repos.SubscriptionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row.ID = id
	row.UpdatedAt = time.Now().UTC()
	m.rows[id] = row
	return nil
}

func (m *memSubsForBookkeeping) UpdateStatus(_ context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil
	}
	r.Status = status
	r.Error = errMsg
	r.UpdatedAt = time.Now().UTC()
	m.rows[id] = r
	return nil
}

// UpdateStatus on the wrapper records the ctx then delegates. If
// updateStatusGate is non-nil, the call BLOCKS on it so the test can
// observe ctx cancellation mid-call.
func (c *ctxObservingSubs) UpdateStatus(ctx context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error {
	c.mu.Lock()
	c.updateStatusCtxes = append(c.updateStatusCtxes, ctx)
	c.mu.Unlock()
	c.updateStatusFired.Add(1)
	if c.updateStatusGate != nil {
		select {
		case <-c.updateStatusGate:
			// released
		case <-ctx.Done():
			// AC pins: the ctx propagates the lifecycle cancel — this
			// is the path the test wants to exercise.
			return ctx.Err()
		}
	}
	return c.memSubsForBookkeeping.UpdateStatus(ctx, id, status, errMsg)
}

func (c *ctxObservingSubs) lastBookkeepingCtx() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.updateStatusCtxes) == 0 {
		return nil
	}
	return c.updateStatusCtxes[len(c.updateStatusCtxes)-1]
}

// TestActivate_BookkeepingObservesLifecycleCancel — OP #187 RED. The
// AC: the failed-handshake bookkeeping path (subscription_handlers.go
// line ~388) MUST derive its ctx from Deps.LifecycleCtx with a bounded
// timeout, so a binary shutdown can abort the bookkeeping write rather
// than dropping into a NEVER-CANCELLED context.Background().
//
// Today the code does:
//
//	bookkeepingCtx := ctx
//	if bookkeepingCtx.Err() != nil {
//	    bookkeepingCtx = context.Background()
//	}
//	s.deps.Subscriptions.UpdateStatus(bookkeepingCtx, ...)
//
// — so when the per-call ctx is dead, an UNBOUNDED ctx is used. This
// test cancels the LifecycleCtx and asserts the bookkeeping
// UpdateStatus observes the cancel within ~50ms (the AC's spec-line
// 30s timeout is bounded by lifecycle, not Background).
func TestActivate_BookkeepingObservesLifecycleCancel(t *testing.T) {
	t.Parallel()

	subs := &ctxObservingSubs{
		memSubsForBookkeeping: newMemSubsForBookkeeping(),
		updateStatusGate:      make(chan struct{}),
	}

	deps := defaultDeps(t)
	deps.Subscriptions = subs

	// A blocking activator that returns an error after the started
	// signal so we deterministically take the bookkeeping branch (line
	// ~380-396 in subscription_handlers.go).
	startedWG := &sync.WaitGroup{}
	startedWG.Add(1)
	bc := newBlockingChannel(startedWG)
	deps.Channels = handlers.ChannelRegistry{"rest-hook": bc}
	deps.ActivationTimeout = 30 * time.Millisecond // forces ctx-dead branch
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	deps.LifecycleCtx = lifecycleCtx
	deps.ActivationWaitGroup = &sync.WaitGroup{}

	srv := newTestServer(t, defaultPrincipal(), deps)
	_ = postSubscriptionWithChannel(t, srv.URL, "rest-hook")

	startedWG.Wait()
	// activator now blocked on its own ctx.Done(); ActivationTimeout
	// will fire, the handler will fall into the bookkeeping branch and
	// call UpdateStatus, which our observer holds on updateStatusGate.
	deadline := time.Now().Add(2 * time.Second)
	for subs.updateStatusFired.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("UpdateStatus never fired — bookkeeping path not exercised")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Now cancel the lifecycle. The AC says the bookkeeping ctx MUST
	// be derived from LifecycleCtx, so this cancel must propagate.
	cancelLifecycle()

	released := make(chan struct{})
	go func() {
		// Once the bookkeeping ctx is cancelled, our UpdateStatus
		// returns. Wait briefly; if the ctx never fires, we'll
		// deadlock — the test guard below catches it.
		deps.ActivationWaitGroup.Wait()
		close(released)
	}()
	select {
	case <-released:
		// good
	case <-time.After(500 * time.Millisecond):
		// Unblock the test by closing the gate so we don't hang the
		// suite, then fail loudly with the diagnostic.
		close(subs.updateStatusGate)
		t.Fatalf("bookkeeping UpdateStatus did not observe LifecycleCtx cancel within 500ms — code path still uses context.Background()")
	}

	got := subs.lastBookkeepingCtx()
	if got == nil {
		t.Fatalf("no bookkeeping ctx captured")
	}
	if err := got.Err(); err == nil {
		t.Errorf("bookkeeping ctx is not cancelled — Err() = nil; expected LifecycleCtx cancel to propagate")
	}
}
