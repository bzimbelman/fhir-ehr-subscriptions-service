// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestVerifier_ConcurrentReplay pins OP #110 at the verifier layer.
//
// verifier.go has a TOCTOU window between JTICache.Seen() and
// JTICache.Put(): two goroutines presenting the SAME bearer token can
// both observe Seen=false, both then Put, and both succeed
// authentication. The intended invariant is "exactly one wins".
//
// We fire many concurrent Authenticate calls bearing the same token
// (same jti) and assert that exactly one returns success (status 0)
// and the rest return 401 with a "replay" reason. With the legacy
// Seen→Put implementation, this test fails because >1 winner sneaks
// through.
func TestVerifier_ConcurrentReplay(t *testing.T) {
	t.Parallel()

	const Iterations = 50
	const Concurrency = 64

	for iter := 0; iter < Iterations; iter++ {
		k := newKey(t)
		srv := jwksServer(t, k)
		clientID := "race-client"
		v := newVerifier(t, "aud", srv.URL+"/jwks", clientID,
			[]string{"system/Subscription.r"})

		now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
		claims := defaultClaims(clientID, "aud", now.Add(5*time.Minute))
		// Force a single shared jti so every concurrent request
		// targets the same replay-cache entry.
		claims["jti"] = uuid.NewString()
		tok := k.sign(t, claims)

		var successes atomic.Int64
		var replays atomic.Int64
		var others atomic.Int64

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(Concurrency)
		for i := 0; i < Concurrency; i++ {
			go func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodGet, "/x", nil)
				req.Header.Set("Authorization", "Bearer "+tok)
				<-start
				_, status, reason := v.Authenticate(req)
				switch {
				case status == 0:
					successes.Add(1)
				case status == http.StatusUnauthorized && containsLower(reason, "replay"):
					replays.Add(1)
				default:
					others.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		got := successes.Load()
		if got != 1 {
			t.Fatalf("iter=%d: successes=%d replays=%d others=%d; want exactly 1 success (concurrent JTI replay race)",
				iter, got, replays.Load(), others.Load())
		}
	}
}

func containsLower(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	// case-insensitive substring without pulling strings.Contains/strings.ToLower
	// — keep it simple; reasons are short ASCII.
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a := s[i+j]
			b := sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
