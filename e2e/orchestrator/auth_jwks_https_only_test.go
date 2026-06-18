// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestE2E_TokenEndpoint_RefusesPlainHTTPJWKSURL builds a TokenEndpoint
// without AllowInsecureJWKS opt-in, points it at an http:// JWKS URL
// (the httptest fixture), and asserts that a token request is refused.
// Regression guard for B-12.
func TestE2E_TokenEndpoint_RefusesPlainHTTPJWKSURL(t *testing.T) {
	t.Parallel()

	clientID := "https-only-client"
	jwksSrv, kid, priv := newJWKSServer(t)
	if !strings.HasPrefix(jwksSrv.URL, "http://") {
		t.Fatalf("test JWKS server should be http://; got %s", jwksSrv.URL)
	}

	// Build the endpoint WITHOUT AllowInsecureJWKS — should refuse the
	// http:// JWKS URL when fetched.
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "test-aud",
		TokenURL:          "https://example.org/token",
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: e2eClientLookup{clientID: auth.ClientRecord{
			ID:      clientID,
			JwksURL: jwksSrv.URL + "/jwks",
			Scopes:  []string{"system/Subscription.r"},
		}},
		// NO AllowInsecureJWKS — secure default.
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://example.org/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", signed)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://example.invalid/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := newE2EResponseRecorder()
	te.ServeHTTP(rec, req)
	if rec.code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (plain http JWKS URL must be refused)", rec.code)
	}
}

// e2eResponseRecorder is a minimal http.ResponseWriter for in-process
// invocations where we don't want to spin up an httptest.Server.
type e2eResponseRecorder struct {
	code  int
	hdr   http.Header
	body  []byte
	wrote bool
}

func newE2EResponseRecorder() *e2eResponseRecorder {
	return &e2eResponseRecorder{hdr: make(http.Header), code: 200}
}

func (r *e2eResponseRecorder) Header() http.Header { return r.hdr }
func (r *e2eResponseRecorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.code = code
	r.wrote = true
}
func (r *e2eResponseRecorder) Write(p []byte) (int, error) {
	r.wrote = true
	r.body = append(r.body, p...)
	return len(p), nil
}
