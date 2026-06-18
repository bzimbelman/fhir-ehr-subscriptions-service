// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

// readinessHandler implements `/readyz`.
//
// LLD §5.2 — the handler walks every registered ReadinessCheck
// concurrently with each check's own timeout, collects results, and
// returns 200 when every check passed and the service is not shutting
// down; 503 with a sorted failed[] list otherwise. The aggregator does
// NOT retry — that decision belongs to the orchestrator.
type readinessHandler struct {
	reg             *registry
	metrics         MetricsEmitter
	perCheckTimeout time.Duration
}

// newReadinessHandler constructs the handler. perCheckTimeout is the
// per-evaluation budget the aggregator imposes on every check; LLD §5.2
// recommends 2s.
func newReadinessHandler(reg *registry, metrics MetricsEmitter, perCheckTimeout time.Duration) *readinessHandler {
	if metrics == nil {
		metrics = nopMetrics{}
	}
	if perCheckTimeout <= 0 {
		perCheckTimeout = 2 * time.Second
	}
	return &readinessHandler{
		reg:             reg,
		metrics:         metrics,
		perCheckTimeout: perCheckTimeout,
	}
}

func (h *readinessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Shutdown short-circuits without invoking any check (LLD §5.2).
	if h.reg.shutdownInProgress() {
		writeJSONStatus(w, http.StatusServiceUnavailable, "unready", []string{"shutting_down"})
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "readyz",
			"status_code": "503",
		})
		return
	}

	entries := h.reg.snapshotReadiness()
	results := runChecksConcurrently(r.Context(), entries, h.perCheckTimeout.Nanoseconds())

	failed := failedNames(results)
	for _, cr := range results {
		if cr.failed {
			h.metrics.Inc(MetricReadinessCheckFailuresTotal, map[string]string{
				"name": cr.name,
			})
		}
	}

	if len(failed) == 0 {
		writeJSONStatus(w, http.StatusOK, "ready", nil)
		h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
			"probe":       "readyz",
			"status_code": strconv.Itoa(http.StatusOK),
		})
		return
	}

	writeJSONStatus(w, http.StatusServiceUnavailable, "unready", failed)
	h.metrics.Inc(MetricProbeRequestsTotal, map[string]string{
		"probe":       "readyz",
		"status_code": strconv.Itoa(http.StatusServiceUnavailable),
	})
}

// failedNames returns the sorted names of failed checks. Sorting keeps
// `/readyz`'s failed[] list deterministic; tests assert on content.
func failedNames(results []checkResult) []string {
	out := make([]string, 0)
	for _, cr := range results {
		if cr.failed {
			out = append(out, cr.name)
		}
	}
	sort.Strings(out)
	return out
}
