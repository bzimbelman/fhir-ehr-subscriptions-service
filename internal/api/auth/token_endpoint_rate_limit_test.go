// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// S-3 (no rate limit on /token): the unauthenticated /token endpoint is
// the only entry point that processes JWT signature verification on
// arbitrary user-controlled bytes. Without an upstream rate limit, an
// attacker can drive RSA-verify CPU exhaustion at line rate. We add a
// per-source-IP token bucket and return 429 once it is empty.
func TestTokenEndpoint_RateLimitsBogusAssertions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	signing := []byte("test-server-signing-key-must-be-32-bytes!")
	cfg := auth.TokenEndpointConfig{
		Audience:          "https://fhir-subs.example.org",
		TokenURL:          "https://fhir-subs.example.org/token",
		AccessTokenSecret: signing,
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup:      fakeClientLookup{},
		Now:               func() time.Time { return now },
		RateLimitPerSource: auth.RateLimit{
			Burst:           3,
			RefillPerSecond: 0, // strict cap; no replenish during the test window
		},
	}
	te, err := auth.NewTokenEndpoint(cfg)
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	send := func() *httptest.ResponseRecorder {
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", "not-a-real-jwt")
		req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10.0.0.1:55555"
		rec := httptest.NewRecorder()
		te.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 3; i++ {
		rec := send()
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: rate-limited prematurely (code=%d)", i, rec.Code)
		}
	}
	rec := send()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt: code = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatalf("4th attempt: missing Retry-After header")
	}
}

// Distinct source IPs get distinct buckets.
func TestTokenEndpoint_RateLimitPerSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	signing := []byte("test-server-signing-key-must-be-32-bytes!")
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:           "https://fhir-subs.example.org",
		TokenURL:           "https://fhir-subs.example.org/token",
		AccessTokenSecret:  signing,
		AccessTokenTTL:     5 * time.Minute,
		ClientLookup:       fakeClientLookup{},
		Now:                func() time.Time { return now },
		RateLimitPerSource: auth.RateLimit{Burst: 1, RefillPerSecond: 0},
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	send := func(remote string) int {
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", "x")
		req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		te.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := send("10.0.0.1:1234"); got == http.StatusTooManyRequests {
		t.Fatalf("first request from .1: code=%d", got)
	}
	if got := send("10.0.0.1:1234"); got != http.StatusTooManyRequests {
		t.Fatalf("second request from .1: code=%d, want 429", got)
	}
	if got := send("10.0.0.2:1234"); got == http.StatusTooManyRequests {
		t.Fatalf("first request from .2: code=%d (different source should not be rate-limited)", got)
	}
}

// S-3 (claimToTime parse error swallowed → fail-closed): the assertion's
// exp claim is required and must parse cleanly. We reject the request
// rather than fall through with a zero-time JTI Put.
func TestTokenEndpoint_HappyPath_JTIReplayCaught(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "lab-client"
	te := newTokenEndpoint(t, "https://fhir-subs.example.org",
		"https://fhir-subs.example.org/token",
		clientID, srv.URL+"/jwks",
		[]string{"system/Subscription.cruds"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	jti := uuid.NewString()
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://fhir-subs.example.org/token",
		"jti": jti,
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	req1 := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req1.RemoteAddr = "10.0.0.7:1"
	rec1 := httptest.NewRecorder()
	te.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first: code=%d body=%s", rec1.Code, rec1.Body.String())
	}

	// Replay must be caught — proving JTI was Put with a non-zero exp.
	req2 := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.RemoteAddr = "10.0.0.7:1"
	rec2 := httptest.NewRecorder()
	te.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("replay: code=%d body=%s, want 401", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "jti replay") {
		t.Fatalf("replay body=%s; want jti replay", rec2.Body.String())
	}
}
