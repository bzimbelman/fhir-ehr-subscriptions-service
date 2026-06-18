// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config"
)

// B-35: Module.WatchSecretFiles must poll the on-disk mtime of every
// resolved ${file:...} placeholder and trigger a reload when the file
// changes. Without this, secret rotation by Vault Agent / cert-manager
// is unreachable when the orchestrator does not signal SIGHUP.
//
// We boot off the canonical architecture_example.yaml fixture (the same
// one TestIntegrationLoadsArchitectureExampleYAML uses) but rewrite a
// single placeholder to ${file:...} so the watcher has at least one
// path to poll.
func TestWatchSecretFiles_TriggersReloadOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "encryption-key.txt")
	if err := os.WriteFile(secretPath, []byte("01234567890abcdef01234567890abcdef"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	src, readErr := os.ReadFile("testdata/architecture_example.yaml")
	if readErr != nil {
		t.Fatalf("read fixture: %v", readErr)
	}
	rewritten := strings.Replace(
		string(src),
		"at_rest_key: \"${env:STORAGE_ENCRYPTION_KEY}\"",
		"at_rest_key: \"${file:"+secretPath+"}\"",
		1,
	)
	if !strings.Contains(rewritten, "${file:"+secretPath+"}") {
		t.Fatalf("rewrite did not land in fixture")
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	for k, v := range map[string]string{
		"DATABASE_URL":                "postgres://example.org/db",
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

	mod, _, err := config.Start(context.Background(), config.CliArgs{
		ConfigPath: cfgPath,
	}, config.Context{Clock: time.Now})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	var reloadCalls atomic.Int64
	mod.OnReload(func(trigger string) {
		if trigger == "file_mtime" {
			reloadCalls.Add(1)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mod.WatchSecretFiles(ctx, 50*time.Millisecond)

	// Mutate the secret on disk. Bump mtime explicitly to be robust
	// against fast-clock filesystems where consecutive WriteFile calls
	// can land on the same nanosecond.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(secretPath, []byte("ffffffffffffffffffffffffffffffff"), 0o600); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(secretPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if reloadCalls.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := reloadCalls.Load(); got < 1 {
		t.Fatalf("file_mtime trigger did not fire reload; calls=%d", got)
	}
}
