// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

type tokenFormData struct {
	values url.Values
}

func tokenForm(assertion string) tokenFormData {
	v := url.Values{}
	v.Set("grant_type", "client_credentials")
	v.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	v.Set("client_assertion", assertion)
	v.Set("scope", "system/Subscription.r")
	return tokenFormData{values: v}
}

func (t tokenFormData) body() io.Reader {
	return strings.NewReader(t.values.Encode())
}

// recordingMetrics captures auth failure reasons in-memory for assertions.
type recordingMetrics struct {
	mu                       sync.Mutex
	failures                 map[string]int
	tokensIssued             int
	jwksSingleflightCollides int
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{failures: map[string]int{}}
}

func (r *recordingMetrics) RecordAuthFailure(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures[reason]++
}

func (r *recordingMetrics) RecordTokenIssued() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokensIssued++
}

func (r *recordingMetrics) RecordJWKSSingleflightCollision() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jwksSingleflightCollides++
}

func (r *recordingMetrics) jwksCollisionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jwksSingleflightCollides
}

func (r *recordingMetrics) failureCount(reason string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures[reason]
}

func newVerifierWithMetrics(t *testing.T, audience, jwksURL, clientID string, scopes []string, m auth.MetricsRecorder) *auth.Verifier {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: audience,
		ClientLookup: fakeClientLookup{
			clientID: {ID: clientID, JwksURL: jwksURL, Scopes: scopes},
		},
		ClockSkew:         60 * time.Second,
		Now:               now,
		Metrics:           m,
		AllowInsecureJWKS: true,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestVerifier_ExpiredToken_RecordsExpiredFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"}, rec)

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims(clientID, "aud", now.Add(-10*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
	if got := rec.failureCount("expired"); got != 1 {
		t.Errorf("failures{reason=expired} = %d; want 1", got)
	}
}

func TestVerifier_AudienceMismatch_RecordsAudienceFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "expected-aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"}, rec)

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	claims := defaultClaims(clientID, "wrong-aud", now.Add(5*time.Minute))
	tok := k.sign(t, claims)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
	if got := rec.failureCount("audience"); got != 1 {
		t.Errorf("failures{reason=audience} = %d; want 1", got)
	}
}

func TestVerifier_UnknownClient_RecordsUnknownClientFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "aud", srv.URL+"/jwks", "registered", []string{"system/Subscription.r"}, rec)

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims("unknown-client", "aud", now.Add(5*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
	if got := rec.failureCount("unknown_client"); got != 1 {
		t.Errorf("failures{reason=unknown_client} = %d; want 1", got)
	}
}

func TestVerifier_RevokedClient_RecordsRevokedFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "aud", srv.URL+"/jwks", clientID, []string{}, rec)

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims(clientID, "aud", now.Add(5*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d", status)
	}
	if got := rec.failureCount("revoked"); got != 1 {
		t.Errorf("failures{reason=revoked} = %d; want 1", got)
	}
}

func TestVerifier_ReplayedJTI_RecordsReplayedFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"}, rec)

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	claims := defaultClaims(clientID, "aud", now.Add(5*time.Minute))
	tok := k.sign(t, claims)
	req1 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req1.Header.Set("Authorization", "Bearer "+tok)
	if _, st, _ := v.Authenticate(req1); st != 0 {
		t.Fatalf("first auth should succeed, got %d", st)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	_, st, _ := v.Authenticate(req2)
	if st != http.StatusUnauthorized {
		t.Fatalf("status = %d", st)
	}
	if got := rec.failureCount("replayed_jti"); got != 1 {
		t.Errorf("failures{reason=replayed_jti} = %d; want 1", got)
	}
}

func TestVerifier_MalformedToken_RecordsMalformedFailure(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	rec := newRecordingMetrics()
	v := newVerifierWithMetrics(t, "aud", srv.URL+"/jwks", "c", []string{"system/Subscription.r"}, rec)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
	if got := rec.failureCount("malformed"); got != 1 {
		t.Errorf("failures{reason=malformed} = %d; want 1", got)
	}
}

func TestTokenEndpoint_Happy_RecordsTokenIssued(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "lab-client"
	rec := newRecordingMetrics()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "aud",
		TokenURL:          "https://api.example/token",
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: fakeClientLookup{
			clientID: {ID: clientID, JwksURL: srv.URL + "/jwks", Scopes: []string{"system/Subscription.r"}},
		},
		Now:               now,
		Metrics:           rec,
		AllowInsecureJWKS: true,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://api.example/token",
		"jti": uuid.NewString(),
		"iat": now().Add(-1 * time.Minute).Unix(),
		"exp": now().Add(2 * time.Minute).Unix(),
	})
	form := tokenForm(assertion)
	req := httptest.NewRequest(http.MethodPost, "/token", form.body())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	te.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if rec.tokensIssued != 1 {
		t.Errorf("tokens_issued = %d; want 1", rec.tokensIssued)
	}
}
