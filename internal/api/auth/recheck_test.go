// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

type stubRechecker struct {
	mu       sync.Mutex
	calls    int32
	status   auth.RecheckStatus
	err      error
	override map[string]auth.RecheckStatus
}

func (s *stubRechecker) Recheck(_ context.Context, _, subID string) (auth.RecheckStatus, error) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	if v, ok := s.override[subID]; ok {
		return v, nil
	}
	return s.status, nil
}

// P2.7: AlwaysActiveRechecker satisfies the SPI and returns active.
func TestAlwaysActiveRechecker(t *testing.T) {
	t.Parallel()
	got, err := auth.AlwaysActiveRechecker{}.Recheck(context.Background(), "client", "sub")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != auth.RecheckActive {
		t.Errorf("status: %v", got)
	}
}

// P2.7: CachedRechecker hits the inner Rechecker on the first call
// and serves the cached value on subsequent calls within TTL.
func TestCachedRechecker_CachesWithinTTL(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{status: auth.RecheckActive}
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := auth.NewCachedRechecker(stub, 30*time.Second, clock)

	for i := 0; i < 5; i++ {
		s, err := c.Recheck(context.Background(), "clientA", "sub-1")
		if err != nil {
			t.Fatalf("recheck: %v", err)
		}
		if s != auth.RecheckActive {
			t.Errorf("status: %v", s)
		}
	}
	if got := atomic.LoadInt32(&stub.calls); got != 1 {
		t.Errorf("inner calls: want 1, got %d", got)
	}
}

// P2.7: CachedRechecker re-queries inner after TTL expiry.
func TestCachedRechecker_RefreshesAfterTTL(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{status: auth.RecheckActive}
	current := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return current }
	c := auth.NewCachedRechecker(stub, 30*time.Second, clock)

	if _, err := c.Recheck(context.Background(), "clientA", "sub-1"); err != nil {
		t.Fatalf("recheck1: %v", err)
	}
	current = current.Add(45 * time.Second)
	if _, err := c.Recheck(context.Background(), "clientA", "sub-1"); err != nil {
		t.Fatalf("recheck2: %v", err)
	}
	if got := atomic.LoadInt32(&stub.calls); got != 2 {
		t.Errorf("inner calls: want 2 after TTL expiry, got %d", got)
	}
}

// P2.7: Invalidate forces the next Recheck to hit inner.
func TestCachedRechecker_Invalidate(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{status: auth.RecheckActive}
	c := auth.NewCachedRechecker(stub, time.Hour, time.Now)

	if _, err := c.Recheck(context.Background(), "client", "sub-1"); err != nil {
		t.Fatalf("recheck1: %v", err)
	}
	c.Invalidate("sub-1")
	if _, err := c.Recheck(context.Background(), "client", "sub-1"); err != nil {
		t.Fatalf("recheck2: %v", err)
	}
	if got := atomic.LoadInt32(&stub.calls); got != 2 {
		t.Errorf("inner calls: want 2 after Invalidate, got %d", got)
	}
}

// P2.7: A revoked subscription stays revoked across cache hits.
func TestCachedRechecker_RevokedIsCached(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{
		status: auth.RecheckRevoked,
	}
	c := auth.NewCachedRechecker(stub, time.Minute, time.Now)
	for i := 0; i < 3; i++ {
		s, err := c.Recheck(context.Background(), "client", "sub-1")
		if err != nil {
			t.Fatalf("recheck: %v", err)
		}
		if s != auth.RecheckRevoked {
			t.Errorf("status: %v", s)
		}
	}
	if got := atomic.LoadInt32(&stub.calls); got != 1 {
		t.Errorf("inner calls: want 1 (cached), got %d", got)
	}
}

// P2.7: Inner error fails open (returns Active) and does NOT poison the cache.
func TestCachedRechecker_FailsOpenOnInnerError(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{
		err: errors.New("auth store down"),
	}
	c := auth.NewCachedRechecker(stub, time.Minute, time.Now)
	got, err := c.Recheck(context.Background(), "client", "sub-1")
	if err == nil {
		t.Errorf("expected error to propagate")
	}
	if got != auth.RecheckActive {
		t.Errorf("status on error: want active, got %v", got)
	}
	// A subsequent call when the auth store is healthy must hit inner
	// (not a cached error state).
	stub.mu.Lock()
	stub.err = nil
	stub.status = auth.RecheckActive
	stub.mu.Unlock()
	got2, err := c.Recheck(context.Background(), "client", "sub-1")
	if err != nil {
		t.Errorf("recheck2: %v", err)
	}
	if got2 != auth.RecheckActive {
		t.Errorf("status: %v", got2)
	}
	if got := atomic.LoadInt32(&stub.calls); got != 2 {
		t.Errorf("inner calls: want 2, got %d (cache must not retain error)", got)
	}
}

// P2.7: TTL = 0 disables caching — every call hits inner.
func TestCachedRechecker_ZeroTTLBypasses(t *testing.T) {
	t.Parallel()
	stub := &stubRechecker{status: auth.RecheckActive}
	c := auth.NewCachedRechecker(stub, 0, time.Now)
	for i := 0; i < 3; i++ {
		if _, err := c.Recheck(context.Background(), "client", "sub-1"); err != nil {
			t.Fatalf("recheck: %v", err)
		}
	}
	if got := atomic.LoadInt32(&stub.calls); got != 3 {
		t.Errorf("inner calls: want 3 (TTL=0), got %d", got)
	}
}
