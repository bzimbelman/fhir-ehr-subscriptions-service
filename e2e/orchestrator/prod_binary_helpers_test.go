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
	"sync"
	"syscall"
	"testing"
	"time"
)

// prodBinaryHandle wraps the running fhir-subs binary so a test can poke
// it (HTTP, MLLP) and shut it down at test end.
type prodBinaryHandle struct {
	cmd       *exec.Cmd
	httpAddr  string
	probeAddr string
	mllpAddr  string
	cancel    func()
	stdout    *prefixWriter
	stderr    *prefixWriter
}

// Stderr returns the captured-line collector for the binary's stderr
// stream so tests can assert on emitted log content (e.g. catalog
// reload, handshake outcome).
func (h *prodBinaryHandle) Stderr() *prefixWriter { return h.stderr }

// SignalHUP sends SIGHUP to the binary so a test can drive the reload
// handler (D-1: catalog reload).
func (h *prodBinaryHandle) SignalHUP(t *testing.T) {
	t.Helper()
	if err := h.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("signal HUP: %v", err)
	}
}

// HTTPURL returns the full http base URL the test should hit.
func (h *prodBinaryHandle) HTTPURL() string { return "http://" + h.httpAddr }

// ProbeURL returns the unauthenticated probe-listener base URL where
// /healthz, /readyz, /startup are served (S-118 split-listener layout).
func (h *prodBinaryHandle) ProbeURL() string { return "http://" + h.probeAddr }

// MLLPAddr returns the bound MLLP host:port (empty when no MLLP
// listener was configured).
func (h *prodBinaryHandle) MLLPAddr() string { return h.mllpAddr }

// Stop stops the running binary by sending SIGINT (so main.go's
// signal handler runs the graceful lifecycle sequencer) and waiting
// for the process to exit. Returns the binary's exit code.
//
// OP #341: previously this called h.cancel() to drive shutdown, but
// exec.CommandContext sends SIGKILL when the parent context is
// canceled — that interrupts the lifecycle sequencer mid-phase, so the
// "lifecycle shutdown complete" log line and any in-flight phase
// duration observations are lost. Sending SIGINT instead lets the
// signal dispatcher complete every phase under the grace budget. We
// only fall back to h.cancel() (SIGKILL) when the binary fails to
// exit within (gracePeriod + 5s) — that path is the safety net for a
// stuck shutdown.
func (h *prodBinaryHandle) Stop(t *testing.T, gracePeriod time.Duration) int {
	t.Helper()
	// If the process has already been signaled (e.g. test called
	// SignalTerm), Signal will return "process already finished" once
	// the binary has exited — that's fine. Best-effort send.
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Signal(os.Interrupt)
	}
	done := make(chan error, 1)
	go func() { done <- h.cmd.Wait() }()
	select {
	case err := <-done:
		if h.cancel != nil {
			h.cancel()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		if err != nil {
			t.Logf("binary exit: %v", err)
			return -1
		}
		return 0
	case <-time.After(gracePeriod + 5*time.Second):
		// Graceful budget exhausted — escalate to SIGKILL via context
		// cancellation, then wait so cmd.Wait returns and the runtime
		// reaps the process.
		if h.cancel != nil {
			h.cancel()
		}
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

	// TopicsCatalogDir, when non-empty, is rendered into topics.catalog_dir
	// so the production binary loads the operator topic JSON files at
	// startup (D-1).
	TopicsCatalogDir string

	// AuthAllowInsecureJWKS rendered into auth.allow_insecure_jwks. The
	// rest-hook handshake activator reuses this flag to allow http://
	// subscriber endpoints in e2e (D-2). Off by default — production is
	// https-only.
	AuthAllowInsecureJWKS bool

	// TracingOTLPEndpoint, when non-empty, is rendered into a top-level
	// `tracing:` block so the production binary configures the OTLP
	// exporter. Empty means "do not emit a tracing block" (current
	// default before story #94 ships). Story #94.
	TracingOTLPEndpoint string

	// TracingSampleRate is the head-sampling rate rendered alongside the
	// endpoint. Ignored when TracingOTLPEndpoint is empty. Defaults to
	// 1.0 in helper rendering (sample everything in tests) when zero.
	// Story #94.
	TracingSampleRate float64

	// TracingInsecure renders tracing.insecure: true so plaintext to
	// non-loopback exporters works in e2e. Ignored when
	// TracingOTLPEndpoint is empty. Story #94.
	TracingInsecure bool

	// SubscriptionCreateRateLimitBurst, when > 0, renders an
	// auth.subscription_create_rate_limit block into the binary's
	// config. RefillPerSecond=0 pins the bucket at Burst (story #104
	// AC #5: prove the wiring fires under real binary boot).
	SubscriptionCreateRateLimitBurst           int
	SubscriptionCreateRateLimitRefillPerSecond float64

	// WSBindingTokenRateLimitBurst, when > 0, renders an
	// auth.ws_binding_token_rate_limit block into the binary's config
	// (story #104).
	WSBindingTokenRateLimitBurst           int
	WSBindingTokenRateLimitRefillPerSecond float64

	// AuthTokenURL, when non-empty, renders auth.token_url so the
	// binary mounts the SMART Backend Services /token endpoint. The
	// caller-supplied URL becomes the audience the binary expects on
	// inbound client_assertion JWTs.
	AuthTokenURL string

	// AuthIssuedSecret is a base64-encoded ≥32-byte HMAC secret. When
	// non-empty alongside AuthTokenURL, the binary signs access tokens
	// with HS256 and the verifier accepts them.
	AuthIssuedSecret string

	// AuthIssuedIssuer is the iss claim baked into bearer JWTs minted
	// by the binary's /token endpoint. Required when AuthTokenURL is
	// set; ignored otherwise.
	AuthIssuedIssuer string

	// AuthAccessTokenTTL is the lifetime of bearer JWTs minted by the
	// /token endpoint. Defaults to 5m in YAML rendering when zero.
	AuthAccessTokenTTL time.Duration

	// LogFormat overrides deployment.log_format ("text" or "json").
	// Empty defaults to "json" — preserving the prior helper default.
	// Story #160 e2e exercises log_format: text.
	LogFormat string

	// LogLevel overrides deployment.log_level. Empty defaults to
	// "info". Story #151 e2e flips this between info/debug across a
	// SIGHUP reload.
	LogLevel string

	// ConfigPathSink, when non-nil, receives the rendered YAML config
	// path so a test can rewrite the file in-place and drive a
	// SIGHUP-triggered or mtime-triggered reload (stories #151, #152).
	ConfigPathSink *string

	// SecretFilePollInterval controls how aggressively the secret-file
	// mtime watcher polls. Tests set this to a small value so a
	// rotation is observed within the test deadline. Empty defaults to
	// the production interval (60s) which would time out. Story #152.
	SecretFilePollInterval time.Duration

	// CodecKeyMaterialFile, when non-empty, renders
	// codec.keys[0].material as ${file:<path>} so the binary
	// interpolates the key from disk. The caller seeds the file. Story
	// #152 e2e exercises rotation through this seam.
	CodecKeyMaterialFile string

	// URLValidatorAllowHTTP renders url_validator.allow_http: true so a
	// test that POSTs a /Subscription with an http://127.0.0.1 endpoint
	// passes the SSRF guard at create-time. Default false (production
	// posture; https-only). OP #184 / #341.
	URLValidatorAllowHTTP bool
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
	// Story #290: /healthz, /readyz, /startup live on a separate
	// probe listener (S-118). Pick a per-test ephemeral port so the
	// default 0.0.0.0:8081 doesn't collide when tests run in parallel
	// or when leftover binaries are still bound.
	probeBind := "127.0.0.1:" + freePort(t)
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = 5 * time.Second
	}
	// Empty AuthAudience means "do not configure auth" — buildAuthEndpoints
	// returns (nil, nil, nil) in that case so the chi router runs with a
	// no-op middleware. Tests that explicitly want bearer auth set
	// AuthAudience to a non-empty string. (Story #94 audit test relies on
	// this so the POST /Subscription request reaches the handler that
	// writes the audit row.)

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
	codecMaterialField := keyB64
	if cfg.CodecKeyMaterialFile != "" {
		codecMaterialField = "${file:" + cfg.CodecKeyMaterialFile + "}"
	}

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

	// Story #290: production mode requires topics.catalog_dir. Tests
	// that configure MLLP (e.g. the graceful-shutdown smoke that does
	// not actually drive a topic) historically left this empty and
	// relied on probe-only mode to skip the check. Now that MLLP forces
	// production mode (probe-only Validate() rejects mllp.listeners),
	// stage an empty directory so the production check passes without
	// requiring every MLLP-using test to write boilerplate.
	if cfg.TopicsCatalogDir == "" && cfg.MLLPBind != "" {
		cfg.TopicsCatalogDir = t.TempDir()
	}
	topicsBlock := ""
	if cfg.TopicsCatalogDir != "" {
		topicsBlock = fmt.Sprintf(`
topics:
  catalog_dir: %s
`, cfg.TopicsCatalogDir)
	}

	authInsecureLine := ""
	urlValidatorBlock := ""
	if cfg.AuthAllowInsecureJWKS {
		authInsecureLine = "\n  allow_insecure_jwks: true"
		// Story #290: e2e rest-hook receivers bind to 127.0.0.1. The
		// URL validator's loopback / private-IP rejection (B-11) would
		// reject those endpoints at create-time. Pair AllowInsecureJWKS
		// (allow http://) with an AllowSubscriberHosts entry for the
		// loopback IP literals so the test's harness rest-hook URL
		// passes both gates. Production leaves both empty.
		authInsecureLine += "\n  allow_subscriber_hosts: [\"127.0.0.1\", \"::1\"]"
		// Story #337: OP #184 split url_validator.* (subscriber endpoint
		// scheme + host gates) from auth.allow_insecure_jwks (insecure
		// JWKS) — the two trust decisions are independent. The legacy
		// auth.allow_subscriber_hosts list is still merged into AllowHosts
		// for backward compat (see wiring.go), but the AllowHTTP gate now
		// lives ONLY under url_validator. Emit the dedicated block here
		// so e2e tests that opt into insecure JWKS for a dev IDP also get
		// http:// rest-hook endpoints accepted on POST /Subscription.
		// Production leaves url_validator empty (https-only, no allow-list).
		urlValidatorBlock = "\nurl_validator:\n  allow_http: true\n  allow_hosts:\n    - \"127.0.0.1\"\n    - \"::1\"\n"
	}
	// Story #117: an empty AuthAudience means "skip the bearer
	// middleware in this e2e". Validate() will reject an empty
	// audience in production mode unless auth.allow_dev_bypass=true is
	// set explicitly. Render the flag so existing e2e tests that
	// want the dev-only no-auth path keep booting.
	if cfg.AuthAudience == "" {
		authInsecureLine += "\n  allow_dev_bypass: true"
	}

	// Story #104: render the per-client rate-limit blocks into the
	// auth section so the production binary's wiring constructs the
	// chi middleware on POST /Subscription and on $get-ws-binding-token.
	if cfg.SubscriptionCreateRateLimitBurst > 0 {
		authInsecureLine += fmt.Sprintf("\n  subscription_create_rate_limit:\n    burst: %d\n    refill_per_second: %v",
			cfg.SubscriptionCreateRateLimitBurst, cfg.SubscriptionCreateRateLimitRefillPerSecond)
	}
	if cfg.WSBindingTokenRateLimitBurst > 0 {
		authInsecureLine += fmt.Sprintf("\n  ws_binding_token_rate_limit:\n    burst: %d\n    refill_per_second: %v",
			cfg.WSBindingTokenRateLimitBurst, cfg.WSBindingTokenRateLimitRefillPerSecond)
	}

	// Real-auth path: token endpoint config so the binary mounts /token
	// and signs bearer JWTs with the supplied HS256 secret. Required
	// when an e2e wants to drive POST /Subscription with a real
	// authenticated principal.
	if cfg.AuthTokenURL != "" {
		ttl := cfg.AuthAccessTokenTTL
		if ttl == 0 {
			ttl = 5 * time.Minute
		}
		authInsecureLine += fmt.Sprintf("\n  token_url: %s\n  issued_secret: %s\n  issued_issuer: %s\n  access_token_ttl: %s",
			cfg.AuthTokenURL, cfg.AuthIssuedSecret, cfg.AuthIssuedIssuer, ttl.String())
	}

	// Story #290: production mode requires auth.trusted_issuers when
	// auth.audience is set. The actual JWKS-key trust is stored per
	// client in auth_clients (see TrustedIssue doc), so the contents
	// are advisory in tests — we just need a non-empty entry that
	// matches the binary-issued iss claim so the verifier loads the
	// signing keys when a real bearer arrives.
	if cfg.AuthAudience != "" {
		issuer := cfg.AuthIssuedIssuer
		if issuer == "" {
			issuer = cfg.AuthAudience
		}
		authInsecureLine += fmt.Sprintf("\n  trusted_issuers:\n    - issuer: %s\n      audience: %s",
			issuer, cfg.AuthAudience)
	}

	if cfg.URLValidatorAllowHTTP && urlValidatorBlock == "" {
		// OP #184 / #341: url_validator.allow_http is decoupled from
		// auth.allow_insecure_jwks. Tests that POST a /Subscription with
		// an http://127.0.0.1 endpoint (because the harness rest-hook
		// receiver is local-only) but DON'T opt into insecure JWKS can
		// still opt into the http scheme via this explicit knob. Skipped
		// when AuthAllowInsecureJWKS already emitted the block above.
		urlValidatorBlock = "\nurl_validator:\n  allow_http: true\n  allow_hosts: [\"127.0.0.1\", \"::1\"]\n"
	}

	tracingBlock := ""
	if cfg.TracingOTLPEndpoint != "" {
		sampleRate := cfg.TracingSampleRate
		if sampleRate == 0 {
			sampleRate = 1.0
		}
		insecureLine := ""
		if cfg.TracingInsecure {
			insecureLine = "\n  insecure: true"
		}
		tracingBlock = fmt.Sprintf(`
tracing:
  otlp_endpoint: %s
  sample_rate: %v%s
`, cfg.TracingOTLPEndpoint, sampleRate, insecureLine)
	}

	// Story #117: e2e tests historically constructed Configs that
	// satisfied buildProductionRuntime (real DB, codec, channels) but
	// did not configure the full production posture (no auth audience,
	// no topics catalog dir, no MLLP listeners, no trusted issuers).
	// Render mode=probe-only so the production-mode strict checks do
	// not reject the test config; the production runtime still builds
	// because cfg.Database.URL is set.
	//
	// Story #290: probe-only Validate() rejects mllp.listeners
	// (operators cannot persist HL7 without DB guarantees there). A
	// test that DOES configure MLLP must therefore boot in production
	// mode. AuthAudience may be empty — allow_dev_bypass: true (set
	// above when AuthAudience == "") is the documented opt-out for the
	// bearer audience requirement under production mode.
	deploymentMode := "probe-only"
	if cfg.MLLPBind != "" {
		deploymentMode = "production"
	}

	logFormat := cfg.LogFormat
	if logFormat == "" {
		logFormat = "json"
	}
	logLevel := cfg.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}

	secretPollLine := ""
	if cfg.SecretFilePollInterval > 0 {
		secretPollLine = fmt.Sprintf("\n  secret_file_poll_interval: %s", cfg.SecretFilePollInterval.String())
	}

	yamlBody := fmt.Sprintf(`deployment:
  facility_id: %s
  environment: e2e
  log_level: %s
  log_format: %s
  mode: %s%s
adapter:
  id: %s
server:
  http:
    bind: %s
    probe_bind: %s
    insecure: %t
lifecycle:
  shutdown_grace_period: %s
database:
  url: %s
codec:
  active_key_version: 1
  keys:
    - version: 1
      material: "%s"
auth:
  audience: %s%s
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
%s%s%s%s
`,
		cfg.FacilityID, logLevel, logFormat, deploymentMode, secretPollLine, cfg.AdapterID, cfg.HTTPBind, probeBind, cfg.Insecure,
		cfg.GracePeriod.String(),
		cfg.DatabaseURL, codecMaterialField, cfg.AuthAudience, authInsecureLine, mllpBlock, topicsBlock, tracingBlock, urlValidatorBlock,
	)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if cfg.ConfigPathSink != nil {
		*cfg.ConfigPathSink = cfgPath
	}

	binCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(binCtx, binPath, "--config", cfgPath)
	stdoutW := newPrefixWriter(t, "fhir-subs out")
	stderrW := newPrefixWriter(t, "fhir-subs err")
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start binary: %v", err)
	}

	h := &prodBinaryHandle{
		cmd:       cmd,
		httpAddr:  cfg.HTTPBind,
		probeAddr: probeBind,
		mllpAddr:  mllpAddrPlaceholder,
		cancel:    cancel,
		stdout:    stdoutW,
		stderr:    stderrW,
	}

	// Wait for /healthz to flip green. /healthz lives on the probe
	// listener (S-118 split); the main listener serves only the auth-
	// gated FHIR routes. Polling the probe address ensures the wait
	// reflects the probe semantics the kubelet will see.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.ProbeURL() + "/healthz")
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
// t.Log. It also retains every emitted line so a test can grep its own
// captured output for log-driven assertions (e.g. "catalog reloaded").
type prefixWriter struct {
	t      *testing.T
	prefix string
	buf    []byte
	mu     sync.Mutex
	lines  []string
}

func newPrefixWriter(t *testing.T, prefix string) *prefixWriter {
	return &prefixWriter{t: t, prefix: prefix}
}

// Lines returns a snapshot of captured lines (newline-stripped).
func (p *prefixWriter) Lines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.lines))
	copy(out, p.lines)
	return out
}

// ContainsLine reports whether any captured line contains substr.
func (p *prefixWriter) ContainsLine(substr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, l := range p.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
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
		p.mu.Lock()
		p.lines = append(p.lines, line)
		p.mu.Unlock()
	}
	return len(b), nil
}
