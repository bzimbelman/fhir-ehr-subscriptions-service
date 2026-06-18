// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/auth"
)

// keyMaterial is a generated RSA key plus its public key id. Tests use
// it to mint signed JWTs and to publish a JWKS.
type keyMaterial struct {
	priv *rsa.PrivateKey
	kid  string
}

func newKey(t *testing.T) *keyMaterial {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return &keyMaterial{priv: priv, kid: uuid.NewString()}
}

// jwks renders the public key as a JWKS document at the given URL.
func (k *keyMaterial) jwks() map[string]any {
	pub := k.priv.PublicKey
	n := encodeBigInt(pub.N.Bytes())
	e := encodeBigInt([]byte{1, 0, 1}) // 65537
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": k.kid,
				"n":   n,
				"e":   e,
			},
		},
	}
}

func encodeBigInt(b []byte) string {
	return jwt.EncodeSegment(b)
}

func (k *keyMaterial) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = k.kid
	s, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

// jwksServer hosts a single client's JWKS at /jwks.
func jwksServer(t *testing.T, k *keyMaterial) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(k.jwks())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeClientLookup is an in-memory implementation of ClientLookup.
type fakeClientLookup map[string]auth.ClientRecord

func (f fakeClientLookup) GetByID(_ context.Context, id string) (*auth.ClientRecord, error) {
	r, ok := f[id]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func newVerifier(t *testing.T, audience, jwksURL, clientID string, scopes []string) *auth.Verifier {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: audience,
		ClientLookup: fakeClientLookup{
			clientID: {
				ID:      clientID,
				JwksURL: jwksURL,
				Scopes:  scopes,
			},
		},
		ClockSkew: 60 * time.Second,
		Now:       now,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func defaultClaims(clientID, audience string, exp time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       clientID,
		"sub":       clientID,
		"aud":       audience,
		"client_id": clientID,
		"jti":       uuid.NewString(),
		"iat":       exp.Add(-5 * time.Minute).Unix(),
		"exp":       exp.Unix(),
		"scope":     "system/Subscription.cruds system/Subscription.r",
	}
}

func TestVerifier_ValidToken_AttachesPrincipal(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)

	clientID := "lab-client"
	v := newVerifier(t, "https://fhir-subs.example.org",
		srv.URL+"/jwks", clientID,
		[]string{"system/Subscription.cruds", "system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims(clientID, "https://fhir-subs.example.org", now.Add(5*time.Minute)))

	req := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	p, status, _ := v.Authenticate(req)
	if status != 0 {
		t.Fatalf("expected status 0 (ok); got %d", status)
	}
	if p == nil {
		t.Fatalf("expected principal, got nil")
	}
	if p.ClientID != clientID {
		t.Errorf("ClientID = %q; want %q", p.ClientID, clientID)
	}
	if !p.HasScope("system/Subscription.cruds") {
		t.Errorf("missing expected scope")
	}
}

func TestVerifier_ExpiredToken_Returns401(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	v := newVerifier(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// expired well before now and outside skew tolerance.
	tok := k.sign(t, defaultClaims(clientID, "aud", now.Add(-10*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	p, status, reason := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (reason=%q)", status, reason)
	}
	if p != nil {
		t.Errorf("expected nil principal on expired token")
	}
	if !strings.Contains(strings.ToLower(reason), "expir") {
		t.Errorf("reason %q does not mention expiration", reason)
	}
}

func TestVerifier_AudienceMismatch_Returns401(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	v := newVerifier(t, "expected-aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	claims := defaultClaims(clientID, "wrong-aud", now.Add(5*time.Minute))
	tok := k.sign(t, claims)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	_, status, reason := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d; reason=%q", status, reason)
	}
	if !strings.Contains(strings.ToLower(reason), "audience") {
		t.Errorf("reason %q should reference audience", reason)
	}
}

func TestVerifier_UnknownClient_Returns401(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	registered := "registered-client"
	// Verifier knows about registered, but token claims a different client.
	v := newVerifier(t, "aud", srv.URL+"/jwks", registered, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims("unknown-client", "aud", now.Add(5*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	_, status, reason := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d; reason=%q", status, reason)
	}
	if !strings.Contains(strings.ToLower(reason), "client") {
		t.Errorf("reason %q should reference unknown client", reason)
	}
}

func TestVerifier_MissingBearer_Returns401(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	v := newVerifier(t, "aud", srv.URL+"/jwks", "c", []string{"system/Subscription.r"})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d", status)
	}
}

func TestVerifier_ReplayedJTI_Returns401(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	v := newVerifier(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	claims := defaultClaims(clientID, "aud", now.Add(5*time.Minute))
	tok := k.sign(t, claims)

	req1 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req1.Header.Set("Authorization", "Bearer "+tok)
	if _, status, _ := v.Authenticate(req1); status != 0 {
		t.Fatalf("first auth should succeed; got %d", status)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	_, status, reason := v.Authenticate(req2)
	if status != http.StatusUnauthorized {
		t.Fatalf("replay should return 401; got %d", status)
	}
	if !strings.Contains(strings.ToLower(reason), "replay") {
		t.Errorf("reason %q should mention replay", reason)
	}
}

func TestVerifier_RevokedClient_Returns403(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	// "revoked" expressed as zero scopes.
	v := newVerifier(t, "aud", srv.URL+"/jwks", clientID, []string{})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims(clientID, "aud", now.Add(5*time.Minute)))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	_, status, _ := v.Authenticate(req)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", status)
	}
}

func TestVerifier_Middleware_AttachesPrincipal(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	v := newVerifier(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, defaultClaims(clientID, "aud", now.Add(5*time.Minute)))

	called := false
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		p := auth.PrincipalFromContext(r.Context())
		if p == nil {
			t.Errorf("expected principal in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatalf("inner handler not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestVerifier_Middleware_RejectsMissingToken(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	v := newVerifier(t, "aud", srv.URL+"/jwks", "c", []string{"system/Subscription.r"})

	called := false
	handler := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	handler.ServeHTTP(rec, req)
	if called {
		t.Fatalf("inner handler should not run on auth failure")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"resourceType":"OperationOutcome"`) {
		t.Errorf("body should be OperationOutcome; got %s", body)
	}
}
