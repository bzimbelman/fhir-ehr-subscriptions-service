// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// Phase A (RED) tests for OpenProject story #295 — H10c: a real-key JWT
// minting helper for realstack tests so adversarial auth scenarios
// (revoked, expired, audience-mismatched, missing-jti, etc.) can run
// without going through Keycloak.
//
// The helper is a real in-repo binary (cmd/test-token-mint/) that runs
// inside the realstack docker-compose stack. It generates an RSA
// keypair at startup, exposes a control-plane HTTP API for minting JWTs
// with arbitrary claim overrides, and publishes a JWKS endpoint. The
// realstack harness wires it up so the prod fhir-subs binary trusts
// tokens it mints (via the per-client auth_clients.jwks_url path the
// verifier already supports).
//
// These tests pin the contract and fail today because:
//
//   - cmd/test-token-mint/ does not yet exist
//   - e2e/realstack/services.go has no TokenMintHandle
//   - e2e/realstack/boot.go does not bring up the test-token-mint
//     service or expose Stack.TokenMint / Stack.MintTestToken
//   - the rendered prod-binary config does not register an
//     auth_clients row pointing at the test mint's JWKS URL
//
// Phase B implements the binary and the harness wiring; Phase C audits
// no fakes/mocks and merges.
//
// Acceptance criteria mapped to test names:
//
//   - in-repo binary exposes mint + JWKS + healthz endpoints  -> TestTokenMint_BinaryEndpointsServed
//   - realstack.Stack surfaces TokenMint{Addr,TokenAPIURL,JWKSURL,Issuer}  -> TestTokenMint_StackHandlePopulated
//   - rendered prod-binary config registers test-mint as a trusted client -> TestTokenMint_ProdBinaryTrustsTestMint
//   - safety: helper is gated by allow_dev_bypass / environment=test     -> TestTokenMint_OnlyEnabledInDevBypassEnvironment
//   - MintTestToken applies arbitrary claim overrides honestly          -> TestTokenMint_MintAppliesClaimOverrides
//   - default mint produces a token the prod binary accepts             -> TestTokenMint_ProdBinaryAcceptsDefaultMint
//
// Findings closed: H10c (sibling of OP #289 H10 rollup).

package realstack_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestTokenMint_BinaryEndpointsServed asserts the test-token-mint
// binary, running inside docker-compose, serves /healthz, /jwks.json,
// and /mint endpoints over real HTTP. No in-process JWT signer.
func TestTokenMint_BinaryEndpointsServed(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	if stack.TokenMint.Addr == "" {
		t.Fatalf("Stack.TokenMint.Addr is empty; harness MUST expose the test-token-mint binary")
	}
	if stack.TokenMint.TokenAPIURL == "" {
		t.Fatalf("Stack.TokenMint.TokenAPIURL is empty")
	}
	if stack.TokenMint.JWKSURL == "" {
		t.Fatalf("Stack.TokenMint.JWKSURL is empty")
	}
	if stack.TokenMint.Issuer == "" {
		t.Fatalf("Stack.TokenMint.Issuer is empty")
	}

	if got := httpStatus(t, stack.TokenMint.TokenAPIURL+"/healthz"); got != http.StatusOK {
		t.Fatalf("test-token-mint /healthz returned %d; want 200", got)
	}

	resp, err := http.Get(stack.TokenMint.JWKSURL)
	if err != nil {
		t.Fatalf("GET %s: %v", stack.TokenMint.JWKSURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/jwks.json returned %d; want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /jwks.json: %v", err)
	}
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		t.Fatalf("decode /jwks.json: %v\n%s", err, body)
	}
	if len(jwks.Keys) == 0 {
		t.Fatalf("/jwks.json has no keys: %s", body)
	}
	// Real RSA key — the kty MUST be RSA, kid MUST be non-empty.
	k0 := jwks.Keys[0]
	if k0["kty"] != "RSA" {
		t.Fatalf("/jwks.json key[0].kty=%v; want RSA (helper must use real RSA, not HS256)", k0["kty"])
	}
	if kid, _ := k0["kid"].(string); kid == "" {
		t.Fatalf("/jwks.json key[0].kid is empty")
	}
	if n, _ := k0["n"].(string); n == "" {
		t.Fatalf("/jwks.json key[0].n is empty (RSA modulus missing)")
	}
}

// TestTokenMint_StackHandlePopulated asserts the Stack.TokenMint handle
// is fully populated by Boot and that the URLs it advertises are real,
// reachable endpoints (not in-memory placeholders).
func TestTokenMint_StackHandlePopulated(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	if !strings.Contains(stack.TokenMint.Addr, ":") {
		t.Fatalf("TokenMint.Addr=%q is not host:port", stack.TokenMint.Addr)
	}
	if !strings.HasPrefix(stack.TokenMint.TokenAPIURL, "http://") {
		t.Fatalf("TokenMint.TokenAPIURL=%q must start with http://", stack.TokenMint.TokenAPIURL)
	}
	if !strings.HasPrefix(stack.TokenMint.JWKSURL, "http://") {
		t.Fatalf("TokenMint.JWKSURL=%q must start with http://", stack.TokenMint.JWKSURL)
	}
	if stack.TokenMint.Issuer == stack.Keycloak.IssuerURL {
		t.Fatalf("TokenMint.Issuer must be distinct from Keycloak.IssuerURL; got %q", stack.TokenMint.Issuer)
	}
}

// TestTokenMint_ProdBinaryTrustsTestMint asserts the prod binary's
// rendered config registers an auth_clients row whose jwks_url points
// at the test-token-mint binary. The verifier resolves JWKS per-client
// (verifier.go line 209), so a token bearing the test client's
// client_id and signed by test-token-mint's RSA key will be trusted.
func TestTokenMint_ProdBinaryTrustsTestMint(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	cfg := readBinaryConfig(t, stack.ConfigPath())
	// The harness must have wired the test-mint JWKS URL into a
	// dev_bypass_client_ids entry OR a trusted_issuers entry that the
	// verifier can resolve per-client. The simplest acceptance is that
	// the rendered config contains the test-token-mint JWKS URL
	// somewhere — the harness then upserts an auth_clients row with
	// that URL at boot.
	if !strings.Contains(cfg, stack.TokenMint.JWKSURL) {
		t.Errorf("rendered binary config does not reference test-token-mint JWKS URL %q", stack.TokenMint.JWKSURL)
	}
	if stack.TokenMint.ClientID == "" {
		t.Fatalf("Stack.TokenMint.ClientID is empty; harness MUST surface the auth_clients id it provisioned")
	}
}

// TestTokenMint_OnlyEnabledInDevBypassEnvironment is the safety guard:
// the rendered prod-binary config must still set
// deployment.environment=test AND auth.allow_dev_bypass=true whenever
// test-token-mint is wired in. This pins that the helper cannot be
// reached on a config that has not opted into dev-bypass.
func TestTokenMint_OnlyEnabledInDevBypassEnvironment(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	cfg := readBinaryConfig(t, stack.ConfigPath())
	if !strings.Contains(cfg, "environment: test") {
		t.Errorf("rendered config must set deployment.environment=test when test-token-mint is wired; got:\n%s", cfg)
	}
	if !strings.Contains(cfg, "allow_dev_bypass: true") {
		t.Errorf("rendered config must set auth.allow_dev_bypass=true when test-token-mint is wired; got:\n%s", cfg)
	}
}

// TestTokenMint_MintAppliesClaimOverrides asserts the helper's mint API
// honors arbitrary claim overrides — the whole point of the helper.
// Adversarial-auth tests need to flip iss, aud, exp, jti, scope, etc.
// without round-tripping through Keycloak.
func TestTokenMint_MintAppliesClaimOverrides(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	overrides := map[string]any{
		"iss":       "https://adversary.invalid/",
		"aud":       "wrong-audience",
		"client_id": "adversary-client",
		"jti":       "fixed-jti-deadbeef",
		"exp":       time.Now().Add(-1 * time.Hour).Unix(), // already expired
		"scope":     "system/Subscription.r",
	}
	tok, err := stack.MintTestToken(ctx, overrides)
	if err != nil {
		t.Fatalf("MintTestToken: %v", err)
	}
	if tok == "" {
		t.Fatalf("MintTestToken returned empty token")
	}
	// Decode payload (segment 1) without verifying — we are pinning the
	// helper applied the overrides, not that the token is valid.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("MintTestToken returned non-JWT %q", tok)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("base64 decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode claims: %v\n%s", err, payload)
	}
	for k, want := range overrides {
		got := claims[k]
		// JSON numbers come back as float64.
		if f, ok := got.(float64); ok {
			if w, ok := want.(int64); ok {
				if int64(f) != w {
					t.Errorf("claim %s: got %v, want %v", k, f, w)
				}
				continue
			}
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			t.Errorf("claim %s: got %v, want %v", k, got, want)
		}
	}
}

// TestTokenMint_ProdBinaryAcceptsDefaultMint asserts the helper's
// default mint (no overrides) produces a token that the prod binary's
// /Subscription endpoint accepts: the harness wires the test-mint
// JWKS into auth_clients, the default claims set iss/aud/scope/jti to
// values the verifier accepts, and the binary returns a non-401.
func TestTokenMint_ProdBinaryAcceptsDefaultMint(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	tok, err := stack.MintTestToken(ctx, nil)
	if err != nil {
		t.Fatalf("MintTestToken (default): %v", err)
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, stack.Binary.URL+"/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /Subscription: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("prod binary rejected default-minted token with 401; helper not wired correctly: %s", body)
	}
}
