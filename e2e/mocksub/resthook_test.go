// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

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

// The rest-hook receiver:
//   * Accepts POST at /hook/{subscription_id}.
//   * Reads the request body, captures headers and method, journals it
//     under (subscription_id, received_at).
//   * Returns 200 OK by default.
//   * Exposes a control-plane API: GET /received returns the full
//     journal, GET /received/{sub} filters by subscription, DELETE
//     /journal clears it. POST /assert/notification_received is a
//     poll-style waiter used by the orchestrator helpers.

func TestRestHookReceiver_RecordsPostAndReturns200(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	body := []byte(`{"resourceType":"Bundle"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hook/sub-1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post hook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	got := r.Received("sub-1")
	if len(got) != 1 {
		t.Fatalf("Received: got %d entries, want 1", len(got))
	}
	if !bytes.Equal(got[0].Body, body) {
		t.Fatalf("Received[0].Body: got %q want %q", got[0].Body, body)
	}
	if got[0].Header.Get("Content-Type") != "application/fhir+json" {
		t.Fatalf("Received[0].Header: got %q", got[0].Header.Get("Content-Type"))
	}
	if got[0].SubscriptionID != "sub-1" {
		t.Fatalf("Received[0].SubscriptionID: got %q want sub-1", got[0].SubscriptionID)
	}
}

func TestRestHookReceiver_GetReceivedJSON(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	for _, id := range []string{"sub-1", "sub-2", "sub-1"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hook/"+id, strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		resp.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/received")
	if err != nil {
		t.Fatalf("get received: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	var entries []map[string]any
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, b)
	}
	if len(entries) != 3 {
		t.Fatalf("entries: got %d want 3", len(entries))
	}
}

func TestRestHookReceiver_FilterBySubscription(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	for _, id := range []string{"sub-1", "sub-2", "sub-1"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hook/"+id, strings.NewReader(`{}`))
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}
	resp, err := http.Get(srv.URL + "/received/sub-1")
	if err != nil {
		t.Fatalf("get filtered: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var entries []map[string]any
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, b)
	}
	if len(entries) != 2 {
		t.Fatalf("filtered entries: got %d want 2", len(entries))
	}
}

func TestRestHookReceiver_DeleteJournalClears(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hook/sub-1", strings.NewReader(`{}`))
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/journal", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete journal: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != 204 {
		t.Fatalf("delete status: got %d want 204", delResp.StatusCode)
	}
	if got := r.Received(""); len(got) != 0 {
		t.Fatalf("after clear: got %d entries", len(got))
	}
}

func TestRestHookReceiver_AssertNotificationReceived_Polls(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	// Fire-and-forget the POST after a short delay; assert/notification_received
	// must wait until the journal has at least one matching entry.
	go func() {
		time.Sleep(100 * time.Millisecond)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/hook/sub-77", strings.NewReader(`{"x":1}`))
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	body := []byte(`{"subscription_id":"sub-77","timeout_ms":2000}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/assert/notification_received", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post assert: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d body=%s", resp.StatusCode, b)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("assert returned too quickly: %v (expected to poll)", elapsed)
	}
}

func TestRestHookReceiver_AssertNotificationReceived_TimesOut(t *testing.T) {
	t.Parallel()
	r := NewRestHookReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	body := []byte(`{"subscription_id":"sub-99","timeout_ms":100}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/assert/notification_received", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post assert: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: got %d body=%s want 408", resp.StatusCode, b)
	}
}
