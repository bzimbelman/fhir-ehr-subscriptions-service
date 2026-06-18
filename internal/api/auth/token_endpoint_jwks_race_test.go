// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestTokenEndpoint_JWKSCache_ConcurrentRequests fires many concurrent
// /token POSTs to exercise the unsynchronised jwksCache.entries map. With
// `go test -race`, an unsynchronised map produces a data-race report;
// without -race, Go's runtime can fatal-error on "concurrent map read and
// map write". This test pins the regression that the cache is mutex- (or
// equivalent) protected.
func TestTokenEndpoint_JWKSCache_ConcurrentRequests(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "race-client"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID,
		srv.URL+"/jwks", []string{"system/Subscription.r"})

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	// Each goroutine signs its own assertion (unique jti) so the
	// replay-cache doesn't reject them; what we exercise is jwksCache.
	mkAssertion := func() string {
		return k.sign(t, jwt.MapClaims{
			"iss": clientID,
			"sub": clientID,
			"aud": "https://x/token",
			"jti": uuid.NewString(),
			"iat": now.Add(-1 * time.Minute).Unix(),
			"exp": now.Add(2 * time.Minute).Unix(),
		})
	}

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			form := url.Values{}
			form.Set("grant_type", "client_credentials")
			form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
			form.Set("client_assertion", mkAssertion())
			req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			te.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("token = %d body=%s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
}
