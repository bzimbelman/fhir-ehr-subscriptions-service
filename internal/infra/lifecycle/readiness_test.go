// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// Tests cover LLD §5.2:
//
//   - All registered checks run CONCURRENTLY (not serially).
//   - A check that exceeds its timeout is reported as failed with no
//     retry, and the slow check does not block the others.
//   - The failed list is the names of failing checks (sorted, deterministic).
//   - shutdown_in_progress short-circuits to 503 with status="shutting_down"
//     (matches the liveness probe label) and failed=["shutting_down"].
//   - 200 only when EVERY check passes.

func TestReadyz_AllPassReturns200(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", okCheck))
	must(t, r.registerReadiness("adapter.epic", okCheck))
	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "ready" {
		t.Fatalf("status field: got %q want \"ready\"", body["status"])
	}
}

func TestReadyz_AnyFailReturns503WithFailedList(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", failCheck("DB down")))
	must(t, r.registerReadiness("adapter.epic", okCheck))
	must(t, r.registerReadiness("mllp_listener", failCheck("not bound")))
	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "unready" {
		t.Fatalf("status field: got %q want \"unready\"", body["status"])
	}
	got := stringSlice(body["failed"])
	want := []string{"mllp_listener", "postgres"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failed: got %v want %v", got, want)
	}
}

func TestReadyz_ChecksRunConcurrently(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	const n = 8
	for i := 0; i < n; i++ {
		name := "check-" + itoa(i)
		must(t, r.registerReadiness(name, sleepCheck(40*time.Millisecond)))
	}
	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)

	rr := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)
	// Serial would be n * 40ms = 320ms. Concurrent is bounded by the
	// slowest check + scheduler slop; assert << serial. Allow a generous
	// 200ms upper bound for CI.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("readiness aggregator did not run checks concurrently: elapsed=%v", elapsed)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
}

func TestReadyz_SlowCheckTimesOutWithoutBlockingOthers(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", okCheck))
	// "blocking" simulates a check that hangs forever; the aggregator's
	// per-check timeout must fire and the check must be reported as
	// failed without blocking other checks.
	must(t, r.registerReadiness("blocking", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}))

	h := newReadinessHandler(r, nopMetrics{}, 50*time.Millisecond)

	rr := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("aggregator hung past per-check timeout: elapsed=%v", elapsed)
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	failed := stringSlice(body["failed"])
	if len(failed) != 1 || failed[0] != "blocking" {
		t.Fatalf("failed: got %v want [blocking]", failed)
	}
}

func TestReadyz_NoRetryOnFailure(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	var calls atomic.Int32
	must(t, r.registerReadiness("flaky", func(ctx context.Context) error {
		calls.Add(1)
		return errors.New("flap")
	}))

	h := newReadinessHandler(r, nopMetrics{}, 50*time.Millisecond)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if got := calls.Load(); got != 1 {
		t.Fatalf("flaky check invoked %d times; aggregator must not retry (LLD §5.2)", got)
	}
}

func TestReadyz_ShutdownInProgressShortCircuits(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	called := false
	must(t, r.registerReadiness("postgres", func(ctx context.Context) error {
		called = true
		return nil
	}))
	r.markShutdownInProgress()

	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if called {
		t.Fatalf("shutdown short-circuit must not invoke readiness checks")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	if body["status"] != "shutting_down" {
		t.Fatalf("status field: got %q want \"shutting_down\"", body["status"])
	}
	failed := stringSlice(body["failed"])
	if len(failed) != 1 || failed[0] != "shutting_down" {
		t.Fatalf("failed: got %v want [shutting_down]", failed)
	}
}

func TestReadyz_PanicInCheckIsReportedAsFailure(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerReadiness("naughty", func(ctx context.Context) error {
		panic("boom")
	}))
	must(t, r.registerReadiness("nice", okCheck))

	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
	body := decodeJSONStatus(t, rr.Result().Body)
	failed := stringSlice(body["failed"])
	if len(failed) != 1 || failed[0] != "naughty" {
		t.Fatalf("failed: got %v want [naughty]", failed)
	}
}

func TestReadyz_NoChecksReturns200(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	h := newReadinessHandler(r, nopMetrics{}, 2*time.Second)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (no checks → ready)", rr.Code)
	}
}

// helpers.

func okCheck(ctx context.Context) error { return nil }

func failCheck(reason string) ReadinessCheck {
	return func(ctx context.Context) error { return errors.New(reason) }
}

func sleepCheck(d time.Duration) ReadinessCheck {
	return func(ctx context.Context) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// stringSlice converts an interface{} JSON array to a sorted []string.
func stringSlice(v any) []string {
	if v == nil {
		return nil
	}
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
