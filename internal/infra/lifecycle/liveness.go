// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
)

// livenessHandler implements `/healthz`.
//
// LLD §5.1 — the handler MUST be a pure read of two in-memory flags. It
// does not touch Postgres, does not run any registered ReadinessCheck,
// does not perform any I/O. The two failure modes are:
//
//   - shutdown_in_progress  → 503 {"status":"shutting_down"}
//   - panic_signaled        → 503 {"status":"panic"}
//
// Anything else returns 200 {"status":"ok"}. A brief DB outage must NOT
// flip liveness — restarting the container does nothing to bring Postgres
// back, and the orchestrator handles that case via readiness instead.
type livenessHandler struct {
	reg     *registry
	metrics MetricsEmitter
}

// newLivenessHandler builds the handler. metrics may be nil; it is
// replaced with a no-op emitter for nil-safety on the hot path.
func newLivenessHandler(reg *registry, metrics MetricsEmitter) *livenessHandler {
	if metrics == nil {
		metrics = nopMetrics{}
	}
	return &livenessHandler{reg: reg, metrics: metrics}
}

func (h *livenessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.reg.shutdownInProgress():
		writeJSONStatus(w, http.StatusServiceUnavailable, "shutting_down", nil)
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "healthz",
			"status_code": "503",
		})
	case h.reg.panicSignaled():
		writeJSONStatus(w, http.StatusServiceUnavailable, "panic", nil)
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "healthz",
			"status_code": "503",
		})
	default:
		writeJSONStatus(w, http.StatusOK, "ok", nil)
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "healthz",
			"status_code": "200",
		})
	}
}
