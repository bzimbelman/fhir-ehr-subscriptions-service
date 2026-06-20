// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/wsbindingcache"
)

// WrapWsBindingTokenCache adapts a *wsbindingcache.Cache to the
// WsBindingTokenCache contract. The adapter is the wiring-layer
// glue between the cache implementation and the handler-level
// interface so the cache pkg stays free of any handlers.* import
// (a cache that the handlers depend on must not depend back on
// handlers, or every consumer pulls handlers transitively).
//
// A nil cache yields the bypass adapter — Get reports miss, Put is
// a no-op — so callers can pass a conditionally-wired cache without
// special-casing.
func WrapWsBindingTokenCache(c *wsbindingcache.Cache) WsBindingTokenCache {
	if c == nil {
		return NoopWsBindingTokenCache{}
	}
	return &wsBindingCacheAdapter{c: c}
}

type wsBindingCacheAdapter struct {
	c *wsbindingcache.Cache
}

func (a *wsBindingCacheAdapter) Get(clientID string, subscriptionID uuid.UUID) (WsBindingTokenCacheEntry, bool) {
	e, ok := a.c.Get(clientID, subscriptionID)
	if !ok {
		return WsBindingTokenCacheEntry{}, false
	}
	return WsBindingTokenCacheEntry{Token: e.Token, ExpiresAt: e.ExpiresAt}, true
}

func (a *wsBindingCacheAdapter) Put(clientID string, subscriptionID uuid.UUID, token string, expiresAt time.Time) {
	a.c.Put(clientID, subscriptionID, token, expiresAt)
}
