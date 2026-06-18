// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net/http"
	"strconv"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/fhirerror"
)

// ClientRateLimiter is a per-authenticated-client token bucket the
// handlers package wraps around POST /Subscription and the
// $get-ws-binding-token operation (S-3.3). It reuses the unexported
// rateLimiter primitive shared with the /token endpoint, but keys on
// the principal's ClientID rather than source IP so a single rogue
// client behind NAT cannot smear a burst across many addresses to
// bypass the cap.
//
// A nil receiver is a no-op so callers can store the limiter on a
// Deps-style struct and leave it unset for unbounded behavior.
type ClientRateLimiter struct {
	inner *rateLimiter
}

// NewClientRateLimiter constructs a ClientRateLimiter from cfg. A zero
// cfg (Burst <= 0) returns nil so callers idiomatically write
// `deps.X = NewClientRateLimiter(cfg, ...)` without branching on
// "feature enabled".
//
// now defaults to time.Now when nil.
func NewClientRateLimiter(cfg RateLimit, now func() time.Time) *ClientRateLimiter {
	if cfg.Burst <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &ClientRateLimiter{inner: newRateLimiter(cfg, now)}
}

// Middleware returns an HTTP middleware that consumes one token per
// request from the caller's bucket. On exhaustion it short-circuits
// with 429 Too Many Requests, an OperationOutcome body matching the
// rest of the FHIR API surface, and a Retry-After header in seconds.
//
// The bucket key is the authenticated principal's ClientID. If no
// principal is in the context, the limiter falls back to the source
// IP — matching the /token endpoint's behavior — so misconfigured
// deployments still degrade safely.
func (c *ClientRateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if c == nil || c.inner == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientRateLimitKey(r)
			ok, retryAfter := c.inner.Allow(key)
			if !ok {
				if retryAfter > 0 {
					secs := int(retryAfter.Seconds())
					if secs < 1 {
						secs = 1
					}
					w.Header().Set("Retry-After", strconv.Itoa(secs))
				}
				fhirerror.WriteError(w, http.StatusTooManyRequests,
					fhirerror.CodeThrottled, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientRateLimitKey prefers the authenticated ClientID, falling back
// to the source IP so a deployment that mounts the limiter upstream of
// auth (or runs without auth in dev) still gets per-source isolation
// rather than a single global bucket.
func clientRateLimitKey(r *http.Request) string {
	if p := PrincipalFromContext(r.Context()); p != nil && p.ClientID != "" {
		return "client:" + p.ClientID
	}
	return "ip:" + rateLimitKey(r)
}
