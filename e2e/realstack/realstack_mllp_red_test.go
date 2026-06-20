// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// Phase A (RED) tests for OpenProject story #293 — H10a Scripted MLLP
// control plane in realstack.
//
// The legacy mockehr.ControlPlane is an in-process Go HTTP handler that
// the legacy e2e/orchestrator suite drove via httptest. It synthesizes
// HL7 v2 frames and emits them over MLLP to the prod binary's listener.
// realstack's no-Go-fakes rule (see feedback_no_fakes_or_mocks.md) bans
// in-process control surfaces; the replacement must be a real-software
// service running inside the docker-compose stack and reachable via
// real network sockets.
//
// These tests pin the public contract of `realstack.MLLPControlPlane`
// and the wiring between Boot(EnableMLLP=true), the prod binary's MLLP
// listener, and the in-repo control plane binary. They fail today
// because:
//
//  1. Stack has no MLLPControlPlane field.
//  2. The docker-compose stack has no test-mllp-control-plane service.
//  3. The cmd/test-mllp-control-plane binary does not exist.
//
// Phase B implements all three. Phase C audits the package for fakes.
//
// Run:
//
//	go test -tags e2e_realstack -count=1 ./e2e/realstack/...
package realstack_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// mllpBootTimeout is the wall-clock budget for a single MLLP-enabled
// Boot. Same magnitude as the H1 boot timeout: the control plane is one
// extra container.
const mllpBootTimeout = 3 * time.Minute

// TestRealStack_MLLPControlPlane_HandleNonEmpty asserts the MLLP control
// plane handle that Boot exposes is fully populated when
// EnableMLLP=true. Tests upstream of this story consume URL/HTTPAddr to
// drive scenarios; an empty handle means the wiring story is unfinished.
func TestRealStack_MLLPControlPlane_HandleNonEmpty(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	if stack.MLLPControlPlane.URL == "" {
		t.Fatalf("Stack.MLLPControlPlane.URL is empty; harness MUST surface a real URL when EnableMLLP=true")
	}
	if stack.MLLPControlPlane.HTTPAddr == "" {
		t.Fatalf("Stack.MLLPControlPlane.HTTPAddr is empty; harness MUST surface a host:port for direct dial")
	}
	if !strings.HasPrefix(stack.MLLPControlPlane.URL, "http://") {
		t.Fatalf("Stack.MLLPControlPlane.URL=%q is not an http URL", stack.MLLPControlPlane.URL)
	}
	if stack.Binary.MLLPAddr == "" {
		t.Fatalf("Stack.Binary.MLLPAddr is empty; EnableMLLP=true MUST open the prod binary's listener")
	}
}

// TestRealStack_MLLPControlPlane_HealthzReachable asserts the control
// plane container exposes /healthz returning 200 — proof the in-repo
// binary is running, not a stub.
func TestRealStack_MLLPControlPlane_HealthzReachable(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	resp, err := http.Get(stack.MLLPControlPlane.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET %s/healthz: %v", stack.MLLPControlPlane.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz returned %d; want 200", resp.StatusCode)
	}
}

// TestRealStack_MLLPControlPlane_AdmitPatient_EmitsADTToProdBinary
// drives the canonical scenario the legacy harness tests rely on:
// POST /scenarios/admit_patient triggers a real MLLP frame on a real
// TCP socket from the control plane container to the prod binary's
// listener; the prod binary parses the frame and ACKs.
//
// Acceptance: 202 Accepted with an MSA|AA ACK body, asserting the round
// trip touched a real wire.
func TestRealStack_MLLPControlPlane_AdmitPatient_EmitsADTToProdBinary(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	body := map[string]string{
		"patient_id":  "MRN-CP-001",
		"message_id":  "CP-ADMIT-1",
		"trigger":     "A01",
		"family_name": "Smith",
		"given_name":  "Pat",
	}
	resp := postScenario(t, stack.MLLPControlPlane.URL+"/scenarios/admit_patient", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("admit_patient: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ackBody struct {
		ACK string `json:"ack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ackBody); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !strings.Contains(ackBody.ACK, "MSA|AA|CP-ADMIT-1") {
		t.Fatalf("admit_patient: ack does not contain MSA|AA|<msgid>: %q", ackBody.ACK)
	}
	// MSH echo from the binary's ACK must reference the inbound control id.
	if !strings.Contains(ackBody.ACK, "CP-ADMIT-1") {
		t.Fatalf("admit_patient: ack does not echo control id CP-ADMIT-1: %q", ackBody.ACK)
	}
}

// TestRealStack_MLLPControlPlane_PlaceOrder_EmitsORM exercises the
// place_order scenario. ORC-1=NW (new) is the canonical happy path the
// pipeline_correctness fanout tests rely on.
func TestRealStack_MLLPControlPlane_PlaceOrder_EmitsORM(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	body := map[string]string{
		"placer_order_id":  "P-1001",
		"filler_order_id":  "F-1001",
		"patient_id":       "MRN-CP-002",
		"message_id":       "CP-ORM-1",
		"universal_svc_id": "CBC^Complete Blood Count^L",
	}
	resp := postScenario(t, stack.MLLPControlPlane.URL+"/scenarios/place_order", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("place_order: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var ackBody struct {
		ACK string `json:"ack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ackBody); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if !strings.Contains(ackBody.ACK, "MSA|AA|CP-ORM-1") {
		t.Fatalf("place_order: ack does not contain MSA|AA|<msgid>: %q", ackBody.ACK)
	}
}

// TestRealStack_MLLPControlPlane_FinalizeLab_EmitsORU exercises the lab
// result path that email_v1_smtp_test.go and FHIR-mapping tests drive.
func TestRealStack_MLLPControlPlane_FinalizeLab_EmitsORU(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	body := map[string]string{
		"message_id":      "CP-ORU-1",
		"patient_id":      "MRN-CP-003",
		"observation_id":  "GLU^Glucose^L",
		"value":           "98",
		"unit":            "mg/dL",
		"reference_range": "70-110",
		"abnormal_flag":   "N",
	}
	resp := postScenario(t, stack.MLLPControlPlane.URL+"/scenarios/finalize_lab", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("finalize_lab: status=%d body=%s", resp.StatusCode, string(raw))
	}
}

// TestRealStack_MLLPControlPlane_CancelAndReplace_EmitsTwoFrames asserts
// the two-frame compound scenario emits a CA followed by an NW with the
// same placer/filler IDs. The handler returns {"emitted":2}; this is
// the contract cancel_and_replace_hl7_test.go relies on.
func TestRealStack_MLLPControlPlane_CancelAndReplace_EmitsTwoFrames(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	body := map[string]string{
		"placer_order_id":        "P-2001",
		"filler_order_id":        "F-2001",
		"patient_id":             "MRN-CP-004",
		"cancel_message_id":      "CP-CA-1",
		"replacement_message_id": "CP-RP-1",
	}
	resp := postScenario(t, stack.MLLPControlPlane.URL+"/scenarios/cancel_and_replace_order", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("cancel_and_replace_order: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out struct {
		Emitted int `json:"emitted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Emitted != 2 {
		t.Fatalf("cancel_and_replace_order: emitted=%d, want 2", out.Emitted)
	}
}

// TestRealStack_MLLPControlPlane_BurstMessages_EmitsN exercises the
// burst path the backpressure / fanout-50 tests use to produce many
// frames in one HTTP call.
func TestRealStack_MLLPControlPlane_BurstMessages_EmitsN(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	const N = 5
	body := map[string]any{
		"count":      N,
		"trigger":    "A01",
		"patient_id": "MRN-CP-005",
	}
	resp := postScenario(t, stack.MLLPControlPlane.URL+"/scenarios/burst_messages", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("burst_messages: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out struct {
		Emitted int `json:"emitted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Emitted != N {
		t.Fatalf("burst_messages: emitted=%d, want %d", out.Emitted, N)
	}
}

// TestRealStack_MLLPControlPlane_BadInputReturns400 pins the input
// validation contract: a missing JSON body or empty content gets a 4xx,
// not a 5xx, and the prod binary is not bothered.
func TestRealStack_MLLPControlPlane_BadInputReturns400(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	resp, err := http.Post(stack.MLLPControlPlane.URL+"/scenarios/admit_patient",
		"application/json",
		strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("admit_patient with bad body: status=%d, want 4xx", resp.StatusCode)
	}
}

// TestRealStack_MLLPControlPlane_AllScenariosRouted asserts every
// canonical scenario name is wired (returns 200/202 on a HEAD-style
// probe — POST without body returns 400 for valid routes, 404 for
// unknown routes; the test relies on that distinction).
func TestRealStack_MLLPControlPlane_AllScenariosRouted(t *testing.T) {
	requireDockerForMLLP(t)

	ctx, cancel := context.WithTimeout(context.Background(), mllpBootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{EnableMLLP: true})
	t.Cleanup(stack.Close)

	scenarios := []string{
		"admit_patient",
		"place_order",
		"finalize_lab",
		"cancel_and_replace_order",
		"burst_messages",
	}
	for _, s := range scenarios {
		s := s
		t.Run(s, func(t *testing.T) {
			resp, err := http.Post(stack.MLLPControlPlane.URL+"/scenarios/"+s,
				"application/json",
				strings.NewReader(""))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				t.Fatalf("scenario %q not routed: 404", s)
			}
		})
	}
}

// TestRealStack_MLLPControlPlane_BinaryDirRendered asserts the in-repo
// cmd/test-mllp-control-plane source exists and follows the same shape
// as cmd/test-resthook-subscriber: a main.go and a Dockerfile in
// e2e/realstack/fixtures/subscribers/. The test works from source on
// disk so it runs without docker.
func TestRealStack_MLLPControlPlane_BinaryDirRendered(t *testing.T) {
	repoRoot := findRepoRootForMLLPTest(t)

	mainGo := filepath.Join(repoRoot, "cmd", "test-mllp-control-plane", "main.go")
	if _, err := os.Stat(mainGo); err != nil {
		t.Fatalf("cmd/test-mllp-control-plane/main.go missing: %v — harness MUST ship a real in-repo binary", err)
	}

	dockerfile := filepath.Join(repoRoot, "e2e", "realstack", "fixtures", "subscribers", "Dockerfile.mllp_controlplane")
	if _, err := os.Stat(dockerfile); err != nil {
		t.Fatalf("Dockerfile.mllp_controlplane missing: %v — harness MUST build the control plane from a real image", err)
	}
}

// TestRealStack_MLLPControlPlane_ComposeServiceWired asserts the
// docker-compose.yml file declares the test-mllp-control-plane service
// alongside the rest-hook and ws subscriber services. We grep the file
// directly so the assertion runs with or without docker.
func TestRealStack_MLLPControlPlane_ComposeServiceWired(t *testing.T) {
	repoRoot := findRepoRootForMLLPTest(t)
	composeFile := filepath.Join(repoRoot, "e2e", "realstack", "docker-compose.yml")
	body, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"test-mllp-control-plane",
		"Dockerfile.mllp_controlplane",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("docker-compose.yml does not reference %q — control plane MUST be a real container", want)
		}
	}
}

// requireDockerForMLLP is a per-suite alias for requireDocker so this
// test file does not depend on test-name ordering with realstack_red_test.go.
func requireDockerForMLLP(t *testing.T) {
	t.Helper()
	if err := realstack.CheckDocker(); err != nil {
		// OP #293: env-gated skip — realstack MLLP control plane suite is docker-driven.
		t.Skipf("docker unavailable: %v", err)
	}
}

// postScenario is a convenience wrapper used across the MLLP control
// plane tests. The caller owns the response body close.
func postScenario(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// findRepoRootForMLLPTest is a per-file repo-root locator. Matches the
// existing findRepoRootForTest helper in realstack_red_test.go but uses
// a distinct name to avoid duplicate symbol errors when both test files
// compile into the same _test package.
func findRepoRootForMLLPTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root from %s", dir)
		}
		dir = parent
	}
}
