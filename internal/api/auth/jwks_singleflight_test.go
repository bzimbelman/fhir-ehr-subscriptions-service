// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"context"
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

// gatedClientLookup wraps a ClientLookup so each GetByID call signals
// arrival on a WaitGroup and then blocks on a release channel. The
// test uses it to deterministically synchronize the start of all N
// concurrent Authenticate goroutines: every goroutine parks inside
// GetByID until the test explicitly releases the gate. Once released,
// the goroutines race through pure in-memory paths (parse JWT,
// keyfuncFor cache check, singleflight.Do) while the leader is the
// only one doing network I/O — by the time the leader's loopback HTTP
// fetch completes, every follower has already joined the in-flight
// singleflight group. This replaces the previous wall-clock 50ms
// sleep barrier (#318), which was timing-dependent and flaked on
// slow CI runners when the leader's fetch returned before stragglers
// reached singleflight.Do.
type gatedClientLookup struct {
	inner   auth.ClientLookup
	arrived *sync.WaitGroup
	release chan struct{}
}

func (g *gatedClientLookup) GetByID(ctx context.Context, id string) (*auth.ClientRecord, error) {
	g.arrived.Done()
	<-g.release
	return g.inner.GetByID(ctx, id)
}

// TestVerifier_JWKS_SingleflightDeduplicatesConcurrentFetches covers
// OP #202: 1000 concurrent first-time Authenticate calls for the same
// client (and hence the same JWKS URL) MUST trigger exactly one HTTP
// fetch on the JWKS server and emit N-1 collisions on the
// fhir_subs_jwks_singleflight_collisions_total counter
// (RecordJWKSSingleflightCollision).
//
// Synchronization is deterministic (no wall-clock sleeps): the test
// gates ClientLookup.GetByID with a WaitGroup so every goroutine
// parks inside the lookup until all N have arrived. Releasing the
// gate lets every goroutine sprint forward through pure in-memory
// code into singleflight.Do; only the elected leader proceeds to a
// real loopback HTTP fetch. Loopback round-trip latency is orders of
// magnitude larger than nanosecond-scale function dispatch, so every
// follower reaches singleflight.Do and joins the in-flight group
// before the leader's HTTP request reaches the JWKS handler.
func TestVerifier_JWKS_SingleflightDeduplicatesConcurrentFetches(t *testing.T) {
	t.Parallel()
	k := newKey(t)

	const N = 1000

	var jwksHits int32
	jwksMux := http.NewServeMux()
	jwksMux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&jwksHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(k.jwks())
	})
	srv := httptest.NewServer(jwksMux)
	t.Cleanup(srv.Close)

	clientID := "stampede-client"
	rec := newRecordingMetrics()
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }

	var arrived sync.WaitGroup
	arrived.Add(N)
	gate := &gatedClientLookup{
		inner: fakeClientLookup{clientID: {
			ID:      clientID,
			JwksURL: srv.URL + "/jwks",
			Scopes:  []string{"system/Subscription.r"},
		}},
		arrived: &arrived,
		release: make(chan struct{}),
	}
	t.Cleanup(func() {
		// Defensive: if a test failure short-circuits before
		// close(gate.release), unblock any parked goroutines so the
		// runtime can collect them. Re-closing a closed channel panics,
		// so guard with sync.Once via a local flag.
		select {
		case <-gate.release:
		default:
			close(gate.release)
		}
	})

	v, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:          "aud",
		ClientLookup:      gate,
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

	var wg sync.WaitGroup
	var oks int32
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, status, _ := v.Authenticate(mkReq())
			if status == 0 {
				atomic.AddInt32(&oks, 1)
			}
		}()
	}

	// Wait for every goroutine to be parked inside GetByID. Once
	// arrived.Wait returns, all N callers are guaranteed to be
	// blocked on `release`; closing the channel unblocks them
	// simultaneously so they race forward through purely in-memory
	// code paths into singleflight.Do.
	arrived.Wait()
	close(gate.release)
	wg.Wait()

	hits := atomic.LoadInt32(&jwksHits)
	if hits != 1 {
		t.Errorf("JWKS hits = %d; want 1 (OP #202: singleflight MUST collapse concurrent first-time fetches)", hits)
	}
	// Followers that joined the in-flight singleflight group MUST be
	// counted as collisions. Goroutines that arrive after the leader
	// populates the cache short-circuit at the outer cache check and
	// never reach singleflight — that's the intended hot-path. The
	// gated start synchronizes every goroutine to leave GetByID at
	// the same instant, so we expect a high collision count, but we
	// only assert the loose [1, N-1] band the metric semantics
	// guarantee.
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
