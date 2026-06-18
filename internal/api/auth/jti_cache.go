// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sync"
	"time"
)

// JTIReplayCache is a bounded LRU-ish cache of seen JWT IDs. The auth
// middleware checks Seen before validating a token; if the JTI has been
// observed within its expiration window, the token is rejected as a
// replay.
//
// Implementation is a simple map gated by a mutex with periodic
// expiration sweeps. The cache is bounded by Capacity; when full the
// oldest entries are evicted regardless of TTL.
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
	}
}

// Seen reports whether jti is currently in the cache (and not expired).
// Empty jti always returns false (never matches).
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
		delete(c.entries, jti)
		return false
	}
	return true
}

// Put records jti with the given expiration. If the cache is full the
// oldest entry is evicted.
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
	if len(c.entries) >= c.cap && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[jti] = exp
	c.order = append(c.order, jti)
}
