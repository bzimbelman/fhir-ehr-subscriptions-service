// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// clientRateLimiters bundles the per-client token buckets the
// production binary plugs into handlers.Deps. Each field is a public
// auth.ClientRateLimiter (or nil to disable). Returning a struct
// instead of a tuple keeps the caller from accidentally swapping
// fields when only one is non-nil.
//
// Story #104 (S-3.3): without these populated, the chi middleware on
// POST /Subscription and on $get-ws-binding-token degrades to
// pass-through because Middleware() is nil-safe. The struct gives
// future surfaces (e.g. /Search rate limit) a single seam to extend.
type clientRateLimiters struct {
	SubscriptionCreate *auth.ClientRateLimiter
	WSBindingToken     *auth.ClientRateLimiter
}

// buildClientRateLimitersFromAuth materializes the per-client rate
// limiters declared in cfg into auth.ClientRateLimiter instances. A
// zero RateLimitConfig (Burst <= 0) yields nil — auth.Middleware
// treats nil as a no-op so operators who omit the block keep
// unbounded behavior, matching the pattern story #92 established for
// Admin.RateLimit.
//
// now is the clock fed to auth.NewClientRateLimiter; pass nil in
// production so the limiter uses time.Now. Tests pass a fake clock
// directly to NewClientRateLimiter rather than threading one through
// here, so this helper deliberately keeps the signature flat.
func buildClientRateLimitersFromAuth(cfg *AuthConfig, now func() time.Time) clientRateLimiters {
	if cfg == nil {
		return clientRateLimiters{}
	}
	return clientRateLimiters{
		SubscriptionCreate: auth.NewClientRateLimiter(auth.RateLimit{
			Burst:           cfg.SubscriptionCreateRateLimit.Burst,
			RefillPerSecond: cfg.SubscriptionCreateRateLimit.RefillPerSecond,
			MaxKeys:         cfg.SubscriptionCreateRateLimit.MaxKeys,
		}, now),
		WSBindingToken: auth.NewClientRateLimiter(auth.RateLimit{
			Burst:           cfg.WSBindingTokenRateLimit.Burst,
			RefillPerSecond: cfg.WSBindingTokenRateLimit.RefillPerSecond,
			MaxKeys:         cfg.WSBindingTokenRateLimit.MaxKeys,
		}, now),
	}
}
