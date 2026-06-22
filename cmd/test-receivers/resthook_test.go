// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Phase A (RED) tests for OpenProject story #294 — H10b Programmable
// rest-hook receiver in realstack.
//
// These tests pin the public contract of the control plane the test
// rest-hook subscriber binary must expose so legacy-harness tests
// (backpressure, failure-modes, hardening, retry-curve, dns-rebinding)
// can drive per-tag response programs through a real container instead
// of an in-process http.Handler.
//
// Tests run against the binary's mux through buildMux(...) — a
// package-level constructor Phase B introduces. Until Phase B lands the
// symbol is undefined and these tests fail to compile, which is the
// canonical "RED" signal for this codebase.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProgrammable_StatusCodeSequence pins the 503-then-200 retry-curve
// scenario backpressure_test.go relied on in the legacy harness.
// Installing { "sequence": [{"status":503},{"status":200}] } on tag "X"
// must cause the first POST to /notify/X to return 503 and the second
// to return 200; subsequent requests fall back to the default 200.
func TestProgrammable_StatusCodeSequence(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":503},{"status":200}]}`)
	if resp, err := http.Post(srv.URL+"/program/sub-1", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install program: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			t.Fatalf("install program: status %d", resp.StatusCode)
		}
	}

	want := []int{503, 200, 200}
	for i, w := range want {
		resp, err := http.Post(srv.URL+"/notify/sub-1", "application/fhir+json", strings.NewReader(`{"resourceType":"Bundle"}`))
		if err != nil {
			t.Fatalf("delivery #%d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != w {
			t.Errorf("delivery #%d: status %d; want %d", i, resp.StatusCode, w)
		}
	}
}

// TestProgrammable_HeaderInjection pins the Retry-After header path —
// retry_curve_e2e_test.go relies on the receiver echoing back a
// Retry-After header so the channel's retry curve uses the server-
// supplied delay instead of the default exponential backoff.
func TestProgrammable_HeaderInjection(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":429,"headers":{"Retry-After":"7"}}]}`)
	if resp, err := http.Post(srv.URL+"/program/sub-2", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install program: %v", err)
	} else {
		resp.Body.Close()
	}

	resp, err := http.Post(srv.URL+"/notify/sub-2", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status %d; want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After=%q; want 7", got)
	}
}

// TestProgrammable_LatencyInjection pins the slow-response scenario
// channels_resthook_failure_modes_test.go relied on (slowloris
// upstream). The receiver must hold the response for at least the
// programmed latency before replying.
func TestProgrammable_LatencyInjection(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":200,"latency_ms":150}]}`)
	if resp, err := http.Post(srv.URL+"/program/sub-3", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install program: %v", err)
	} else {
		resp.Body.Close()
	}

	start := time.Now()
	resp, err := http.Post(srv.URL+"/notify/sub-3", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	resp.Body.Close()
	elapsed := time.Since(start)
	if elapsed < 140*time.Millisecond {
		t.Errorf("delivery elapsed %v; want >= ~150ms", elapsed)
	}
}

// TestProgrammable_PerTagSelectivity pins that programs are scoped by
// tag — installing a 503-program on sub-A must not affect deliveries
// to sub-B (which fall through to the default 200).
func TestProgrammable_PerTagSelectivity(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":503}],"default_status":503}`)
	if resp, err := http.Post(srv.URL+"/program/sub-A", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install program: %v", err)
	} else {
		resp.Body.Close()
	}

	respA, err := http.Post(srv.URL+"/notify/sub-A", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("deliver A: %v", err)
	}
	respA.Body.Close()
	if respA.StatusCode != 503 {
		t.Errorf("sub-A: status %d; want 503", respA.StatusCode)
	}

	respB, err := http.Post(srv.URL+"/notify/sub-B", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("deliver B: %v", err)
	}
	respB.Body.Close()
	if respB.StatusCode != http.StatusOK {
		t.Errorf("sub-B (no program): status %d; want 200", respB.StatusCode)
	}
}

// TestProgrammable_CloseAfterBytes pins the mid-body-close scenario
// channels_resthook_failure_modes_test.go relied on. With
// close_after_bytes=N, the receiver writes N bytes then aborts the
// connection; the channel sees a partial response and must treat it as
// a delivery failure.
func TestProgrammable_CloseAfterBytes(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":200,"body":"hello world","close_after_bytes":5}]}`)
	if resp, err := http.Post(srv.URL+"/program/sub-4", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install program: %v", err)
	} else {
		resp.Body.Close()
	}

	resp, err := http.Post(srv.URL+"/notify/sub-4", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		// A connection abort here is acceptable — the contract is that
		// the client does not see the full programmed body.
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) >= len("hello world") {
		t.Errorf("read %d bytes (%q); close_after_bytes=5 must truncate", len(body), string(body))
	}
}

// TestProgrammable_DefaultBehaviorPreserved pins backwards compatibility:
// when no program is installed, deliveries must still 200 and the
// journal must capture them — exactly the contract realstack_red_test.go
// already pins.
func TestProgrammable_DefaultBehaviorPreserved(t *testing.T) {
	jr := newJournal()
	srv := httptest.NewServer(buildMux(jr))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/notify/legacy", "application/fhir+json", strings.NewReader(`{"resourceType":"Bundle"}`))
	if err != nil {
		t.Fatalf("delivery: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("default status %d; want 200", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/notifications/legacy")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer getResp.Body.Close()
	var captured []ReceivedRequest
	if err := json.NewDecoder(getResp.Body).Decode(&captured); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(captured) != 1 {
		t.Errorf("captured %d requests; want 1", len(captured))
	}
}

// TestProgrammable_ProgramClear pins DELETE /program/{tag} clearing the
// installed program — tests that share a stack across cases must be
// able to reset per-tag state without restarting the container.
func TestProgrammable_ProgramClear(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	prog := []byte(`{"sequence":[{"status":503}],"default_status":503}`)
	if resp, err := http.Post(srv.URL+"/program/sub-5", "application/json", bytes.NewReader(prog)); err != nil {
		t.Fatalf("install: %v", err)
	} else {
		resp.Body.Close()
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/program/sub-5", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatalf("delete program: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			t.Errorf("delete program: status %d", resp.StatusCode)
		}
	}

	resp, err := http.Post(srv.URL+"/notify/sub-5", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("delivery after clear: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("after clear: status %d; want default 200", resp.StatusCode)
	}
}

// TestProgrammable_BadDSL pins input validation: garbage program JSON
// must yield a 4xx and not affect future deliveries on the tag.
func TestProgrammable_BadDSL(t *testing.T) {
	srv := httptest.NewServer(buildMux(newJournal()))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/program/sub-6", "application/json", strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Errorf("bad DSL: status %d; want 4xx", resp.StatusCode)
	}

	delivery, err := http.Post(srv.URL+"/notify/sub-6", "application/fhir+json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("delivery after bad install: %v", err)
	}
	delivery.Body.Close()
	if delivery.StatusCode != http.StatusOK {
		t.Errorf("delivery after rejected program: status %d; want 200", delivery.StatusCode)
	}
}
