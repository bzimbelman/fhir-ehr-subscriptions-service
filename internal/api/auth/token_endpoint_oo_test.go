// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// extractOAuthErrorFromOutcome decodes an OperationOutcome and returns
// the OAuth `error` code carried in the first issue's
// details.coding[system=urn:ietf:rfc:6749].code, plus the diagnostics
// string (which mirrors the OAuth error_description).
func extractOAuthErrorFromOutcome(t *testing.T, body []byte) (oauthCode, diagnostics string) {
	t.Helper()
	var oo struct {
		ResourceType string `json:"resourceType"`
		Issue        []struct {
			Severity    string `json:"severity"`
			Code        string `json:"code"`
			Diagnostics string `json:"diagnostics"`
			Details     struct {
				Coding []struct {
					System string `json:"system"`
					Code   string `json:"code"`
				} `json:"coding"`
			} `json:"details"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &oo); err != nil {
		t.Fatalf("unmarshal OperationOutcome: %v; body=%s", err, body)
	}
	if oo.ResourceType != "OperationOutcome" {
		t.Fatalf("resourceType = %q; want OperationOutcome (body=%s)", oo.ResourceType, body)
	}
	if len(oo.Issue) == 0 {
		t.Fatalf("expected at least one issue; body=%s", body)
	}
	issue := oo.Issue[0]
	if issue.Severity != "error" {
		t.Errorf("severity = %q; want error", issue.Severity)
	}
	if issue.Code != "security" {
		t.Errorf("code = %q; want security", issue.Code)
	}
	for _, c := range issue.Details.Coding {
		if c.System == "urn:ietf:rfc:6749" {
			return c.Code, issue.Diagnostics
		}
	}
	t.Fatalf("no urn:ietf:rfc:6749 coding found; body=%s", body)
	return "", ""
}

func TestTokenEndpoint_BadGrantType_ReturnsOperationOutcome(t *testing.T) {
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)
	form := url.Values{}
	form.Set("grant_type", "password")
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "fhir+json") {
		t.Errorf("Content-Type = %q; want fhir+json", ct)
	}
	code, _ := extractOAuthErrorFromOutcome(t, rec.Body.Bytes())
	if code != "unsupported_grant_type" {
		t.Errorf("oauth code = %q; want unsupported_grant_type", code)
	}
}

func TestTokenEndpoint_InvalidClient_ReturnsOperationOutcome(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// Sign with iss != client_id → "iss mismatch" → invalid_client
	assertion := k.sign(t, jwt.MapClaims{
		"iss": "wrong-issuer",
		"sub": clientID,
		"aud": "https://x/token",
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
		t.Fatalf("status = %d; want 401", rec.Code)
	}
	code, _ := extractOAuthErrorFromOutcome(t, rec.Body.Bytes())
	if code != "invalid_client" {
		t.Errorf("oauth code = %q; want invalid_client", code)
	}
}

func TestTokenEndpoint_InvalidGrantPathPreserved(t *testing.T) {
	// "invalid_grant" is RFC 6749 — it surfaces when the client
	// presents an assertion that fails validation. The endpoint maps
	// validation failures to invalid_client per the SMART backend
	// services profile, but the test exercises a missing-assertion
	// path that should map to invalid_client (RFC 7521 §5).
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	// no client_assertion
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", rec.Code)
	}
	code, _ := extractOAuthErrorFromOutcome(t, rec.Body.Bytes())
	if code != "invalid_client" {
		t.Errorf("oauth code = %q; want invalid_client", code)
	}
}
