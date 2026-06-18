// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func tokenEndpointForm(assertion string) url.Values {
	v := url.Values{}
	v.Set("grant_type", "client_credentials")
	v.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	v.Set("client_assertion", assertion)
	return v
}

func postToken(t *testing.T, te http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	return rec
}

func TestTokenEndpoint_MalformedAssertion_NotAJWT(t *testing.T) {
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)
	rec := postToken(t, te, tokenEndpointForm("not.a.jwt.at.all"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OperationOutcome") {
		t.Errorf("expected OperationOutcome; got %s", rec.Body.String())
	}
}

func TestTokenEndpoint_ExpiredAssertion(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-30 * time.Minute).Unix(),
		"exp": now.Add(-10 * time.Minute).Unix(), // expired 10 min ago
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

// signWithKid signs claims with key k but stamps a different kid header
// so JWKS lookup misses.
func signWithKid(t *testing.T, k *keyMaterial, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	out, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

func TestTokenEndpoint_UnknownKid(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := signWithKid(t, k, "not-the-kid", jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestTokenEndpoint_SignedWithDifferentKey(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	tok.Header["kid"] = k.kid // matching kid, but the signature won't verify
	signed, err := tok.SignedString(other)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := postToken(t, te, tokenEndpointForm(signed))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestTokenEndpoint_MissingExp(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		// no exp
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestTokenEndpoint_MissingIat(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		// no iat
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestTokenEndpoint_MissingIss(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		// no iss
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestTokenEndpoint_AudienceAsArray(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": []any{"https://other/token", "https://x/token"}, // RFC 7519 array
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenEndpoint_JTIReplay(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	jti := uuid.NewString()
	claims := jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": jti,
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	}
	assertion := k.sign(t, claims)

	first := postToken(t, te, tokenEndpointForm(assertion))
	if first.Code != http.StatusOK {
		t.Fatalf("first attempt = %d body=%s", first.Code, first.Body.String())
	}

	// Replay the same assertion (same jti) — should be rejected.
	second := postToken(t, te, tokenEndpointForm(assertion))
	if second.Code != http.StatusUnauthorized {
		t.Errorf("replay = %d; want 401", second.Code)
	}
}
