// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TestE2E_TokenEndpoint_RejectsMissingJTI POSTs a client_credentials
// assertion with no jti claim. The server must respond 401 with an
// invalid_request / invalid_client OperationOutcome and must not mint
// a token. Regression guard for B-7.
func TestE2E_TokenEndpoint_RejectsMissingJTI(t *testing.T) {
	t.Parallel()

	clientID := "jti-required-client"
	jwksSrv, kid, priv := newJWKSServer(t)
	_, srv := newTokenEndpointE2E(t, clientID, jwksSrv.URL+"/jwks")

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": srv.URL,
		// NO jti — must be rejected.
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
	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("expected OperationOutcome; got %s", body)
	}
}
