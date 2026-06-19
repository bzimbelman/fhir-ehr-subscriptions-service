// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProductionWiring_AdminRoutesAreReachable boots runWithHooks
// against a real Postgres pool and asserts the production binary
// mounts /admin/topics on the SAME chi router that backs
// /Subscription. Story #92 acceptance criterion 1: today the admin
// routes are 404 because cmd/fhir-subs/wiring.go only calls
// handlers.RegisterRoutes; this test fails until Phase B teaches
// wiring.go to also call handlers.RegisterAdminRoutes.
//
// Skipped when TEST_DATABASE_URL is unset — buildProductionRuntime
// requires Postgres and the harness here cannot fake it. The skip
// message is loud enough that CI will surface a regression rather
// than silently treat the test as covered.
func TestProductionWiring_AdminRoutesAreReachable(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping production-binary admin route wiring " +
			"assertion. Phase B / coordinator: set this env var in CI to gate-check that " +
			"cmd/fhir-subs/wiring.go invokes handlers.RegisterAdminRoutes on the production " +
			"chi router. The handlers-package admin_test.go covers behavior; this test " +
			"covers the wire-up.")
	}

	// Operator config: full production runtime, with the admin token
	// and dead-letters store wired. The Config struct fields the
	// admin section needs (Admin.Token, Admin.PathPrefix) are added
	// by Phase B; this test references them indirectly through
	// applySets so it does NOT block compilation if the fields are
	// not yet present in the struct (applySets returns an error for
	// unknown keys, which becomes a runtime test failure).
	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: pickFreeAddr(t), Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		// Codec / Auth: minimal viable — Phase B fills in the
		// real env-driven config.
	}
	if err := applySets(cfg, []string{
		"admin.token=" + strings.Repeat("a", 32),
		"admin.path_prefix=/admin",
	}); err != nil {
		// `applySets` returns "unsupported key" until Phase B teaches
		// it about the admin section. That's the expected RED failure
		// for this test — we want it to fail until Phase B wires the
		// CLI/--set surface.
		t.Fatalf("applySets admin keys: %v (Phase B must teach config.go about admin.* keys)", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var addr string
	listening := make(chan struct{})
	hooks := runHooks{
		onListening: func(a string) {
			addr = a
			close(listening)
		},
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, io.Discard, hooks) }()

	select {
	case <-listening:
	case <-time.After(15 * time.Second):
		t.Fatal("server never started")
	}

	// /admin/topics with the matching admin token MUST return 200.
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/admin/topics", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/admin/topics: status=%d, want 200 (story #92: admin routes must be wired)", resp.StatusCode)
	}

	// Wrong token MUST return 401 (proves auth middleware ran — i.e.
	// the route mounted, it didn't 404 due to missing wire-up).
	req, _ = http.NewRequest(http.MethodGet, "http://"+addr+"/admin/topics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin GET wrong token: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/admin/topics wrong token: status=%d, want 401", resp2.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestProductionWiring_ConfigSupportsAdminSection asserts the typed
// Config struct exposes hooks for the admin token, path prefix, and
// dead-letters wiring story #92 requires. The check is purely
// structural — applySets returns "unsupported key" for fields that
// don't exist, so the test fails until Phase B adds the keys.
//
// Runs without a database; complements the DB-gated wiring test above
// by failing CI even when TEST_DATABASE_URL is unset.
func TestProductionWiring_ConfigSupportsAdminSection(t *testing.T) {
	t.Parallel()

	cfg := &Config{}

	cases := []struct {
		key string
		val string
	}{
		{"admin.token", strings.Repeat("a", 32)},
		{"admin.path_prefix", "/admin"},
		// admin.rate_limit.* and admin.dead_letters_enabled are
		// candidate Phase B fields; Phase B picks the schema. This
		// test only pins the two that are absolutely required by AC #1.
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("Config does not support --set %s — Phase B must add this admin "+
					"section to config.go: %v", c.key, err)
			}
		})
	}
}
