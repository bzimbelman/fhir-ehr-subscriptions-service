// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestE2E_ProdBinary_DBUnreachable_FailsLoud asserts the production
// binary fails fast and loud when the configured Postgres is
// unreachable. Specifically:
//
//   - the binary exits non-zero,
//   - stderr contains a clear "database" diagnostic,
//   - the HTTP listener never binds, so /healthz / /readyz are not
//     reachable on the configured port.
//
// This proves the wiring's failure-mode contract: a misconfigured DB
// at startup is not silently degraded into "probe-only mode" — k8s
// must restart the pod, not route traffic to it.
//
// B-4.
func TestE2E_ProdBinary_DBUnreachable_FailsLoud(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dockerGate(t, harnessSetupErr, allowNoDocker)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	httpPort := freePort(t)

	// Build the binary.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	binPath := filepath.Join(t.TempDir(), "fhir-subs")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/fhir-subs")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Use an unreachable port deliberately. The pgxpool's connect
	// retries with the AcquireTimeout; we override to a tight
	// connect_timeout so the test runs in seconds, not minutes.
	unreachableURL := "postgres://test:test@127.0.0.1:1/no_such_db?connect_timeout=2&sslmode=disable"

	key := make([]byte, 32)
	keyB64 := base64.StdEncoding.EncodeToString(key)

	yamlBody := fmt.Sprintf(`deployment:
  facility_id: e2e
  environment: e2e
adapter:
  id: default
server:
  http:
    bind: 127.0.0.1:%s
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
database:
  url: %s
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: %s
auth:
  audience: ""
`, httpPort, unreachableURL, keyB64)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	binCtx, binCancel := context.WithTimeout(ctx, 50*time.Second)
	defer binCancel()
	cmd := exec.CommandContext(binCtx, binPath, "--config", cfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = newPrefixWriter(t, "fhir-subs out")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	// While it is running (or trying to), confirm the listener is NOT
	// bound. The binary should fail before binding, but if it raced
	// ahead of us we'd want to see that as a regression — sleep
	// briefly first so the bound check is meaningful.
	time.Sleep(2 * time.Second)
	resp, err := http.Get("http://127.0.0.1:" + httpPort + "/healthz")
	if err == nil {
		_ = resp.Body.Close()
		t.Errorf("binary bound the listener despite unreachable DB; status=%d",
			resp.StatusCode)
	}

	// Wait for exit.
	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("binary exited 0; want non-zero on DB-unreachable")
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("waitErr type: %T (%v)", waitErr, waitErr)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("exit code 0; want non-zero")
	}

	stderrStr := stderr.String()
	t.Logf("binary stderr:\n%s", stderrStr)
	if !bytes.Contains(stderr.Bytes(), []byte("database")) &&
		!bytes.Contains(stderr.Bytes(), []byte("postgres")) &&
		!bytes.Contains(stderr.Bytes(), []byte("dial")) {
		t.Errorf("stderr lacks DB diagnostic; got %q", stderrStr)
	}
}
