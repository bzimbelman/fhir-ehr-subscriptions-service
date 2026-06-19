// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sync"
	"time"
)

// JTIReplayCache is a bounded FIFO cache of seen JWT IDs. The auth
// middleware checks Seen before validating a token; if the JTI has been
// observed within its expiration window, the token is rejected as a
// replay.
//
// Eviction is FIFO over insertion order. Both the live map and the
// order list are kept in sync so:
//
//   - Seen on an expired entry removes it from BOTH the map AND the
//     order list (B-9).
//   - Put past capacity evicts the actual oldest live entry, never a
//     ghost (B-9).
//   - The order list's underlying array is rebuilt when its capacity
//     exceeds 2× the configured cap, so re-slicing via order[1:] does
//     not leak the original array indefinitely.
type JTIReplayCache struct {
	cap     int
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]time.Time
	order   []string
}

// NewJTIReplayCache constructs a replay cache with the given capacity.
// A capacity of zero defaults to 100k per the LLD.
func NewJTIReplayCache(capacity int, now func() time.Time) *JTIReplayCache {
	if capacity <= 0 {
		capacity = 100_000
	}
	if now == nil {
		now = time.Now
	}
	return &JTIReplayCache{
		cap:     capacity,
		now:     now,
		entries: make(map[string]time.Time, capacity),
		order:   make([]string, 0, capacity),
	}
}

// Seen reports whether jti is currently in the cache (and not expired).
// Empty jti always returns false (never matches). An expired entry is
// removed from both entries and order so subsequent Put eviction picks
// the right victim.
func (c *JTIReplayCache) Seen(jti string) bool {
	if jti == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	exp, ok := c.entries[jti]
	if !ok {
		return false
	}
	if c.now().After(exp) {
		c.removeLocked(jti)
		return false
	}
	return true
}

// CheckAndPut atomically checks whether jti is present (and unexpired)
// and, if not, records it with the given expiration. It returns true if
// jti was already in the cache (caller must reject the request as a
// replay), false if jti was newly inserted (caller may proceed).
//
// This is the only safe way to perform replay-then-record under
// concurrency: separate Seen + Put calls leave a TOCTOU window where
// two requests with the same jti both pass the check before either
// records it (B-?, OP #110).
//
// Empty jti returns true to fail closed — RFC 7523 §3 requires jti and
// upstream code is expected to reject before reaching here, but we
// don't trust that here.
func (c *JTIReplayCache) CheckAndPut(jti string, exp time.Time) (alreadySeen bool) {
	if jti == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Mirror Seen's expired-sweep behavior.
	if existingExp, ok := c.entries[jti]; ok {
		if c.now().After(existingExp) {
			c.removeLocked(jti)
			// fall through to insert
		} else {
			return true
		}
	}

	// Mirror Put's eviction-then-insert flow.
	for len(c.entries) >= c.cap && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[jti] = exp
	c.order = append(c.order, jti)
	c.maybeCompactLocked()
	return false
}

// Put records jti with the given expiration. If the cache is full the
// oldest live entry is evicted.
func (c *JTIReplayCache) Put(jti string, exp time.Time) {
	if jti == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[jti]; exists {
		c.entries[jti] = exp
		return
	}
	for len(c.entries) >= c.cap && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[jti] = exp
	c.order = append(c.order, jti)
	c.maybeCompactLocked()
}

// removeLocked drops jti from both entries and order. Called only with
// c.mu held.
func (c *JTIReplayCache) removeLocked(jti string) {
	delete(c.entries, jti)
	for i, v := range c.order {
		if v == jti {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// maybeCompactLocked rebuilds the order slice when its capacity has
// drifted past 2× the configured cap. This bounds the underlying array
// growth caused by repeated re-slicing via order[1:] (B-9).
func (c *JTIReplayCache) maybeCompactLocked() {
	if cap(c.order) <= 2*c.cap {
		return
	}
	fresh := make([]string, len(c.order), c.cap)
	copy(fresh, c.order)
	c.order = fresh
}
