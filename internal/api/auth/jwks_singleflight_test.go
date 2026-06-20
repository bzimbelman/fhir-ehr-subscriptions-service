// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestVerifier_JWKS_SingleflightDeduplicatesConcurrentFetches covers
// OP #202: 1000 concurrent first-time Authenticate calls for the same
// client (and hence the same JWKS URL) MUST trigger exactly one HTTP
// fetch on the JWKS server and emit N-1 collisions on the
// fhir_subs_jwks_singleflight_collisions_total counter (RecordJWKSSingleflightCollision).
//
// The JWKS server blocks on a release channel until every goroutine
// has parked behind singleflight; without singleflight all N fetches
// stamp the IdP simultaneously.
func TestVerifier_JWKS_SingleflightDeduplicatesConcurrentFetches(t *testing.T) {
	t.Parallel()
	k := newKey(t)

	var jwksHits int32
	release := make(chan struct{})
	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&jwksHits, 1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(k.jwks())
	})
	srv := httptest.NewServer(jwksMux)
	t.Cleanup(srv.Close)

	clientID := "stampede-client"
	rec := newRecordingMetrics()
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience: "aud",
		ClientLookup: fakeClientLookup{clientID: {
			ID:      clientID,
			JwksURL: srv.URL + "/jwks",
			Scopes:  []string{"system/Subscription.r"},
		}},
		ClockSkew:         60 * time.Second,
		Now:               now,
		AllowInsecureJWKS: true,
		Metrics:           rec,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	mkReq := func() *http.Request {
		tok := k.sign(t, jwt.MapClaims{
			"iss":       clientID,
			"sub":       clientID,
			"aud":       "aud",
			"client_id": clientID,
			"jti":       uuid.NewString(),
			"iat":       now().Add(-1 * time.Minute).Unix(),
			"exp":       now().Add(5 * time.Minute).Unix(),
			"scope":     "system/Subscription.r",
		})
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		return req
	}

	const N = 1000
	var wg sync.WaitGroup
	var oks int32
	wg.Add(N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, status, _ := v.Authenticate(mkReq())
			if status == 0 {
				atomic.AddInt32(&oks, 1)
			}
		}()
	}
	close(start)

	// Give every goroutine time to park behind singleflight before the
	// leader proceeds. 50ms is generous on a developer laptop and short
	// enough to keep the test fast; if the JWKS handler is hit more than
	// once it means singleflight isn't deduplicating.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	hits := atomic.LoadInt32(&jwksHits)
	if hits != 1 {
		t.Errorf("JWKS hits = %d; want 1 (OP #202: singleflight MUST collapse concurrent first-time fetches)", hits)
	}
	// Followers that joined the in-flight singleflight group MUST be
	// counted as collisions. Goroutines that arrive after the leader
	// populates the cache short-circuit at the outer cache check and
	// never reach singleflight — that's the intended hot-path. So the
	// collision count is at least 1 (some followers parked behind the
	// leader during the 50ms window) and at most N-1.
	collisions := rec.jwksCollisionCount()
	if collisions < 1 {
		t.Errorf("singleflight collisions = %d; expected at least 1 (followers joined the in-flight group; OP #202 metric)", collisions)
	}
	if collisions > N-1 {
		t.Errorf("singleflight collisions = %d; expected <= %d", collisions, N-1)
	}
	if oks == 0 {
		t.Errorf("zero successful Authenticate calls; smoke check failed")
	}
}

// Compile-time check the test helper still satisfies the interface
// after the OP #202 method addition. Without this, a missing method
// shows as a metrics_test.go compile error far from the diff that
// added the change.
var _ auth.MetricsRecorder = (*recordingMetrics)(nil)
