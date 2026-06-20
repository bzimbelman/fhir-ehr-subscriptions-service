// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// Phase A (RED) tests for OpenProject story #294 — H10b Programmable
// rest-hook receiver in realstack.
//
// These tests pin the realstack handle the test-resthook-subscriber
// binary's control plane is exposed through:
// Stack.RestHookSubscriber.ControlAPIURL. Until Phase B implements the
// field + the binary's POST /program/{tag} endpoint, these tests fail.
package realstack_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestRealStack_RestHookSubscriber_ExposesControlAPIURL pins the new
// handle. The field MUST be set to the same listener as QueryAPIURL
// (the binary mounts /program/* on its primary HTTP listener) and a
// GET to the binary's healthz over the control URL must succeed.
func TestRealStack_RestHookSubscriber_ExposesControlAPIURL(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	if stack.RestHookSubscriber.ControlAPIURL == "" {
		t.Fatalf("Stack.RestHookSubscriber.ControlAPIURL is empty; OP #294 requires the harness to surface the control plane URL")
	}
	if !strings.HasPrefix(stack.RestHookSubscriber.ControlAPIURL, "http://") {
		t.Fatalf("ControlAPIURL=%q is not an http URL", stack.RestHookSubscriber.ControlAPIURL)
	}

	if got := httpStatus(t, stack.RestHookSubscriber.ControlAPIURL+"/healthz"); got != 200 {
		t.Fatalf("ControlAPIURL /healthz returned %d; want 200 (control plane shares the binary's listener)", got)
	}
}

// TestRealStack_RestHookSubscriber_ProgramRoundTrip drives the full
// program-install -> delivery flow against the docker-compose-deployed
// binary. This is the bytes-flow-through-the-real-binary acceptance
// criterion of OP #294: no in-process Go shims, no in-memory handler
// substitution.
func TestRealStack_RestHookSubscriber_ProgramRoundTrip(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	prog := []byte(`{"sequence":[{"status":503},{"status":200}]}`)
	tag := "h10b-roundtrip"

	installURL := stack.RestHookSubscriber.ControlAPIURL + "/program/" + tag
	resp, err := http.Post(installURL, "application/json", bytes.NewReader(prog))
	if err != nil {
		t.Fatalf("POST %s: %v", installURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("install program: status %d", resp.StatusCode)
	}

	notifyURL := stack.RestHookSubscriber.QueryAPIURL + "/notify/" + tag
	want := []int{503, 200}
	for i, w := range want {
		resp, err := http.Post(notifyURL, "application/fhir+json", strings.NewReader(`{"resourceType":"Bundle"}`))
		if err != nil {
			t.Fatalf("delivery #%d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != w {
			t.Errorf("delivery #%d: status %d; want %d", i, resp.StatusCode, w)
		}
	}
}
