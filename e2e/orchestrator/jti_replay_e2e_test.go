// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestE2E_TokenEndpoint_JTIReplay_OnlyOneSucceeds pins OP #110 at the
// /token endpoint. The token endpoint has the same Seen→Put TOCTOU
// race as the verifier: two concurrent POSTs with the SAME signed
// client_assertion (same jti) can both observe Seen=false, both then
// Put, and both mint a token.
//
// We mint ONE assertion (one jti) and POST it twice in parallel.
// Exactly one POST must succeed (200) and exactly one must fail (401
// "replay"). With the legacy implementation, the race lets both
// succeed, which is the security defect this story closes.
func TestE2E_TokenEndpoint_JTIReplay_OnlyOneSucceeds(t *testing.T) {
	t.Parallel()

	// We deliberately fire MORE than 2 concurrent POSTs to widen the
	// TOCTOU window. The race spec says "fire N=2", but the window
	// between Seen and Put is so tight in practice that 2 goroutines
	// rarely interleave on a fast machine. Bumping to N=32 across
	// many iterations reliably reproduces (multiple 200s for the
	// same jti); we still assert exactly one 200 response and the
	// rest 401, which is the security invariant.
	const Iterations = 200
	const N = 32

	clientID := "jti-race-client"
	jwksSrv, kid, priv := newJWKSServer(t)

	for iter := 0; iter < Iterations; iter++ {
		_, srv := newTokenEndpointE2E(t, clientID, jwksSrv.URL+"/jwks")

		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": clientID,
			"sub": clientID,
			"aud": srv.URL,
			"jti": uuid.NewString(), // SINGLE jti — shared by both POSTs
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
		body := form.Encode()

		var ok200 atomic.Int64
		var fail401 atomic.Int64
		var others atomic.Int64

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(N)
		for i := 0; i < N; i++ {
			go func() {
				defer wg.Done()
				<-start
				resp, postErr := http.Post(srv.URL,
					"application/x-www-form-urlencoded",
					strings.NewReader(body))
				if postErr != nil {
					t.Errorf("POST: %v", postErr)
					return
				}
				_ = resp.Body.Close()
				switch resp.StatusCode {
				case http.StatusOK:
					ok200.Add(1)
				case http.StatusUnauthorized:
					fail401.Add(1)
				default:
					others.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		gotOK := ok200.Load()
		gotFail := fail401.Load()
		gotOther := others.Load()
		// The security invariant: exactly ONE 200 (the winner), and
		// the remaining N-1 must be 401 ("replay"). NOT zero winners
		// and NOT more than one. If today's legacy Seen→Put lets two
		// goroutines both pass Seen=false and both Put, we will see
		// gotOK > 1 — that is the race this story closes.
		if gotOK != 1 || gotFail != int64(N-1) || gotOther != 0 {
			t.Fatalf("iter=%d: 200=%d 401=%d other=%d (N=%d); want exactly 1×200 and %d×401 (concurrent JTI replay race)",
				iter, gotOK, gotFail, gotOther, N, N-1)
		}
	}
}
