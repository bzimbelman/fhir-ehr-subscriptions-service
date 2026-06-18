// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestE2E_TokenEndpoint_RateLimitsPerSourceIP exercises the S-3 fix:
// per-source-IP rate limiting on /token. The test floods the unauth
// endpoint with bogus assertions from the same source and asserts the
// fourth attempt is 429 with a Retry-After header.
func TestE2E_TokenEndpoint_RateLimitsPerSourceIP(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tokenURL := "http://" + ln.Addr().String()

	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "test-aud",
		TokenURL:          tokenURL,
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup:      e2eClientLookup{},
		AllowInsecureJWKS: true,
		RateLimitPerSource: auth.RateLimit{
			Burst:           3,
			RefillPerSecond: 0,
		},
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: te}}
	srv.Start()
	t.Cleanup(srv.Close)

	post := func() (int, string) {
		form := url.Values{}
		form.Set("grant_type", "client_credentials")
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", "not-a-real-jwt")
		resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
			strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Retry-After")
	}

	for i := 0; i < 3; i++ {
		code, _ := post()
		if code == http.StatusTooManyRequests {
			t.Fatalf("burst attempt %d rate-limited prematurely (code=%d)", i, code)
		}
	}
	code, retry := post()
	if code != http.StatusTooManyRequests {
		t.Fatalf("4th attempt code=%d; want 429", code)
	}
	if retry == "" {
		t.Fatalf("4th attempt missing Retry-After header")
	}
}
