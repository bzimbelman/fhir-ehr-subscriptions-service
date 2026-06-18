// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/auth"
)

func newTokenEndpoint(t *testing.T, audience, tokenURL, clientID, jwksURL string, scopes []string) *auth.TokenEndpoint {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	signing := []byte("test-server-signing-key-must-be-32-bytes!")
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          audience,
		TokenURL:          tokenURL,
		AccessTokenSecret: signing,
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: fakeClientLookup{
			clientID: {
				ID:      clientID,
				JwksURL: jwksURL,
				Scopes:  scopes,
			},
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}
	return te
}

func TestTokenEndpoint_HappyPath(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "lab-client"
	te := newTokenEndpoint(t, "https://fhir-subs.example.org",
		"https://fhir-subs.example.org/token",
		clientID, srv.URL+"/jwks",
		[]string{"system/Subscription.cruds", "system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://fhir-subs.example.org/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	form.Set("scope", "system/Subscription.cruds system/Subscription.r")

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if resp.AccessToken == "" {
		t.Errorf("access_token empty")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("token_type = %q; want Bearer", resp.TokenType)
	}
	if resp.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d", resp.ExpiresIn)
	}
}

func TestTokenEndpoint_BadGrantType(t *testing.T) {
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)
	form := url.Values{}
	form.Set("grant_type", "password") // not client_credentials
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestTokenEndpoint_BadAssertionAudience(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://wrong/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestTokenEndpoint_RoundTripIssuedTokenIsAcceptedByVerifier(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "https://api.example/aud",
		"https://api.example/token", clientID, srv.URL+"/jwks",
		[]string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://api.example/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	form.Set("scope", "system/Subscription.r")

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// Now verify with a Verifier that uses the SAME server signing key
	// so it can validate the issued token. The Verifier uses the
	// server-signed-tokens path: client_id resolves the registered
	// client; signature is server-signed, not client-signed.
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:     "https://api.example/aud",
		ClientLookup: fakeClientLookup{clientID: {ID: clientID, JwksURL: srv.URL + "/jwks", Scopes: []string{"system/Subscription.r"}}},
		IssuedSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		IssuedIssuer: "https://api.example/aud",
		Now:          func() time.Time { return now.Add(30 * time.Second) },
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	authReq := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	authReq.Header.Set("Authorization", "Bearer "+resp.AccessToken)
	p, status, reason := v.Authenticate(authReq)
	if status != 0 {
		t.Fatalf("issued token rejected: status=%d reason=%q", status, reason)
	}
	if p == nil || p.ClientID != clientID {
		t.Fatalf("principal = %+v", p)
	}
	_ = context.TODO
}
