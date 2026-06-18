// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newReg() *lifecycleRegistry { return newLifecycleRegistry() }

func TestRegistry_StateTransitions(t *testing.T) {
	t.Parallel()
	reg := newReg()

	if reg.shutdownInProgress() {
		t.Fatal("shutdown should default false")
	}
	if reg.startupComplete() {
		t.Fatal("startup_complete should default false")
	}
	reg.markStartupComplete()
	if !reg.startupComplete() {
		t.Fatal("startup_complete not set")
	}
	reg.markShutdownInProgress()
	if !reg.shutdownInProgress() {
		t.Fatal("shutdown_in_progress not set")
	}
	// Idempotent.
	reg.markShutdownInProgress()
	if !reg.shutdownInProgress() {
		t.Fatal("idempotent set should leave true")
	}
}

// TestMetadata_StubOperationOutcome asserts the /metadata stub still
// returns FHIR-shaped JSON. The real CapabilityStatement lands when
// api/http-fhir's full handler ships (audit S-1 / future-work P1.7).
func TestMetadata_StubOperationOutcome(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/metadata", makeMetadata())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body struct {
		ResourceType string `json:"resourceType"`
		Issue        []struct {
			Severity    string `json:"severity"`
			Code        string `json:"code"`
			Diagnostics string `json:"diagnostics"`
		} `json:"issue"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ResourceType != "OperationOutcome" {
		t.Fatalf("resourceType: %q", body.ResourceType)
	}
	if len(body.Issue) == 0 {
		t.Fatal("expected at least one issue")
	}
	if !strings.Contains(strings.ToLower(body.Issue[0].Diagnostics), "starting") {
		t.Fatalf("diagnostics should mention starting: %q", body.Issue[0].Diagnostics)
	}
}
