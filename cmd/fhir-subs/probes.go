// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// lifecycleRegistry is a tiny in-process flag pair the test runHooks
// surface still references. The production probe surface lives in
// internal/infra/lifecycle.LifecycleModule, which is what run.go wires
// in. The lifecycleRegistry shim exists only to keep the
// runHooks.onShutdownStart parameter type stable for tests that observe
// the shutdown transition through that hook.
type lifecycleRegistry struct {
	startup  atomic.Bool
	shutdown atomic.Bool
}

func newLifecycleRegistry() *lifecycleRegistry { return &lifecycleRegistry{} }

func (r *lifecycleRegistry) markStartupComplete()     { r.startup.Store(true) }
func (r *lifecycleRegistry) startupComplete() bool    { return r.startup.Load() }
func (r *lifecycleRegistry) markShutdownInProgress()  { r.shutdown.Store(true) }
func (r *lifecycleRegistry) shutdownInProgress() bool { return r.shutdown.Load() }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Errors here mean the client hung up; nothing to do but ignore.
	_ = json.NewEncoder(w).Encode(body)
}

// makeMetadata is the bare-server fallback for `GET /metadata` used
// only in probe-only mode (no DB configured). The real
// CapabilityStatement is built by internal/api/handlers
// (server.buildCapabilityStatement) and mounted by
// handlers.RegisterRoutes / RegisterPublicRoutes once the DB pool, auth
// verifier, channel registry, and topic catalog are wired (P1.7).
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
