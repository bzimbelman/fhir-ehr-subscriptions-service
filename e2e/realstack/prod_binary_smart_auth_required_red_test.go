// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// OpenProject story #238 — End-to-end SMART auth coverage:
//   - the prod binary's real verifier (auth.Middleware backed by
//     auth.NewVerifier) is wired and actively gating the FHIR API.
//   - a real RS256-signed JWT minted by the test-token-mint helper
//     against an issuer the binary's auth_clients table trusts is
//     accepted.
//   - the same request without a token, with garbage in the
//     Authorization header, with a token signed by an unknown key,
//     and with an expired token are all rejected with 401.
//
// The negative cases pin the surface that pages an operator on real
// auth wiring breakage. The positive case pins that the helper-minted
// token actually round-trips through auth.Middleware against the
// docker-compose-deployed binary.
package realstack_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestRealStack_ProdBinary_SmartAuth_RequiresJWT_PositiveAndNegative
// drives the binary's real verifier through a single Boot. The
// /Subscription endpoint is the gated surface; we GET it (rather
// than POSTing a full Subscription) so the test focuses on auth and
// is independent of FHIR validation.
func TestRealStack_ProdBinary_SmartAuth_RequiresJWT_PositiveAndNegative(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	gatedURL := stack.Binary.URL + "/Subscription"

	// --- Positive: helper-minted, default-claim token is accepted. ---
	tok, err := stack.MintTestToken(ctx, nil)
	if err != nil {
		t.Fatalf("MintTestToken: %v", err)
	}
	if got := authStatus(t, ctx, gatedURL, "Bearer "+tok); got == http.StatusUnauthorized {
		t.Fatalf("positive case: helper-minted token was rejected with 401; auth_clients wiring or verifier broken")
	}

	// --- Negative: no Authorization header at all. ---
	if got := authStatus(t, ctx, gatedURL, ""); got != http.StatusUnauthorized {
		t.Errorf("no-token case: status %d; want 401", got)
	}

	// --- Negative: garbage Bearer value (not a JWT). ---
	if got := authStatus(t, ctx, gatedURL, "Bearer not-a-jwt"); got != http.StatusUnauthorized {
		t.Errorf("garbage-token case: status %d; want 401", got)
	}

	// --- Negative: well-formed JWT signed by an unknown key. The
	// verifier's per-client JWKS lookup must reject the signature. ---
	bad := mintAdversarialToken(t, "realstack-test-mint", false)
	if got := authStatus(t, ctx, gatedURL, "Bearer "+bad); got != http.StatusUnauthorized {
		t.Errorf("unknown-key case: status %d; want 401", got)
	}

	// --- Negative: expired token via the test-token-mint helper. ---
	expired, err := stack.MintTestToken(ctx, map[string]any{
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("MintTestToken expired: %v", err)
	}
	if got := authStatus(t, ctx, gatedURL, "Bearer "+expired); got != http.StatusUnauthorized {
		t.Errorf("expired-token case: status %d; want 401", got)
	}

	// --- Negative: unknown client_id (claim resolves to no auth_clients
	// row, so the verifier cannot find a JWKS to validate against). ---
	unknownClient := mintAdversarialToken(t, "no-such-client-id", true)
	if got := authStatus(t, ctx, gatedURL, "Bearer "+unknownClient); got != http.StatusUnauthorized {
		t.Errorf("unknown-client case: status %d; want 401", got)
	}
}

// authStatus issues a GET against the gated URL with the given
// Authorization header value (or none when empty) and returns the
// observed status code. We never want to follow redirects or interpret
// the body — only the status matters for these auth assertions.
func authStatus(t *testing.T, ctx context.Context, url, authz string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// mintAdversarialToken signs a JWT with a freshly-generated RSA key
// the realstack-deployed binary has never heard of. When applyTestMintShape
// is true the token's claims mimic the helper's default shape
// (iss/aud/sub/client_id/scope/jti/iat/exp set), which lets us pin
// "client_id resolves but signature mismatches" vs. "client_id does
// not resolve at all".
func mintAdversarialToken(t *testing.T, clientID string, applyTestMintShape bool) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate adversarial key: %v", err)
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss": "https://adversary.invalid/",
		"aud": "fhir-subs-test",
		"sub": clientID,
		"jti": "adversary-" + randomToken(t),
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	}
	if applyTestMintShape {
		claims["client_id"] = clientID
		claims["scope"] = "system/Subscription.cruds"
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "adversary-key"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign adversarial token: %v", err)
	}
	return signed
}

// randomToken returns 16 hex-ish chars suitable for a jti value.
func randomToken(t *testing.T) string {
	t.Helper()
	var n uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &n); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(uint64Bytes(n))
}

func uint64Bytes(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

// TestRealStack_ProdBinary_SmartAuth_AcceptsKeycloakClientCredentials
// pins that the SECOND trusted issuer wired by the harness — the real
// Keycloak realm — also produces accepted tokens. The harness wires
// both Keycloak and test-token-mint as trusted issuers; this test
// proves both work, so a regression that breaks the
// trusted_issuers[].jwks_url plumbing fails the suite.
func TestRealStack_ProdBinary_SmartAuth_AcceptsKeycloakClientCredentials(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	tok, err := stack.MintClientToken(ctx)
	if err != nil {
		t.Fatalf("MintClientToken (keycloak): %v", err)
	}
	gatedURL := stack.Binary.URL + "/Subscription"
	if got := authStatus(t, ctx, gatedURL, "Bearer "+tok); got == http.StatusUnauthorized {
		t.Fatalf("keycloak client_credentials token was rejected with 401; trusted_issuers wiring may be broken")
	}
}

// TestRealStack_ProdBinary_SmartAuth_PostSubscriptionRequiresAuth pins
// the write surface specifically: an unauthenticated POST /Subscription
// must NOT create a row. The negative is the operator-paging surface;
// without this assertion a regression that drops the auth middleware
// from the write route would only show up at runtime.
func TestRealStack_ProdBinary_SmartAuth_PostSubscriptionRequiresAuth(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	body := `{"resourceType":"Subscription","status":"requested","topic":"http://example.org/topics/h238","content":"full-resource","channel":{"type":"rest-hook","endpoint":"http://example.invalid/never-reached"}}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		stack.Binary.URL+"/Subscription", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/fhir+json")
	// Deliberately no Authorization header.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Subscription: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /Subscription without auth: status %d; want 401. Body: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}

	// Sanity: no leaked OperationOutcome with internal-server-error
	// shape — 401 must be the verifier's surface, not an unrecovered
	// panic.
	if strings.Contains(string(rb), "panic") {
		t.Errorf("401 body contains 'panic'; verifier may have crashed: %s", string(rb))
	}
}
