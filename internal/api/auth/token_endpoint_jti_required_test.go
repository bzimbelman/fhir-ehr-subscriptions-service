// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestTokenEndpoint_AssertionMissingJTI_Rejected pins the regression in
// B-7: RFC 7523 §3 mandates `jti`, and the token endpoint must refuse a
// client_credentials assertion that has no jti. Without this, replay
// detection is silently bypassed because the cache lookup is gated on
// `jti != ""`.
func TestTokenEndpoint_AssertionMissingJTI_Rejected(t *testing.T) {
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
		// no jti — must be rejected
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (assertion without jti must be rejected)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "OperationOutcome") {
		t.Errorf("expected OperationOutcome; got %s", rec.Body.String())
	}
}

func TestTokenEndpoint_AssertionEmptyJTI_Rejected(t *testing.T) {
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
		"jti": "", // explicit empty — also a bypass
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	rec := postToken(t, te, tokenEndpointForm(assertion))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (assertion with empty jti must be rejected)", rec.Code)
	}
}

// TestVerifier_TokenMissingJTI_Rejected pins the equivalent invariant on
// the verification path. A bearer token presented to the API without a
// jti claim must be rejected — otherwise replay protection silently
// disengages whenever a client omits jti.
func TestVerifier_TokenMissingJTI_Rejected(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "lab-client"
	v := newVerifier(t, "aud", srv.URL+"/jwks", clientID, []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := k.sign(t, jwt.MapClaims{
		"iss":       clientID,
		"sub":       clientID,
		"client_id": clientID,
		"aud":       "aud",
		"scope":     "system/Subscription.r",
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
		// no jti
	})

	req := httptestNewBearerReq(tok)
	_, status, _ := v.Authenticate(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (token without jti must be rejected)", status)
	}
}

// httptestNewBearerReq builds a GET request carrying tok as a bearer
// credential. Helper kept in a separate spot to avoid colliding with
// the (already rich) helpers used by the other auth tests.
func httptestNewBearerReq(tok string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, "https://example/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

// guard against accidental import-only behaviour during refactors.
var _ = auth.NewVerifier
