// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// lifecycleRegistry is the minimal slice of the infra/lifecycle registry the
// entry point needs today: two atomic flags driving probe responses. The full
// registry described in docs/low-level-design/lifecycle.md (ReadinessCheck,
// ShutdownHook, phase-bucketed hooks) lands when infra/lifecycle ships.
type lifecycleRegistry struct {
	startup  atomic.Bool
	shutdown atomic.Bool
}

func newLifecycleRegistry() *lifecycleRegistry { return &lifecycleRegistry{} }

func (r *lifecycleRegistry) markStartupComplete()     { r.startup.Store(true) }
func (r *lifecycleRegistry) startupComplete() bool    { return r.startup.Load() }
func (r *lifecycleRegistry) markShutdownInProgress()  { r.shutdown.Store(true) }
func (r *lifecycleRegistry) shutdownInProgress() bool { return r.shutdown.Load() }

// probeMux returns an http.Handler hosting /healthz, /readyz, /startup, and
// the /metadata stub. The mux is intentionally local so the entry point can
// hand a single handler to http.Server. The infra/lifecycle module will
// replace this with the full probe server (its own bind, dedicated timeouts,
// CapabilityStatement-aware metadata).
func probeMux(reg *lifecycleRegistry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthz(reg))
	mux.HandleFunc("/readyz", makeReadyz(reg))
	mux.HandleFunc("/startup", makeStartup(reg))
	mux.HandleFunc("/metadata", makeMetadata())
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Errors here mean the client hung up; nothing to do but ignore.
	_ = json.NewEncoder(w).Encode(body)
}

func makeHealthz(reg *lifecycleRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if reg.shutdownInProgress() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "shutting_down"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func makeReadyz(reg *lifecycleRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if reg.shutdownInProgress() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "unready",
				"failed": []string{"shutting_down"},
			})
			return
		}
		// No components wired yet — every readiness check fails by definition.
		// The infra/lifecycle module will replace this with the registry walk.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unready",
			"failed": []string{"all_components"},
		})
	}
}

func makeStartup(reg *lifecycleRegistry) http.HandlerFunc {
	readyz := makeReadyz(reg)
	return func(w http.ResponseWriter, r *http.Request) {
		if !reg.startupComplete() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "starting",
				"phase":  "boot",
			})
			return
		}
		readyz(w, r)
	}
}

// makeMetadata returns a stub OperationOutcome saying the service is starting.
// The real CapabilityStatement is owned by api/http-fhir; this stub satisfies
// the e2e harness's "GET /metadata returns FHIR-shaped JSON" assertion until
// that module ships.
func makeMetadata() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"resourceType": "OperationOutcome",
			"issue": []map[string]any{
				{
					"severity":    "information",
					"code":        "informational",
					"diagnostics": "fhir-subs service is starting; CapabilityStatement not yet available.",
				},
			},
		})
	}
}
