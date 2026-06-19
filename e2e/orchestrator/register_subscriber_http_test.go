// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRegisterSubscriber_PostsFHIRSubscriptionToAPI pins OP #150 AC #4:
// the helper MUST hit POST /Subscription on the supplied API base URL
// (no raw SQL bypass), MUST send a FHIR Subscription resource as the
// body, and MUST poll until status=active.
//
// This test stands up an in-process httptest.Server that imitates the
// production binary's API surface enough for the helper to drive it:
//
//   - POST /Subscription returns 201 with a Location header carrying
//     the new id and a Subscription body whose status starts at
//     "requested".
//   - The first GET /Subscription/{id} returns status=requested; the
//     second returns status=active. This proves the helper polls until
//     active rather than treating 201 as terminal.
//
// If RegisterSubscriber is reverted to the raw-SQL bypass, this test
// will fail because the assertion server never sees a POST.
func TestRegisterSubscriber_PostsFHIRSubscriptionToAPI(t *testing.T) {
	t.Parallel()

	var (
		postCalls atomic.Int64
		getCalls  atomic.Int64
		gotBody   atomic.Value // string
		gotXCID   atomic.Value // string
		gotCT     atomic.Value // string
	)

	const subID = "11111111-2222-3333-4444-555555555555"
	mux := http.NewServeMux()
	mux.HandleFunc("/Subscription/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			postCalls.Add(1)
			b, _ := io.ReadAll(r.Body)
			gotBody.Store(string(b))
			gotXCID.Store(r.Header.Get("X-Client-Id"))
			gotCT.Store(r.Header.Get("Content-Type"))
			w.Header().Set("Location", "/Subscription/"+subID)
			w.Header().Set("Content-Type", "application/fhir+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"resourceType":"Subscription","id":"` + subID + `","status":"requested"}`))
		case http.MethodGet:
			n := getCalls.Add(1)
			status := "requested"
			if n >= 2 {
				status = "active"
			}
			w.Header().Set("Content-Type", "application/fhir+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"resourceType":"Subscription","id":"` + subID + `","status":"` + status + `"}`))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := RegisterSubscriber(ctx, nil, RegisterSubscriberOptions{
		ClientID:    "client-150",
		TopicURL:    "http://example.org/topic/observation",
		ChannelType: "rest-hook",
		Endpoint:    "http://subscriber.example.com/hook",
		APIBaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("RegisterSubscriber: %v", err)
	}
	if got != subID {
		t.Errorf("subscription id: got %q want %q", got, subID)
	}
	if postCalls.Load() != 1 {
		t.Errorf("POST /Subscription calls: got %d want 1 (raw-SQL bypass forbidden)",
			postCalls.Load())
	}
	if getCalls.Load() < 2 {
		t.Errorf("GET /Subscription/{id} polls: got %d want >= 2 (helper must wait for active)",
			getCalls.Load())
	}
	body, _ := gotBody.Load().(string)
	if !strings.Contains(body, `"resourceType":"Subscription"`) {
		t.Errorf("POST body missing FHIR Subscription envelope: %s", body)
	}
	if !strings.Contains(body, `"topic":"http://example.org/topic/observation"`) {
		t.Errorf("POST body missing topic: %s", body)
	}
	if !strings.Contains(body, `"endpoint":"http://subscriber.example.com/hook"`) {
		t.Errorf("POST body missing endpoint: %s", body)
	}
	if !strings.Contains(body, `"code":"rest-hook"`) {
		t.Errorf("POST body missing channelType.code rest-hook: %s", body)
	}
	if x, _ := gotXCID.Load().(string); x != "client-150" {
		t.Errorf("X-Client-Id header: got %q want client-150", x)
	}
	if ct, _ := gotCT.Load().(string); !strings.Contains(ct, "application/fhir+json") {
		t.Errorf("Content-Type header: got %q want application/fhir+json", ct)
	}

	// Sanity: the body must parse as JSON with resourceType=Subscription.
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("body not JSON: %v body=%s", err, body)
	}
	if rt, _ := doc["resourceType"].(string); rt != "Subscription" {
		t.Errorf("resourceType=%q want Subscription", rt)
	}
}

// TestRegisterSubscriber_FailsClosedWhenStatusNeverActive pins that the
// helper times out (rather than returning silently) when the API never
// reports status=active. This is the sister assertion to the happy-path
// test: pre-fix code returned a row id immediately because the SQL
// UPDATE made the row "active" by fiat. The HTTP-based helper must
// instead surface "activation never completed".
func TestRegisterSubscriber_FailsClosedWhenStatusNeverActive(t *testing.T) {
	t.Parallel()

	const subID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	mux := http.NewServeMux()
	mux.HandleFunc("/Subscription/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Location", "/Subscription/"+subID)
			w.Header().Set("Content-Type", "application/fhir+json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"resourceType":"Subscription","id":"` + subID + `","status":"requested"}`))
		case http.MethodGet:
			// Always requested, never active.
			w.Header().Set("Content-Type", "application/fhir+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"resourceType":"Subscription","id":"` + subID + `","status":"requested"}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Use a short context so the test does not hang if the helper
	// regresses to a fire-and-forget POST.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := RegisterSubscriber(ctx, nil, RegisterSubscriberOptions{
		ClientID:        "client-150-stuck",
		TopicURL:        "http://example.org/topic/observation",
		ChannelType:     "rest-hook",
		Endpoint:        "http://subscriber.example.com/hook",
		APIBaseURL:      srv.URL,
		ActivateTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatalf("RegisterSubscriber: want error when activation never completes; got nil")
	}
}
