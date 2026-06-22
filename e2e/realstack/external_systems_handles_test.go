// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// OP #346: integration test that drives the realstack harness in
// external-systems mode against a side-stack of containers brought up
// via the same docker-compose file. Exercises the env-gated path the
// docs/test-harness-realstack.md "point at zdock" walkthrough relies
// on, without requiring an actual zdock/cluster reachable from CI.

package realstack_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestRealStack_ExternalSystemsModeUsesEnvSuppliedURLs proves the
// env-gate works end-to-end:
//
//  1. Bring up a side-stack (postgres/keycloak/hapi-fhir) under the
//     "external-local" profile in a dedicated compose project.
//  2. Set FHIR_SUBS_TEST_DB_URL / FHIR_SUBS_TEST_FHIR_URL /
//     FHIR_SUBS_TEST_OIDC_ISSUER_URL to point at that side-stack.
//  3. Boot the realstack harness.
//  4. Assert Stack.UsesExternalSystems() is true, the rendered binary
//     config references the external Postgres URL, and the harness's
//     own compose project did NOT bring up postgres/keycloak/hapi-fhir
//     containers (i.e. the profile was skipped).
//  5. Tear everything down.
//
// This is the "1+ realstack test demonstrably runs against an
// externally-pointed dependency" AC for OP #346.
func TestRealStack_ExternalSystemsModeUsesEnvSuppliedURLs(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	// OP #346: skip if any of the env vars are already set externally —
	// the test owns the env state for the duration of its run, and we
	// don't want to silently accept a CI-injected zdock-pointer that
	// may be down.
	for _, name := range []string{
		"FHIR_SUBS_TEST_DB_URL",
		"FHIR_SUBS_TEST_FHIR_URL",
		"FHIR_SUBS_TEST_OIDC_ISSUER_URL",
	} {
		if val := os.Getenv(name); val != "" {
			t.Skipf("OP #346: env %s is already set (%q); test-managed external mode would be lost", name, val)
		}
	}

	// 1. Bring up a side-stack of postgres/keycloak/hapi-fhir under
	// the "external-local" profile in a dedicated project. We use the
	// shared docker-compose.yml so the images and healthchecks are
	// exactly what production tests use.
	repoRoot := findRepoRootForTest(t)
	composeFile := filepath.Join(repoRoot, "e2e", "realstack", "docker-compose.yml")
	sideProject := "realstack-ext-side-" + shortHexForTest(t)
	t.Cleanup(func() {
		downCtx, dCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dCancel()
		out, err := exec.CommandContext(downCtx,
			"docker", "compose",
			"-f", composeFile,
			"-p", sideProject,
			"--profile", "external-local",
			"down", "-v", "--remove-orphans",
		).CombinedOutput()
		if err != nil {
			t.Logf("[external-side-stack] down failed: %v\n%s", err, out)
		}
	})

	upCtx, upCancel := context.WithTimeout(ctx, 4*time.Minute)
	defer upCancel()
	upOut, err := exec.CommandContext(upCtx,
		"docker", "compose",
		"-f", composeFile,
		"-p", sideProject,
		"--profile", "external-local",
		"up", "-d", "--wait", "--wait-timeout", "180",
		"postgres", "keycloak", "hapi-fhir",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("side-stack up failed: %v\n%s", err, upOut)
	}

	pgAddr := composePortForTest(t, composeFile, sideProject, "postgres", "5432")
	kcAddr := composePortForTest(t, composeFile, sideProject, "keycloak", "8080")
	hapiAddr := composePortForTest(t, composeFile, sideProject, "hapi-fhir", "8080")

	dbURL := fmt.Sprintf("postgres://fhirsubs:fhirsubs@%s/fhirsubs?sslmode=disable", pgAddr)
	fhirURL := fmt.Sprintf("http://%s/fhir", hapiAddr)
	oidcURL := fmt.Sprintf("http://%s/realms/fhir-subs", kcAddr)

	// 2. Inject env vars for the harness Boot. t.Setenv restores on
	// test exit so subsequent tests in the package see clean state.
	t.Setenv("FHIR_SUBS_TEST_DB_URL", dbURL)
	t.Setenv("FHIR_SUBS_TEST_FHIR_URL", fhirURL)
	t.Setenv("FHIR_SUBS_TEST_OIDC_ISSUER_URL", oidcURL)

	// 3. Boot the harness. It should NOT bring up postgres/keycloak/
	// hapi-fhir under its own project — the profile is skipped.
	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	// 4. Assertions.
	if !stack.UsesExternalSystems() {
		t.Fatalf("Stack.UsesExternalSystems()=false; want true with all three env vars set")
	}

	// /readyz must work — the binary is launched against the external
	// dependencies and should report ready once migrations have run
	// against the side-stack Postgres.
	resp, err := http.Get(stack.Binary.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz returned %d; want 200", resp.StatusCode)
	}

	// Rendered config references the external Postgres URL exactly —
	// no localhost fallback, no compose-resolved address.
	cfg := readBinaryConfig(t, stack.ConfigPath())
	if !strings.Contains(cfg, dbURL) {
		t.Errorf("rendered config does not reference external Postgres URL %q", dbURL)
	}
	if !strings.Contains(cfg, oidcURL) {
		t.Errorf("rendered config does not reference external OIDC issuer %q", oidcURL)
	}

	// The harness's OWN compose project (Stack.ProjectName()) must
	// NOT contain postgres/keycloak/hapi-fhir containers — those are
	// gated behind "external-local" and the profile was skipped. We
	// run `docker compose ps` against the harness project and assert
	// none of the three names appear.
	psCtx, psCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer psCancel()
	psOut, err := exec.CommandContext(psCtx,
		"docker", "compose",
		"-f", composeFile,
		"-p", stack.ProjectName(),
		"ps", "--services",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose ps for harness project: %v\n%s", err, psOut)
	}
	psServices := strings.Split(strings.TrimSpace(string(psOut)), "\n")
	for _, name := range []string{"postgres", "keycloak", "hapi-fhir"} {
		for _, svc := range psServices {
			if strings.TrimSpace(svc) == name {
				t.Errorf("harness project %q has %q running; OP #346 says external-local profile must be skipped when env vars are set", stack.ProjectName(), name)
			}
		}
	}
}

// shortHexForTest returns a short test-stable suffix derived from the
// nanosecond timestamp. Good enough to keep two concurrent test runs
// from colliding on a project name.
func shortHexForTest(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%x", time.Now().UnixNano())[:10]
}

// composePortForTest resolves a compose service's host-side port via
// `docker compose port`. Mirrors boot.go's composeServiceAddr but uses
// public exec because the unexported helper isn't reachable from
// _test.go in the realstack_test package.
func composePortForTest(t *testing.T, composeFile, project, service, containerPort string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx,
		"docker", "compose",
		"-f", composeFile,
		"-p", project,
		"port", service, containerPort,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose port %s %s: %v\n%s", service, containerPort, err, out)
	}
	addr := strings.TrimSpace(string(out))
	if strings.HasPrefix(addr, "0.0.0.0:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, "[::]:") {
		addr = "127.0.0.1:" + strings.TrimPrefix(addr, "[::]:")
	}
	if addr == "" {
		t.Fatalf("docker compose port %s %s returned empty", service, containerPort)
	}
	return addr
}
