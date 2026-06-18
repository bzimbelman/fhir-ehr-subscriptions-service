// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config"
)

// TestB35_FileMtimePollTriggersReload (B-35) starts the config module
// with file-mtime polling enabled, mutates a ${file:...}-backed secret
// on disk, and asserts the watcher fires Reload(TriggerFileMtime)
// without any signal.
//
// This is the rotation path Vault Agent / cert-manager use: rotate
// the on-disk file, no signaling rights into the process. Pre-fix the
// process kept the old value indefinitely.
func TestB35_FileMtimePollTriggersReload(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "encryption-key.txt")
	cfgPath := writeBootableConfig(t, &secretPath)

	mod, _, err := config.Start(context.Background(), config.CliArgs{
		ConfigPath: cfgPath,
	}, config.Context{Clock: time.Now})
	if err != nil {
		t.Fatalf("config start: %v", err)
	}
	defer func() { _ = mod.Shutdown(context.Background()) }()

	var mtimeCount atomic.Int64
	mod.OnReload(func(trigger string) {
		if trigger == "file_mtime" {
			mtimeCount.Add(1)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mod.WatchSecretFiles(ctx, 50*time.Millisecond)

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
		if mtimeCount.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := mtimeCount.Load(); got < 1 {
		t.Fatalf("file_mtime poll did not fire reload; calls=%d", got)
	}
}
