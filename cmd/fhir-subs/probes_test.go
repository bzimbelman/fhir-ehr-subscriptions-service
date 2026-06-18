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

func TestHealthz_OK(t *testing.T) {
	t.Parallel()
	reg := newReg()
	srv := httptest.NewServer(probeMux(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field: %v", body["status"])
	}
}

func TestHealthz_ShuttingDown(t *testing.T) {
	t.Parallel()
	reg := newReg()
	reg.markShutdownInProgress()

	srv := httptest.NewServer(probeMux(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestReadyz_NoComponentsWired(t *testing.T) {
	t.Parallel()
	reg := newReg()

	srv := httptest.NewServer(probeMux(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var body struct {
		Status string   `json:"status"`
		Failed []string `json:"failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "unready" {
		t.Fatalf("status: %q", body.Status)
	}
	if len(body.Failed) != 1 || body.Failed[0] != "all_components" {
		t.Fatalf("failed: %v", body.Failed)
	}
}

func TestReadyz_ShuttingDown(t *testing.T) {
	t.Parallel()
	reg := newReg()
	reg.markShutdownInProgress()

	srv := httptest.NewServer(probeMux(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body struct {
		Status string   `json:"status"`
		Failed []string `json:"failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "unready" {
		t.Fatalf("status: %q", body.Status)
	}
	if len(body.Failed) != 1 || body.Failed[0] != "shutting_down" {
		t.Fatalf("failed: %v", body.Failed)
	}
}

func TestStartup_Until_Complete(t *testing.T) {
	t.Parallel()
	reg := newReg()

	srv := httptest.NewServer(probeMux(reg))
	t.Cleanup(srv.Close)

	// Before startup_complete: 503.
	{
		resp, err := http.Get(srv.URL + "/startup")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status before: %d", resp.StatusCode)
		}
	}

	// After startup_complete: still 503 because no components wired (matches /readyz).
	reg.markStartupComplete()
	{
		resp, err := http.Get(srv.URL + "/startup")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })

		// /readyz returns 503 because no components are wired (failed=[all_components]).
		// /startup mirrors /readyz once startup_complete is set.
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status after startup_complete: %d", resp.StatusCode)
		}
		var body struct {
			Status string   `json:"status"`
			Failed []string `json:"failed"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Status != "unready" {
			t.Fatalf("status: %q", body.Status)
		}
	}
}

func TestMetadata_StubOperationOutcome(t *testing.T) {
	t.Parallel()
	reg := newReg()

	srv := httptest.NewServer(probeMux(reg))
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
