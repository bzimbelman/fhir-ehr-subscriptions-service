// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package resthook

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// resthook is the demo subscriber's REST-hook receiver. These tests stand
// up a real httptest.Server and drive the HTTP API end-to-end — no mocks.

func TestReceiver_HandleHook_JournalsPostBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()

	body := []byte(`{"resourceType":"Bundle","type":"history","entry":[]}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/hook/sub-123", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("X-Custom", "trace-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify journaled entry via /received.
	listResp, err := http.Get(srv.URL + "/received")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listResp.Body.Close()
	var got []ReceivedNotification
	if err := json.NewDecoder(listResp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 journaled entry, got %d", len(got))
	}
	e := got[0]
	if e.SubscriptionID != "sub-123" {
		t.Fatalf("subscription id: got %q want sub-123", e.SubscriptionID)
	}
	if e.Method != http.MethodPost {
		t.Fatalf("method: got %q want POST", e.Method)
	}
	if e.Path != "/hook/sub-123" {
		t.Fatalf("path: got %q want /hook/sub-123", e.Path)
	}
	if !bytes.Equal(e.Body, body) {
		t.Fatalf("body: got %q want %q", e.Body, body)
	}
	if e.Header.Get("X-Custom") != "trace-1" {
		t.Fatalf("custom header lost: %v", e.Header)
	}
	if e.ReceivedAt.IsZero() {
		t.Fatalf("ReceivedAt should be set")
	}
}

func TestReceiver_HandleHook_AcceptsPUT(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/hook/sub-PUT", strings.NewReader("body"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	got := r.Received("sub-PUT")
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
}

func TestReceiver_HandleHook_RejectsGet(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/hook/sub-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestReceiver_HandleHook_MissingSubscriptionID(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/hook/", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (missing subscription id), got %d", resp.StatusCode)
	}
}

func TestReceiver_GetReceivedFiltered(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	for _, tc := range []struct {
		sub  string
		body string
	}{
		{"alpha", "A1"},
		{"beta", "B1"},
		{"alpha", "A2"},
	} {
		_, err := http.Post(srv.URL+"/hook/"+tc.sub, "text/plain", strings.NewReader(tc.body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
	}

	resp, err := http.Get(srv.URL + "/received/alpha")
	if err != nil {
		t.Fatalf("get filtered: %v", err)
	}
	defer resp.Body.Close()
	var got []ReceivedNotification
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 alpha entries, got %d", len(got))
	}
	for _, e := range got {
		if e.SubscriptionID != "alpha" {
			t.Fatalf("filter leak: got %q", e.SubscriptionID)
		}
	}
}

func TestReceiver_GetReceived_RejectsPost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/received", "text/plain", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}

	resp2, err := http.Post(srv.URL+"/received/alpha", "text/plain", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 on /received/<sub>, got %d", resp2.StatusCode)
	}
}

func TestReceiver_DeleteJournal_ClearsAll(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	_, _ = http.Post(srv.URL+"/hook/sub-1", "text/plain", strings.NewReader("x"))
	if got := len(r.Received("")); got != 1 {
		t.Fatalf("pre-delete journal length: got %d want 1", got)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/journal", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if got := len(r.Received("")); got != 0 {
		t.Fatalf("post-delete journal length: got %d want 0", got)
	}
}

func TestReceiver_DeleteJournal_RejectsGet(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/journal")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestReceiver_AssertNotificationReceived_AlreadyJournaled(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	_, _ = http.Post(srv.URL+"/hook/sub-A", "text/plain", strings.NewReader("payload"))

	body, _ := json.Marshal(map[string]any{"subscription_id": "sub-A", "timeout_ms": 1000})
	resp, err := http.Post(srv.URL+"/assert/notification_received", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post assert: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, raw)
	}
	var got ReceivedNotification
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SubscriptionID != "sub-A" {
		t.Fatalf("subscription id: got %q want sub-A", got.SubscriptionID)
	}
	if string(got.Body) != "payload" {
		t.Fatalf("body: got %q want payload", got.Body)
	}
}

func TestReceiver_AssertNotificationReceived_BlocksUntilArrival(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	// Caller waits with a 2s timeout; we POST after 100ms.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = http.Post(srv.URL+"/hook/sub-WAIT", "text/plain", strings.NewReader("late"))
	}()

	body, _ := json.Marshal(map[string]any{"subscription_id": "sub-WAIT", "timeout_ms": 2000})
	start := time.Now()
	resp, err := http.Post(srv.URL+"/assert/notification_received", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post assert: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d (%s)", resp.StatusCode, raw)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("assert returned too early; expected to wait, took %v", elapsed)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("assert took too long: %v", elapsed)
	}
}

func TestReceiver_AssertNotificationReceived_TimesOut(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	body, _ := json.Marshal(map[string]any{"subscription_id": "missing", "timeout_ms": 100})
	resp, err := http.Post(srv.URL+"/assert/notification_received", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("expected 408, got %d", resp.StatusCode)
	}
}

func TestReceiver_AssertNotificationReceived_DefaultTimeout(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	// Pre-populate so the call returns immediately even though we don't
	// send a timeout_ms (exercises the default 5000ms branch).
	_, _ = http.Post(srv.URL+"/hook/sub-D", "text/plain", strings.NewReader("x"))
	body, _ := json.Marshal(map[string]any{"subscription_id": "sub-D"})
	resp, err := http.Post(srv.URL+"/assert/notification_received", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestReceiver_AssertNotificationReceived_BadJSON(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/assert/notification_received", "application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestReceiver_AssertNotificationReceived_RejectsGet(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(NewReceiver().Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/assert/notification_received")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

func TestReceiver_Received_FilterEmptyReturnsAll(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()
	_, _ = http.Post(srv.URL+"/hook/a", "text/plain", strings.NewReader("1"))
	_, _ = http.Post(srv.URL+"/hook/b", "text/plain", strings.NewReader("2"))
	if got := len(r.Received("")); got != 2 {
		t.Fatalf("Received(\"\"): got %d want 2", got)
	}
}

func TestReceivedNotification_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	orig := ReceivedNotification{
		SubscriptionID: "sub-RT",
		ReceivedAt:     time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC),
		Method:         http.MethodPost,
		Path:           "/hook/sub-RT",
		Header:         http.Header{"Content-Type": []string{"application/fhir+json"}},
		Body:           []byte(`{"resourceType":"Bundle"}`),
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Body must be a JSON string (UTF-8) on the wire, not base64.
	if !bytes.Contains(raw, []byte(`"body":"{\"resourceType\":\"Bundle\"}"`)) {
		t.Fatalf("expected body as JSON string, got: %s", raw)
	}
	var rt ReceivedNotification
	if err := json.Unmarshal(raw, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt.SubscriptionID != orig.SubscriptionID || rt.Method != orig.Method || rt.Path != orig.Path {
		t.Fatalf("scalar fields lost: %+v", rt)
	}
	if !bytes.Equal(rt.Body, orig.Body) {
		t.Fatalf("body lost: got %q want %q", rt.Body, orig.Body)
	}
	if rt.Header.Get("Content-Type") != "application/fhir+json" {
		t.Fatalf("header lost: %v", rt.Header)
	}
}

func TestReceivedNotification_UnmarshalRejectsBadJSON(t *testing.T) {
	t.Parallel()
	var rn ReceivedNotification
	if err := rn.UnmarshalJSON([]byte("not-json")); err == nil {
		t.Fatalf("expected unmarshal error on bad JSON")
	}
}

func TestReceiver_ConcurrentPostsAreAllJournaled(t *testing.T) {
	t.Parallel()
	r := NewReceiver()
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := http.Post(srv.URL+"/hook/sub-CONC", "text/plain", strings.NewReader("x"))
			if err != nil {
				t.Errorf("post %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(r.Received("sub-CONC")); got != n {
		t.Fatalf("expected %d entries, got %d", n, got)
	}
}
