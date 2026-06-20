// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Options configures Boot. Zero value is valid: every knob has a
// sensible default so simple call sites stay one-line.
type Options struct {
	// ProjectName overrides the auto-generated docker-compose project
	// name. Empty (the default) generates a unique-per-Boot name. Set
	// only when an external orchestrator is reusing a stack across
	// multiple test runs.
	ProjectName string

	// SkipBinaryLaunch leaves cmd/fhir-subs unstarted. Used by tests
	// that want to drive the dependency stack directly without the
	// production binary in the picture (e.g. Keycloak provisioning
	// smoke tests). Default: false (binary IS launched).
	SkipBinaryLaunch bool

	// BootTimeout caps the whole-stack ready window. Default: 90s,
	// matching the story acceptance criterion.
	BootTimeout time.Duration

	// EnableMLLP causes the rendered config to open an MLLP listener.
	// Default: false (HL7 v2 path tests opt in).
	EnableMLLP bool

	// EnableTracing causes the rendered config to point the binary at
	// the OTel collector. Default: true.
	EnableTracing bool

	// SubscriptionCreateRateLimit installs a per-authenticated-client
	// token bucket on POST /Subscription. Burst <= 0 (the default)
	// leaves the limit disabled. Set by H3 LoadDriver scenarios that
	// need to drive the binary into 429 territory.
	SubscriptionCreateRateLimit RateLimit

	// WSBindingTokenRateLimit installs a per-authenticated-client
	// token bucket on the $get-ws-binding-token operation. Burst <= 0
	// (the default) leaves the limit disabled.
	WSBindingTokenRateLimit RateLimit
}

// Stack is the handle Boot returns. Field naming mirrors the docker
// service names so tests read top-down: stack.Postgres.Addr,
// stack.Keycloak.IssuerURL, stack.RestHookSubscriber.QueryAPIURL.
type Stack struct {
	Postgres           PostgresHandle
	Keycloak           KeycloakHandle
	HAPIFHIR           HAPIFHIRHandle
	Mailpit            MailpitHandle
	Prometheus         PrometheusHandle
	OTel               OTelHandle
	CoreDNS            CoreDNSHandle
	Nginx              NginxHandle
	Mitmproxy          MitmproxyHandle
	RestHookSubscriber SubscriberHandle
	WSSubscriber       SubscriberHandle
	TokenMint          TokenMintHandle
	Binary             BinaryHandle

	project    string
	composeDir string
	configPath string
	binaryProc *exec.Cmd
	binaryStop func()
	closed     bool
	closeMu    sync.Mutex

	// keycloakClientID and keycloakClientSecret are the credentials
	// provisionKeycloak created on the realm. MintClientToken uses
	// them with the client_credentials grant to mint real bearer
	// tokens for load tests.
	keycloakClientID     string
	keycloakClientSecret string
}

// ProjectName returns the docker-compose project name the Stack is
// running under. Tests that need to address containers directly (for
// example via `docker compose -p <project> exec`) read this.
func (s *Stack) ProjectName() string { return s.project }

// ConfigPath returns the host-side path to the rendered config file the
// harness wrote for the production binary. Tests that audit the
// config (e.g. assert it points at the real Postgres URL) read this.
func (s *Stack) ConfigPath() string { return s.configPath }

// Close tears the stack down. Safe to call multiple times.
func (s *Stack) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true

	if s.binaryStop != nil {
		s.binaryStop()
	}

	if s.project != "" && s.composeDir != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := composeCommand(ctx, s.composeDir, s.project, "down", "-v", "--remove-orphans").CombinedOutput()
		if err != nil {
			// Cleanup failure shouldn't block the test; surface for diagnosis.
			fmt.Fprintf(os.Stderr, "[realstack] docker compose down -v failed for project %s: %v\n%s\n", s.project, err, out)
		}
	}
}

// CheckDocker returns nil when docker + docker compose are usable on
// the host. The harness's RED tests call this in requireDocker() to
// skip gracefully on runners without docker.
func CheckDocker() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		return fmt.Errorf("docker: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "docker", "compose", "version").CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Boot brings up the full real-stack and the production binary. It
// fails the test with t.Fatalf on any setup error; the returned Stack
// is always non-nil and fully populated.
func Boot(ctx context.Context, t *testing.T, opts Options) *Stack {
	t.Helper()
	if err := CheckDocker(); err != nil {
		// OP #259: env-gated skip — docker unavailable, real-stack harness is testcontainers-driven.
		t.Skipf("docker unavailable: %v", err)
	}

	if opts.BootTimeout == 0 {
		opts.BootTimeout = 90 * time.Second
	}
	bootCtx, cancel := context.WithTimeout(ctx, opts.BootTimeout)
	defer cancel()

	repoRoot := findRepoRoot(t)
	composeDir := filepath.Join(repoRoot, "e2e", "realstack")

	project := opts.ProjectName
	if project == "" {
		project = generateProjectName()
	}

	s := &Stack{
		project:    project,
		composeDir: composeDir,
	}

	// `docker compose up -d --build --wait` brings every service to
	// healthy. --build rebuilds the in-repo subscriber binaries when
	// their source changes; --wait blocks until every healthcheck
	// reports up.
	upArgs := []string{"up", "-d", "--build", "--wait", "--wait-timeout", strconv.Itoa(int(opts.BootTimeout.Seconds()))}
	upOut, err := composeCommand(bootCtx, composeDir, project, upArgs...).CombinedOutput()
	if err != nil {
		t.Cleanup(s.Close)
		t.Fatalf("docker compose up failed for project %s: %v\n%s", project, err, upOut)
	}

	// Resolve every host-side port mapping.
	if err := s.populateServiceEndpoints(bootCtx); err != nil {
		t.Cleanup(s.Close)
		t.Fatalf("resolve service endpoints: %v", err)
	}

	// Provision the Keycloak realm/client and capture the issuer URL.
	if err := s.provisionKeycloak(bootCtx); err != nil {
		t.Cleanup(s.Close)
		t.Fatalf("provision keycloak: %v", err)
	}

	// Render the binary's config and launch it.
	if !opts.SkipBinaryLaunch {
		if err := s.launchBinary(bootCtx, repoRoot, opts); err != nil {
			t.Cleanup(s.Close)
			t.Fatalf("launch binary: %v", err)
		}
		// After the binary has run its migrations, seed an auth_clients
		// row that points the verifier at the test-token-mint JWKS URL.
		// This is what makes tokens minted by cmd/test-token-mint
		// trustworthy to the prod binary's per-client verifier path.
		if err := s.provisionTestMintClient(bootCtx); err != nil {
			t.Cleanup(s.Close)
			t.Fatalf("provision test-token-mint client: %v", err)
		}
	}

	return s
}

// provisionTestMintClient inserts (or updates) the auth_clients row the
// prod binary's verifier resolves when it sees a token bearing the
// realstack-test-mint client_id. The row's jwks_url points at the
// test-token-mint container's host-published JWKS endpoint.
//
// Idempotent — re-running Boot with a reused project name (rare; the
// default generates fresh names) updates the URL in place.
func (s *Stack) provisionTestMintClient(ctx context.Context) error {
	if s.TokenMint.JWKSURL == "" || s.TokenMint.ClientID == "" {
		return fmt.Errorf("test-token-mint handle not populated")
	}
	conn, err := pgx.Connect(ctx, s.Postgres.URL)
	if err != nil {
		return fmt.Errorf("pgx connect: %w", err)
	}
	defer conn.Close(ctx)

	const sql = `
		INSERT INTO auth_clients (id, jwks_url, scopes, display_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
			SET jwks_url = excluded.jwks_url,
			    scopes = excluded.scopes,
			    display_name = excluded.display_name,
			    updated_at = now()`
	scopes := []string{
		"system/Subscription.cruds",
		"system/Subscription.r",
		"system/Subscription.s",
		"system/Subscription.c",
		"system/Subscription.u",
		"system/Subscription.d",
		"system/Subscription.$status",
		"system/Subscription.$events",
		"system/Subscription.$get-ws-binding-token",
	}
	if _, err := conn.Exec(ctx, sql, s.TokenMint.ClientID, s.TokenMint.JWKSURL, scopes, "realstack test-token-mint helper"); err != nil {
		return fmt.Errorf("upsert auth_clients: %w", err)
	}
	return nil
}

// MintTestToken POSTs to the test-token-mint binary's /mint endpoint
// with the given claim overrides and returns the signed JWT. Pass nil
// to use the helper's default claim set (which the prod binary
// accepts). Pass {"exp": <past>} to mint an expired token, etc.
func (s *Stack) MintTestToken(ctx context.Context, overrides map[string]any) (string, error) {
	if s.TokenMint.TokenAPIURL == "" {
		return "", fmt.Errorf("realstack: TokenMint.TokenAPIURL is empty; was the stack booted?")
	}
	body, err := json.Marshal(map[string]any{"overrides": overrides})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TokenMint.TokenAPIURL+"/mint", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("test-token-mint /mint: %d %s", resp.StatusCode, string(respBody))
	}
	var out struct {
		Token string `json:"token"`
		Kid   string `json:"kid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("test-token-mint /mint: empty token")
	}
	return out.Token, nil
}

// populateServiceEndpoints reads host-side port bindings from `docker
// compose port` for every service and fills the Stack handles.
func (s *Stack) populateServiceEndpoints(ctx context.Context) error {
	type binding struct {
		service       string
		containerPort string
		setAddr       func(string)
	}
	bindings := []binding{
		{"postgres", "5432/tcp", func(addr string) {
			s.Postgres.Addr = addr
			s.Postgres.URL = fmt.Sprintf("postgres://fhirsubs:fhirsubs@%s/fhirsubs?sslmode=disable", addr)
		}},
		{"keycloak", "8080/tcp", func(addr string) {
			s.Keycloak.Addr = addr
		}},
		{"hapi-fhir", "8080/tcp", func(addr string) {
			s.HAPIFHIR.Addr = addr
			s.HAPIFHIR.BaseURL = "http://" + addr + "/fhir"
		}},
		{"mailpit", "1025/tcp", func(addr string) {
			s.Mailpit.SMTPAddr = addr
		}},
		{"mailpit", "8025/tcp", func(addr string) {
			s.Mailpit.APIAddr = addr
			s.Mailpit.APIBaseURL = "http://" + addr
		}},
		{"prometheus", "9090/tcp", func(addr string) {
			s.Prometheus.Addr = addr
			s.Prometheus.BaseURL = "http://" + addr
		}},
		{"otel-collector", "4317/tcp", func(addr string) {
			s.OTel.OTLPAddr = addr
			s.OTel.OTLPEndpoint = "http://" + addr
		}},
		{"coredns", "53/udp", func(addr string) {
			s.CoreDNS.Addr = addr
		}},
		{"nginx", "8443/tcp", func(addr string) { s.Nginx.Addr = addr }},
		{"mitmproxy", "2525/tcp", func(addr string) { s.Mitmproxy.Addr = addr }},
		{"test-resthook-subscriber", "8090/tcp", func(addr string) {
			s.RestHookSubscriber.Addr = addr
			s.RestHookSubscriber.EndpointURL = "http://" + addr + "/notify"
			s.RestHookSubscriber.QueryAPIURL = "http://" + addr
			s.RestHookSubscriber.ControlAPIURL = "http://" + addr
		}},
		{"test-ws-subscriber", "8091/tcp", func(addr string) {
			s.WSSubscriber.Addr = addr
			s.WSSubscriber.EndpointURL = "ws://" + addr + "/ws"
			s.WSSubscriber.QueryAPIURL = "http://" + addr
		}},
		{"test-token-mint", "8092/tcp", func(addr string) {
			s.TokenMint.Addr = addr
			s.TokenMint.TokenAPIURL = "http://" + addr
			s.TokenMint.JWKSURL = "http://" + addr + "/jwks.json"
			// Issuer matches the -issuer flag the Dockerfile passes in
			// (the binary stamps this on default-minted tokens). The
			// hostname is the docker network name, which the prod
			// binary inside compose resolves via DNS — but here Issuer
			// is informational; the JWKS-resolution path uses
			// JWKSURL above (a host:port reachable from the binary).
			s.TokenMint.Issuer = "http://test-token-mint:8092/"
			s.TokenMint.ClientID = "realstack-test-mint"
		}},
	}

	for _, b := range bindings {
		addr, err := composeServiceAddr(ctx, s.composeDir, s.project, b.service, b.containerPort)
		if err != nil {
			return fmt.Errorf("service %s port %s: %w", b.service, b.containerPort, err)
		}
		b.setAddr(addr)
	}
	return nil
}

// provisionKeycloak waits for Keycloak to report ready, then creates a
// realm + client + service-account user via Keycloak's REST API. Sets
// IssuerURL/JWKSURL/TokenURL on the Keycloak handle.
func (s *Stack) provisionKeycloak(ctx context.Context) error {
	base := "http://" + s.Keycloak.Addr
	realm := "fhir-subs"

	// Get an admin token via the master realm. Default credentials are
	// admin/admin (set in docker-compose.yml).
	form := strings.NewReader("grant_type=password&client_id=admin-cli&username=admin&password=admin")
	tokReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/realms/master/protocol/openid-connect/token", form)
	tokReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var adminToken string
	if err := retryUntil(ctx, 30*time.Second, func() error {
		resp, err := http.DefaultClient.Do(tokReq.Clone(ctx))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("keycloak admin token: %d %s", resp.StatusCode, string(body))
		}
		var out struct {
			AccessToken string `json:"access_token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		adminToken = out.AccessToken
		return nil
	}); err != nil {
		return fmt.Errorf("admin token: %w", err)
	}

	// Create the realm. 409 (exists) is fine.
	body := map[string]any{
		"realm":   realm,
		"enabled": true,
	}
	if err := keycloakAdminPOST(ctx, base+"/admin/realms", adminToken, body); err != nil && !isConflict(err) {
		return fmt.Errorf("create realm: %w", err)
	}

	// Create a confidential client with service accounts enabled.
	const (
		clientID     = "fhir-subs-test"
		clientSecret = "fhir-subs-test-secret"
	)
	clientBody := map[string]any{
		"clientId":                  clientID,
		"enabled":                   true,
		"protocol":                  "openid-connect",
		"publicClient":              false,
		"serviceAccountsEnabled":    true,
		"directAccessGrantsEnabled": true,
		"standardFlowEnabled":       false,
		"secret":                    clientSecret,
	}
	if err := keycloakAdminPOST(ctx, base+"/admin/realms/"+realm+"/clients", adminToken, clientBody); err != nil && !isConflict(err) {
		return fmt.Errorf("create client: %w", err)
	}
	s.keycloakClientID = clientID
	s.keycloakClientSecret = clientSecret

	s.Keycloak.Realm = realm
	s.Keycloak.IssuerURL = base + "/realms/" + realm
	s.Keycloak.JWKSURL = s.Keycloak.IssuerURL + "/protocol/openid-connect/certs"
	s.Keycloak.TokenURL = s.Keycloak.IssuerURL + "/protocol/openid-connect/token"
	return nil
}

// launchBinary builds cmd/fhir-subs (cached in the test binary cache),
// renders a config file pointing at every real dependency, and starts
// the binary as a child process. Blocks until /readyz returns 200.
func (s *Stack) launchBinary(ctx context.Context, repoRoot string, opts Options) error {
	binDir, err := os.MkdirTemp("", "realstack-bin-*")
	if err != nil {
		return err
	}
	binPath := filepath.Join(binDir, "fhir-subs")

	build := exec.CommandContext(ctx, "go", "build", "-o", binPath, "./cmd/fhir-subs")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %v\n%s", err, out)
	}

	httpAddr, err := allocateLocalPort()
	if err != nil {
		return err
	}
	s.Binary.URL = "http://" + httpAddr

	mllpBlock := ""
	if opts.EnableMLLP {
		mllpAddr, err := allocateLocalPort()
		if err != nil {
			return err
		}
		s.Binary.MLLPAddr = mllpAddr
		mllpBlock = fmt.Sprintf(`
mllp:
  listeners:
    - name: realstack
      bind: %s
  max_message_bytes: 1048576
  persist_timeout: 5s
  shutdown_drain_grace: 5s
`, mllpAddr)
	}

	tracingBlock := ""
	if opts.EnableTracing {
		tracingBlock = fmt.Sprintf(`
tracing:
  otlp_endpoint: %s
  insecure: true
  sample_rate: 1.0
`, s.OTel.OTLPEndpoint)
	}

	codecKey := make([]byte, 32)
	if _, err := rand.Read(codecKey); err != nil {
		return err
	}
	keyB64 := base64.StdEncoding.EncodeToString(codecKey)

	rateLimitBlock := renderRateLimitBlock(opts.SubscriptionCreateRateLimit, opts.WSBindingTokenRateLimit)

	cfgYAML := fmt.Sprintf(`
deployment:
  facility_id: realstack
  environment: test
  log_level: info
  log_format: json
  mode: full
adapter:
  id: default
server:
  http:
    bind: %s
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
  audience: fhir-subs-test
  issuer: %s
  jwks_url: %s
  allow_dev_bypass: true
  trusted_issuers:
    - issuer: %s
      audience: fhir-subs-test
      jwks_url: %s
    - issuer: %s
      audience: fhir-subs-test
      jwks_url: %s%s
%s%s
`, httpAddr, s.Postgres.URL, keyB64,
		s.Keycloak.IssuerURL, s.Keycloak.JWKSURL,
		s.Keycloak.IssuerURL, s.Keycloak.JWKSURL,
		s.TokenMint.Issuer, s.TokenMint.JWKSURL,
		rateLimitBlock, tracingBlock, mllpBlock)

	configFile := filepath.Join(binDir, "config.yaml")
	if err := os.WriteFile(configFile, []byte(cfgYAML), 0o600); err != nil {
		return err
	}
	s.configPath = configFile

	cmdCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cmdCtx, binPath, "--config", configFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	s.binaryProc = cmd
	s.binaryStop = func() {
		cancel()
		_ = cmd.Wait()
	}

	// Wait for /readyz=200.
	if err := retryUntil(ctx, 60*time.Second, func() error {
		resp, err := http.Get(s.Binary.URL + "/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}); err != nil {
		s.binaryStop()
		return fmt.Errorf("/readyz never returned 200: %w", err)
	}
	return nil
}

// composeCommand returns an exec.Cmd configured to run docker compose
// against the harness's compose file and the per-test project name.
func composeCommand(ctx context.Context, composeDir, project string, args ...string) *exec.Cmd {
	full := append([]string{
		"compose",
		"-f", filepath.Join(composeDir, "docker-compose.yml"),
		"-p", project,
	}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	cmd.Dir = composeDir
	cmd.Env = append(os.Environ(),
		// Ensure the compose CLI knows where to put generated content.
		"COMPOSE_PROJECT_NAME="+project,
	)
	return cmd
}

// composeServiceAddr returns the host-side host:port for the given
// service+containerPort under the given compose project.
func composeServiceAddr(ctx context.Context, composeDir, project, service, containerPort string) (string, error) {
	out, err := composeCommand(ctx, composeDir, project, "port", service, strings.Split(containerPort, "/")[0]).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compose port %s %s: %v\n%s", service, containerPort, err, out)
	}
	addr := strings.TrimSpace(string(out))
	// Output is like "0.0.0.0:55432". Normalize 0.0.0.0 to 127.0.0.1
	// so the test process dialing the address actually reaches the
	// docker-published port.
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, "[::]:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	if addr == "" {
		return "", fmt.Errorf("compose port %s %s returned empty", service, containerPort)
	}
	return addr, nil
}

// generateProjectName returns a unique-per-Boot docker compose project
// name. Format: realstack-<8 hex chars>.
func generateProjectName() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return "realstack-" + hex.EncodeToString(buf)
}

// allocateLocalPort returns a host:port a child process can bind. The
// kernel hands out an ephemeral port; we close the listener and return
// the address. The window between Close and the child Bind is short
// enough that collisions are vanishingly rare in practice.
func allocateLocalPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

// retryUntil polls fn at 250ms intervals until it returns nil or
// timeout elapses. Returns the last error on timeout.
func retryUntil(ctx context.Context, timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s; last error: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// findRepoRoot walks upward from the test binary's working directory
// until it finds the go.mod that anchors this module.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root from %s", dir)
		}
		dir = parent
	}
}

// keycloakAdminPOST issues a POST to a Keycloak admin endpoint with a
// bearer token. Returns conflictError on 409 so callers can ignore
// "already exists" outcomes.
func keycloakAdminPOST(ctx context.Context, url, token string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(data)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusConflict {
		return conflictError{status: resp.StatusCode, body: string(rb)}
	}
	return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, string(rb))
}

type conflictError struct {
	status int
	body   string
}

func (e conflictError) Error() string {
	return fmt.Sprintf("conflict: %d %s", e.status, e.body)
}

func isConflict(err error) bool {
	var ce conflictError
	return errors.As(err, &ce)
}

// renderRateLimitBlock emits the auth.subscription_create_rate_limit
// and auth.ws_binding_token_rate_limit YAML fragments the production
// binary's config.go reads. An all-zero RateLimit emits nothing for
// that surface — the binary then leaves the limit nil-disabled,
// matching the operator-facing default.
func renderRateLimitBlock(create, wsToken RateLimit) string {
	var b strings.Builder
	if create.Burst > 0 {
		fmt.Fprintf(&b, "\n  subscription_create_rate_limit:\n    burst: %d\n    refill_per_second: %g\n    max_keys: %d",
			create.Burst, create.RefillPerSecond, create.MaxKeys)
	}
	if wsToken.Burst > 0 {
		fmt.Fprintf(&b, "\n  ws_binding_token_rate_limit:\n    burst: %d\n    refill_per_second: %g\n    max_keys: %d",
			wsToken.Burst, wsToken.RefillPerSecond, wsToken.MaxKeys)
	}
	return b.String()
}

// MintClientToken obtains a real bearer token from the Keycloak
// container using the client_credentials grant against the realm and
// confidential client provisionKeycloak created. Used by H3 LoadDriver
// scenarios that need to drive the binary's authenticated endpoints
// under sustained RPS.
func (s *Stack) MintClientToken(ctx context.Context) (string, error) {
	if s.Keycloak.TokenURL == "" {
		return "", fmt.Errorf("realstack: Keycloak.TokenURL is empty; was the stack booted?")
	}
	if s.keycloakClientID == "" || s.keycloakClientSecret == "" {
		return "", fmt.Errorf("realstack: Keycloak client credentials missing; was provisionKeycloak run?")
	}

	form := url.Values{
		"grant_type":    []string{"client_credentials"},
		"client_id":     []string{s.keycloakClientID},
		"client_secret": []string{s.keycloakClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Keycloak.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("keycloak token: %d %s", resp.StatusCode, string(body))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("keycloak token: empty access_token")
	}
	return out.AccessToken, nil
}
