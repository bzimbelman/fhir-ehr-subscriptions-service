// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config"
)

// B-35: Module.WatchSecretFiles must poll the on-disk mtime of every
// resolved ${file:...} placeholder and trigger a reload when the file
// changes. Without this, secret rotation by Vault Agent / cert-manager
// is unreachable when the orchestrator does not signal SIGHUP.
func TestWatchSecretFiles_TriggersReloadOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("first-value"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	cfgYAML := `deployment:
  facility_id: testbench
  log_level: info
storage:
  postgres:
    encryption_key: ${file:` + secretPath + `}
`
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
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

	// Mutate the secret on disk. mtime must move forward — sleep a tick
	// past the filesystem timestamp granularity.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(secretPath, []byte("second-value"), 0o600); err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	// On HFS/APFS, mtime granularity is ~1ns but Linux can be 1s; bump
	// it explicitly to make the test robust on both.
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
