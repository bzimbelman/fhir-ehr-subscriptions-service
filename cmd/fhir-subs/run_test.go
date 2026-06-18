// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRun_GracefulShutdown_OnContextCancel asserts that run() returns once the
// caller cancels the context (the signal handler does the same thing for
// SIGTERM/SIGINT) and that /readyz starts reporting "shutting_down" during
// the shutdown window.
func TestRun_GracefulShutdown_OnContextCancel(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logs := &strings.Builder{}
	var (
		readyURL string
		started  = make(chan struct{})
	)

	hooks := runHooks{
		onListening: func(addr string) {
			readyURL = "http://" + addr
			close(started)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, logs, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}

	// Healthy state.
	{
		resp, err := http.Get(readyURL + "/healthz")
		if err != nil {
			t.Fatalf("healthz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("healthz status: %d", resp.StatusCode)
		}
	}

	// Trigger graceful shutdown.
	cancel()

	// run should return within the grace period.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned err: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s")
	}
}

// TestRun_ReadyzReportsShuttingDown asserts that during the brief shutdown
// window, /readyz reports failed=[shutting_down]. We achieve this with a
// large grace period so the test has time to observe the in-flight state.
func TestRun_ReadyzReportsShuttingDown(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		// Wide grace period so we can reliably observe the shutting_down state.
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		readyURL string
		mu       sync.Mutex
		seenAddr string
	)

	startCh := make(chan struct{})
	hooks := runHooks{
		onListening: func(addr string) {
			mu.Lock()
			seenAddr = addr
			mu.Unlock()
			close(startCh)
		},
		onShutdownStart: func(_ *lifecycleRegistry) {
			// Block briefly so the test observes the shutting_down readiness.
			time.Sleep(150 * time.Millisecond)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-startCh:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}
	mu.Lock()
	readyURL = "http://" + seenAddr
	mu.Unlock()

	// Cancel and observe /readyz reports shutting_down.
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(readyURL + "/readyz")
		if err != nil {
			// Server has stopped accepting; we're done.
			break
		}
		var body struct {
			Status string   `json:"status"`
			Failed []string `json:"failed"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode == 503 && len(body.Failed) == 1 && body.Failed[0] == "shutting_down" {
			// Observed it.
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestRun_RejectsTLSWithoutCertWhenNotInsecure asserts the validation
// short-circuit before binding.
func TestRun_RejectsTLSWithoutCertWhenNotInsecure(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: false}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runWithHooks(ctx, cfg, io.Discard, runHooks{})
	if err == nil {
		t.Fatal("expected error: insecure=false without cert files")
	}
	if !strings.Contains(err.Error(), "tls") {
		t.Fatalf("error should mention tls: %v", err)
	}
}

// pickFreeAddr returns 127.0.0.1:<random-free-port>.
func pickFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return fmt.Sprintf("%s", addr)
}
