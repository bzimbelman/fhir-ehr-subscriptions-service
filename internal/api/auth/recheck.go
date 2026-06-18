// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"sync"
	"time"
)

// RecheckStatus is the outcome of a delivery-time scope re-check.
//
// Per docs/low-level-design/subscriptions-engine.md §3, the submatcher
// is required to re-validate the owning client's authorization just
// before fanout: a subscription whose creator's credentials have been
// revoked (token revocation, client deactivated, scope withdrawn) must
// stop receiving notifications immediately, not at the next manual
// delete.
type RecheckStatus int

// RecheckStatus values.
const (
	// RecheckActive means the owning client is still authorized to
	// receive notifications on this subscription. Submatcher proceeds
	// to insert the deliveries row.
	RecheckActive RecheckStatus = iota
	// RecheckRevoked means the owning client is no longer authorized.
	// Submatcher transitions the subscription to error/off, audits the
	// transition, and skips the fanout.
	RecheckRevoked
)

// String renders the status for log + test diagnostics.
func (s RecheckStatus) String() string {
	switch s {
	case RecheckActive:
		return "active"
	case RecheckRevoked:
		return "revoked"
	default:
		return "unknown"
	}
}

// Rechecker is the SPI the submatcher consumes at delivery prep.
//
// Implementations are expected to be cheap-on-cache-hit and
// safe-on-error (an error short-circuits to RecheckActive in the
// caller — fail-open by default to avoid hard-failing a healthy
// pipeline on a transient auth-store outage). The caller layers a
// subscription-level TTL cache on top of this SPI; implementations
// should NOT cache themselves unless the cache is invalidated on
// revocation events.
type Rechecker interface {
	// Recheck returns RecheckActive if the client identified by
	// clientID still holds the scope set required to maintain the
	// subscription identified by subscriptionID. The pair is the
	// minimum information the submatcher carries; richer SPIs that
	// need the topic URL, the channel type, etc., can extend this
	// interface (the submatcher will fall back to the existing shape
	// if the implementation does not support the extension).
	Recheck(ctx context.Context, clientID, subscriptionID string) (RecheckStatus, error)
}

// CachedRechecker wraps a Rechecker with a subscription-level TTL
// cache. It is goroutine-safe and is the recommended adapter for
// production wiring where the auth store is a remote service.
//
// The cache is keyed by subscription ID (not client ID): a single
// client may own thousands of subscriptions; checking once per
// subscription per TTL bounds the auth-store load while still letting
// a revocation propagate within `TTL` seconds. The cache stores both
// active and revoked outcomes — operators who need a faster
// revocation propagation should set a tighter TTL or invalidate
// explicitly via Invalidate.
type CachedRechecker struct {
	inner Rechecker
	ttl   time.Duration
	clock func() time.Time

	mu      sync.Mutex
	entries map[string]cachedRecheckEntry
}

type cachedRecheckEntry struct {
	status   RecheckStatus
	expires  time.Time
	cachedAt time.Time
}

// NewCachedRechecker wraps inner with a TTL cache. ttl ≤ 0 disables
// caching (every Recheck call hits inner). clock may be nil; nil uses
// time.Now.
func NewCachedRechecker(inner Rechecker, ttl time.Duration, clock func() time.Time) *CachedRechecker {
	if clock == nil {
		clock = time.Now
	}
	return &CachedRechecker{
		inner:   inner,
		ttl:     ttl,
		clock:   clock,
		entries: make(map[string]cachedRecheckEntry),
	}
}

// Recheck returns the cached status if the entry is still fresh,
// otherwise calls the inner Rechecker and refreshes the entry.
//
// On an inner error, the cached entry (if any) is preserved and the
// error is returned — callers fail-open to RecheckActive on error per
// the SPI contract.
func (c *CachedRechecker) Recheck(ctx context.Context, clientID, subscriptionID string) (RecheckStatus, error) {
	if c.ttl <= 0 || c.inner == nil {
		if c.inner == nil {
			return RecheckActive, nil
		}
		return c.inner.Recheck(ctx, clientID, subscriptionID)
	}
	now := c.clock()
	c.mu.Lock()
	if e, ok := c.entries[subscriptionID]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.status, nil
	}
	c.mu.Unlock()

	status, err := c.inner.Recheck(ctx, clientID, subscriptionID)
	if err != nil {
		return RecheckActive, err
	}
	c.mu.Lock()
	c.entries[subscriptionID] = cachedRecheckEntry{
		status:   status,
		expires:  now.Add(c.ttl),
		cachedAt: now,
	}
	c.mu.Unlock()
	return status, nil
}

// Invalidate evicts the cached entry for subscriptionID. Callers that
// receive an out-of-band revocation signal (e.g., a webhook from the
// auth store) can drop the cache entry here so the next Recheck call
// hits the inner Rechecker.
func (c *CachedRechecker) Invalidate(subscriptionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, subscriptionID)
}

// AlwaysActiveRechecker is the default Rechecker installed when the
// production wiring does not configure a real auth-store integration.
// It returns RecheckActive for every call. Use only when the auth
// model has no revocation surface (test deployments, single-tenant
// pilots).
type AlwaysActiveRechecker struct{}

// Recheck satisfies Rechecker.
func (AlwaysActiveRechecker) Recheck(context.Context, string, string) (RecheckStatus, error) {
	return RecheckActive, nil
}
