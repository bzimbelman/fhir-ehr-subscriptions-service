// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestE2E_ProdBinary_S104_PerClientRateLimitFires_RealAuth proves the
// wire-up for OP #104 against the production binary using a REAL
// authenticated principal — no fakes, no mocks. The binary boots with
// audience + token_url + issued_secret configured; an httptest JWKS
// server publishes a real RSA public key; an auth_clients row is
// seeded directly into the binary's Postgres pool with the JWKS URL.
// The test mints a real client_assertion JWT, exchanges it at the
// binary's /token endpoint for a real bearer JWT, and POSTs
// /Subscription/ with Authorization: Bearer <jwt> until the limiter
// returns 429.
//
// Wire-up regression check: temporarily disabling
// buildClientRateLimitersFromAuth in cmd/fhir-subs/wiring.go must
// flip the (burst+1)th attempt from 429 to a non-429 (handler-level
// response). That is the proof the e2e is exercising the production
// wiring, not test plumbing.
//
// All test infrastructure used here is REAL:
//   - real Postgres (resetPipelineTables + h.DB INSERT)
//   - real RSA private key generated in-process
//   - real httptest.NewServer publishing a JWKS document
//   - real JWT signing (golang-jwt/jwt/v5)
//   - real /token POST against the running binary
//   - real bearer JWT verified by the binary's auth.Verifier on
//     POST /Subscription/
//   - real chi middleware short-circuit with 429 + Retry-After
//
// The only "fakes" anywhere in the auth package's unit tests
// (fakeClientLookup) are bypassed entirely: the production binary
// uses repos.AuthClientsRepo against the real pool.
func TestE2E_ProdBinary_S104_PerClientRateLimitFires_RealAuth(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// Real RSA key + httptest JWKS server — the same primitives the
	// auth package's verifier_test.go uses, just lifted into the e2e
	// orchestrator. Not mocked: real cryptography, real HTTP server.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	kid := uuid.NewString()
	jwks := jwksDocFromPublic(&priv.PublicKey, kid)
	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	jwksSrv := httptest.NewServer(jwksMux)
	t.Cleanup(jwksSrv.Close)

	// Seed auth_clients with a row pointing the binary's verifier at
	// the httptest JWKS server. Real Postgres write.
	clientID := "e2e-rl-real-" + uuid.New().String()[:8]
	if _, err := h.DB.Exec(ctx, `INSERT INTO auth_clients (id, jwks_url, scopes, display_name)
		VALUES ($1, $2, ARRAY['system/Subscription.cruds']::text[], $1)`,
		clientID, jwksSrv.URL+"/jwks"); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	// 32-byte HS256 secret for the binary's access-token signing.
	// Base64-encoded for the YAML config, raw bytes never logged.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	issuedSecret := base64.StdEncoding.EncodeToString(secretBytes)

	audience := "https://e2e-rl/token"
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:                      h.DBURL,
		FacilityID:                       "e2e-rl-real",
		AdapterID:                        "default",
		Insecure:                         true,
		GracePeriod:                      5 * time.Second,
		AuthAudience:                     audience,
		AuthAllowInsecureJWKS:            true, // httptest is http://
		AuthTokenURL:                     audience,
		AuthIssuedSecret:                 issuedSecret,
		AuthIssuedIssuer:                 "e2e-rl-issuer",
		AuthAccessTokenTTL:               5 * time.Minute,
		SubscriptionCreateRateLimitBurst: 2,
	})
	defer bin.Stop(t, 5*time.Second)

	// mintBearer exchanges a freshly-signed client_assertion (unique
	// JTI) at the binary's /token endpoint for a real bearer JWT. The
	// binary's verifier enforces JTI single-use on both the assertion
	// AND the access token, so every API call needs a fresh bearer —
	// this matches the binary's actual replay-protection semantics.
	// The rate limiter is keyed on the authenticated principal's
	// ClientID, which is stable across bearer rotation, so the
	// per-bearer-per-call dance does not relax the burst.
	mintBearer := func() string {
		now := time.Now().UTC()
		assertion := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": clientID,
			"sub": clientID,
			"aud": audience,
			"jti": uuid.NewString(),
			"iat": now.Add(-30 * time.Second).Unix(),
			"exp": now.Add(2 * time.Minute).Unix(),
		})
		assertion.Header["kid"] = kid
		signedAssertion, err := assertion.SignedString(priv)
		if err != nil {
			t.Fatalf("sign assertion: %v", err)
		}
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", signedAssertion)
		form.Set("scope", "system/Subscription.cruds")

		tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
			bin.HTTPURL()+"/token", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatalf("build /token req: %v", err)
		}
		tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		tokResp, err := http.DefaultClient.Do(tokReq)
		if err != nil {
			t.Fatalf("POST /token: %v", err)
		}
		tokBody, _ := io.ReadAll(tokResp.Body)
		_ = tokResp.Body.Close()
		if tokResp.StatusCode != http.StatusOK {
			t.Fatalf("/token status=%d body=%s", tokResp.StatusCode, tokBody)
		}
		var tokWire struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(tokBody, &tokWire); err != nil {
			t.Fatalf("decode token response: %v body=%s", err, tokBody)
		}
		if tokWire.AccessToken == "" {
			t.Fatalf("empty access_token in response: %s", tokBody)
		}
		return tokWire.AccessToken
	}

	// POST /Subscription/ with the real bearer. The body deliberately
	// has both `channelType` (FHIR R5 shape) and `channel` (R4 shape)
	// so handler-level validation passes regardless of which the
	// active validator prefers; if the limiter middleware
	// short-circuits, validator outcome is irrelevant — that's the
	// point.
	subBody := []byte(`{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topic/observation",
		"channelType": {"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type", "code": "rest-hook"},
		"endpoint": "https://subscriber.example.com/hook",
		"contentType": "application/fhir+json",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://subscriber.example.com/hook"}
	}`)

	send := func() (int, string, []byte) {
		bearer := mintBearer() // fresh JTI per call — JTI replay cache rejects reuse.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			bin.HTTPURL()+"/Subscription/", bytes.NewReader(subBody))
		if err != nil {
			t.Fatalf("build POST /Subscription: %v", err)
		}
		req.Header.Set("Content-Type", "application/fhir+json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /Subscription: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Retry-After"), body
	}

	// First two requests — under the burst — must NOT be rate-limited.
	// Any non-429 satisfies the assertion; the limiter is the chi
	// middleware that short-circuits ahead of every other handler
	// (auth ran in /token; the bearer is good).
	for i := 0; i < 2; i++ {
		code, _, body := send()
		if code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: 429 before burst exhausted (production wiring may be off-by-one); body=%s",
				i+1, body)
		}
		// Sanity: a real authenticated principal should NOT see 401
		// here. If 401 leaks through, the bearer/audience plumbing is
		// wrong and the rate-limit assertion below would be a
		// false-positive.
		if code == http.StatusUnauthorized {
			t.Fatalf("attempt %d: 401 — auth path broken (bearer not accepted by verifier); body=%s",
				i+1, body)
		}
	}

	// Third attempt — over burst — must be 429 with Retry-After. If
	// the wiring is broken (Deps.SubscriptionCreateRateLimit nil), the
	// nil-safe Middleware passes through and we get the same handler
	// response as the first two.
	code, retry, body := send()
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd attempt: status=%d, want 429 — production binary wiring is NOT plumbing "+
			"auth.subscription_create_rate_limit into Deps (story #104 AC #1/AC #5). body=%s",
			code, body)
	}
	if retry == "" {
		t.Fatalf("3rd attempt: missing Retry-After header on 429")
	}
	n, err := strconv.Atoi(retry)
	if err != nil || n < 1 {
		t.Fatalf("Retry-After=%q (parse=%v) want positive integer seconds", retry, err)
	}
	if !bytes.Contains(body, []byte("OperationOutcome")) {
		t.Errorf("429 body not FHIR OperationOutcome: %s", body)
	}
}

// jwksDocFromPublic renders the supplied RSA public key as a JWKS
// document. Mirrors the helper used by internal/api/auth/verifier_test.go
// — kept inline here to avoid an import cycle and to make the e2e
// test self-contained.
func jwksDocFromPublic(pub *rsa.PublicKey, kid string) map[string]any {
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}) // 65537
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
}
