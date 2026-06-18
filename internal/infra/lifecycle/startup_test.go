// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Tests cover LLD §5.3:
//
//   - Until MarkStartupComplete is called, /startup returns 503
//     {"status":"starting"} regardless of readiness checks.
//   - After MarkStartupComplete, /startup proxies /readyz semantics
//     (200 when all checks pass; 503 with failed[] when any fails;
//     503 with failed=["shutting_down"] during shutdown).

func TestStartup_BeforeCompleteReturns503Starting(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	// Pre-register a passing check; until startup_complete flips, the
	// startup probe MUST 503 even with passing checks.
	must(t, r.registerReadiness("postgres", okCheck))
	h := newStartupHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/startup", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "starting" {
		t.Fatalf("status field: got %q want \"starting\"", body["status"])
	}
}

func TestStartup_AfterCompleteProxiesReadyz_Pass(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", okCheck))
	r.markStartupComplete()
	h := newStartupHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/startup", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "ready" {
		t.Fatalf("status field: got %q want \"ready\"", body["status"])
	}
}

func TestStartup_AfterCompleteProxiesReadyz_Fail(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", failCheck("DB down")))
	r.markStartupComplete()
	h := newStartupHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/startup", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "unready" {
		t.Fatalf("status field: got %q want \"unready\"", body["status"])
	}
	failed := stringSlice(body["failed"])
	if len(failed) != 1 || failed[0] != "postgres" {
		t.Fatalf("failed: got %v want [postgres]", failed)
	}
}

func TestStartup_ShutdownInProgressReturns503Shutdown(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	r.markStartupComplete()
	r.markShutdownInProgress()
	h := newStartupHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/startup", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "unready" {
		t.Fatalf("status field: got %q want \"unready\"", body["status"])
	}
	failed := stringSlice(body["failed"])
	if len(failed) != 1 || failed[0] != "shutting_down" {
		t.Fatalf("failed: got %v want [shutting_down]", failed)
	}
}

func TestStartup_OncesMarkComplete_TheFlagIsStable(t *testing.T) {
	t.Parallel()
	// Once startup_complete is set, repeated MarkStartupComplete calls
	// are idempotent and the flag does not flip back to "starting".
	r := newRegistry()
	r.markStartupComplete()
	r.markStartupComplete()
	if !r.startupComplete() {
		t.Fatalf("startup_complete should remain true after repeated mark calls")
	}
}
