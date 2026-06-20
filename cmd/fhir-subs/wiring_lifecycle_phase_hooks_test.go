// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// Story #207 — RED Phase A. wiring.go must register a shutdown hook
// against EACH of the three orchestrated phases so the five-phase
// sequencer sees real work in every phase. Today PhaseStopAccepting
// has only the optional MLLP listener hook (and nothing about the HTTP
// listener, scheduler stop-accepting, etc.) and PhaseCloseConnections
// is missing the database pool close (it's bundled into storage.drain
// in the wrong phase).
//
// These tests pin the operator-visible hook contract:
//
//   PhaseStopAccepting: {http.listener.stop_accepting, mllp.stop_accepting?, scheduler.stop_accepting}
//   PhaseDrainInFlight: {pipeline.supervisors.drain, api.activations.drain, storage.drain}
//   PhaseCloseConnections: {database.pool.close, channels.close, observability.shutdown, resthook.activator.close}
//
// MLLP listener is conditional on cfg.MLLP.Listeners — these tests
// exercise the production config (no MLLP) so mllp.stop_accepting is
// allowed to be absent.

// requirePhaseHas checks that every name in want appears in the list
// of hook names registered in phase.
func requirePhaseHas(t *testing.T, lcMod *lifecycle.LifecycleModule, phase lifecycle.Phase, want []string) {
	t.Helper()
	names := lcMod.RegisteredShutdownNames(phase)
	for _, w := range want {
		if !slices.Contains(names, w) {
			t.Errorf("phase %s: missing hook %q (registered: %v)", phase.String(), w, names)
		}
	}
}

// TestWiring_PhaseStopAcceptingHasHTTPHook asserts the HTTP listener's
// stop-accepting transition is wired as a sequencer hook in
// PhaseStopAccepting. Today srv.Shutdown is invoked directly from
// run.go after RequestShutdown returns — so the lifecycle module's
// Phase 2 has zero HTTP-listener responsibility and the per-phase
// budget cannot bound it.
func TestWiring_PhaseStopAcceptingHasHTTPHook(t *testing.T) {
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

	requirePhaseHas(t, lcMod, lifecycle.PhaseStopAccepting, []string{
		"http.listener.stop_accepting",
		"scheduler.stop_accepting",
	})

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}

// TestWiring_PhaseDrainInFlightHasPipelineAndActivationsAndStorage
// pins the drain phase's hook surface. pipeline.supervisors.drain is
// owned by buildSupervisedPipeline; api.activations.drain and
// storage.drain are owned by registerLifecycle. All three MUST be
// present in PhaseDrainInFlight.
func TestWiring_PhaseDrainInFlightHasPipelineAndActivationsAndStorage(t *testing.T) {
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

	requirePhaseHas(t, lcMod, lifecycle.PhaseDrainInFlight, []string{
		"pipeline.supervisors.drain",
		"api.activations.drain",
		"storage.drain",
	})

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}

// TestWiring_PhaseCloseConnectionsHasDBPoolAndChannelsAndActivator
// asserts the close-connections phase has the database pool close, the
// channels close fan-out, observability.shutdown, AND the rest-hook
// activator transport close.
//
// Today database.pool.close is missing: storage.drain in
// PhaseDrainInFlight closes the pool as a side effect, and there is no
// hook in PhaseCloseConnections for the connection-tier teardown. The
// rest-hook activator's idle-conn transport also has no close path.
func TestWiring_PhaseCloseConnectionsHasDBPoolAndChannelsAndActivator(t *testing.T) {
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

	requirePhaseHas(t, lcMod, lifecycle.PhaseCloseConnections, []string{
		"database.pool.close",
		"channels.close",
		"observability.shutdown",
		"resthook.activator.close",
	})

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}

// TestWiring_PhaseCloseConnectionsHasAuthTokenAndJWKSHooks — OP #208
// RED. The AC extends the rest-hook activator close pattern (already
// present at line ~198-206) to the auth verifier's JWKS fetcher AND
// the token endpoint client. Both spin a long-lived http.Transport
// that holds idle TCP/TLS sockets to the operator's IDP; on shutdown
// those connections must be released so the process exits without
// warm sockets.
//
// Today neither hook is registered. This test pins the contract that
// PhaseCloseConnections includes "auth.token_endpoint.close" and
// "auth.jwks_fetcher.close" in addition to the rest-hook activator
// close.
func TestWiring_PhaseCloseConnectionsHasAuthTokenAndJWKSHooks(t *testing.T) {
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

	requirePhaseHas(t, lcMod, lifecycle.PhaseCloseConnections, []string{
		"auth.token_endpoint.close",
		"auth.jwks_fetcher.close",
	})

	lcMod.RequestShutdown(context.Background(), "test")
	_ = lcMod.WaitForExit(ctx)
}
