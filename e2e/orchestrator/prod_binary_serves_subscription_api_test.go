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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// observationTopicForServesAPI is a SubscriptionTopic the binary loads
// at startup so the POST /Subscription with topic
// http://example.org/topic/observation passes the "topic in catalog"
// gate. Matches anything (no queryCriteria) — the test pins the
// route + auth wiring, not topic semantics.
const observationTopicForServesAPI = `{
  "resourceType": "SubscriptionTopic",
  "url": "http://example.org/topic/observation",
  "version": "1.0.0",
  "title": "Observation (OP #341 e2e — serves API)",
  "status": "active",
  "resourceTrigger": [{
    "resource": "Observation",
    "supportedInteraction": ["create", "update"]
  }],
  "notificationShape": [{
    "resource": "Observation"
  }]
}`

// TestE2E_ProdBinary_ServesSubscriptionAPI proves the production binary
// — built from cmd/fhir-subs and started with `run --config <file>` —
// actually mounts handlers.RegisterRoutes against a real Postgres pool
// AND exercises the real auth.Verifier middleware (OP #146 banned the
// AuthAudience: "" auth-bypass trick that previously hid wire-up gaps).
//
// AC #3 from OpenProject #146: "The 401 path test in
// prod_binary_serves_subscription_api_test.go MUST be replaced with a
// 401-without-token AND 200-with-token assertion that exercises the
// real verifier."
//
// All test infrastructure is REAL — no fakes, no mocks:
//   - real Postgres (h.DB)
//   - real RSA private key generated in-process
//   - real httptest.NewServer publishing a JWKS document
//   - real JWT signing (golang-jwt/jwt/v5)
//   - real /token POST against the running binary
//   - real bearer JWT verified by the binary's auth.Verifier
//   - real chi router mounting handlers.RegisterRoutes
func TestE2E_ProdBinary_ServesSubscriptionAPI(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// Real RSA key + httptest JWKS server — the same primitives the
	// auth package's verifier_test.go uses, lifted into the e2e
	// orchestrator. Real cryptography, real HTTP server.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	kid := uuid.NewString()
	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksDocFromPublic(&priv.PublicKey, kid))
	})
	jwksSrv := httptest.NewServer(jwksMux)
	t.Cleanup(jwksSrv.Close)

	// Seed auth_clients with a row pointing the binary's verifier at the
	// httptest JWKS server. Real Postgres write.
	//
	// OP #341 / #290: subscription handlers gate on the literal
	// per-letter scopes (system/Subscription.c / .r / .u / .d). The
	// /token endpoint intersects requested-scopes with
	// auth_clients.scopes, so seeding only "cruds" leaves the bearer
	// without `.c` and POST /Subscription returns 403 "insufficient
	// scope". Seed the composite alias AND every per-letter scope so the
	// bearer minted via /token carries the granted set the handlers
	// actually check.
	clientID := "e2e-prod-serves-" + uuid.New().String()[:8]
	if _, err := h.DB.Exec(ctx, `INSERT INTO auth_clients (id, jwks_url, scopes, display_name)
		VALUES ($1, $2, ARRAY['system/Subscription.cruds','system/Subscription.c','system/Subscription.r','system/Subscription.u','system/Subscription.d']::text[], $1)`,
		clientID, jwksSrv.URL+"/jwks"); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	// Real HS256 secret for the binary's access-token signing path.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	issuedSecret := base64.StdEncoding.EncodeToString(secretBytes)
	audience := "https://e2e-prod-serves/token"

	// Stage a topic catalog directory so /Subscription POST does not
	// reject with "topic not in catalog" (OP #154 persists loaded
	// topics into subscription_topics; the createSubscription handler
	// reads through PgTopicsStore.ListActive). Without this, the
	// 201-with-token assertion can never succeed.
	topicsDir := t.TempDir()
	if werr := os.WriteFile(filepath.Join(topicsDir, "observation.json"),
		[]byte(observationTopicForServesAPI), 0o600); werr != nil {
		t.Fatalf("write topic: %v", werr)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-serves",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          audience, // OP #146: real verifier wired (was "")
		AuthAllowInsecureJWKS: true,     // httptest is http://
		AuthTokenURL:          audience,
		AuthIssuedSecret:      issuedSecret,
		AuthIssuedIssuer:      "e2e-prod-serves-issuer",
		AuthAccessTokenTTL:    5 * time.Minute,
		TopicsCatalogDir:      topicsDir,
		URLValidatorAllowHTTP: true, // /Subscription endpoint is http://127.0.0.1
	})
	defer bin.Stop(t, 5*time.Second)

	// Sanity: probe must say ready.
	{
		resp, err := http.Get(bin.ProbeURL() + "/readyz")
		if err != nil {
			t.Fatalf("readyz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("readyz: status %d (want 200)", resp.StatusCode)
		}
	}

	// R4B-backport schema requires `channel` (the project's Subscription
	// schema validates the R4B shape). Include the R5 `channelType` /
	// `endpoint` fields too — both are accepted by the parser and are
	// what production handlers persist on the row.
	subBody := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topic/observation",
		"channelType": {"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type", "code": "rest-hook"},
		"endpoint": "http://127.0.0.1:9/hook",
		"contentType": "application/fhir+json",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "http://127.0.0.1:9/hook"}
	}`

	// AC #3a: POST /Subscription/ with NO Authorization header MUST
	// return 401. This proves the real verifier.Middleware is on the
	// route — pre-#146 the harness installed a fixed-principal
	// middleware that returned 401 from the handler (because the
	// X-Client-Id header it sniffed was missing), masking the wire-up
	// gap. Post-#146 the 401 comes from auth.Verifier — proven by the
	// 200 path immediately below.
	noTokReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/Subscription/", bytes.NewReader([]byte(subBody)))
	if err != nil {
		t.Fatalf("build no-token POST: %v", err)
	}
	noTokReq.Header.Set("Content-Type", "application/fhir+json")
	noTokResp, err := http.DefaultClient.Do(noTokReq)
	if err != nil {
		t.Fatalf("POST /Subscription (no token): %v", err)
	}
	noTokBody, _ := io.ReadAll(noTokResp.Body)
	_ = noTokResp.Body.Close()
	if noTokResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /Subscription with no Authorization: status=%d (want 401), body=%s",
			noTokResp.StatusCode, string(noTokBody))
	}
	// Verifier emits an OperationOutcome with diagnostic "missing bearer
	// token". Pin the wire-shape so a future regression that returns a
	// generic 401 from the chi router is caught.
	if !bytes.Contains(noTokBody, []byte("OperationOutcome")) {
		t.Errorf("401 body did not include OperationOutcome — verifier may not be on the chain. body=%s",
			string(noTokBody))
	}

	// AC #3b: POST /Subscription/ with a real bearer token MUST succeed
	// (201 Created). This proves the real verifier.Middleware accepts a
	// validly-signed token and the handler is mounted behind it.
	bearer := mintRealBearer(t, ctx, bin.HTTPURL(), audience, clientID, kid, priv)

	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/Subscription/", bytes.NewReader([]byte(subBody)))
	if err != nil {
		t.Fatalf("build token POST: %v", err)
	}
	tokReq.Header.Set("Content-Type", "application/fhir+json")
	tokReq.Header.Set("Authorization", "Bearer "+bearer)
	tokResp, err := http.DefaultClient.Do(tokReq)
	if err != nil {
		t.Fatalf("POST /Subscription (with token): %v", err)
	}
	tokBody, _ := io.ReadAll(tokResp.Body)
	_ = tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /Subscription with valid bearer: status=%d (want 201), body=%s",
			tokResp.StatusCode, string(tokBody))
	}
	if loc := tokResp.Header.Get("Location"); !strings.HasPrefix(loc, "/Subscription/") {
		t.Errorf("missing Location header on 201, got %q", loc)
	}
	// Verify the response is shaped like FHIR.
	var got map[string]any
	if err := json.Unmarshal(tokBody, &got); err != nil {
		t.Fatalf("response not JSON: %v body=%s", err, tokBody)
	}
	if rt, _ := got["resourceType"].(string); rt != "Subscription" {
		t.Errorf("response.resourceType = %q, want Subscription", rt)
	}

	// Sanity: GET /metadata is reachable.
	mResp, err := http.Get(bin.HTTPURL() + "/metadata")
	if err != nil {
		t.Fatalf("GET /metadata: %v", err)
	}
	defer func() { _ = mResp.Body.Close() }()
	mBody, _ := io.ReadAll(mResp.Body)
	if mResp.StatusCode == http.StatusNotFound {
		t.Fatalf("/metadata returned 404 — route is NOT mounted")
	}
	if !bytes.Contains(mBody, []byte("CapabilityStatement")) &&
		!bytes.Contains(mBody, []byte("OperationOutcome")) {
		t.Errorf("metadata body unexpected (status=%d): %s", mResp.StatusCode, string(mBody))
	}
}

// mintRealBearer goes through the binary's real /token endpoint:
// signs a client_assertion JWT with priv, exchanges it via OAuth2
// JWT-Bearer client credentials, returns the access_token. No fakes
// anywhere: every call hits the running binary's chi router.
func mintRealBearer(t *testing.T, ctx context.Context, binURL, audience, clientID, kid string, priv *rsa.PrivateKey) string {
	t.Helper()
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
	signed, err := assertion.SignedString(priv)
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", signed)
	// Story #290: handlers check literal CRUD letters
	// (system/Subscription.c / .r / .u / .d), so request both the
	// composite "cruds" alias AND the per-letter scopes. The token
	// endpoint intersects with auth_clients.scopes; tests that seed
	// only "cruds" still get just "cruds" back.
	form.Set("scope", "system/Subscription.cruds system/Subscription.c system/Subscription.r system/Subscription.u system/Subscription.d")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, binURL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build /token req: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /token: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/token status=%d body=%s", resp.StatusCode, body)
	}
	var wire struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode token response: %v body=%s", err, body)
	}
	if wire.AccessToken == "" {
		t.Fatalf("empty access_token in /token response: %s", body)
	}
	return wire.AccessToken
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
