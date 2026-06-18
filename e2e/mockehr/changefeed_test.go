// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The vendor change-feed mock is the EHR's "we publish a stream of
// resource-version events" surface. The default-adapter Vendor API
// Client subscribes to it via SSE and emits resource_changes rows.
//
// The mock contract:
//
//   * GET /change-feed (SSE, Accept: text/event-stream) streams events
//     until the connection closes.
//   * Publish(event) injects a new event the next subscriber will see.
//   * Each event is `data: {json}\n\n` per the SSE spec; events carry
//     `id` (vendor record id), `resource_type`, `change_kind`, and
//     a `version` integer that monotonically increments.

func TestChangeFeed_SSEStreamPublishedEvent(t *testing.T) {
	t.Parallel()
	cf := NewChangeFeed()
	srv := httptest.NewServer(cf.Handler())
	defer srv.Close()

	go func() {
		// Wait briefly for the client to connect, then publish.
		time.Sleep(50 * time.Millisecond)
		cf.Publish(ChangeFeedEvent{
			ID:           "evt-1",
			ResourceType: "Patient",
			ResourceID:   "p1",
			ChangeKind:   "create",
			Version:      1,
		})
	}()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/change-feed", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sse: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type: got %q want text/event-stream", got)
	}

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if !strings.Contains(payload, `"id":"evt-1"`) {
				t.Fatalf("first SSE event payload unexpected: %q", payload)
			}
			return
		}
	}
	t.Fatal("did not receive SSE data line within deadline")
}

func TestChangeFeed_PublishedBeforeSubscribeIsBuffered(t *testing.T) {
	t.Parallel()
	cf := NewChangeFeed()
	cf.Publish(ChangeFeedEvent{ID: "evt-pre", ResourceType: "Patient", Version: 1})

	srv := httptest.NewServer(cf.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/change-feed", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"id":"evt-pre"`) {
			return
		}
	}
	t.Fatal("did not receive buffered event")
}
