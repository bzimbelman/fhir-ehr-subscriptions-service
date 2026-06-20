// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package observability_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
)

// startMod is a small helper that builds a fresh observability module for
// the lifecycle-adapter tests. The covergate-relevant code under test is
// the LifecycleMetricsAdapter dispatch: we rely on Start to construct a
// real *metrics.Inventory (no mocks), then exercise the adapter's
// forwarding behavior end-to-end via /metrics scrape.
func startMod(t *testing.T) *observability.ObservabilityModule {
	t.Helper()
	mod, _, err := observability.Start(context.Background(), observability.Config{
		Logging: observability.LoggingConfig{Level: "info", Format: "json"},
	}, observability.Context{
		StoragePool: newFakeStore(),
		Clock:       func() time.Time { return time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("observability.Start: %v", err)
	}
	t.Cleanup(func() { _ = mod.Shutdown(context.Background()) })
	return mod
}

func scrapeBody(t *testing.T, mod *observability.ObservabilityModule) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mod.PrometheusHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("scrape: status %d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// NewLifecycleMetricsAdapter returns nil for nil module / module with nil
// inventory. The host wiring uses the nil-or-real return as a signal —
// passing nil to lcMod.SetMetrics would later panic on the first emit.
func TestNewLifecycleMetricsAdapter_NilGuards(t *testing.T) {
	t.Parallel()
	if got := observability.NewLifecycleMetricsAdapter(nil); got != nil {
		t.Fatalf("expected nil adapter for nil module; got %#v", got)
	}
	// A zero-value *ObservabilityModule has nil inventory and is the
	// other guard branch. Construct one explicitly via a typed nil
	// pointer-style — but since the field is unexported we exercise the
	// "nil module" path here and rely on the real Start path below to
	// cover the inventory-bound branch.
}

// Inc forwards to the matching counter for every registered lifecycle
// metric name and is a silent no-op for unknown names.
func TestLifecycleMetricsAdapter_Inc_ForwardsByName(t *testing.T) {
	t.Parallel()
	mod := startMod(t)
	a := observability.NewLifecycleMetricsAdapter(mod)
	if a == nil {
		t.Fatalf("adapter must be non-nil for a real module")
	}

	a.Inc("fhir_subs_lifecycle_shutdown_initiated_total", map[string]string{"reason": "sigterm"})
	a.Inc("fhir_subs_lifecycle_shutdown_initiated_total", map[string]string{"reason": "sigterm"})
	a.Inc("fhir_subs_lifecycle_shutdown_forced_total", nil)
	a.Inc("fhir_subs_lifecycle_shutdown_hook_outcome_total", map[string]string{"hook": "drain", "outcome": "drained"})
	a.Inc("fhir_subs_lifecycle_probe_requests_total", map[string]string{"probe": "readyz", "status_code": "200"})
	a.Inc("fhir_subs_lifecycle_readiness_check_failures_total", map[string]string{"name": "db"})
	// Unknown metric name must be dropped silently — no panic, no series.
	a.Inc("fhir_subs_unknown_metric_xyz", map[string]string{"k": "v"})

	body := scrapeBody(t, mod)

	wantSubstrings := []string{
		`fhir_subs_lifecycle_shutdown_initiated_total{reason="sigterm"} 2`,
		`fhir_subs_lifecycle_shutdown_forced_total 1`,
		`fhir_subs_lifecycle_shutdown_hook_outcome_total{hook="drain",outcome="drained"} 1`,
		`fhir_subs_lifecycle_probe_requests_total{probe="readyz",status_code="200"} 1`,
		`fhir_subs_lifecycle_readiness_check_failures_total{name="db"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\nbody:\n%s", want, body)
		}
	}
	if strings.Contains(body, "fhir_subs_unknown_metric_xyz") {
		t.Errorf("unknown metric must be dropped; body contained the name")
	}
}

// Add forwards a delta to the counter that matches the metric name.
// Same dispatch shape as Inc — the test asserts that each branch is
// wired to the right Inventory handle by reading the scraped totals.
func TestLifecycleMetricsAdapter_Add_ForwardsByName(t *testing.T) {
	t.Parallel()
	mod := startMod(t)
	a := observability.NewLifecycleMetricsAdapter(mod)
	if a == nil {
		t.Fatalf("adapter must be non-nil for a real module")
	}

	a.Add("fhir_subs_lifecycle_shutdown_initiated_total", 3, map[string]string{"reason": "sigint"})
	a.Add("fhir_subs_lifecycle_shutdown_forced_total", 2, nil)
	a.Add("fhir_subs_lifecycle_shutdown_hook_outcome_total", 4, map[string]string{"hook": "scheduler", "outcome": "timed_out"})
	a.Add("fhir_subs_lifecycle_probe_requests_total", 5, map[string]string{"probe": "livez", "status_code": "200"})
	a.Add("fhir_subs_lifecycle_readiness_check_failures_total", 6, map[string]string{"name": "kafka"})
	// Unknown name is silently dropped.
	a.Add("fhir_subs_unknown_metric_xyz", 99, nil)

	body := scrapeBody(t, mod)

	wantSubstrings := []string{
		`fhir_subs_lifecycle_shutdown_initiated_total{reason="sigint"} 3`,
		`fhir_subs_lifecycle_shutdown_forced_total 2`,
		`fhir_subs_lifecycle_shutdown_hook_outcome_total{hook="scheduler",outcome="timed_out"} 4`,
		`fhir_subs_lifecycle_probe_requests_total{probe="livez",status_code="200"} 5`,
		`fhir_subs_lifecycle_readiness_check_failures_total{name="kafka"} 6`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\nbody:\n%s", want, body)
		}
	}
	if strings.Contains(body, "fhir_subs_unknown_metric_xyz") {
		t.Errorf("unknown metric must be dropped; body contained the name")
	}
}

// Observe forwards to the phase-duration histogram for the registered
// lifecycle histogram name and is a silent no-op otherwise.
func TestLifecycleMetricsAdapter_Observe_ForwardsByName(t *testing.T) {
	t.Parallel()
	mod := startMod(t)
	a := observability.NewLifecycleMetricsAdapter(mod)

	a.Observe("fhir_subs_lifecycle_phase_duration_seconds", 0.123, map[string]string{"phase": "drain"})
	a.Observe("fhir_subs_lifecycle_phase_duration_seconds", 0.456, map[string]string{"phase": "drain"})
	// Unknown histogram name must be dropped silently.
	a.Observe("fhir_subs_unknown_histogram", 1.0, nil)

	body := scrapeBody(t, mod)

	// Histogram exposes _count / _sum aggregates — the count is the
	// reliable invariant (sum is float-valued and brittle to assert).
	if !strings.Contains(body, `fhir_subs_lifecycle_phase_duration_seconds_count{phase="drain"} 2`) {
		t.Errorf(`scrape missing fhir_subs_lifecycle_phase_duration_seconds_count{phase="drain"} 2; body:\n%s`, body)
	}
	if strings.Contains(body, "fhir_subs_unknown_histogram") {
		t.Errorf("unknown histogram name must be dropped silently")
	}
}

// Set forwards to the startup-complete gauge for the registered
// lifecycle gauge name and is a silent no-op otherwise.
func TestLifecycleMetricsAdapter_Set_ForwardsByName(t *testing.T) {
	t.Parallel()
	mod := startMod(t)
	a := observability.NewLifecycleMetricsAdapter(mod)

	a.Set("fhir_subs_lifecycle_startup_complete", 1, nil)
	// Unknown gauge name is silently dropped.
	a.Set("fhir_subs_unknown_gauge", 42, map[string]string{"k": "v"})

	body := scrapeBody(t, mod)

	if !strings.Contains(body, `fhir_subs_lifecycle_startup_complete 1`) {
		t.Errorf("scrape missing fhir_subs_lifecycle_startup_complete=1; body:\n%s", body)
	}
	if strings.Contains(body, "fhir_subs_unknown_gauge") {
		t.Errorf("unknown gauge must be dropped silently")
	}
}

// A nil adapter receiver must not panic on any forwarder. The lifecycle
// module reaches for the adapter without nil-checking on every emit, so
// the adapter's defensive nil-receiver path is the load-bearing
// safety net.
func TestLifecycleMetricsAdapter_NilReceiver_IsNoOp(t *testing.T) {
	t.Parallel()
	var a *observability.LifecycleMetricsAdapter
	a.Inc("fhir_subs_lifecycle_shutdown_initiated_total", map[string]string{"reason": "x"})
	a.Add("fhir_subs_lifecycle_shutdown_initiated_total", 1, map[string]string{"reason": "x"})
	a.Observe("fhir_subs_lifecycle_phase_duration_seconds", 0.5, map[string]string{"phase": "p"})
	a.Set("fhir_subs_lifecycle_startup_complete", 1, nil)
	// Reaching this line without a panic is the assertion.
}

// A nil-labels argument from the lifecycle module must round-trip into
// an empty (but non-nil) prometheus.Labels so the underlying counter
// dispatches without panicking. The unlabeled forced-total counter is
// the easiest witness.
func TestLifecycleMetricsAdapter_NilLabels_AreSafe(t *testing.T) {
	t.Parallel()
	mod := startMod(t)
	a := observability.NewLifecycleMetricsAdapter(mod)

	a.Inc("fhir_subs_lifecycle_shutdown_forced_total", nil)
	a.Add("fhir_subs_lifecycle_shutdown_forced_total", 2, nil)

	body := scrapeBody(t, mod)
	if !strings.Contains(body, `fhir_subs_lifecycle_shutdown_forced_total 3`) {
		t.Errorf("scrape missing fhir_subs_lifecycle_shutdown_forced_total=3; body:\n%s", body)
	}
}
