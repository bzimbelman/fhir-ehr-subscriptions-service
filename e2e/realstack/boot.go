// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

import (
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

	// URLValidatorAllowHTTP, when true, sets url_validator.allow_http
	// in the rendered binary config so the activator can dial plain
	// http:// rest-hook endpoints. Production keeps this false. Story
	// #236 e2e tests opt in because the test-resthook-subscriber
	// publishes on plain http://127.0.0.1:<port>.
	URLValidatorAllowHTTP bool

	// URLValidatorAllowHosts seeds url_validator.allow_hosts in the
	// rendered binary config. Hosts in this list bypass the binary's
	// SSRF private-IP rejection (handlers.URLValidatorConfig.AllowHosts).
	// Story #236 e2e tests pass "127.0.0.1" so the activator can reach
	// the test-resthook-subscriber's host-published port.
	URLValidatorAllowHosts []string
}

// Stack is the handle Boot returns. Field naming mirrors the docker
// service names so tests read top-down: stack.Postgres.Addr,
// stack.Keycloak.IssuerURL, stack.RestHookSubscriber.QueryAPIURL.
//
// OP #344: Prometheus / OTel / CoreDNS / Nginx / Mitmproxy / TokenMint
// fields were removed alongside their docker-compose services. Their
// assertions are exercised by proper unit/integration tests against the
// production binary's in-process seams; see services.go for pointers.
type Stack struct {
	Postgres           PostgresHandle
	Keycloak           KeycloakHandle
	HAPIFHIR           HAPIFHIRHandle
	Mailpit            MailpitHandle
	RestHookSubscriber SubscriberHandle
	WSSubscriber       SubscriberHandle
	Binary             BinaryHandle
	MLLPControlPlane   MLLPControlPlaneHandle

	project    string
	composeDir string
	configPath string
	binaryProc *exec.Cmd
	binaryStop func()
	closed     bool
	closeMu    sync.Mutex

	// external is the parsed external-system env-var snapshot Boot
	// captured at start. UseExternal=true means Boot skipped the
	// docker-compose "external-local" profile and pointed the binary
	// at the supplied URLs. Non-default state, so accessor methods on
	// Stack consult it before falling back to compose-resolved values.
	external ExternalSystemConfig

	// keycloakClientID and keycloakClientSecret are the credentials
	// provisionKeycloak created on the realm. MintClientToken uses
	// them with the client_credentials grant to mint real bearer
	// tokens for load tests AND positive-path realstack tests (OP
	// #344 removed the test-token-mint helper).
	keycloakClientID     string
	keycloakClientSecret string
}

// profileExternalLocal is the docker-compose profile name that gates
// the postgres, keycloak, and hapi-fhir services. Boot activates it
// when the operator has NOT supplied the three external-system env
// vars (FHIR_SUBS_TEST_DB_URL / FHIR_SUBS_TEST_FHIR_URL /
// FHIR_SUBS_TEST_OIDC_ISSUER_URL). See OP #346 for context.
const profileExternalLocal = "external-local"

// UsesExternalSystems reports whether Boot is pointing the production
// binary at externally-managed Postgres, HAPI FHIR, and Keycloak (true
// when all three env vars were set at Boot time). Tests assert against
// this to verify the env-gate landed without spinning up local
// containers for those services.
func (s *Stack) UsesExternalSystems() bool { return s.external.UseExternal }

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
//
// OP #346: Boot reads three external-system env vars at start
// (FHIR_SUBS_TEST_DB_URL, FHIR_SUBS_TEST_FHIR_URL,
// FHIR_SUBS_TEST_OIDC_ISSUER_URL). When all three are set the harness
// skips the docker-compose "external-local" profile and uses the
// supplied URLs; when any are unset the profile is activated so
// Postgres, Keycloak, and HAPI FHIR come up locally. See
// docs/test-harness-realstack.md for the operator-facing walkthrough.
func Boot(ctx context.Context, t *testing.T, opts Options) *Stack {
	t.Helper()
	if err := CheckDocker(); err != nil {
		// OP #259: env-gated skip — docker unavailable, real-stack harness is testcontainers-driven.
		t.Skipf("docker unavailable: %v", err)
	}

	extCfg, err := ParseExternalSystemConfig(os.Getenv)
	if err != nil {
		t.Fatalf("parse external-system env vars: %v", err)
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
		external:   extCfg,
	}

	// `docker compose up -d --build --wait` brings every service to
	// healthy. --build rebuilds the in-repo subscriber binaries when
	// their source changes; --wait blocks until every healthcheck
	// reports up.
	//
	// OP #346: when the operator has NOT supplied the three external-
	// system env vars, activate the "external-local" profile so
	// postgres/keycloak/hapi-fhir come up locally alongside the
	// receivers. When the env vars ARE set, skip the profile — the
	// harness will point the binary at the externally-managed services.
	upArgs := []string{}
	if !extCfg.UseExternal {
		upArgs = append(upArgs, "--profile", profileExternalLocal)
	}
	upArgs = append(upArgs, "up", "-d", "--build", "--wait", "--wait-timeout", strconv.Itoa(int(opts.BootTimeout.Seconds())))
	upOut, err := composeCommand(bootCtx, composeDir, project, upArgs...).CombinedOutput()
	if err != nil {
		t.Cleanup(s.Close)
		t.Fatalf("docker compose up failed for project %s: %v\n%s", project, err, upOut)
	}

	// Populate Postgres / Keycloak / HAPI FHIR handles from env vars
	// when the operator has chosen the external path; otherwise resolve
	// them from the locally-spawned compose services. Mailpit and the
	// receivers are always local — they are not in scope for OP #346
	// and are too test-specific to relocate.
	if extCfg.UseExternal {
		if err := s.populateExternalSystemHandles(bootCtx); err != nil {
			t.Cleanup(s.Close)
			t.Fatalf("populate external system handles: %v", err)
		}
	}

	// Resolve every host-side port mapping for the services that are
	// actually running locally (mailpit, receivers, and — only when the
	// "external-local" profile is active — postgres/keycloak/hapi-fhir).
	if err := s.populateServiceEndpoints(bootCtx); err != nil {
		t.Cleanup(s.Close)
		t.Fatalf("resolve service endpoints: %v", err)
	}

	// Provision the Keycloak realm/client and capture the issuer URL.
	// Works against both local and external Keycloaks: the admin API
	// path is identical and "realm already exists" is treated as a
	// no-op (see provisionKeycloak's 409 handling).
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
		// row that points the verifier at the Keycloak realm's JWKS
		// URL with the test client_id. This is what makes tokens
		// minted by Stack.MintClientToken (Keycloak client_credentials
		// grant) trustworthy to the prod binary's per-client verifier
		// path. OP #344 replaced the cmd/test-token-mint provisioning
		// with this Keycloak-only seam.
		if err := s.provisionKeycloakAuthClient(bootCtx); err != nil {
			t.Cleanup(s.Close)
			t.Fatalf("provision keycloak auth_clients row: %v", err)
		}
		// Bring up the MLLP scripted control plane container after the
		// prod binary's MLLP listener has a host-side address. The
		// container reaches the host's MLLP port via host.docker.internal
		// (host-gateway alias) and is gated behind the "mllp" compose
		// profile so non-MLLP test runs skip the build.
		if opts.EnableMLLP {
			if err := s.launchMLLPControlPlane(bootCtx); err != nil {
				t.Cleanup(s.Close)
				t.Fatalf("launch mllp control plane: %v", err)
			}
		}
	}

	return s
}

// provisionKeycloakAuthClient inserts (or updates) the auth_clients row
// the prod binary's verifier resolves when it sees a token bearing the
// Keycloak client's client_id. The row's jwks_url points at the
// Keycloak realm's JWKS endpoint (Stack.Keycloak.JWKSURL).
//
// OP #344 replaced the cmd/test-token-mint helper with this Keycloak-
// only seam: realstack tests that need a positive-path bearer token
// call Stack.MintClientToken (Keycloak client_credentials grant) and
// the verifier accepts it because this row exists.
//
// Idempotent — re-running Boot with a reused project name (rare; the
// default generates fresh names) updates the URL in place.
func (s *Stack) provisionKeycloakAuthClient(ctx context.Context) error {
	if s.Keycloak.JWKSURL == "" || s.keycloakClientID == "" {
		return fmt.Errorf("keycloak handle not populated (JWKSURL=%q clientID=%q)", s.Keycloak.JWKSURL, s.keycloakClientID)
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
	if _, err := conn.Exec(ctx, sql, s.keycloakClientID, s.Keycloak.JWKSURL, scopes, "realstack keycloak client_credentials"); err != nil {
		return fmt.Errorf("upsert auth_clients: %w", err)
	}
	return nil
}

// populateExternalSystemHandles fills the Postgres, Keycloak, and
// HAPI FHIR handles from the operator-supplied env vars (Boot's
// extCfg.UseExternal=true path). The harness skips the
// "external-local" compose profile in this mode so there are no
// docker-published ports to read for these three services; everything
// is sourced from the env-supplied URLs.
//
// The OIDC issuer is parsed into a base URL + realm name so the
// existing provisionKeycloak admin-API code works unchanged against an
// externally-managed Keycloak (works for both http://keycloak:8080 and
// https://keycloak.bzonfhir.com style hosts).
func (s *Stack) populateExternalSystemHandles(ctx context.Context) error {
	cfg := s.external

	// Postgres: the operator hands us a full DSN. We don't have a
	// host:port we can advertise via Postgres.Addr (the URL may
	// embed user:pass and use TLS), so leave Addr empty and rely on
	// callers using URL.
	s.Postgres.URL = cfg.DBURL

	// HAPI FHIR: BaseURL is exactly what the env var supplied.
	s.HAPIFHIR.BaseURL = cfg.FHIRBaseURL

	// Keycloak: split <scheme>://<authority>/realms/<realm> into base
	// URL and realm name. provisionKeycloak then uses BaseURL +
	// /realms/master/... for the admin token.
	parsed, err := url.Parse(cfg.OIDCIssuerURL)
	if err != nil {
		return fmt.Errorf("parse FHIR_SUBS_TEST_OIDC_ISSUER_URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("FHIR_SUBS_TEST_OIDC_ISSUER_URL=%q is missing scheme or host", cfg.OIDCIssuerURL)
	}
	// Path should be /realms/<realm-name>; pull out the realm so the
	// harness uses the operator-supplied name (not the hardcoded
	// "fhir-subs"). Tolerate trailing slashes.
	trimmed := strings.Trim(parsed.Path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] != "realms" || parts[1] == "" {
		return fmt.Errorf("FHIR_SUBS_TEST_OIDC_ISSUER_URL=%q does not match scheme://host/realms/<name>", cfg.OIDCIssuerURL)
	}
	realm := parts[1]
	baseURL := parsed.Scheme + "://" + parsed.Host
	s.Keycloak.BaseURL = baseURL
	s.Keycloak.Realm = realm
	// IssuerURL/JWKSURL/TokenURL are the public values the prod binary
	// receives via the rendered config; we set them here so the rest
	// of Boot can rely on them being non-empty in both modes.
	// provisionKeycloak overwrites these with the same values once the
	// realm is provisioned — idempotent.
	s.Keycloak.IssuerURL = baseURL + "/realms/" + realm
	s.Keycloak.JWKSURL = s.Keycloak.IssuerURL + "/protocol/openid-connect/certs"
	s.Keycloak.TokenURL = s.Keycloak.IssuerURL + "/protocol/openid-connect/token"

	// Defensive ping so tests fail fast with a clear error when the
	// operator misconfigures the env vars (e.g. tunnel down).
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pingTCP(pingCtx, parsed.Host, parsed.Scheme); err != nil {
		return fmt.Errorf("dial external Keycloak %s: %w", parsed.Host, err)
	}
	return nil
}

// pingTCP opens a single TCP dial against host:<scheme-port>. host may
// or may not include an explicit port — when missing, scheme defaults
// to 443 (https) or 80 (http).
func pingTCP(ctx context.Context, host, scheme string) error {
	if !strings.Contains(host, ":") {
		switch scheme {
		case "https":
			host += ":443"
		default:
			host += ":80"
		}
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return err
	}
	return conn.Close()
}

// populateServiceEndpoints reads host-side port bindings from `docker
// compose port` for every service and fills the Stack handles.
//
// OP #346: when external mode is active, postgres/keycloak/hapi-fhir
// are NOT brought up locally (the "external-local" profile is skipped)
// and the corresponding handles were already filled by
// populateExternalSystemHandles from the env-supplied URLs. Skip those
// three services here; mailpit and the receivers always run locally.
func (s *Stack) populateServiceEndpoints(ctx context.Context) error {
	type binding struct {
		service       string
		containerPort string
		setAddr       func(string)
	}
	bindings := []binding{}
	if !s.external.UseExternal {
		bindings = append(bindings,
			binding{"postgres", "5432/tcp", func(addr string) {
				s.Postgres.Addr = addr
				s.Postgres.URL = fmt.Sprintf("postgres://fhirsubs:fhirsubs@%s/fhirsubs?sslmode=disable", addr)
			}},
			binding{"keycloak", "8080/tcp", func(addr string) {
				s.Keycloak.Addr = addr
				s.Keycloak.BaseURL = "http://" + addr
			}},
			binding{"hapi-fhir", "8080/tcp", func(addr string) {
				s.HAPIFHIR.Addr = addr
				s.HAPIFHIR.BaseURL = "http://" + addr + "/fhir"
			}},
		)
	}
	bindings = append(bindings, []binding{
		{"mailpit", "1025/tcp", func(addr string) {
			s.Mailpit.SMTPAddr = addr
		}},
		{"mailpit", "8025/tcp", func(addr string) {
			s.Mailpit.APIAddr = addr
			s.Mailpit.APIBaseURL = "http://" + addr
		}},
		// OP #344: prometheus / otel-collector / coredns / nginx /
		// mitmproxy bindings deleted alongside their docker services.
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
		// OP #344: test-token-mint binding deleted alongside the
		// docker service. Realstack tests that need a positive-path
		// bearer token call Stack.MintClientToken (Keycloak grant).
	}...)

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
//
// OP #346: works against both local (BaseURL=http://<addr>) and
// external (BaseURL=https://keycloak.bzonfhir.com) Keycloaks. The
// realm name comes from s.Keycloak.Realm when populateExternalSystem
// Handles already set it (so the operator's chosen realm is honored);
// otherwise defaults to "fhir-subs".
func (s *Stack) provisionKeycloak(ctx context.Context) error {
	base := s.Keycloak.BaseURL
	if base == "" {
		// Defensive — populateServiceEndpoints / populateExternalSystem
		// Handles must always set BaseURL before this runs.
		return fmt.Errorf("keycloak BaseURL is empty; was Boot's endpoint resolution skipped?")
	}
	realm := s.Keycloak.Realm
	if realm == "" {
		realm = "fhir-subs"
	}

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

	// OP #344: the otel-collector service was deleted; the rendered
	// config no longer sets tracing.otlp_endpoint here. The tracing
	// pipeline's wiring + span emission are pinned by
	// internal/api/handlers/tracing_test.go and
	// internal/infra/observability/tracing/tracing_test.go (in-memory
	// exporter via tracetest). Options.EnableTracing is preserved as a
	// no-op flag so existing call sites compile; future stories can
	// re-attach an in-process OTLP listener if cross-binary tracing
	// coverage is wanted.
	_ = opts.EnableTracing
	tracingBlock := ""

	codecKey := make([]byte, 32)
	if _, err := rand.Read(codecKey); err != nil {
		return err
	}
	keyB64 := base64.StdEncoding.EncodeToString(codecKey)

	rateLimitBlock := renderRateLimitBlock(opts.SubscriptionCreateRateLimit, opts.WSBindingTokenRateLimit)
	urlValidatorBlock := renderURLValidatorBlock(opts.URLValidatorAllowHTTP, opts.URLValidatorAllowHosts)

	// OP #344: trusted_issuers used to carry an additional entry for
	// the cmd/test-token-mint helper alongside Keycloak. With the
	// helper deleted, only Keycloak remains.
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
      jwks_url: %s%s%s
%s%s
`, httpAddr, s.Postgres.URL, keyB64,
		s.Keycloak.IssuerURL, s.Keycloak.JWKSURL,
		s.Keycloak.IssuerURL, s.Keycloak.JWKSURL,
		rateLimitBlock, urlValidatorBlock, tracingBlock, mllpBlock)

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

// launchMLLPControlPlane brings up the test-mllp-control-plane
// container and resolves its host-published port. The container is
// declared under the "mllp" compose profile so the default
// `up --build --wait` cycle skips it; this function is the explicit
// opt-in path.
//
// The container reaches the prod binary's MLLP listener on the host
// via host.docker.internal:<port> — the docker-compose service entry
// declares a host-gateway alias for Linux runners; macOS docker
// resolves the name natively.
func (s *Stack) launchMLLPControlPlane(ctx context.Context) error {
	if s.Binary.MLLPAddr == "" {
		return fmt.Errorf("EnableMLLP=true but Binary.MLLPAddr is empty")
	}
	// Translate 127.0.0.1:<port> into host.docker.internal:<port>.
	_, port, ok := strings.Cut(s.Binary.MLLPAddr, ":")
	if !ok {
		return fmt.Errorf("Binary.MLLPAddr %q is not host:port", s.Binary.MLLPAddr)
	}
	target := "host.docker.internal:" + port

	// Bring the service up via the "mllp" profile with the resolved
	// target injected as env. --build ensures the image exists; --wait
	// blocks on the healthcheck.
	args := []string{"--profile", "mllp", "up", "-d", "--build", "--wait", "--wait-timeout", "60", "test-mllp-control-plane"}
	cmd := composeCommand(ctx, s.composeDir, s.project, args...)
	cmd.Env = append(cmd.Env, "FHIRSUBS_MLLP_TARGET="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose up test-mllp-control-plane: %v\n%s", err, out)
	}

	// Resolve the host port for the control plane.
	addr, err := composeServiceAddr(ctx, s.composeDir, s.project, "test-mllp-control-plane", "8093/tcp")
	if err != nil {
		return fmt.Errorf("resolve test-mllp-control-plane port: %w", err)
	}
	s.MLLPControlPlane.HTTPAddr = addr
	s.MLLPControlPlane.URL = "http://" + addr

	// Sanity probe — the healthcheck already waited, but a final dial
	// gives a clearer error when the env injection failed and the
	// binary exited at startup.
	if err := retryUntil(ctx, 15*time.Second, func() error {
		resp, herr := http.Get(s.MLLPControlPlane.URL + "/healthz")
		if herr != nil {
			return herr
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("test-mllp-control-plane /healthz never returned 200: %w", err)
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
// renderURLValidatorBlock emits the url_validator YAML fragment when
// the test opts in to non-default SSRF policy. Production deployments
// keep both knobs at their secure defaults (https-only, no
// private-IP allow-listed hosts); story #236 e2e tests need
// http://127.0.0.1:<port> to reach the test-resthook-subscriber's
// host-published port.
//
// Empty string when both inputs are zero-valued so the binary's
// defaults survive (see cmd/fhir-subs/config.go URLValidatorSettings).
func renderURLValidatorBlock(allowHTTP bool, allowHosts []string) string {
	if !allowHTTP && len(allowHosts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nurl_validator:\n")
	if allowHTTP {
		b.WriteString("  allow_http: true\n")
	}
	if len(allowHosts) > 0 {
		b.WriteString("  allow_hosts:\n")
		for _, h := range allowHosts {
			fmt.Fprintf(&b, "    - %q\n", h)
		}
	}
	return b.String()
}

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
