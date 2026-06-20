// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestImmutableFieldChanges_PassesWhenOnlyHotFieldsDiffer pins the
// hot-vs-immutable contract: changing log_level alone is accepted; the
// returned rejection list is empty.
//
// Story #151.
func TestImmutableFieldChanges_PassesWhenOnlyHotFieldsDiffer(t *testing.T) {
	t.Parallel()
	prior := &Config{
		Deployment: DeploymentConfig{LogLevel: "info"},
		Database:   DatabaseConfig{URL: "postgres://app/db"},
	}
	next := &Config{
		Deployment: DeploymentConfig{LogLevel: "debug"},
		Database:   DatabaseConfig{URL: "postgres://app/db"},
	}
	if got := immutableFieldChanges(prior, next); len(got) != 0 {
		t.Fatalf("hot-only change should not reject; got=%v", got)
	}
}

// TestImmutableFieldChanges_RejectsDatabaseURLChange asserts the
// database.url path is on the immutable list per story #151 AC.
func TestImmutableFieldChanges_RejectsDatabaseURLChange(t *testing.T) {
	t.Parallel()
	prior := &Config{Database: DatabaseConfig{URL: "postgres://before/db"}}
	next := &Config{Database: DatabaseConfig{URL: "postgres://after/db"}}
	got := immutableFieldChanges(prior, next)
	if !reflect.DeepEqual(got, []string{"database.url"}) {
		t.Fatalf("want [database.url], got %v", got)
	}
}

// TestImmutableFieldChanges_RejectsBindAndTLS asserts every immutable
// path the AC names is wired up. We mutate one knob at a time and
// assert the path lands in the rejection list.
func TestImmutableFieldChanges_RejectsBindAndTLS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{
			name: "bind",
			mut:  func(c *Config) { c.Server.HTTP.Bind = "0.0.0.0:9999" },
			want: "server.http.bind",
		},
		{
			name: "probe_bind",
			mut:  func(c *Config) { c.Server.HTTP.ProbeBind = "0.0.0.0:9991" },
			want: "server.http.probe_bind",
		},
		{
			name: "insecure",
			mut:  func(c *Config) { c.Server.HTTP.Insecure = !c.Server.HTTP.Insecure },
			want: "server.http.insecure",
		},
		{
			name: "tls",
			mut:  func(c *Config) { c.Server.HTTP.TLS.MinVersion = "1.3" },
			want: "server.http.tls",
		},
		{
			name: "shutdown_grace_period",
			mut:  func(c *Config) { c.Lifecycle.ShutdownGracePeriod = 99 * time.Second },
			want: "lifecycle.shutdown_grace_period",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prior := &Config{
				Server: ServerConfig{HTTP: HTTPConfig{
					Bind: "0.0.0.0:8443", ProbeBind: "0.0.0.0:8081", Insecure: true,
					TLS: TLSConfig{MinVersion: "1.2"},
				}},
				Lifecycle: LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
			}
			next := *prior
			tc.mut(&next)
			got := immutableFieldChanges(prior, &next)
			found := false
			for _, p := range got {
				if p == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("want %q in rejection list, got %v", tc.want, got)
			}
		})
	}
}

// TestScanSecretFilePaths_ExtractsEverFilePlaceholder asserts the
// watcher's path-extraction logic finds every ${file:...} regardless
// of YAML position or duplication.
//
// Story #152.
func TestScanSecretFilePaths_ExtractsEveryFilePlaceholder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	body := `
auth:
  audience: "${file:/etc/audience.txt}"
  client_secret: "${file:/etc/secret.txt}"
codec:
  keys:
    - version: 1
      material: "${file:/etc/audience.txt}"
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}
	got := scanSecretFilePaths(cfgPath)
	want := []string{"/etc/audience.txt", "/etc/secret.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scanSecretFilePaths: got %v, want %v", got, want)
	}
}

// TestReloadCoordinator_AppliesLogLevelChange exercises the full
// coordinator path on a real on-disk config: we mutate log_level on
// the file and assert the log-level var is swapped to the new
// threshold. No SIGHUP signal involved — this is a unit test of the
// reload path, not the dispatcher.
//
// Story #151.
func TestReloadCoordinator_AppliesLogLevelChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	body := []byte(`deployment:
  facility_id: f1
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: a1
server:
  http:
    bind: 127.0.0.1:18443
    probe_bind: 127.0.0.1:18081
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
auth:
  allow_dev_bypass: true
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg.Source = &ConfigSource{Path: cfgPath}

	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logs := &strings.Builder{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level}))

	coord := newReloadCoordinator(cfg, logger, level)

	mutated := strings.Replace(string(body), "log_level: info", "log_level: debug", 1)
	if err := os.WriteFile(cfgPath, []byte(mutated), 0o600); err != nil {
		t.Fatalf("rewrite cfg: %v", err)
	}

	coord.reload(reloadTriggerSIGHUP)

	if level.Level() != slog.LevelDebug {
		t.Fatalf("level was not swapped: have %v, want %v", level.Level(), slog.LevelDebug)
	}
	out := logs.String()
	if !strings.Contains(out, "config reload applied") {
		t.Fatalf("missing 'config reload applied' log line: %s", out)
	}
	if !strings.Contains(out, "sighup") {
		t.Fatalf("missing trigger=sighup label: %s", out)
	}
}

// TestReloadCoordinator_RejectsImmutableChange asserts the coordinator
// emits the WARN line and does NOT swap state when the operator
// rotates an immutable field on disk. Also asserts hot-apply hooks
// are NOT called.
//
// Story #151.
func TestReloadCoordinator_RejectsImmutableChange(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.yaml")
	body := []byte(`deployment:
  facility_id: f1
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: a1
server:
  http:
    bind: 127.0.0.1:18443
    probe_bind: 127.0.0.1:18081
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
auth:
  allow_dev_bypass: true
database:
  url: postgres://before/db
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg.Source = &ConfigSource{Path: cfgPath}

	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logs := &strings.Builder{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level}))

	coord := newReloadCoordinator(cfg, logger, level)

	var hotApplied atomic.Int32
	coord.registerHotApply(func(_, _ *Config) { hotApplied.Add(1) })

	mutated := strings.Replace(string(body),
		"url: postgres://before/db",
		"url: postgres://after/db", 1)
	if err := os.WriteFile(cfgPath, []byte(mutated), 0o600); err != nil {
		t.Fatalf("rewrite cfg: %v", err)
	}

	coord.reload(reloadTriggerSIGHUP)

	out := logs.String()
	if !strings.Contains(out, "config reload rejected") {
		t.Fatalf("missing 'config reload rejected' line: %s", out)
	}
	if !strings.Contains(out, "database.url") {
		t.Fatalf("rejected_paths did not mention database.url: %s", out)
	}
	if hotApplied.Load() != 0 {
		t.Fatalf("hot-apply hook fired after immutable rejection (count=%d)", hotApplied.Load())
	}
}

// TestReloadCoordinator_FileMtimeWatcherFiresReload asserts the
// coordinator's mtime poller observes a ${file:...}-backed rotation
// and triggers reload(file_mtime) without any signal.
//
// Story #152.
func TestReloadCoordinator_FileMtimeWatcherFiresReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyPath, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), 0o600); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	cfgPath := filepath.Join(dir, "cfg.yaml")
	body := []byte(`deployment:
  facility_id: f1
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: a1
server:
  http:
    bind: 127.0.0.1:18443
    probe_bind: 127.0.0.1:18081
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
auth:
  audience: "${file:` + keyPath + `}"
  allow_dev_bypass: true
`)
	if err := os.WriteFile(cfgPath, body, 0o600); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg.Source = &ConfigSource{Path: cfgPath}

	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logs := &strings.Builder{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level}))

	coord := newReloadCoordinator(cfg, logger, level)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord.startSecretFileWatcher(ctx, 25*time.Millisecond)

	time.Sleep(60 * time.Millisecond)

	if err := os.WriteFile(keyPath, []byte("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"), 0o600); err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(keyPath, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out := logs.String()
		if strings.Contains(out, "config reload applied") &&
			strings.Contains(out, "file_mtime") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("mtime rotation never produced a 'config reload applied' line with trigger=file_mtime; logs:\n%s", logs.String())
}
