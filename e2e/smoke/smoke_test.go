// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_smoke

// Package smoke is the ~3-minute PR-default e2e gate. The build tag
// `e2e_smoke` keeps it cheap so PR CI stays fast while still exercising
// the binary against the documented surface (probe-only boot, the
// /healthz / /readyz / /startup probes, and the --version banner). The
// full e2e suite under //go:build e2e remains the merge-to-main + label
// gated path.
//
// Acceptance criteria (OP #127):
//   - Smoke subset (~3 min) on every PR.
//   - Full suite on `main` and on PRs labeled `full-e2e`.
//   - CI MUST fail on e2e failure.
package smoke_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSmoke_VersionBanner(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "fhir-subs ") {
		t.Fatalf("--version output should start with `fhir-subs `, got %q", out)
	}
}

func TestSmoke_CheckConfigOK(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	cfg := filepath.Join(repoRoot(t), ".github", "ci", "smoke-config.yaml")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("missing fixture: %v", err)
	}
	out, err := exec.Command(bin, "--check-config", "--config", cfg).CombinedOutput()
	if err != nil {
		t.Fatalf("--check-config: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "config ok") {
		t.Fatalf("expected `config ok`, got %q", out)
	}
}

func TestSmoke_HealthzServesOnProbeListener(t *testing.T) {
	t.Parallel()
	bin := buildBinary(t)
	mainAddr := freeAddr(t)
	probeAddr := freeAddr(t)
	yaml := fmt.Sprintf(`
deployment:
  facility_id: smoke
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
`, mainAddr, probeAddr)
	cfg := writeFixture(t, yaml)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", cfg)
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
	})

	url := "http://" + probeAddr + "/healthz"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			var body struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("/healthz never returned 200\nstderr:\n%s", stderr.String())
}

func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fhir-subs")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/fhir-subs")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("repo root not found")
	return ""
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// silence unused-import warnings when build tag elides the file body.
var _ = errors.New
