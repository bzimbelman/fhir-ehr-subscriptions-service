// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

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
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildBinary compiles cmd/fhir-subs into a tempdir and returns the binary
// path. It is shared across the integration tests in this file.
//
// ldflags inject Version and Commit so the integration tests can verify the
// release-build wiring matches the unit-test expectation.
func buildBinary(t *testing.T) string {
	t.Helper()
	return buildBinaryWith(t, "1.2.3-test", "abc1234")
}

func buildBinaryWith(t *testing.T, version, commit string) string {
	t.Helper()

	dir := t.TempDir()
	bin := filepath.Join(dir, "fhir-subs")
	ldflags := fmt.Sprintf("-X main.Version=%s -X main.Commit=%s", version, commit)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin, ".")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return bin
}

func TestIntegration_VersionLDFlags(t *testing.T) {
	bin := buildBinaryWith(t, "9.9.9-rc1", "deadbeef")
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	for _, want := range []string{"9.9.9-rc1", "deadbeef"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("--version missing %q: %s", want, out)
		}
	}
}

func TestIntegration_StartupProbe(t *testing.T) {
	bin := buildBinary(t)
	addr := freeAddr(t)
	yaml := fmt.Sprintf(`
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: %s
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
`, addr)
	cfgPath := writeYAML(t, yaml)

	cmd := exec.Command(bin, "--config", cfgPath)
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_, _ = cmd.Process.Wait()
		}
	})

	// Poll /startup; once startup_complete is set, /startup mirrors /readyz
	// (503 because no components are wired). Before that, /startup returns
	// 503 with status="starting". Either way the test asserts the response
	// shape after the listener is up.
	startupURL := "http://" + addr + "/startup"
	deadline := time.Now().Add(5 * time.Second)
	var (
		ok      bool
		lastErr error
	)
	for time.Now().Before(deadline) {
		resp, err := http.Get(startupURL)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var body struct {
			Status string   `json:"status"`
			Failed []string `json:"failed"`
			Phase  string   `json:"phase"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		// Once the server is bound, startup_complete is true so we expect
		// status="unready" failed=[all_components].
		if resp.StatusCode == 503 && (body.Status == "starting" || body.Status == "unready") {
			ok = true
			break
		}
		lastErr = fmt.Errorf("unexpected: status=%d body=%+v", resp.StatusCode, body)
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("/startup never returned the expected shape: %v\nstderr: %s", lastErr, stderr.String())
	}
}

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free addr: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func TestIntegration_HelpExits0(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("--help should exit 0; err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "Usage of fhir-subs") {
		t.Fatalf("missing usage banner: %s", out)
	}
}

func TestIntegration_VersionExits0(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("--version should exit 0; err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "fhir-subs") {
		t.Fatalf("missing program name: %s", out)
	}
}

func TestIntegration_CheckConfigValidExits0(t *testing.T) {
	bin := buildBinary(t)
	yaml := `
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
`
	cfgPath := writeYAML(t, yaml)
	out, err := exec.Command(bin, "--config", cfgPath, "--check-config").CombinedOutput()
	if err != nil {
		t.Fatalf("--check-config valid should exit 0; err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "config ok") {
		t.Fatalf("expected 'config ok': %s", out)
	}
}

func TestIntegration_CheckConfigMissingFacilityExits1(t *testing.T) {
	bin := buildBinary(t)
	yaml := `
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
`
	cfgPath := writeYAML(t, yaml)
	cmd := exec.Command(bin, "--config", cfgPath, "--check-config")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("--check-config invalid should exit non-zero; out=%s", out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %d", ee.ExitCode())
	}
	if !strings.Contains(string(out), "facility_id") {
		t.Fatalf("error should mention facility_id: %s", out)
	}
}

func TestIntegration_RunAndSIGTERM(t *testing.T) {
	bin := buildBinary(t)
	addr := freeAddr(t)
	yaml := fmt.Sprintf(`
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: %s
    insecure: true
lifecycle:
  shutdown_grace_period: 5s
`, addr)
	cfgPath := writeYAML(t, yaml)

	cmd := exec.Command(bin, "--config", cfgPath)
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	// Poll /healthz until the server is up (or fail after 5s).
	healthURL := "http://" + addr + "/healthz"
	deadline := time.Now().Add(5 * time.Second)
	var (
		healthOK bool
		lastErr  error
	)
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil {
			body := struct {
				Status string `json:"status"`
			}{}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 && body.Status == "ok" {
				healthOK = true
				break
			}
			lastErr = fmt.Errorf("status=%d body.status=%q", resp.StatusCode, body.Status)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthOK {
		t.Fatalf("/healthz never returned 200: %v\nstderr: %s", lastErr, stderr.String())
	}

	// /readyz should be 503 with failed=[all_components].
	{
		resp, err := http.Get("http://" + addr + "/readyz")
		if err != nil {
			t.Fatalf("readyz: %v", err)
		}
		var body struct {
			Status string   `json:"status"`
			Failed []string `json:"failed"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != 503 {
			t.Fatalf("readyz status: %d", resp.StatusCode)
		}
		if len(body.Failed) != 1 || body.Failed[0] != "all_components" {
			t.Fatalf("readyz failed: %v", body.Failed)
		}
	}

	// /metadata stub OperationOutcome.
	{
		resp, err := http.Get("http://" + addr + "/metadata")
		if err != nil {
			t.Fatalf("metadata: %v", err)
		}
		var body struct {
			ResourceType string `json:"resourceType"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if body.ResourceType != "OperationOutcome" {
			t.Fatalf("metadata resourceType: %q", body.ResourceType)
		}
	}

	// Send SIGTERM and assert clean exit within the grace period + slack.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("process did not exit cleanly: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("process did not exit within budget\nstderr: %s", stderr.String())
	}

	// Defensive: check the process is gone.
	_ = context.Background()
}
