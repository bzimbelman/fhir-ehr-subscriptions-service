// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
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
		onProbeListening: func(addr string) {
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
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
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
		onProbeListening: func(addr string) {
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
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", ProbeBind: "127.0.0.1:0", Insecure: false}},
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

// TestRun_WarnsOnWildcardBindWithInsecure asserts that booting with the
// default `0.0.0.0:<port>` bind AND insecure=true emits a warn-level
// log line so an operator who wires the production binary to a
// container is reminded that the listener is reachable from any host
// (S-1.8). Bind defaults remain `0.0.0.0` for backwards-compatibility;
// the warning is the documented opt-in signal.
func TestRun_WarnsOnWildcardBindWithInsecure(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "0.0.0.0:0", ProbeBind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logs := &strings.Builder{}
	started := make(chan struct{})
	hooks := runHooks{onListening: func(_ string) { close(started) }}
	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, logs, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	out := logs.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "wildcard bind") {
		t.Fatalf("expected wildcard-bind warning log: %s", out)
	}
}

// TestRun_UsesObservabilityLogger asserts that runWithHooks routes log
// output through the observability/logging package, which installs the
// PHI-redacting handler. The test gets the live logger via
// runHooks.onLoggerReady and emits a record with a `body` attribute
// (a documented PHI field name in observability/logging.PHIFieldNames).
// If the obs logger is wired the `body` value is rewritten to
// "[redacted]" in the output. (S-1.4)
func TestRun_UsesObservabilityLogger(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", LogLevel: "info", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logs := &strings.Builder{}
	started := make(chan struct{})
	hooks := runHooks{
		onListening: func(_ string) { close(started) },
		onLoggerReady: func(lg *slog.Logger) {
			// PHI-shaped attribute: redactor must scrub the value.
			lg.Info("phi probe", "body", "patient-name-leaked")
		},
	}
	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, logs, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	out := logs.String()
	if !strings.Contains(out, "phi probe") {
		t.Fatalf("phi probe line missing: %s", out)
	}
	if strings.Contains(out, "patient-name-leaked") {
		t.Fatalf("redactor did not scrub PHI body value: %s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected [redacted] marker for PHI body field: %s", out)
	}
}

// TestRun_LogsCloseErrorAfterShutdownTimeout asserts that when the
// graceful Shutdown deadline is exceeded and the forced Close path runs,
// any error returned by srv.Close is logged at warn level rather than
// silently dropped (S-1.5). We exercise the path by overriding the
// runHooks-installed test seam to make Shutdown fail and Close fail too.
func TestRun_LogsCloseErrorAfterShutdownTimeout(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
		// Microsecond grace so the shutdown ctx is already expired by the
		// time we reach srv.Shutdown — that returns ctx.DeadlineExceeded
		// which forces the Close() branch.
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: time.Microsecond},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logs := &strings.Builder{}

	started := make(chan struct{})
	hooks := runHooks{
		onListening: func(_ string) { close(started) },
		shutdownErr: func() error { return fmt.Errorf("simulated shutdown failure") },
		forceClose:  func() error { return errSimulatedClose },
	}
	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, logs, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("server never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	if !strings.Contains(logs.String(), "force close") {
		t.Fatalf("expected force-close log; got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), errSimulatedClose.Error()) {
		t.Fatalf("expected close error in logs; got: %s", logs.String())
	}
}

var errSimulatedClose = fmt.Errorf("simulated close error")

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
