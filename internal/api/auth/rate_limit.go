// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimit configures a per-source-IP token bucket. The /token endpoint
// is unauthenticated and CPU-intensive (RSA verify on user-controlled
// bytes), so an unrate-limited deployment can be DoS'd by replaying bad
// assertions at line rate (S-3).
//
// A zero RateLimit (Burst=0) disables limiting entirely.
type RateLimit struct {
	// Burst is the bucket capacity — the maximum number of immediate
	// requests allowed before refill kicks in.
	Burst int
	// RefillPerSecond is the steady-state allowed rate. Zero is valid:
	// it pins the bucket at Burst (strict cap, no replenishment).
	RefillPerSecond float64
	// MaxKeys caps the number of distinct source IPs tracked. Zero
	// defaults to 65536; once full, the oldest bucket is evicted.
	MaxKeys int
}

// rateLimiter is a per-key token bucket keyed by source IP. Safe for
// concurrent use.
type rateLimiter struct {
	cfg     RateLimit
	now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	order   []string
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(cfg RateLimit, now func() time.Time) *rateLimiter {
	if cfg.MaxKeys <= 0 {
		cfg.MaxKeys = 65536
	}
	return &rateLimiter{
		cfg:     cfg,
		now:     now,
		buckets: make(map[string]*tokenBucket),
	}
}

// Allow consumes one token from key's bucket and returns whether the
// request may proceed. When false, retryAfter hints how long the caller
// should wait before retrying.
func (r *rateLimiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if r == nil || r.cfg.Burst <= 0 {
		return true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	b, exists := r.buckets[key]
	if !exists {
		if len(r.buckets) >= r.cfg.MaxKeys {
			// Evict oldest entry to bound memory.
			old := r.order[0]
			r.order = r.order[1:]
			delete(r.buckets, old)
		}
		b = &tokenBucket{tokens: float64(r.cfg.Burst), last: now}
		r.buckets[key] = b
		r.order = append(r.order, key)
	}
	// Refill since last touch.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.cfg.RefillPerSecond
		if b.tokens > float64(r.cfg.Burst) {
			b.tokens = float64(r.cfg.Burst)
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}
	// No tokens — compute when the bucket will hold one.
	if r.cfg.RefillPerSecond <= 0 {
		// Fixed cap, no replenish — suggest 60s as conservative hint.
		return false, time.Minute
	}
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / r.cfg.RefillPerSecond * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// rateLimitKey extracts the source identifier for the bucket. Honors
// X-Forwarded-For when a TrustedProxies allowlist is configured;
// otherwise uses the immediate RemoteAddr host.
//
// In v1 we use the immediate peer; deployments behind a trusted
// load-balancer should rely on the LB's own rate limit or supply a
// proxy-aware adapter at the HTTP middleware seam.
func rateLimitKey(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}
