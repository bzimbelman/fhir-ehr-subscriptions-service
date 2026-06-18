// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// S-3.3 unit tests for the exported ClientRateLimiter primitive used
// by the chi handlers. The handler-level tests prove the wire-up; here
// we exercise the bucket math, key extraction (principal vs. source IP
// fallback), 429+Retry-After response shape, and the disabled (nil)
// no-op path so callers can rely on `nil` meaning "no policy".

// noopHandler is the downstream handler the limiter wraps in tests; on
// pass-through it writes 200 + a sentinel body.
func noopHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// TestClientRateLimiter_Nil_NoOp asserts a nil receiver short-circuits
// to the wrapped handler unconditionally.
func TestClientRateLimiter_Nil_NoOp(t *testing.T) {
	t.Parallel()
	var lim *auth.ClientRateLimiter // nil
	mw := lim.Middleware()
	h := mw(noopHandler())

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: code=%d, want 200 (nil limiter)", i+1, rec.Code)
		}
	}
}

// TestClientRateLimiter_PrincipalIDIsKey asserts the bucket key is the
// authenticated ClientID — not the source IP — so a NAT'd burst from a
// single client cannot be smeared across many IPs and bypass the cap.
func TestClientRateLimiter_PrincipalIDIsKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	lim := auth.NewClientRateLimiter(auth.RateLimit{Burst: 1, RefillPerSecond: 0}, func() time.Time { return now })

	send := func(remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		ctx := auth.WithPrincipal(req.Context(), &auth.Principal{ClientID: "client-A"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		lim.Middleware()(noopHandler()).ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send("10.0.0.1:1234"); got != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", got)
	}
	// Different RemoteAddr but SAME principal → still throttled.
	if got := send("10.0.0.99:1234"); got != http.StatusTooManyRequests {
		t.Fatalf("second request from new IP, same principal: got %d, want 429", got)
	}
}

// TestClientRateLimiter_FallsBackToSourceIP asserts that if no
// principal is present (e.g., wired upstream of auth in some unusual
// deployment), the limiter still works keyed by source IP rather than
// degenerating into a global single bucket.
func TestClientRateLimiter_FallsBackToSourceIP(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	lim := auth.NewClientRateLimiter(auth.RateLimit{Burst: 1, RefillPerSecond: 0}, func() time.Time { return now })

	send := func(remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		// no principal
		rec := httptest.NewRecorder()
		lim.Middleware()(noopHandler()).ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send("10.0.0.1:1234"); got != http.StatusOK {
		t.Fatalf("first .1: got %d, want 200", got)
	}
	if got := send("10.0.0.1:5555"); got != http.StatusTooManyRequests {
		t.Fatalf("second .1: got %d, want 429", got)
	}
	// Different IP gets its own bucket.
	if got := send("10.0.0.2:1234"); got != http.StatusOK {
		t.Fatalf("first .2 (new IP): got %d, want 200", got)
	}
}

// TestClientRateLimiter_RetryAfterIsPositiveInteger asserts the 429
// response carries Retry-After as a positive integer-seconds value
// (not RFC 1123 date) — RFC 6585 permits either, but the existing
// /token implementation emits seconds and clients/ops dashboards rely
// on that shape.
func TestClientRateLimiter_RetryAfterIsPositiveInteger(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	lim := auth.NewClientRateLimiter(auth.RateLimit{Burst: 1, RefillPerSecond: 0}, func() time.Time { return now })

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		ctx := auth.WithPrincipal(req.Context(), &auth.Principal{ClientID: "c1"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		lim.Middleware()(noopHandler()).ServeHTTP(rec, req)
		return rec
	}

	_ = send() // spend the token
	rec := send()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second: code=%d, want 429", rec.Code)
	}
	val := rec.Header().Get("Retry-After")
	if val == "" {
		t.Fatalf("missing Retry-After")
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		t.Fatalf("Retry-After=%q want positive integer seconds (err=%v)", val, err)
	}
}

// TestClientRateLimiter_ResetAfterRefill asserts that a controllable
// clock advancing past the refill window restores the bucket and the
// previously-throttled caller succeeds.
func TestClientRateLimiter_ResetAfterRefill(t *testing.T) {
	t.Parallel()
	current := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	clock := &advanceableClock{now: current}
	lim := auth.NewClientRateLimiter(auth.RateLimit{Burst: 1, RefillPerSecond: 1}, clock.Now)

	send := func() int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		ctx := auth.WithPrincipal(req.Context(), &auth.Principal{ClientID: "c1"})
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()
		lim.Middleware()(noopHandler()).ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send(); got != http.StatusOK {
		t.Fatalf("first: got %d", got)
	}
	if got := send(); got != http.StatusTooManyRequests {
		t.Fatalf("second (immediate): got %d, want 429", got)
	}
	clock.Advance(2 * time.Second)
	if got := send(); got != http.StatusOK {
		t.Fatalf("third (after refill): got %d, want 200", got)
	}
}

type advanceableClock struct {
	now time.Time
}

func (c *advanceableClock) Now() time.Time          { return c.now }
func (c *advanceableClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
