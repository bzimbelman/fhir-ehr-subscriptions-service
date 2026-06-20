// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestE2E_ConfigInterpolation_BootsWithEnvAndFilePlaceholders builds the
// production binary, writes a config that uses BOTH `${env:VAR}` and
// `${file:/path}` placeholders (database URL via env, codec key
// material via mounted secret file), exports the env var, drops the
// real file, and asserts the binary boots and `/healthz` returns 200.
//
// Story #119. The placeholder pass MUST run on raw bytes so the binary
// connects to the real Postgres pool (proves env interpolation worked)
// and the codec activates with the real key (proves file interpolation
// worked — a literal `${file:...}` would be rejected as a non-base64
// codec key).
func TestE2E_ConfigInterpolation_BootsWithEnvAndFilePlaceholders(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Real 32-byte AES key written to a real file the binary will read
	// via `${file:/path}` — exactly how a Kubernetes-mounted Secret
	// looks at runtime.
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(keyBytes)

	secretsDir := t.TempDir()
	keyPath := filepath.Join(secretsDir, "at_rest_key")
	// Trailing newline mirrors `printf 'KEY\n' > /run/secrets/key`.
	if err := os.WriteFile(keyPath, []byte(keyB64+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	// Real env var the binary will read via `${env:DATABASE_URL}`.
	t.Setenv("STORY_119_DATABASE_URL", h.DBURL)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	binPath := filepath.Join(t.TempDir(), "fhir-subs")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/fhir-subs")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build cmd/fhir-subs: %v\n%s", err, out)
	}

	httpBind := "127.0.0.1:" + freePort(t)
	// /healthz, /readyz, /startup live on the probe listener (S-118
	// split-listener layout); the main http listener serves only the
	// auth-gated FHIR routes. Bind the probe listener to a free port so
	// the test does not collide with the production default 0.0.0.0:8081.
	probeBind := "127.0.0.1:" + freePort(t)
	yamlBody := fmt.Sprintf(`deployment:
  facility_id: e2e-interp
  environment: e2e
  log_level: info
  log_format: json
  mode: probe-only
adapter:
  id: default
server:
  http:
    bind: %s
    probe_bind: %s
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
database:
  url: ${env:STORY_119_DATABASE_URL}
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: ${file:%s}
auth:
  allow_dev_bypass: true
`, httpBind, probeBind, keyPath)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	binCtx, binCancel := context.WithCancel(ctx)
	defer binCancel()
	cmd := exec.CommandContext(binCtx, binPath, "--config", cfgPath)
	cmd.Env = os.Environ() // inherit the t.Setenv var
	stdoutW := newPrefixWriter(t, "fhir-subs out")
	stderrW := newPrefixWriter(t, "fhir-subs err")
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	// Poll /healthz until 200 — proves the binary parsed the config,
	// substituted the placeholders, opened the real Postgres pool, and
	// activated the real codec key. Probes live on the probe listener,
	// not the auth-gated http listener (S-118), so polling probeBind
	// is what the kubelet actually does in production.
	deadline := time.Now().Add(45 * time.Second)
	healthURL := "http://" + probeBind + "/healthz"
	var lastErr error
	var lastStatus int
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err != nil {
			lastErr = err
		} else {
			lastStatus = resp.StatusCode
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return // SUCCESS
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("binary never reported healthy: lastStatus=%d lastErr=%v", lastStatus, lastErr)
}
