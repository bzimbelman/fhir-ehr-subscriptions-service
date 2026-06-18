// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"sync"
	"time"
)

// Principal is the authenticated caller derived from a verified bearer
// token plus the registered client's record. It is attached to the
// request context by the auth middleware.
//
// Scopes is the canonical granted-scope slice (preserved for logging
// and serialization). scopeSet is a lazily-built lookup index used by
// HasScope/HasAllScopes; without it, lookups were O(n) on every gate
// (S-3).
type Principal struct {
	ClientID string
	Scopes   []string
	JTI      string
	Exp      time.Time

	scopeOnce sync.Once
	scopeSet  map[string]struct{}
}

func (p *Principal) buildScopeSet() {
	p.scopeOnce.Do(func() {
		set := make(map[string]struct{}, len(p.Scopes))
		for _, s := range p.Scopes {
			set[s] = struct{}{}
		}
		p.scopeSet = set
	})
}

// HasScope reports whether the principal has been granted scope s.
func (p *Principal) HasScope(s string) bool {
	p.buildScopeSet()
	_, ok := p.scopeSet[s]
	return ok
}

// HasAllScopes reports whether the principal has every scope in needed.
func (p *Principal) HasAllScopes(needed ...string) bool {
	p.buildScopeSet()
	for _, s := range needed {
		if _, ok := p.scopeSet[s]; !ok {
			return false
		}
	}
	return true
}

type principalCtxKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext returns the principal stored in ctx, or nil if
// none. Handlers that require a principal MUST be wired behind the auth
// middleware, which guarantees the principal is present.
func PrincipalFromContext(ctx context.Context) *Principal {
	if v := ctx.Value(principalCtxKey{}); v != nil {
		if p, ok := v.(*Principal); ok {
			return p
		}
	}
	return nil
}
