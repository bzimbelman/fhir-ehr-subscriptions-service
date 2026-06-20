// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// OP #226: when VerifierConfig.TrustedIssuers is configured, the
// verifier MUST refuse tokens whose iss is NOT in the list — even if
// the token is otherwise signature-valid against a registered client's
// JWKS. The pre-#226 implementation treated TrustedIssuers as advisory
// (config docstring: "today the fields are advisory"), which means an
// operator who configured the list believed they had pinned trust
// when in fact any signed-by-the-client token was accepted.
func TestVerifier_RejectsTokenWithUntrustedIssuer(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)

	clientID := "lab-client"
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: "https://fhir-subs.example.org",
		ClientLookup: fakeClientLookup{
			clientID: {
				ID:      clientID,
				JwksURL: srv.URL + "/jwks",
				Scopes:  []string{"system/Subscription.cruds"},
			},
		},
		ClockSkew:         60 * time.Second,
		Now:               now,
		AllowInsecureJWKS: true,
		TrustedIssuers: []auth.TrustedIssuer{
			{Issuer: "https://idp.trusted.example.org", JWKSURL: srv.URL + "/jwks"},
		},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	exp := time.Date(2026, 6, 18, 12, 5, 0, 0, time.UTC)
	claims := jwt.MapClaims{
		// iss is NOT in the TrustedIssuers list — must be refused.
		"iss":       "https://idp.attacker.example.org",
		"sub":       clientID,
		"aud":       "https://fhir-subs.example.org",
		"client_id": clientID,
		"jti":       uuid.NewString(),
		"iat":       exp.Add(-5 * time.Minute).Unix(),
		"exp":       exp.Unix(),
		"scope":     "system/Subscription.cruds",
	}
	tok := k.sign(t, claims)

	req := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	p, status, reason := v.Authenticate(req)
	if status == 0 {
		t.Fatalf("expected non-zero status (untrusted iss); got 0, principal=%+v", p)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; reason=%q", status, reason)
	}
	if !strings.Contains(strings.ToLower(reason), "iss") &&
		!strings.Contains(strings.ToLower(reason), "trust") {
		t.Errorf("reason %q should mention untrusted/issuer", reason)
	}
}

// OP #226: when iss IS in TrustedIssuers, the token must be accepted
// (regression check — confirms the filter is not too strict).
func TestVerifier_AcceptsTokenWithTrustedIssuer(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)

	clientID := "lab-client"
	trusted := "https://idp.trusted.example.org"
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: "https://fhir-subs.example.org",
		ClientLookup: fakeClientLookup{
			clientID: {
				ID:      clientID,
				JwksURL: srv.URL + "/jwks",
				Scopes:  []string{"system/Subscription.cruds"},
			},
		},
		ClockSkew:         60 * time.Second,
		Now:               now,
		AllowInsecureJWKS: true,
		TrustedIssuers: []auth.TrustedIssuer{
			{Issuer: trusted, JWKSURL: srv.URL + "/jwks"},
		},
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	exp := time.Date(2026, 6, 18, 12, 5, 0, 0, time.UTC)
	claims := jwt.MapClaims{
		"iss":       trusted, // matches TrustedIssuers entry
		"sub":       clientID,
		"aud":       "https://fhir-subs.example.org",
		"client_id": clientID,
		"jti":       uuid.NewString(),
		"iat":       exp.Add(-5 * time.Minute).Unix(),
		"exp":       exp.Unix(),
		"scope":     "system/Subscription.cruds",
	}
	tok := k.sign(t, claims)

	req := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	p, status, reason := v.Authenticate(req)
	if status != 0 {
		t.Fatalf("expected status 0 (trusted iss); got %d reason=%q", status, reason)
	}
	if p == nil {
		t.Fatal("expected principal")
	}
}

// OP #226: when TrustedIssuers is empty, the verifier behaves as it
// always has — no issuer filter applied. Operators can still rely on
// per-client JWKS pinning. This is the legacy / unmanaged-trust case.
func TestVerifier_EmptyTrustedIssuers_NoFilter(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)

	clientID := "lab-client"
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: "https://fhir-subs.example.org",
		ClientLookup: fakeClientLookup{
			clientID: {
				ID:      clientID,
				JwksURL: srv.URL + "/jwks",
				Scopes:  []string{"system/Subscription.cruds"},
			},
		},
		ClockSkew:         60 * time.Second,
		Now:               now,
		AllowInsecureJWKS: true,
		// TrustedIssuers omitted → no filter
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	exp := time.Date(2026, 6, 18, 12, 5, 0, 0, time.UTC)
	claims := jwt.MapClaims{
		"iss":       "https://any-iss.example.org",
		"sub":       clientID,
		"aud":       "https://fhir-subs.example.org",
		"client_id": clientID,
		"jti":       uuid.NewString(),
		"iat":       exp.Add(-5 * time.Minute).Unix(),
		"exp":       exp.Unix(),
		"scope":     "system/Subscription.cruds",
	}
	tok := k.sign(t, claims)
	req := httptest.NewRequest(http.MethodGet, "/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	p, status, reason := v.Authenticate(req)
	if status != 0 {
		t.Fatalf("expected status 0 (no TrustedIssuers configured); got %d reason=%q", status, reason)
	}
	if p == nil {
		t.Fatal("expected principal")
	}
}
