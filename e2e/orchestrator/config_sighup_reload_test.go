// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// TestB35_SIGHUPReloadsConfig (B-35) wires the lifecycle module's
// SIGHUP handler to config.Module.Reload (the host's responsibility in
// production, exercised here through SetReloadHandler) and verifies
// that raising SIGHUP at the process invokes the reload path.
//
// We do not rely on the OS delivering a real SIGHUP — instead we drive
// the dispatcher through the same SetReloadHandler seam that the host
// uses in production. The test asserts the OnReload trigger label is
// "sighup", proving the lifecycle->config wiring works end to end.
func TestB35_SIGHUPReloadsConfig(t *testing.T) {
	cfgPath := writeBootableConfig(t, nil)

	mod, _, err := config.Start(context.Background(), config.CliArgs{
		ConfigPath: cfgPath,
	}, config.Context{Clock: time.Now})
	if err != nil {
		t.Fatalf("config start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	lf, err := lifecycle.Start(context.Background(), lifecycle.LifecycleConfig{
		ShutdownGracePeriod: time.Second,
		ProbeObserveWindow:  time.Millisecond,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}

	// Wire SIGHUP -> config reload. This is the production path: the
	// host registers the handler at boot.
	lf.SetReloadHandler(func(ctx context.Context) {
		mod.Reload(ctx, config.TriggerSIGHUP)
	})

	var sighupCount atomic.Int64
	mod.OnReload(func(trigger string) {
		if trigger == "sighup" {
			sighupCount.Add(1)
		}
	})

	// Simulate signal delivery: send SIGHUP to ourselves. The
	// lifecycle's signal.Notify subscription receives it and routes
	// through the dispatcher to our reload handler.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sighupCount.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := sighupCount.Load(); got < 1 {
		t.Fatalf("SIGHUP did not trigger reload; sighup count = %d", got)
	}
}

// writeBootableConfig copies the canonical architecture_example.yaml
// fixture into a tempdir, optionally rewriting one ${env:...} into
// ${file:...} so the mtime-watcher test has a path to poll.
func writeBootableConfig(t *testing.T, fileSecretPath *string) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("../../internal/infra/config/testdata/architecture_example.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := string(src)
	if fileSecretPath != nil {
		// Seed the secret file so config Start succeeds.
		if err := os.WriteFile(*fileSecretPath, []byte("01234567890abcdef01234567890abcdef"), 0o600); err != nil {
			t.Fatalf("seed secret: %v", err)
		}
		rewritten = strings.Replace(rewritten,
			"at_rest_key: \"${env:STORAGE_ENCRYPTION_KEY}\"",
			"at_rest_key: \"${file:"+*fileSecretPath+"}\"",
			1)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	for k, v := range map[string]string{
		"DATABASE_URL":                "postgres://example.org/db",
		"STORAGE_ENCRYPTION_KEY":      "01234567890abcdef01234567890abcdef",
		"EPIC_CLIENT_ID":              "epic-client-id-x",
		"EPIC_INTERCONNECT_KEY":       "epic-interconnect-key-x",
		"SMTP_USERNAME":               "smtp-user",
		"SMTP_PASSWORD":               "smtp-pass",
		"KAFKA_USER":                  "kafka-user",
		"KAFKA_PASSWORD":              "kafka-pass",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example/v1/traces",
	} {
		t.Setenv(k, v)
	}
	return cfgPath
}
