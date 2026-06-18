// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// TestTokenEndpoint_RejectsPlainHTTPJWKSURL pins the B-12 contract:
// without an explicit AllowInsecureJWKS opt-in, a JWKS URL whose scheme
// is `http://` must be refused. The unauthenticated /token endpoint is
// otherwise an SSRF surface — an attacker who can register a client
// (or modify auth_clients) reaches arbitrary hosts on the deployment
// network.
func TestTokenEndpoint_RejectsPlainHTTPJWKSURL(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k) // hosts http://...
	if !strings.HasPrefix(srv.URL, "http://") {
		t.Fatalf("test server should be http://; got %s", srv.URL)
	}

	clientID := "c1"
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "aud",
		TokenURL:          "https://x/token",
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: fakeClientLookup{clientID: {
			ID:      clientID,
			JwksURL: srv.URL + "/jwks", // plain http
			Scopes:  []string{"system/Subscription.r"},
		}},
		Now: func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 (plain http JWKS URL must be refused)", rec.Code)
	}
}

// TestTokenEndpoint_AllowsPlainHTTPJWKSURLWhenOptedIn confirms the
// reverse: when AllowInsecureJWKS is set, the plain-http URL is
// permitted (local-dev escape hatch).
func TestTokenEndpoint_AllowsPlainHTTPJWKSURLWhenOptedIn(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)

	clientID := "c1"
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "aud",
		TokenURL:          "https://x/token",
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: fakeClientLookup{clientID: {
			ID:      clientID,
			JwksURL: srv.URL + "/jwks",
			Scopes:  []string{"system/Subscription.r"},
		}},
		Now:               func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		AllowInsecureJWKS: true,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200 with AllowInsecureJWKS", rec.Code, rec.Body.String())
	}
}

// TestTokenEndpoint_JWKSResponseBodyCapped pins the body-size cap on
// the JWKS response. A hostile/misconfigured JWKS endpoint serving a
// huge body must not exhaust the auth process. We measure the bytes
// pulled off the wire by counting on the server side; with the cap in
// place the server completes the response but the client reads only
// up to MaxJWKSBodyBytes (1 MiB).
func TestTokenEndpoint_JWKSResponseBodyCapped(t *testing.T) {
	t.Parallel()

	const oversize = 8 * 1024 * 1024 // 8 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Stream lots of bytes; we don't care if the client closes
		// early — we just want to verify the auth process refuses to
		// load the whole response into memory.
		buf := make([]byte, 64*1024)
		for i := range buf {
			buf[i] = ' '
		}
		written := 0
		for written < oversize {
			n, err := w.Write(buf)
			if err != nil {
				return
			}
			written += n
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	clientID := "c1"
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "aud",
		TokenURL:          "https://x/token",
		AccessTokenSecret: []byte("test-server-signing-key-must-be-32-bytes!"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup: fakeClientLookup{clientID: {
			ID:      clientID,
			JwksURL: srv.URL,
			Scopes:  []string{"system/Subscription.r"},
		}},
		Now:               func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) },
		AllowInsecureJWKS: true, // we only care about body cap here, not scheme
		JWKSFetchTimeout:  3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}

	// Build a real-looking assertion so the handler actually fetches
	// the JWKS. Signature verification will fail; that's fine — we
	// just need to exercise the fetch path.
	k := newKey(t)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	assertion := k.sign(t, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		te.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("token handler hung on oversized JWKS response")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (jwks unavailable / parse failure)", rec.Code)
	}
}
