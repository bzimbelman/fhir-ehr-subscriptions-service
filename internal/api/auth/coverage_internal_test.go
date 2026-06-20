// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

// Internal tests covering the small pure helpers + Close lifecycle methods
// flagged by OP #308. No mocks: real net/http, real http.Transport, real
// time.Now, real maps. Some helpers (audienceMatches, claimToTime,
// jwksPolicy.validate, normalizeHosts, diagnosticForReason,
// classifyAssertionErr, RecheckStatus.String, maybeCompactLocked) are
// unexported, so this lives in `package auth` rather than `auth_test`.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRecheckStatus_String(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   RecheckStatus
		want string
	}{
		{"active", RecheckActive, "active"},
		{"revoked", RecheckRevoked, "revoked"},
		{"unknown_negative", RecheckStatus(-1), "unknown"},
		{"unknown_high", RecheckStatus(99), "unknown"},
	}
	for _, tc := range cases {
		got := tc.in.String()
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeHosts(t *testing.T) {
	t.Parallel()
	if got := normalizeHosts(nil); got != nil {
		t.Errorf("nil hosts: got %v, want nil", got)
	}
	if got := normalizeHosts([]string{}); got != nil {
		t.Errorf("empty hosts: got %v, want nil", got)
	}

	got := normalizeHosts([]string{"  ", "", "   "})
	if len(got) != 0 {
		t.Errorf("all-blank: got %v, want empty map", got)
	}

	got = normalizeHosts([]string{"Example.COM", "  example.com  ", "Other.Org"})
	if _, ok := got["example.com"]; !ok {
		t.Error("expected example.com (case-folded) in set")
	}
	if _, ok := got["other.org"]; !ok {
		t.Error("expected other.org (case-folded) in set")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 distinct entries (dedup), got %d: %v", len(got), got)
	}
}

func TestJwksPolicy_Validate(t *testing.T) {
	t.Parallel()
	allowList := normalizeHosts([]string{"idp.example.com"})

	cases := []struct {
		name    string
		policy  jwksPolicy
		raw     string
		wantErr bool
	}{
		{"https no allowlist", jwksPolicy{}, "https://anything.example.com/jwks", false},
		{"http rejected by default", jwksPolicy{}, "http://idp.example.com/jwks", true},
		{"http allowed when allowInsecure", jwksPolicy{allowInsecure: true}, "http://idp.example.com/jwks", false},
		{"unsupported scheme ftp", jwksPolicy{}, "ftp://idp.example.com/jwks", true},
		{"unsupported scheme empty", jwksPolicy{}, "/no-scheme", true},
		{"missing host", jwksPolicy{}, "https:///path", true},
		{"allowlist hit case-insensitive", jwksPolicy{allowedHosts: allowList}, "https://IDP.example.com/jwks", false},
		{"allowlist miss", jwksPolicy{allowedHosts: allowList}, "https://other.example.com/jwks", true},
		{"unparseable url", jwksPolicy{}, "://%%%", true},
	}
	for _, tc := range cases {
		err := tc.policy.validate(tc.raw)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestAudienceMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		claim    any
		expected string
		want     bool
	}{
		{"string match", "https://api.example.com/token", "https://api.example.com/token", true},
		{"string mismatch", "x", "y", false},
		{"any-slice match", []any{"a", "b", "match"}, "match", true},
		{"any-slice miss", []any{"a", "b"}, "match", false},
		{"any-slice non-string entry skipped", []any{42, "match"}, "match", true},
		{"string-slice match", []string{"a", "match"}, "match", true},
		{"string-slice miss", []string{"a", "b"}, "match", false},
		{"nil claim", nil, "match", false},
		{"unrecognized type (int)", 42, "42", false},
	}
	for _, tc := range cases {
		got := audienceMatches(tc.claim, tc.expected)
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestClaimToTime(t *testing.T) {
	t.Parallel()
	wantUnix := int64(1750000000)

	cases := []struct {
		name    string
		in      any
		want    int64
		wantErr bool
	}{
		{"float64", float64(wantUnix), wantUnix, false},
		{"int64", int64(wantUnix), wantUnix, false},
		{"json.Number int", json.Number("1750000000"), wantUnix, false},
		{"json.Number not int", json.Number("not-a-number"), 0, true},
		{"unknown type string", "1750000000", 0, true},
		{"nil", nil, 0, true},
	}
	for _, tc := range cases {
		got, err := claimToTime(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if got.Unix() != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got.Unix(), tc.want)
		}
	}
}

func TestDiagnosticForReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason string
		want   string
	}{
		{"expired", "assertion expired"},
		{"audience", "assertion audience mismatch"},
		{"malformed", "assertion malformed"},
		{"unknown_client", "unknown client"},
		{"replayed_jti", "assertion jti replay"},
		{"signature", "assertion invalid"},
		{"some_unrecognized_reason", "assertion invalid"},
		{"", "assertion invalid"},
	}
	for _, tc := range cases {
		got := diagnosticForReason(tc.reason)
		if got != tc.want {
			t.Errorf("reason=%q: got %q, want %q", tc.reason, got, tc.want)
		}
	}
}

func TestClassifyAssertionErr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"expired", jwt.ErrTokenExpired, "expired"},
		{"signature invalid", jwt.ErrTokenSignatureInvalid, "signature"},
		{"not yet valid", jwt.ErrTokenNotValidYet, "expired"},
		{"kid in message", errors.New("auth: kid not found"), "signature"},
		{"generic error defaults to signature", errors.New("something else"), "signature"},
	}
	for _, tc := range cases {
		got := classifyAssertionErr(tc.err)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNewClientRateLimiter_DisabledOnZeroBurst(t *testing.T) {
	t.Parallel()
	if got := NewClientRateLimiter(RateLimit{}, nil); got != nil {
		t.Errorf("expected nil for zero burst, got %v", got)
	}
	if got := NewClientRateLimiter(RateLimit{Burst: -1}, nil); got != nil {
		t.Errorf("expected nil for negative burst, got %v", got)
	}
}

func TestNewClientRateLimiter_NilNowDefaultsToTimeNow(t *testing.T) {
	t.Parallel()
	got := NewClientRateLimiter(RateLimit{Burst: 5, RefillPerSecond: 1}, nil)
	if got == nil {
		t.Fatal("expected non-nil limiter")
	}
	if got.inner == nil {
		t.Fatal("expected non-nil inner rateLimiter")
	}
}

func TestClientRateLimiter_Middleware_NilReceiverPassthrough(t *testing.T) {
	t.Parallel()
	var c *ClientRateLimiter
	mw := c.Middleware()
	called := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	if h == nil {
		t.Fatal("middleware returned nil handler")
	}
	// Build a minimal request and exercise the wrapped handler.
	req, err := http.NewRequest(http.MethodGet, "http://example.com/x", http.NoBody)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	h.ServeHTTP(noopResponseWriter{}, req)
	if !called {
		t.Error("expected next handler to be called for nil-receiver passthrough")
	}
}

func TestTokenEndpoint_Close_NilReceiverIsNoOp(t *testing.T) {
	t.Parallel()
	var te *TokenEndpoint
	if err := te.Close(); err != nil {
		t.Errorf("nil-receiver Close returned %v", err)
	}
}

func TestTokenEndpoint_Close_NilHTTPClientIsNoOp(t *testing.T) {
	t.Parallel()
	te := &TokenEndpoint{}
	if err := te.Close(); err != nil {
		t.Errorf("nil-HTTPClient Close returned %v", err)
	}
}

func TestTokenEndpoint_Close_ReleasesIdleConnections(t *testing.T) {
	t.Parallel()
	tr := &http.Transport{}
	te := &TokenEndpoint{cfg: TokenEndpointConfig{HTTPClient: &http.Client{Transport: tr}}}
	if err := te.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
	// CloseIdleConnections is idempotent; calling Close again is fine.
	if err := te.Close(); err != nil {
		t.Errorf("second Close returned %v", err)
	}
}

func TestTokenEndpoint_Close_NonHTTPTransportLeftAlone(t *testing.T) {
	t.Parallel()
	te := &TokenEndpoint{cfg: TokenEndpointConfig{HTTPClient: &http.Client{Transport: wrappingTransport{}}}}
	if err := te.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

func TestVerifier_Close_NilReceiverIsNoOp(t *testing.T) {
	t.Parallel()
	var v *Verifier
	if err := v.Close(); err != nil {
		t.Errorf("nil-receiver Close returned %v", err)
	}
}

func TestVerifier_Close_NilHTTPClientIsNoOp(t *testing.T) {
	t.Parallel()
	v := &Verifier{}
	if err := v.Close(); err != nil {
		t.Errorf("nil-HTTPClient Close returned %v", err)
	}
}

func TestVerifier_Close_ReleasesIdleConnections(t *testing.T) {
	t.Parallel()
	tr := &http.Transport{}
	v := &Verifier{cfg: VerifierConfig{HTTPClient: &http.Client{Transport: tr}}}
	if err := v.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

func TestVerifier_Close_NonHTTPTransportLeftAlone(t *testing.T) {
	t.Parallel()
	v := &Verifier{cfg: VerifierConfig{HTTPClient: &http.Client{Transport: wrappingTransport{}}}}
	if err := v.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
}

func TestMaybeCompactLocked_RebuildsOversizedOrderSlice(t *testing.T) {
	t.Parallel()
	// Force a deliberately oversized order slice — the natural Put loop
	// rarely grows cap(order) past 2*cap because Go's slice growth
	// after order[1:] is conservative. Construct the over-cap state
	// directly to exercise the rebuild branch.
	clock := func() time.Time { return time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC) }
	c := NewJTIReplayCache(2, clock)
	c.mu.Lock()
	c.order = make([]string, 0, 100) // cap >> 2*c.cap
	c.order = append(c.order, "a", "b")
	c.entries["a"] = clock().Add(time.Hour)
	c.entries["b"] = clock().Add(time.Hour)
	c.maybeCompactLocked()
	if cap(c.order) > 2*c.cap {
		t.Errorf("after compaction cap=%d, want <= %d", cap(c.order), 2*c.cap)
	}
	if len(c.order) != 2 {
		t.Errorf("after compaction len=%d, want 2", len(c.order))
	}
	c.mu.Unlock()
}

func TestWriteAuthFailure_StatusToCode(t *testing.T) {
	t.Parallel()
	// Drives the status==Forbidden branch (default branch is already covered).
	rec := httptest.NewRecorder()
	writeAuthFailure(rec, http.StatusForbidden, "scope mismatch")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "scope mismatch") {
		t.Errorf("body missing reason: %q", rec.Body.String())
	}
}

// noopResponseWriter satisfies http.ResponseWriter for tests that only
// need a sink and don't care about output.
type noopResponseWriter struct{}

func (noopResponseWriter) Header() http.Header         { return http.Header{} }
func (noopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (noopResponseWriter) WriteHeader(_ int)           {}

// wrappingTransport is a real RoundTripper that is *not* a *http.Transport
// — used to verify the Close guard against caller-supplied wrappers.
type wrappingTransport struct{}

func (wrappingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}
