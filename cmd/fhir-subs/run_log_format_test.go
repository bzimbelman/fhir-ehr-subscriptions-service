// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestRun_HonorsLogFormatText asserts run() routes the configured
// deployment.log_format ("text") through to logging.NewLogger so the
// emitted records use slog's text handler. Pre-fix run.go hardcoded
// `Format: "json"` and ignored cfg.Deployment.LogFormat entirely.
//
// Story #160.
func TestRun_HonorsLogFormatText(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{
			FacilityID: "f1",
			LogLevel:   "info",
			LogFormat:  "text",
			Mode:       DeploymentModeProbeOnly,
		},
		Adapter:   AdapterConfig{ID: "a1"},
		Server:    ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
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
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}

	out := logs.String()
	if out == "" {
		t.Fatal("no log output captured")
	}
	// Slog's TextHandler emits time=... level=... msg=... key=value
	// pairs. The JSON handler wraps every record in `{...}`.
	if strings.Contains(strings.SplitN(out, "\n", 2)[0], "{") {
		t.Fatalf("log_format: text was set but first line is JSON-shaped:\n%s", out)
	}
	if !strings.Contains(out, "level=") || !strings.Contains(out, "msg=") {
		t.Fatalf("log_format: text was set but output lacks slog text-handler markers:\n%s", out)
	}
}

// TestRun_LogFormatEmptyDefaultsToJSON pins the empty-format default to
// JSON so an operator who omits deployment.log_format keeps today's
// behavior (and all the JSON-shipping log collectors keep working).
//
// Story #160 AC: "Empty-format MUST default to `json`".
func TestRun_LogFormatEmptyDefaultsToJSON(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{
			FacilityID: "f1",
			LogLevel:   "info",
			LogFormat:  "", // explicit empty — the default
			Mode:       DeploymentModeProbeOnly,
		},
		Adapter:   AdapterConfig{ID: "a1"},
		Server:    ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), ProbeBind: pickFreeAddr(t), Insecure: true}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
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
	_ = io.EOF

	out := logs.String()
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.HasPrefix(strings.TrimSpace(first), "{") {
		t.Fatalf("empty log_format must default to json; first line is not a JSON object:\n%s", first)
	}
}
