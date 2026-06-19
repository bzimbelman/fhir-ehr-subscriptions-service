// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// TestRegisterPublicRoutes_MetadataReachableWithoutAuth proves that a
// chi.Mux assembled the way production wiring should — RegisterPublicRoutes
// first, RegisterRoutes second — serves GET /metadata to a caller with
// no Authorization header. FHIR conformance probes (Inferno, HL7 testkit)
// rely on this behavior; auditing today, /metadata sits inside the
// auth-protected group and probes get 401. Story #93 / S-2.1.
func TestRegisterPublicRoutes_MetadataReachableWithoutAuth(t *testing.T) {
	t.Parallel()

	deps := defaultDeps(t)
	// Auth middleware that 401s every request that reaches it. If the
	// production wiring layout is correct, /metadata never reaches this
	// middleware.
	deps.Auth = func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}

	r := chi.NewRouter()
	handlers.RegisterPublicRoutes(r, deps)
	handlers.RegisterRoutes(r, deps)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("GET /metadata: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metadata = %d; want 200; body=%s", resp.StatusCode, body)
	}
	// The body must parse as a FHIR CapabilityStatement.
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("response not JSON: %v; body=%s", err, body)
	}
	rt, _ := doc["resourceType"].(string)
	if rt != "CapabilityStatement" {
		t.Fatalf("resourceType = %q, want CapabilityStatement; body=%s",
			rt, body)
	}
}

// TestRegisterPublicRoutes_OnlyMetadata asserts that RegisterPublicRoutes
// does NOT mount any of the FHIR API endpoints — those must remain
// behind the auth gate that RegisterRoutes installs. A regression here
// (e.g. accidentally registering /Subscription on the public mux) would
// expose every FHIR resource to unauthenticated callers, which is the
// inverse of the bug story #93 fixes.
func TestRegisterPublicRoutes_OnlyMetadata(t *testing.T) {
	t.Parallel()

	deps := defaultDeps(t)
	r := chi.NewRouter()
	handlers.RegisterPublicRoutes(r, deps)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/Subscription",
		"/Subscription/abc",
		"/SubscriptionTopic",
		"/admin/dead_letters",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("GET %s = 200 from public router (must not be public)", path)
		}
	}
}

// TestRegisterRoutes_MetadataNotInAuthGroup is the inverse of the test
// above: when only RegisterRoutes runs (no public mounting), GET
// /metadata MUST return 401 with the auth gate active. Today the
// auth-protected /metadata returns 401 — story #93 moves it to
// RegisterPublicRoutes so this test will keep passing while the public
// path becomes reachable. If a future refactor reintroduces a duplicate
// /metadata route inside the auth group, the auth-only assembly here
// will surface it as a 200 and break this test.
func TestRegisterRoutes_MetadataNotInAuthGroup(t *testing.T) {
	t.Parallel()

	deps := defaultDeps(t)
	deps.Auth = func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}

	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("GET /metadata: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// After story #93 lands /metadata is no longer mounted inside the
	// auth-protected group. The catch-all NotFound responder runs
	// behind the auth gate and emits 401 for any unknown path.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /metadata via RegisterRoutes only = %d; want 401 (catch-all behind auth); body=%s",
			resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "unauthorized") {
		t.Fatalf("body should be the auth middleware's 401 line: %s", body)
	}
}
