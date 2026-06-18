// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests cover LLD §5.1: `/healthz` MUST NOT touch Postgres or invoke any
// registered ReadinessCheck. It returns 200 with {"status":"ok"} unless
// the shutdown_in_progress flag is set (503 with "shutting_down") or the
// panic_signaled flag is set (503 with "panic").

func TestHealthz_DefaultReturns200OK(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	h := newLivenessHandler(r, nopMetrics{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusOK)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "ok" {
		t.Fatalf("status field: got %q want \"ok\"", body["status"])
	}
}

func TestHealthz_DoesNotInvokeReadinessChecks(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	called := false
	must(t, r.registerReadiness("postgres", func(ctx context.Context) error {
		called = true
		return errors.New("DB is down")
	}))
	h := newLivenessHandler(r, nopMetrics{})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if called {
		t.Fatalf("liveness must not invoke readiness checks; postgres check was called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d (DB outage must NOT change liveness)", rr.Code, http.StatusOK)
	}
}

func TestHealthz_ShuttingDown(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	r.markShutdownInProgress()
	h := newLivenessHandler(r, nopMetrics{})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusServiceUnavailable)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "shutting_down" {
		t.Fatalf("status field: got %q want \"shutting_down\"", body["status"])
	}
}

func TestHealthz_PanicSignaled(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	r.markPanicSignaled()
	h := newLivenessHandler(r, nopMetrics{})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want %d", rr.Code, http.StatusServiceUnavailable)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "panic" {
		t.Fatalf("status field: got %q want \"panic\"", body["status"])
	}
}

func TestHealthz_ContentType(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	h := newLivenessHandler(r, nopMetrics{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	got := rr.Result().Header.Get("Content-Type")
	if !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content-type: got %q want application/json...", got)
	}
}

// helpers used by the probe-handler tests.

func decodeJSONStatus(t *testing.T, body io.ReadCloser) map[string]any {
	t.Helper()
	defer body.Close()
	var out map[string]any
	dec := json.NewDecoder(body)
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}
