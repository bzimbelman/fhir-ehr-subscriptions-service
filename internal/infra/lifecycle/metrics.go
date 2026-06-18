// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

// MetricsEmitter is the metrics seam between the lifecycle module and the
// host's observability stack. The module emits typed events; an adapter at
// the host translates them to Prometheus, OTLP, or whatever the deployment
// wires up. Tests use a fake.
//
// All counter / histogram / gauge names emitted from this package carry
// the canonical fhir_subs_lifecycle_ prefix already (LLD §12). Emitters
// render them on the wire as-is — no host-side rewrap.
type MetricsEmitter interface {
	// Inc bumps a counter by 1 with the given labels.
	Inc(name string, labels map[string]string)
	// Add bumps a counter by delta with the given labels.
	Add(name string, delta float64, labels map[string]string)
	// Observe records a histogram observation with the given labels.
	Observe(name string, value float64, labels map[string]string)
	// Set sets a gauge value with the given labels.
	Set(name string, value float64, labels map[string]string)
}

// Metric names emitted by the lifecycle module. These are the canonical
// wire names (LLD §12); emitters render them as-is.
const (
	// MetricPhaseDurationSeconds is a histogram of wall-clock time per
	// shutdown phase. Label: phase.
	MetricPhaseDurationSeconds = "fhir_subs_lifecycle_phase_duration_seconds"

	// MetricShutdownForcedTotal is a counter of shutdowns that hit the
	// grace-period wall-clock deadline.
	MetricShutdownForcedTotal = "fhir_subs_lifecycle_shutdown_forced_total"

	// MetricReadinessCheckFailuresTotal is a counter of readiness
	// failures by registered check name. Label: name.
	MetricReadinessCheckFailuresTotal = "fhir_subs_lifecycle_readiness_check_failures_total"

	// MetricStartupComplete is a gauge: 1 once MarkStartupComplete has
	// been called, 0 before.
	MetricStartupComplete = "fhir_subs_lifecycle_startup_complete"

	// MetricProbeRequestsTotal is a counter of probe requests by probe
	// name and resulting status code. Labels: probe, status_code.
	MetricProbeRequestsTotal = "fhir_subs_lifecycle_probe_requests_total"

	// MetricShutdownInitiatedTotal is a counter incremented once per
	// shutdown. Label: reason.
	MetricShutdownInitiatedTotal = "fhir_subs_lifecycle_shutdown_initiated_total"

	// MetricShutdownHookOutcomeTotal is a counter of per-hook outcomes.
	// Labels: hook, outcome (drained|timed_out|errored).
	MetricShutdownHookOutcomeTotal = "fhir_subs_lifecycle_shutdown_hook_outcome_total"
)

// nopMetrics is a no-op MetricsEmitter used when the host supplies nil.
type nopMetrics struct{}

func (nopMetrics) Inc(string, map[string]string)              {}
func (nopMetrics) Add(string, float64, map[string]string)     {}
func (nopMetrics) Observe(string, float64, map[string]string) {}
func (nopMetrics) Set(string, float64, map[string]string)     {}
