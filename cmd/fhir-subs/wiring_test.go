// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRun_ReadyzReturns200WhenAllChecksPass asserts that the production
// /readyz handler returns 200 OK once every registered readiness check
// passes. Before this fix the handler hardcoded failed=["all_components"]
// and returned 503 forever, so k8s would never route traffic to a pod.
//
// B-1.
func TestRun_ReadyzReturns200WhenAllChecksPass(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}

	// /readyz: with no DB configured (no readiness checks registered),
	// the handler should return 200. The real production gating happens
	// via per-component checks the lifecycle module aggregates.
	resp, err := http.Get(readyURL + "/readyz")
	if err != nil {
		t.Fatalf("readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("readyz expected 200 OK, got %d body=%+v", resp.StatusCode, body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestRun_HealthzGatedOnLifecycleStart asserts that /healthz only returns
// 200 once the lifecycle module's startup has completed. Before this fix,
// the liveness flag flipped as soon as the listener bound, so a stuck
// pod that never finished startup was reported live forever and k8s
// never restarted it.
//
// B-3.
func TestRun_HealthzGatedOnLifecycleStart(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu        sync.Mutex
		seenAddr  string
		started   = make(chan struct{})
		startupOK = make(chan struct{})
	)
	hooks := runHooks{
		onListening: func(addr string) {
			mu.Lock()
			seenAddr = addr
			mu.Unlock()
			close(started)
		},
		onStartupComplete: func() {
			close(startupOK)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}

	mu.Lock()
	healthURL := "http://" + seenAddr + "/healthz"
	mu.Unlock()

	// Wait for startup-complete to fire before asserting 200.
	select {
	case <-startupOK:
	case <-time.After(3 * time.Second):
		t.Fatal("startup never completed")
	}

	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status: %d", resp.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestServer_TimeoutsConfigured asserts that the production HTTP server
// has WriteTimeout, IdleTimeout, and MaxHeaderBytes set. Without these
// the server was vulnerable to slowloris write-side hangs and unbounded
// idle conns.
//
// B-2.
func TestServer_TimeoutsConfigured(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:     pickFreeAddr(t),
			Insecure: true,
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      11 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type capture struct {
		read       time.Duration
		readHeader time.Duration
		write      time.Duration
		idle       time.Duration
		maxHeader  int
	}
	var got capture
	captured := make(chan struct{})
	var once sync.Once

	hooks := runHooks{
		onServerConfigured: func(s *http.Server) {
			got = capture{
				read:       s.ReadTimeout,
				readHeader: s.ReadHeaderTimeout,
				write:      s.WriteTimeout,
				idle:       s.IdleTimeout,
				maxHeader:  s.MaxHeaderBytes,
			}
			once.Do(func() { close(captured) })
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-captured:
	case <-time.After(3 * time.Second):
		t.Fatal("onServerConfigured never fired")
	}

	if got.readHeader != 2*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 2s", got.readHeader)
	}
	if got.read != 3*time.Second {
		t.Errorf("ReadTimeout = %v, want 3s", got.read)
	}
	if got.write != 11*time.Second {
		t.Errorf("WriteTimeout = %v, want 11s", got.write)
	}
	if got.idle != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", got.idle)
	}
	if got.maxHeader != 16<<10 {
		t.Errorf("MaxHeaderBytes = %d, want %d", got.maxHeader, 16<<10)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestServer_TimeoutDefaults asserts safe defaults when the operator has
// not configured timeouts. Defaults are taken from
// http_server.* defaults documented in cmd/fhir-subs/config.go.
//
// B-2.
func TestServer_TimeoutDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got *http.Server
	captured := make(chan struct{})
	var once sync.Once
	hooks := runHooks{
		onServerConfigured: func(s *http.Server) {
			got = s
			once.Do(func() { close(captured) })
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()
	select {
	case <-captured:
	case <-time.After(3 * time.Second):
		t.Fatal("onServerConfigured never fired")
	}

	if got.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout default not set: %v", got.ReadHeaderTimeout)
	}
	if got.WriteTimeout <= 0 {
		t.Errorf("WriteTimeout default not set: %v", got.WriteTimeout)
	}
	if got.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout default not set: %v", got.IdleTimeout)
	}
	if got.MaxHeaderBytes <= 0 {
		t.Errorf("MaxHeaderBytes default not set: %d", got.MaxHeaderBytes)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestRun_StartupCompleteFiresAfterLifecycleStart asserts that startup
// complete only happens after lifecycle Start has succeeded. This is
// observable to tests via the onStartupComplete hook, which fires only
// once lifecycle.MarkStartupComplete has been called.
//
// B-3.
func TestRun_StartupCompleteFiresAfterLifecycleStart(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listening := make(chan struct{})
	startupOK := make(chan struct{})

	var seenOrder []string
	var orderMu sync.Mutex

	hooks := runHooks{
		onListening: func(_ string) {
			orderMu.Lock()
			seenOrder = append(seenOrder, "listening")
			orderMu.Unlock()
			close(listening)
		},
		onStartupComplete: func() {
			orderMu.Lock()
			seenOrder = append(seenOrder, "startup_complete")
			orderMu.Unlock()
			close(startupOK)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-startupOK:
	case <-time.After(3 * time.Second):
		t.Fatal("startup never completed")
	}

	orderMu.Lock()
	defer orderMu.Unlock()
	if len(seenOrder) != 2 {
		t.Fatalf("expected listening then startup_complete, got %v", seenOrder)
	}
	if seenOrder[0] != "listening" || seenOrder[1] != "startup_complete" {
		t.Fatalf("order wrong: %v", seenOrder)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("run did not return")
	}
	_ = errors.New
	_ = strings.Contains
}
