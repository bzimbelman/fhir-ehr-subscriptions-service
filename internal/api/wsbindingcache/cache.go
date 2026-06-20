// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package wsbindingcache is the in-process per-client cache that
// short-circuits $get-ws-binding-token mints (OP #242) before the
// repo path takes the lock. The cache is layered IN FRONT of the
// DB-backed reuse lookup added in OP #241 — the cache is a fast
// path; the DB remains the source of truth.
//
// # Concurrency
//
// All public methods are safe for concurrent use. The implementation
// uses a single mutex that guards both the LRU list and the entry
// map; entry counts are O(MaxKeys) so the lock is never held across
// network or disk I/O.
//
// # Lifecycle
//
// The Cache itself is GC-bound. Operators who need active sweeping
// of expired entries should call StartSweeper, which spawns a
// goroutine that ticks at the supplied interval and exits when the
// supplied context is canceled. The lifecycle module is the
// canonical caller; tests can drive Sweep directly to keep test
// runtime bounded.
package wsbindingcache

import (
	"container/list"
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Options configures a Cache.
type Options struct {
	// MaxKeys caps the number of resident entries. Eviction is LRU.
	// Zero means "unbounded"; production callers MUST pass a positive
	// value to keep memory bounded.
	MaxKeys int

	// Now is the clock the cache consults to decide expiry. Empty
	// uses time.Now.
	Now func() time.Time
}

// Entry is the cached token record. It is a value type returned by
// Get; mutating the returned copy does not affect the cache.
type Entry struct {
	Token     string
	ExpiresAt time.Time
}

// Stats reports per-cache counters; the operator surface exposes
// these as `ws_binding_token_cache_*_total` metrics.
type Stats struct {
	Hits      int64
	Misses    int64
	Evictions int64
}

// Cache is a per-client TTL'd LRU.
type Cache struct {
	mu      sync.Mutex
	now     func() time.Time
	maxKeys int
	order   *list.List
	entries map[cacheKey]*list.Element

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

type cacheKey struct {
	clientID       string
	subscriptionID uuid.UUID
}

type cacheVal struct {
	key   cacheKey
	entry Entry
}

// New constructs a Cache.
func New(opts Options) *Cache {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Cache{
		now:     now,
		maxKeys: opts.MaxKeys,
		order:   list.New(),
		entries: make(map[cacheKey]*list.Element),
	}
}

// Close releases any resources held by the cache. Today this is a
// no-op but callers SHOULD call it for forward compatibility — a
// future implementation may hold a background sweeper that needs
// joining.
func (c *Cache) Close() {}

// Get returns the cached entry under (clientID, subscriptionID) and
// reports whether it was a hit. Expired entries miss; a nil cache
// always misses.
func (c *Cache) Get(clientID string, subscriptionID uuid.UUID) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	k := cacheKey{clientID: clientID, subscriptionID: subscriptionID}
	c.mu.Lock()
	elem, ok := c.entries[k]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return Entry{}, false
	}
	val := elem.Value.(cacheVal)
	if !val.entry.ExpiresAt.After(c.now()) {
		c.order.Remove(elem)
		delete(c.entries, k)
		c.mu.Unlock()
		c.misses.Add(1)
		return Entry{}, false
	}
	c.order.MoveToFront(elem)
	c.mu.Unlock()
	c.hits.Add(1)
	return val.entry, true
}

// Put records a token under (clientID, subscriptionID). Existing
// entries under the same key are overwritten and bumped to MRU. When
// the cache is full the LRU entry is evicted.
func (c *Cache) Put(clientID string, subscriptionID uuid.UUID, token string, expiresAt time.Time) {
	if c == nil {
		return
	}
	k := cacheKey{clientID: clientID, subscriptionID: subscriptionID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[k]; ok {
		elem.Value = cacheVal{key: k, entry: Entry{Token: token, ExpiresAt: expiresAt}}
		c.order.MoveToFront(elem)
		return
	}
	elem := c.order.PushFront(cacheVal{key: k, entry: Entry{Token: token, ExpiresAt: expiresAt}})
	c.entries[k] = elem
	if c.maxKeys > 0 && c.order.Len() > c.maxKeys {
		victim := c.order.Back()
		if victim != nil {
			vk := victim.Value.(cacheVal).key
			c.order.Remove(victim)
			delete(c.entries, vk)
			c.evictions.Add(1)
		}
	}
}

// Sweep purges expired entries.
func (c *Cache) Sweep() {
	if c == nil {
		return
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, elem := range c.entries {
		val := elem.Value.(cacheVal)
		if !val.entry.ExpiresAt.After(now) {
			c.order.Remove(elem)
			delete(c.entries, k)
		}
	}
}

// Len reports the resident entry count. Test-only convenience.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// Stats reports the per-counter snapshot.
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	return Stats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Evictions: c.evictions.Load(),
	}
}

// StartSweeper spawns a goroutine that calls c.Sweep on a ticker
// until ctx is canceled. afterSweep, when non-nil, is invoked after
// every sweep call (the lifecycle wiring uses this to bump a metric;
// tests use it as a synchronization point).
//
// The returned channel closes when the sweeper goroutine exits, so
// the lifecycle module can join shutdown deterministically.
func StartSweeper(ctx context.Context, c *Cache, interval time.Duration, afterSweep func()) <-chan struct{} {
	done := make(chan struct{})
	if c == nil || interval <= 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.Sweep()
				if afterSweep != nil {
					afterSweep()
				}
			}
		}
	}()
	return done
}
