// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeMetrics records every emit so tests can assert on labels and
// names. Thread-safe for concurrent emit (the sequencer fans hooks out
// concurrently).
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string][]map[string]string
	hist     map[string][]float64
	gauges   map[string]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		counters: make(map[string][]map[string]string),
		hist:     make(map[string][]float64),
		gauges:   make(map[string]float64),
	}
}

func (f *fakeMetrics) Inc(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[name] = append(f.counters[name], cloneLabels(labels))
}

func (f *fakeMetrics) Add(name string, delta float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < int(delta); i++ {
		f.counters[name] = append(f.counters[name], cloneLabels(labels))
	}
}

func (f *fakeMetrics) Observe(name string, value float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hist[name] = append(f.hist[name], value)
}

func (f *fakeMetrics) Set(name string, value float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gauges[name] = value
}

func (f *fakeMetrics) snapshotCounters(name string) []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]string, len(f.counters[name]))
	for i, l := range f.counters[name] {
		out[i] = cloneLabels(l)
	}
	return out
}

func (f *fakeMetrics) snapshotHist(name string) []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.hist[name]...)
}

func (f *fakeMetrics) gauge(name string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gauges[name]
}

func cloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Tests cover LLD §12 — the canonical metric names emitted by the
// lifecycle module — and verify each documented metric fires at least
// once on the documented call path.

func TestMetrics_StartupCompleteGaugeFlips(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	mod, err := newModuleForTest(LifecycleConfig{
		ShutdownGracePeriod: 100 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	}, LifecycleContext{Metrics: fm})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	if got := fm.gauge(MetricStartupComplete); got != 0 {
		t.Fatalf("startup_complete gauge before MarkStartupComplete: got %v want 0", got)
	}
	mod.MarkStartupComplete()
	if got := fm.gauge(MetricStartupComplete); got != 1 {
		t.Fatalf("startup_complete gauge after MarkStartupComplete: got %v want 1", got)
	}
}

func TestMetrics_ReadinessFailureCounter(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	r := newRegistry()
	must(t, r.registerReadiness("postgres", func(ctx context.Context) error {
		return errors.New("DB down")
	}))
	must(t, r.registerReadiness("ok", okCheck))

	h := newReadinessHandler(r, fm, time.Second)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	got := fm.snapshotCounters(MetricReadinessCheckFailuresTotal)
	if len(got) != 1 {
		t.Fatalf("readiness_check_failures_total emits: got %d want 1", len(got))
	}
	if got[0]["name"] != "postgres" {
		t.Fatalf("failure label name: got %q want \"postgres\"", got[0]["name"])
	}
}

func TestMetrics_ProbeRequestsCounter(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	r := newRegistry()
	hLive := newLivenessHandler(r, fm)
	hReady := newReadinessHandler(r, fm, time.Second)

	hLive.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	hReady.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))

	got := fm.snapshotCounters(MetricProbeRequestsTotal)
	if len(got) != 2 {
		t.Fatalf("probe_requests_total emits: got %d want 2", len(got))
	}
	probes := map[string]string{}
	for _, l := range got {
		probes[l["probe"]] = l["status_code"]
	}
	if probes["healthz"] != "200" {
		t.Fatalf("healthz status_code label: got %q want 200", probes["healthz"])
	}
	if probes["readyz"] != "200" {
		t.Fatalf("readyz status_code label: got %q want 200", probes["readyz"])
	}
}

func TestMetrics_ShutdownInitiatedAndPhaseDuration(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	mod, err := newModuleForTest(LifecycleConfig{
		ShutdownGracePeriod: 100 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	}, LifecycleContext{Metrics: fm})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	mod.RegisterShutdown(ShutdownHook{
		Name:  "drain",
		Phase: PhaseDrainInFlight,
		Run:   func(ctx context.Context) error { return nil },
	})
	mod.RequestShutdown(context.Background(), "test")
	mod.WaitForExit(context.Background())

	initiated := fm.snapshotCounters(MetricShutdownInitiatedTotal)
	if len(initiated) != 1 || initiated[0]["reason"] != "test" {
		t.Fatalf("shutdown_initiated_total: got %v", initiated)
	}

	durations := fm.snapshotHist(MetricPhaseDurationSeconds)
	if len(durations) < 4 {
		t.Fatalf("phase_duration_seconds emits: got %d want >=4 (one per executed phase)", len(durations))
	}
}

func TestMetrics_ShutdownForcedTotalOnHang(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	mod, err := newModuleForTest(LifecycleConfig{
		ShutdownGracePeriod: 50 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	}, LifecycleContext{Metrics: fm})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	mod.RegisterShutdown(ShutdownHook{
		Name:  "hanger",
		Phase: PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	mod.RequestShutdown(context.Background(), "test")
	mod.WaitForExit(context.Background())

	if got := fm.snapshotCounters(MetricShutdownForcedTotal); len(got) != 1 {
		t.Fatalf("shutdown_forced_total: got %d emits, want 1", len(got))
	}
}

func TestMetrics_HookOutcomeCounter(t *testing.T) {
	t.Parallel()
	fm := newFakeMetrics()
	mod, err := newModuleForTest(LifecycleConfig{
		ShutdownGracePeriod: 100 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	}, LifecycleContext{Metrics: fm})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	mod.RegisterShutdown(ShutdownHook{
		Name:  "good",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return nil },
	})
	mod.RegisterShutdown(ShutdownHook{
		Name:  "bad",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return errors.New("fail") },
	})
	mod.RequestShutdown(context.Background(), "test")
	mod.WaitForExit(context.Background())

	got := fm.snapshotCounters(MetricShutdownHookOutcomeTotal)
	outcomes := map[string]string{}
	for _, l := range got {
		outcomes[l["hook"]] = l["outcome"]
	}
	if outcomes["good"] != "drained" {
		t.Fatalf("good hook outcome: got %q want \"drained\"", outcomes["good"])
	}
	if outcomes["bad"] != "errored" {
		t.Fatalf("bad hook outcome: got %q want \"errored\"", outcomes["bad"])
	}
}
