// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

// internal-package tests so we can introspect cache state via methods
// not exported in the public surface. Cache size assertions are how we
// pin the B-9 invariants.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestJTIReplayCache_BoundedAfterExpiredSweep pins B-9: when Seen
// expires entries, the order slice MUST stay in sync; otherwise
// subsequent Puts evict ghost entries (delete the wrong key) and the
// map can grow past cap as more new keys arrive than Put thinks slots
// are free for. Reproduce the desync and assert the cache stays
// bounded.
func TestJTIReplayCache_BoundedAfterExpiredSweep(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	cur := t0
	clock := func() time.Time { return cur }
	c := NewJTIReplayCache(4, clock)

	// Fill to cap with entries that all expire at t0+1m.
	for i := 0; i < 4; i++ {
		c.Put(fmt.Sprintf("expired-%d", i), t0.Add(1*time.Minute))
	}

	// Advance clock past the expiry then force expired eviction via
	// Seen on each one (mirrors how the verifier paths exercise it).
	cur = t0.Add(2 * time.Minute)
	for i := 0; i < 4; i++ {
		_ = c.Seen(fmt.Sprintf("expired-%d", i))
	}

	// Insert 4 fresh entries. Because Seen left ghosts in `order`,
	// each Put will evict-by-ghost (delete a key that's already gone)
	// and `len(entries)` ends up >cap.
	for i := 0; i < 4; i++ {
		c.Put(fmt.Sprintf("fresh-%d", i), t0.Add(10*time.Minute))
	}

	c.mu.Lock()
	gotEntries := len(c.entries)
	gotOrder := len(c.order)
	c.mu.Unlock()
	if gotEntries > 4 {
		t.Fatalf("entries=%d; cap is 4 (entries grew past cap due to desync)", gotEntries)
	}
	if gotOrder > 4 {
		t.Fatalf("order=%d; cap is 4 (order grew past cap)", gotOrder)
	}
}

// TestJTIReplayCache_SeenSweepsExpiredAndKeepsConsistent pins the other
// half of B-9: when Seen evicts an expired entry from the map, the
// matching entry in `order` must also be removed (or otherwise reaped)
// so subsequent eviction decisions don't reference ghosts.
func TestJTIReplayCache_SeenSweepsExpiredAndKeepsConsistent(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return t0 }
	c := NewJTIReplayCache(4, clock)

	// Two entries that are already expired relative to the clock.
	c.Put("expired-1", t0.Add(-1*time.Minute))
	c.Put("expired-2", t0.Add(-1*time.Minute))
	// Two entries that are still alive.
	c.Put("alive-1", t0.Add(5*time.Minute))
	c.Put("alive-2", t0.Add(5*time.Minute))

	// Seen on the expired entries returns false AND drops them from
	// both entries and order.
	if c.Seen("expired-1") {
		t.Fatalf("Seen returned true for expired-1")
	}
	if c.Seen("expired-2") {
		t.Fatalf("Seen returned true for expired-2")
	}

	// Map must reflect the sweeps.
	c.mu.Lock()
	gotEntries := len(c.entries)
	gotOrder := len(c.order)
	c.mu.Unlock()
	if gotEntries != 2 {
		t.Errorf("entries len after expired-sweep = %d; want 2", gotEntries)
	}
	// Order MUST also drop the expired ones — otherwise the next Put
	// will evict by the wrong identity.
	if gotOrder != 2 {
		t.Errorf("order len after expired-sweep = %d; want 2 (order/entries desync)", gotOrder)
	}

	// One more Put that should NOT evict alive-1 because the slots are
	// available now.
	c.Put("new", t0.Add(5*time.Minute))
	if !c.Seen("alive-1") {
		t.Errorf("alive-1 was evicted despite available slots")
	}
}

// TestJTIReplayCache_ConcurrentSafe runs Put + Seen across many
// goroutines under -race. With the legacy implementation the mutex is
// already correct; with a rewrite using golang-lru or similar, the test
// also pins that no synchronisation regression slipped in.
func TestJTIReplayCache_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	c := NewJTIReplayCache(64, func() time.Time { return now })

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				c.Put(fmt.Sprintf("g%d-j%d", i, j), now.Add(5*time.Minute))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.Seen(fmt.Sprintf("g%d-j%d", i, j))
			}
		}()
	}
	wg.Wait()
}
