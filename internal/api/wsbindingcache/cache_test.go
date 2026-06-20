// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package wsbindingcache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/wsbindingcache"
)

// TestCache_HitWithinTTL: an entry put under (sub, client) is
// returned by Get within its TTL.
func TestCache_HitWithinTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 16, Now: clock})
	defer c.Close()

	subID := uuid.New()
	c.Put("client-A", subID, "tok-1", now.Add(5*time.Minute))

	got, ok := c.Get("client-A", subID)
	if !ok {
		t.Fatalf("expected hit; got miss")
	}
	if got.Token != "tok-1" {
		t.Errorf("token = %q; want tok-1", got.Token)
	}
}

// TestCache_MissAfterExpiry: an expired entry is reported as miss.
func TestCache_MissAfterExpiry(t *testing.T) {
	t.Parallel()
	var nowMu sync.Mutex
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 16, Now: clock})
	defer c.Close()

	subID := uuid.New()
	c.Put("client-A", subID, "tok-1", now.Add(1*time.Minute))

	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()

	if _, ok := c.Get("client-A", subID); ok {
		t.Errorf("expected miss after TTL; got hit")
	}
}

// TestCache_LRUEvictionOnOverflow: putting MaxKeys+1 entries evicts
// the least-recently-used entry. Cache MUST report eviction via the
// counters reported by Stats.
func TestCache_LRUEvictionOnOverflow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 2, Now: clock})
	defer c.Close()

	a, b, d := uuid.New(), uuid.New(), uuid.New()
	c.Put("client-A", a, "tok-a", now.Add(5*time.Minute))
	c.Put("client-B", b, "tok-b", now.Add(5*time.Minute))
	// Bump A so B is the LRU candidate.
	if _, ok := c.Get("client-A", a); !ok {
		t.Fatalf("expected hit on A")
	}
	c.Put("client-D", d, "tok-d", now.Add(5*time.Minute))

	if _, ok := c.Get("client-B", b); ok {
		t.Errorf("expected B to be evicted (LRU); got hit")
	}
	if _, ok := c.Get("client-A", a); !ok {
		t.Errorf("expected A still present; got miss")
	}
	if _, ok := c.Get("client-D", d); !ok {
		t.Errorf("expected D present; got miss")
	}

	stats := c.Stats()
	if stats.Evictions == 0 {
		t.Errorf("expected at least one eviction; got 0 (stats=%+v)", stats)
	}
}

// TestCache_HitMissMetricsTracked verifies Stats reports the hit and
// miss counters.
func TestCache_HitMissMetricsTracked(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 4, Now: clock})
	defer c.Close()

	subID := uuid.New()
	c.Put("client-A", subID, "tok-1", now.Add(5*time.Minute))

	if _, ok := c.Get("client-A", subID); !ok {
		t.Fatalf("expected hit")
	}
	if _, ok := c.Get("client-Z", subID); ok {
		t.Fatalf("expected miss for unknown client")
	}

	stats := c.Stats()
	if stats.Hits != 1 {
		t.Errorf("Hits = %d; want 1", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("Misses = %d; want 1", stats.Misses)
	}
}

// TestSweeper_PurgesExpiredEntries: a Sweep call drops entries whose
// expires_at has lapsed.
func TestSweeper_PurgesExpiredEntries(t *testing.T) {
	t.Parallel()
	var nowMu sync.Mutex
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 8, Now: clock})
	defer c.Close()

	a, b := uuid.New(), uuid.New()
	c.Put("client-A", a, "tok-a", now.Add(30*time.Second)) // will expire
	c.Put("client-B", b, "tok-b", now.Add(5*time.Minute))  // still valid

	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()

	c.Sweep()

	if c.Len() != 1 {
		t.Errorf("expected one surviving entry post-sweep; got %d", c.Len())
	}
	if _, ok := c.Get("client-A", a); ok {
		t.Errorf("expected A purged; still present")
	}
	if _, ok := c.Get("client-B", b); !ok {
		t.Errorf("expected B retained; missing")
	}
}

// TestStartSweeper_RunsOnTickAndStopsOnContextCancel: the lifecycle-
// driven sweeper goroutine must drain expired entries and exit when
// its context is canceled.
func TestStartSweeper_RunsOnTickAndStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	var nowMu sync.Mutex
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 8, Now: clock})
	defer c.Close()

	subID := uuid.New()
	c.Put("client-A", subID, "tok", now.Add(30*time.Second))

	var sweeps atomic.Int32
	hook := func() { sweeps.Add(1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wsbindingcache.StartSweeper(ctx, c, 5*time.Millisecond, hook)

	nowMu.Lock()
	now = now.Add(2 * time.Minute)
	nowMu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for sweeps.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if sweeps.Load() == 0 {
		t.Fatalf("sweeper hook never ran")
	}
	if c.Len() != 0 {
		t.Errorf("expected sweeper to purge expired entry; %d remain", c.Len())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("sweeper did not stop within 2s of cancel")
	}
}

// TestPut_OverwritesExistingEntry: calling Put under an existing key
// updates the resident entry and bumps it to MRU.
func TestPut_OverwritesExistingEntry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 4, Now: func() time.Time { return now }})
	defer c.Close()

	subID := uuid.New()
	c.Put("client-A", subID, "tok-1", now.Add(time.Minute))
	c.Put("client-A", subID, "tok-2", now.Add(2*time.Minute))

	got, ok := c.Get("client-A", subID)
	if !ok || got.Token != "tok-2" {
		t.Errorf("overwrite did not take effect; got %+v ok=%v", got, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d; want 1 (overwrite must not duplicate)", c.Len())
	}
}

// TestStartSweeper_NilCacheReturnsClosedDone: the sweeper helper
// short-circuits cleanly when no cache is supplied.
func TestStartSweeper_NilCacheReturnsClosedDone(t *testing.T) {
	t.Parallel()
	done := wsbindingcache.StartSweeper(context.Background(), nil, time.Second, nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected nil-cache sweeper to close done immediately")
	}
}

// TestClose_IsIdempotent: Close is allowed to be called repeatedly.
func TestClose_IsIdempotent(t *testing.T) {
	t.Parallel()
	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 1})
	c.Close()
	c.Close()
}

// TestNew_DefaultClock: omitting Options.Now uses time.Now without
// panicking. Smoke check — the package's other tests inject a clock,
// so this verifies the fallback path.
func TestNew_DefaultClock(t *testing.T) {
	t.Parallel()
	c := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 1})
	defer c.Close()
	subID := uuid.New()
	c.Put("client-A", subID, "tok", time.Now().Add(time.Minute))
	if _, ok := c.Get("client-A", subID); !ok {
		t.Errorf("expected hit under default clock")
	}
}

// TestNilCache_BypassFallsThrough: a nil *Cache is safe — Get returns
// miss without panicking. The handler short-circuit code path must
// remain inert when the cache is unconfigured.
func TestNilCache_BypassFallsThrough(t *testing.T) {
	t.Parallel()
	var c *wsbindingcache.Cache
	if _, ok := c.Get("client-A", uuid.New()); ok {
		t.Errorf("nil cache should miss")
	}
	c.Put("client-A", uuid.New(), "tok", time.Now().Add(time.Minute))
	c.Sweep()
	stats := c.Stats()
	if stats.Hits != 0 || stats.Misses != 0 {
		t.Errorf("nil cache stats must be zero; got %+v", stats)
	}
}
