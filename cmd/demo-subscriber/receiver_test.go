// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestReceiver_PrintsBundleOnPOST hosts the demo's listener, fires a
// notification Bundle at it, and asserts the printer rendered a line
// with the patient + topic + event-number highlights.
func TestReceiver_PrintsBundleOnPOST(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	rcv := newReceiver(&sink, true /*pretty*/, true /*noColor*/)
	srv := startListener(t, rcv)
	defer srv.Close()

	body := []byte(`{
		"resourceType": "Bundle",
		"type": "subscription-notification",
		"entry": [
			{"resource": {
				"resourceType":"SubscriptionStatus",
				"type":"event-notification",
				"topic":"http://demo.org/topics/lab-results",
				"notificationEvent":[{"eventNumber":42,"focus":{"reference":"Observation/o1"}}]
			}},
			{"resource": {
				"resourceType":"Observation",
				"id":"o1",
				"subject":{"reference":"Patient/ABC123"}
			}}
		]
	}`)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/hook/sub-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sink.String(), "Patient/ABC123") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	out := sink.String()
	if !strings.Contains(out, "Patient/ABC123") {
		t.Errorf("output missing patient: %q", out)
	}
	if !strings.Contains(out, "lab-results") {
		t.Errorf("output missing topic short form: %q", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("output missing event number: %q", out)
	}
}

// TestReceiver_HandshakeReturnsOK accepts the rest-hook activation
// handshake (a POST with empty / handshake-shaped body) and returns 200
// so the bridge can flip the subscription to active.
func TestReceiver_HandshakeReturnsOK(t *testing.T) {
	t.Parallel()

	var sink bytes.Buffer
	rcv := newReceiver(&sink, true /*pretty*/, true /*noColor*/)
	srv := startListener(t, rcv)
	defer srv.Close()

	// Handshake body shape used by the rest-hook activator: a Bundle
	// with type=handshake.
	hs := []byte(`{"resourceType":"Bundle","type":"handshake"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/hook/sub-1", bytes.NewReader(hs))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
}
