// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"testing"
	"time"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// startTestPostgres returns a connection URL for a Postgres 16 container,
// or t.Skip if Docker isn't available.
func startTestPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("wiring_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
		tcpostgres.WithSQLDriver("pgx/v5"),
	)
	if err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Skipf("connection string unavailable: %v", err)
	}
	return url
}

// b64Key returns a deterministic 32-byte AES-256 key, base64-encoded
// the way operators supply codec.keys[].material in YAML.
func b64Key() string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// fullProductionConfig builds a *Config with every required field for
// buildProductionRuntime to succeed: database URL, codec keys, auth
// audience, MLLP listener, topic catalog dir.
func fullProductionConfig(t *testing.T, dbURL string) *Config {
	t.Helper()
	return &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Environment: "test", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", ProbeBind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: b64Key()},
			},
		},
		Auth: AuthConfig{
			Audience: "fhir-subs-test",
		},
		Topics: TopicsConfig{CatalogDir: t.TempDir()},
	}
}

// TestWiring_StorageShutdownHookRegistered asserts that buildProductionRuntime
// wires storage.Start (partition maintainer + retention sweeper goroutines)
// into the lifecycle module's shutdown sequencer. Story #95 acceptance
// criterion: "Unit test in cmd/fhir-subs/wiring_test.go asserting partition +
// retention runners are listed in the shutdown registry."
func TestWiring_StorageShutdownHookRegistered(t *testing.T) {
	t.Parallel()

	url := startTestPostgres(t)
	cfg := fullProductionConfig(t, url)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.Default()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	t.Cleanup(func() { rt.shutdown(context.Background()) })

	// Assert the storage drain hook is registered in PhaseDrainInFlight.
	// Storage owns both the partition maintainer goroutine and the
	// retention sweeper goroutine; a single hook drains both via
	// Storage.Shutdown.
	names := lcMod.RegisteredShutdownNames(lifecycle.PhaseDrainInFlight)
	found := false
	for _, n := range names {
		if n == "storage.drain" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'storage.drain' shutdown hook in PhaseDrainInFlight, got: %v", names)
	}

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}

// TestWiring_StorageHookCancelsBackgroundRunners asserts that triggering
// the lifecycle shutdown sequencer cancels storage.Start's partition
// maintainer + retention sweeper goroutines so the process exits
// cleanly. Without this, a stuck Tick could pin shutdown past the
// configured grace period.
func TestWiring_StorageHookCancelsBackgroundRunners(t *testing.T) {
	t.Parallel()

	url := startTestPostgres(t)
	cfg := fullProductionConfig(t, url)
	cfg.Lifecycle.ShutdownGracePeriod = 5 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.Default()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: cfg.Lifecycle.ShutdownGracePeriod,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	// Drive shutdown and confirm the lifecycle module reports a clean
	// drain (no forced-exit, no failed hooks). Storage.drain runs in
	// PhaseDrainInFlight and must complete before the budget expires.
	lcMod.RequestShutdown(context.Background(), "test")
	rep := lcMod.WaitForExit(ctx)

	if rep.ForcedExit {
		t.Errorf("shutdown reported ForcedExit=true; storage drain likely did not respect ctx cancellation: failed=%v", rep.HooksFailed)
	}
	for _, name := range rep.HooksFailed {
		if name == "storage.drain" {
			t.Errorf("storage.drain shutdown hook failed/timed out")
		}
	}
}

// TestWiring_NextMonthPartitionExistsAfterStartup asserts that once
// buildProductionRuntime returns, the partition maintainer has already
// run its initial Tick and the next-month partition exists for
// resource_changes. This covers the migration 0001 boot regression: 0001
// creates only 3 partitions, so a binary that ran for >3 months without
// the partition maintainer would lose inserts at "no partition for
// value" once wall-clock advanced past the seeded range.
func TestWiring_NextMonthPartitionExistsAfterStartup(t *testing.T) {
	t.Parallel()

	url := startTestPostgres(t)
	cfg := fullProductionConfig(t, url)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.Default()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	t.Cleanup(func() { rt.shutdown(context.Background()) })

	// Wait briefly for the initial partition Tick to complete. storage.Start
	// fires the first tick synchronously inside the goroutine, so the
	// partition usually exists immediately, but we poll for a short
	// budget to absorb scheduling jitter.
	next := time.Now().AddDate(0, 1, 0)
	suffix := time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006_01")
	target := "resource_changes_" + suffix

	deadline := time.Now().Add(10 * time.Second)
	var exists bool
	for time.Now().Before(deadline) {
		if err := rt.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname = $1)`, target,
		).Scan(&exists); err != nil {
			t.Fatalf("query partition existence: %v", err)
		}
		if exists {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !exists {
		t.Errorf("expected next-month partition %s to exist after startup", target)
	}

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}
