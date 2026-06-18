// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"crypto/rand"
	"crypto/rsa"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestE2E_TokenEndpoint_DoesNotLeakLibraryError signs a client_credentials
// assertion with a key that doesn't match the JWKS server, then asserts
// the OperationOutcome diagnostics body contains no jwt-library
// internal phrases. Regression guard for B-8.
func TestE2E_TokenEndpoint_DoesNotLeakLibraryError(t *testing.T) {
	t.Parallel()

	clientID := "scrub-client"
	jwksSrv, kid, _ := newJWKSServer(t)
	_, srv := newTokenEndpointE2E(t, clientID, jwksSrv.URL+"/jwks")

	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": srv.URL,
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(other)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", signed)
	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	bannedPhrases := []string{
		"crypto/rsa",
		"rsa.PublicKey",
		"verification error",
		"token signature is invalid",
		"could not parse token",
		"jwt:",
		"keyfunc",
		kid,
	}
	for _, ph := range bannedPhrases {
		if strings.Contains(string(body), ph) {
			t.Errorf("response leaks %q; body=%s", ph, body)
		}
	}
}

// TestE2E_TokenEndpoint_MalformedAssertionScrubbed POSTs an assertion
// that isn't a JWT at all and asserts the diagnostics body remains
// generic.
func TestE2E_TokenEndpoint_MalformedAssertionScrubbed(t *testing.T) {
	t.Parallel()

	jwksSrv, _, _ := newJWKSServer(t)
	_, srv := newTokenEndpointE2E(t, "any", jwksSrv.URL+"/jwks")

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", "not.a.jwt")
	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, ph := range []string{"jwt:", "token contains an invalid number of segments"} {
		if strings.Contains(string(body), ph) {
			t.Errorf("response leaks %q; body=%s", ph, body)
		}
	}
}
