// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// TestE2E_ProdBinary_S104_PerClientRateLimitFires proves the wire-up
// for OP #104: when the operator sets
// `auth.subscription_create_rate_limit.burst` in the YAML config, the
// production fhir-subs binary actually rate-limits POST /Subscription
// — i.e. the chi middleware was registered with a non-nil
// *auth.ClientRateLimiter.
//
// Before the wiring landed, this test would see HTTP 401 on every
// attempt (auth denial; rate limiter pass-through because Deps field
// was nil). After wiring, the limiter middleware short-circuits with
// 429 once the burst is exhausted — even before auth runs, because
// chi.With chains the limiter ahead of the auth-aware handler.
//
// The harness-only test in api_per_client_rate_limit_test.go injects
// the limiter directly into Deps; this one proves that booting the
// real binary with a YAML rate-limit block populates Deps the same way.
func TestE2E_ProdBinary_S104_PerClientRateLimitFires(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)
	if _, err := h.DB.Exec(ctx, `INSERT INTO auth_clients (id, scopes, display_name)
		VALUES ($1, ARRAY['system/Subscription.cruds']::text[], $1)`,
		"e2e-rl-client"); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:                      h.DBURL,
		FacilityID:                       "e2e-rl",
		AdapterID:                        "default",
		Insecure:                         true,
		GracePeriod:                      5 * time.Second,
		AuthAudience:                     "",
		SubscriptionCreateRateLimitBurst: 2,
		// RefillPerSecond=0 pins the bucket: after 2 accepted requests,
		// every subsequent POST returns 429 until Retry-After elapses.
	})
	defer bin.Stop(t, 5*time.Second)

	subBody := []byte(`{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topic/observation",
		"channelType": {"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type", "code": "rest-hook"},
		"endpoint": "https://subscriber.example.com/hook",
		"contentType": "application/fhir+json",
		"content": "id-only"
	}`)

	send := func() (int, string, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			bin.HTTPURL()+"/Subscription/", bytes.NewReader(subBody))
		if err != nil {
			t.Fatalf("build POST: %v", err)
		}
		req.Header.Set("Content-Type", "application/fhir+json")
		req.Header.Set("X-Client-Id", "e2e-rl-client")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /Subscription: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Retry-After"), body
	}

	// First two requests must NOT be rate-limited (still under the
	// burst). Any non-429 status proves the limiter passed them
	// through; the actual request body may still 401/422 depending
	// on auth/topic state — the limiter is a pre-handler chi
	// middleware, so 429 short-circuits everything else.
	for i := 0; i < 2; i++ {
		code, _, body := send()
		if code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: 429 before burst exhausted (production wiring may be off-by-one); body=%s",
				i+1, body)
		}
	}

	// Third request must be 429 with Retry-After. If wiring is broken
	// (Deps.SubscriptionCreateRateLimit nil — pre-#104 state), the
	// limiter's nil-safe Middleware returns pass-through and we get
	// the same handler-level response as the first two attempts.
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
	// OperationOutcome shape for the 429 body keeps the FHIR API
	// surface uniform.
	if !bytes.Contains(body, []byte("OperationOutcome")) {
		t.Errorf("429 body not FHIR OperationOutcome: %s", body)
	}
}
