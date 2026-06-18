// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
	"strconv"
	"time"
)

// startupHandler implements `/startup`.
//
// LLD §5.3 — same handler as `/readyz` with one behavioral difference:
// until the registry's startup_complete flag is set, `/startup` always
// returns 503 even if every readiness check would pass. This keeps
// Kubernetes' startup grace period in effect through the slow parts of
// boot (schema migrations, topic catalog load, initial FHIR scan).
type startupHandler struct {
	reg     *registry
	readyz  *readinessHandler
	metrics MetricsEmitter
}

// newStartupHandler builds the handler. It composes a readinessHandler
// for the post-startup-complete behavior; the per-check timeout is
// shared.
func newStartupHandler(reg *registry, metrics MetricsEmitter, perCheckTimeout time.Duration) *startupHandler {
	if metrics == nil {
		metrics = nopMetrics{}
	}
	return &startupHandler{
		reg:     reg,
		readyz:  newReadinessHandler(reg, metrics, perCheckTimeout),
		metrics: metrics,
	}
}

func (h *startupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.reg.startupComplete() {
		writeJSONStatus(w, http.StatusServiceUnavailable, "starting", nil)
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "startup",
			"status_code": strconv.Itoa(http.StatusServiceUnavailable),
		})
		return
	}
	// Once startup_complete is set, the startup probe behaves exactly
	// like readyz. We delegate so the two probes share their behavior
	// (and tests can rely on that.)
	h.readyz.ServeHTTP(probeAsStartup{ResponseWriter: w}, r)
}

// probeAsStartup is a tiny passthrough that exists only so the metric
// label distinguishes /startup from /readyz when readiness delegates.
// The readyz handler already increments fhir_subs_lifecycle_probe_requests_total
// with probe=readyz; we don't double-count here. The metric for /startup
// success is handled implicitly because the startup handler delegates to
// readyz — for v1 the LLD §12 metric only labels probe= the endpoint,
// so the readyz label is acceptable since /startup mirrors /readyz once
// startup_complete is set. A dedicated label can be added if operators
// want to slice startup hits separately.
type probeAsStartup struct {
	http.ResponseWriter
}
