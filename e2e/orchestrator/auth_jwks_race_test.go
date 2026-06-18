// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestE2E_TokenEndpoint_JWKSCacheRace fires 50 concurrent /token POSTs
// at a real running TokenEndpoint instance and asserts none of them
// race the (now-mutex-protected) jwksCache. Pre-fix the race detector
// would flag this; this is a regression guard for B-5.
func TestE2E_TokenEndpoint_JWKSCacheRace(t *testing.T) {
	t.Parallel()

	clientID := "race-client"
	jwksSrv, kid, priv := newJWKSServer(t)
	te, srv := newTokenEndpointE2E(t, clientID, jwksSrv.URL+"/jwks")
	_ = te

	now := time.Now()
	mkAssertion := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": clientID,
			"sub": clientID,
			"aud": srv.URL,
			"jti": uuid.NewString(),
			"iat": now.Add(-1 * time.Minute).Unix(),
			"exp": now.Add(2 * time.Minute).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(priv)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return s
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
			resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded",
				strings.NewReader(form.Encode()))
			if err != nil {
				t.Errorf("POST: %v", err)
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
}

// e2eClientLookup is a tiny in-memory ClientLookup for e2e tests.
type e2eClientLookup map[string]auth.ClientRecord

func (e e2eClientLookup) GetByID(_ context.Context, id string) (*auth.ClientRecord, error) {
	r, ok := e[id]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

// newJWKSServer hosts a single test key as a JWKS doc at /jwks.
func newJWKSServer(t *testing.T) (srv *httptest.Server, kid string, priv *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	kid = uuid.NewString()
	doc := jwksDoc(priv, kid)
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, kid, priv
}

// newTokenEndpointE2E binds a TokenEndpoint to an httptest server. The
// server's URL is determined first (via an unstarted listener) so that
// TokenURL and the assertion's aud claim agree.
func newTokenEndpointE2E(t *testing.T, clientID, jwksURL string) (*auth.TokenEndpoint, *httptest.Server) {
	t.Helper()
	// Pre-bind so we know the URL before constructing the
	// TokenEndpoint. The token URL is the canonical /token and clients
	// must use it as the assertion's audience.
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
		ClientLookup: e2eClientLookup{clientID: auth.ClientRecord{
			ID:      clientID,
			JwksURL: jwksURL,
			Scopes:  []string{"system/Subscription.r"},
		}},
		AllowInsecureJWKS: true,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	srv := &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: te},
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return te, srv
}

func jwksDoc(priv *rsa.PrivateKey, kid string) map[string]any {
	pub := priv.PublicKey
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": kid,
				"n":   n,
				"e":   e,
			},
		},
	}
}
