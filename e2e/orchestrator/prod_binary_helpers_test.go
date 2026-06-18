// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// prodBinaryHandle wraps the running fhir-subs binary so a test can poke
// it (HTTP, MLLP) and shut it down at test end.
type prodBinaryHandle struct {
	cmd      *exec.Cmd
	httpAddr string
	mllpAddr string
	cancel   func()
}

// HTTPURL returns the full http base URL the test should hit.
func (h *prodBinaryHandle) HTTPURL() string { return "http://" + h.httpAddr }

// MLLPAddr returns the bound MLLP host:port (empty when no MLLP
// listener was configured).
func (h *prodBinaryHandle) MLLPAddr() string { return h.mllpAddr }

// Stop stops the running binary by canceling its parent context (the
// signal handler in main.go translates that to a graceful shutdown).
// Returns the binary's exit code.
func (h *prodBinaryHandle) Stop(t *testing.T, gracePeriod time.Duration) int {
	t.Helper()
	if h.cancel != nil {
		h.cancel()
	}
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case err := <-done:
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		if err != nil {
			t.Logf("binary exit: %v", err)
			return -1
		}
		return 0
	case <-time.After(gracePeriod + 5*time.Second):
		_ = h.cmd.Process.Kill()
		<-done
		return -2
	}
}

// SignalTerm sends SIGTERM to the running binary so a test can exercise
// the signal-driven graceful shutdown path.
func (h *prodBinaryHandle) SignalTerm(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}
}

// prodBinaryConfig describes the config the helper writes to a
// temporary YAML before launching the binary.
type prodBinaryConfig struct {
	DatabaseURL  string
	FacilityID   string
	AdapterID    string
	AuthAudience string
	HTTPBind     string
	MLLPBind     string // empty = no MLLP
	Insecure     bool
	GracePeriod  time.Duration
}

// startProdBinary builds and launches cmd/fhir-subs against the
// supplied DB URL. Returns a handle the test uses to drive it.
//
// The function blocks until /healthz returns 200 or
// startupTimeout elapses, so callers can immediately POST against the
// returned URL.
func startProdBinary(t *testing.T, ctx context.Context, cfg prodBinaryConfig) *prodBinaryHandle {
	t.Helper()

	if cfg.HTTPBind == "" {
		cfg.HTTPBind = "127.0.0.1:" + freePort(t)
	}
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = 5 * time.Second
	}
	if cfg.AuthAudience == "" {
		cfg.AuthAudience = "https://api.test.local"
	}

	// Build the binary into a temp dir.
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

	// Generate a 32-byte AES key for the codec.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)

	mllpBlock := ""
	mllpAddrPlaceholder := ""
	if cfg.MLLPBind != "" {
		mllpBlock = fmt.Sprintf(`
mllp:
  listeners:
    - name: prod-feed
      bind: %s
  max_message_bytes: 1048576
  persist_timeout: 5s
  shutdown_drain_grace: 5s
`, cfg.MLLPBind)
		mllpAddrPlaceholder = cfg.MLLPBind
	}

	yamlBody := fmt.Sprintf(`deployment:
  facility_id: %s
  environment: e2e
  log_level: info
  log_format: json
adapter:
  id: %s
server:
  http:
    bind: %s
    insecure: %t
lifecycle:
  shutdown_grace_period: %s
database:
  url: %s
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: %s
auth:
  audience: %s
pipeline:
  hl7_processor:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  matcher:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  submatcher:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  scheduler:
    claim_batch_size: 16
    idle_poll_interval: 100ms
  correlation_hold_window: 1s
%s
`,
		cfg.FacilityID, cfg.AdapterID, cfg.HTTPBind, cfg.Insecure,
		cfg.GracePeriod.String(),
		cfg.DatabaseURL, keyB64, cfg.AuthAudience, mllpBlock,
	)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	binCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(binCtx, binPath, "--config", cfgPath)
	cmd.Stdout = newPrefixWriter(t, "fhir-subs out")
	cmd.Stderr = newPrefixWriter(t, "fhir-subs err")
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start binary: %v", err)
	}

	h := &prodBinaryHandle{
		cmd:      cmd,
		httpAddr: cfg.HTTPBind,
		mllpAddr: mllpAddrPlaceholder,
		cancel:   cancel,
	}

	// Wait for /healthz to flip green.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.HTTPURL() + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return h
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.Stop(t, cfg.GracePeriod)
	t.Fatalf("binary never reported /healthz=200")
	return nil
}

// freePort picks an ephemeral port and returns it as a string. The
// returned port is closed before return, so a small race-window exists
// before the binary binds it; tests pick this rather than rely on the
// binary's "0" support so the address is deterministic for log lines.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen :0: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()
	return fmt.Sprintf("%d", addr.Port)
}

// findRepoRoot walks up from the test binary's CWD until it finds the
// go.mod that declares this module. Used because exec.Command("go
// build") needs the repo root, and the e2e tests run with a CWD
// inside e2e/orchestrator.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		f := filepath.Join(dir, "go.mod")
		body, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(body),
			"github.com/bzimbelman/fhir-ehr-subscriptions-service") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod with the project module not found")
		}
		dir = parent
	}
}

// prefixWriter prefixes every newline-terminated chunk with "[name]" so
// concurrent test logs can be told apart. Implements io.Writer over
// t.Log.
type prefixWriter struct {
	t      *testing.T
	prefix string
	buf    []byte
}

func newPrefixWriter(t *testing.T, prefix string) *prefixWriter {
	return &prefixWriter{t: t, prefix: prefix}
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		idx := -1
		for i, c := range p.buf {
			if c == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(p.buf[:idx]), "\r")
		p.buf = p.buf[idx+1:]
		p.t.Logf("[%s] %s", p.prefix, line)
	}
	return len(b), nil
}
