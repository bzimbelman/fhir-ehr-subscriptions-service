// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// stubAuthMiddleware is a chi-shape middleware that 401s when the request
// is missing X-Test-Auth: yes. It exercises the same dispatch chain as the
// real auth.Verifier without dragging JWT/JWKS into the test.
func stubAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Auth") != "yes" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), defaultPrincipal())))
	})
}

// newAuthWiredServer builds a router exactly as production wiring should:
// the auth middleware is supplied via Deps.Auth; RegisterRoutes is the
// only thing the caller invokes (no separate r.Use is required).
func newAuthWiredServer(t *testing.T, deps handlers.Deps) *httptest.Server {
	t.Helper()
	deps.Auth = stubAuthMiddleware
	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// TestRegisterRoutes_NotFound_Requires401WithoutAuth proves the catch-all
// NotFound route runs behind the auth middleware: an unauthenticated
// caller hitting an unknown URL gets 401, not the FHIR 404
// OperationOutcome. Story #58 / N-1.4.
func TestRegisterRoutes_NotFound_Requires401WithoutAuth(t *testing.T) {
	t.Parallel()
	srv := newAuthWiredServer(t, defaultDeps(t))

	resp, err := http.Get(srv.URL + "/no/such/endpoint")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", resp.StatusCode, body)
	}
}

// TestRegisterRoutes_MethodNotAllowed_Requires401WithoutAuth proves the
// MethodNotAllowed path is also gated by auth: an unauthenticated caller
// hitting a known path with a wrong method must get 401, not the FHIR 405
// OperationOutcome. Story #58 / N-1.4.
func TestRegisterRoutes_MethodNotAllowed_Requires401WithoutAuth(t *testing.T) {
	t.Parallel()
	srv := newAuthWiredServer(t, defaultDeps(t))

	// /metadata is GET-only; PATCH must surface MethodNotAllowed.
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/metadata", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /metadata: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", resp.StatusCode, body)
	}
}

// TestRegisterRoutes_NotFound_Auth_Returns404 proves that with auth
// satisfied, unknown URLs surface the FHIR 404 OperationOutcome from the
// handlers' catch-all. Confirms the typed Auth slot does not break the
// existing behavior.
func TestRegisterRoutes_NotFound_Auth_Returns404(t *testing.T) {
	t.Parallel()
	srv := newAuthWiredServer(t, defaultDeps(t))

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/no/such/endpoint", nil)
	req.Header.Set("X-Test-Auth", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("body did not contain OperationOutcome: %s", body)
	}
}

// TestRegisterRoutes_MethodNotAllowed_Auth_Returns405 proves that with
// auth satisfied, wrong-method requests surface the FHIR 405
// OperationOutcome.
func TestRegisterRoutes_MethodNotAllowed_Auth_Returns405(t *testing.T) {
	t.Parallel()
	srv := newAuthWiredServer(t, defaultDeps(t))

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/metadata", nil)
	req.Header.Set("X-Test-Auth", "yes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /metadata: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("body did not contain OperationOutcome: %s", body)
	}
}

// TestRegisterRoutes_PanicsWhenAuthNil proves wiring without an auth
// middleware fails loud at RegisterRoutes time, not silently at first
// unauthenticated request. Story #58 / N-1.4.
func TestRegisterRoutes_PanicsWhenAuthNil(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	// deps.Auth deliberately left nil — production wiring must always
	// supply a chi.Middleware-shape value.
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("RegisterRoutes(nil Auth) did not panic")
		}
	}()
	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)
}

// TestDeps_Auth_IsChiMiddlewareTyped is a compile-time assertion that
// Deps.Auth is the chi-compatible middleware shape (and therefore
// assignable to chi.Middlewares' element type). If a future refactor
// renames or re-types the field, this test stops compiling — the
// invariant the story #58 audit point cared about.
func TestDeps_Auth_IsChiMiddlewareTyped(t *testing.T) {
	t.Parallel()
	var _ handlers.Middleware = stubAuthMiddleware
	var d handlers.Deps
	d.Auth = stubAuthMiddleware
	// Assignable to chi's per-middleware shape.
	var fn func(http.Handler) http.Handler = d.Auth
	_ = fn
}
