// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"time"
)

// Principal is the authenticated caller derived from a verified bearer
// token plus the registered client's record. It is attached to the
// request context by the auth middleware.
type Principal struct {
	ClientID string
	Scopes   []string
	JTI      string
	Exp      time.Time
}

// HasScope reports whether the principal has been granted scope s.
func (p *Principal) HasScope(s string) bool {
	for _, granted := range p.Scopes {
		if granted == s {
			return true
		}
	}
	return false
}

// HasAllScopes reports whether the principal has every scope in needed.
func (p *Principal) HasAllScopes(needed ...string) bool {
	for _, s := range needed {
		if !p.HasScope(s) {
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
