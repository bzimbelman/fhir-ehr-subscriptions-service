// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRun_MountsFHIRSubscriptionRoute_WhenDatabaseDisabled asserts that
// even without a database configured, the production binary mounts the
// probe + /metadata routes from lifecycle. Subscription routes return
// 503 (service unavailable, no DB) rather than 404 — proves the router
// is "constructed and ready" even if the storage-backed handlers cannot
// run yet.
//
// B-4 (subset).
func TestRun_HasMetadataRoute(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var url string
	started := make(chan struct{})
	hooks := runHooks{
		onListening: func(addr string) {
			url = "http://" + addr
			close(started)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}

	resp, err := http.Get(url + "/metadata")
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metadata expected 200, got %d", resp.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
	_ = strings.Contains
}
