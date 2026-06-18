// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestRestHookActivator_D2_2xxSucceeds asserts that when the subscriber
// endpoint returns 2xx to the synthetic handshake POST, the activator
// returns HandshakeSucceeded. Before D-2 every subscription always
// flipped to active regardless of the subscriber's response.
//
// D-2.
func TestRestHookActivator_D2_2xxSucceeds(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/fhir+json") {
			t.Errorf("Content-Type: got %q, want application/fhir+json", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	act := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: true,
		Timeout:   5 * time.Second,
	})

	row := repos.SubscriptionRow{
		ID:       uuid.New(),
		Endpoint: srv.URL,
		TopicURL: "http://example.org/topics/x",
	}
	out, err := act.ActivateSubscription(context.Background(), row)
	if err != nil {
		t.Fatalf("ActivateSubscription: %v", err)
	}
	if out != handlers.HandshakeSucceeded {
		t.Errorf("outcome: got %q, want %q", out, handlers.HandshakeSucceeded)
	}
	if hits.Load() != 1 {
		t.Errorf("subscriber hits: got %d, want 1", hits.Load())
	}
}

// TestRestHookActivator_D2_4xxFails asserts that a 4xx response from the
// subscriber endpoint produces HandshakeFailed.
//
// D-2.
func TestRestHookActivator_D2_4xxFails(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	act := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: true,
		Timeout:   5 * time.Second,
	})

	row := repos.SubscriptionRow{
		ID:       uuid.New(),
		Endpoint: srv.URL,
		TopicURL: "http://example.org/topics/x",
	}
	out, err := act.ActivateSubscription(context.Background(), row)
	if err != nil {
		t.Fatalf("ActivateSubscription: %v", err)
	}
	if out != handlers.HandshakeFailed {
		t.Errorf("outcome: got %q, want %q", out, handlers.HandshakeFailed)
	}
}

// TestRestHookActivator_D2_DialErrorFails asserts that a connection
// failure (closed port) produces HandshakeFailed instead of
// HandshakeSucceeded.
//
// D-2.
func TestRestHookActivator_D2_DialErrorFails(t *testing.T) {
	t.Parallel()

	act := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: true,
		Timeout:   2 * time.Second,
	})

	row := repos.SubscriptionRow{
		ID:       uuid.New(),
		Endpoint: "http://127.0.0.1:1", // closed port
		TopicURL: "http://example.org/topics/x",
	}
	out, err := act.ActivateSubscription(context.Background(), row)
	if err != nil {
		t.Fatalf("ActivateSubscription: %v", err)
	}
	if out != handlers.HandshakeFailed {
		t.Errorf("outcome: got %q, want %q", out, handlers.HandshakeFailed)
	}
}

// TestRestHookActivator_D2_BundleHandshakeShape asserts the synthetic
// POST carries a Bundle whose entry is a SubscriptionStatus with
// type=handshake. A subscriber distinguishes the activation probe from
// a real notification via this field.
//
// D-2.
func TestRestHookActivator_D2_BundleHandshakeShape(t *testing.T) {
	t.Parallel()

	type capture struct {
		body string
	}
	var cap capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.body = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	act := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: true,
		Timeout:   5 * time.Second,
	})
	row := repos.SubscriptionRow{
		ID:       uuid.New(),
		Endpoint: srv.URL,
		TopicURL: "http://example.org/topics/x",
	}
	if _, err := act.ActivateSubscription(context.Background(), row); err != nil {
		t.Fatalf("ActivateSubscription: %v", err)
	}
	for _, want := range []string{
		`"resourceType":"Bundle"`,
		`"SubscriptionStatus"`,
		`"type":"handshake"`,
	} {
		if !strings.Contains(cap.body, want) {
			t.Errorf("body missing %q; got %q", want, cap.body)
		}
	}
}

// TestRestHookActivator_D2_RejectsHTTPWhenInsecureDisallowed asserts
// that with AllowHTTP=false a plain http:// endpoint never produces a
// network call. The handshake fails with no out-bound request.
//
// D-2.
func TestRestHookActivator_D2_RejectsHTTPWhenInsecureDisallowed(t *testing.T) {
	t.Parallel()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	act := newRestHookActivator(restHookActivatorOptions{
		AllowHTTP: false, // production default — reject http
		Timeout:   2 * time.Second,
	})
	row := repos.SubscriptionRow{
		ID:       uuid.New(),
		Endpoint: srv.URL, // http://...
		TopicURL: "http://example.org/topics/x",
	}
	out, err := act.ActivateSubscription(context.Background(), row)
	if err != nil {
		t.Fatalf("ActivateSubscription: %v", err)
	}
	if out != handlers.HandshakeFailed {
		t.Errorf("outcome: got %q, want %q", out, handlers.HandshakeFailed)
	}
	if hits.Load() != 0 {
		t.Errorf("subscriber hits: got %d, want 0 (handshake must reject http before dial)", hits.Load())
	}
}
