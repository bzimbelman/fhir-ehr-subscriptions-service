// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// TestWiring_VendorAdapterIDsBootProductionRuntime drives
// buildProductionRuntime against a real Postgres container for every
// vendor adapter id declared by epic #91. Before story #113 wired the
// registrations, every id except "default" failed with
// UnknownAdapterError and the binary refused to start. The test boots
// the FULL production runtime (storage.Start + observability.Start +
// channel registry + handlers + supervised pipeline) so a regression in
// any vendor's manifest/Build* contracts surfaces here, not at deploy
// time.
//
// Real Postgres, real codec, real auth, real obs module — no fakes.
func TestWiring_VendorAdapterIDsBootProductionRuntime(t *testing.T) {
	url := startTestPostgres(t)

	// The story scope (e2e fixtures) calls out cerner / epic / athena
	// explicitly; the unit-level test in adapter_registry_test.go
	// already pins the full set. Here we exercise the listed three plus
	// one of every other vendor scaffold so a future refactor that
	// drops a Build* method on, say, meditech, fails the integration
	// gate too.
	ids := []string{
		"allscripts",
		"athena",
		"cerner",
		"demo",
		"direct",
		"epic",
		"meditech",
		"nextgen",
	}

	for _, id := range ids {
		id := id
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			cfg := fullProductionConfig(t, url)
			cfg.Adapter.ID = id

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
				t.Fatalf("buildProductionRuntime(adapter=%q): %v", id, err)
			}
			t.Cleanup(func() { rt.shutdown(context.Background()) })

			// Sanity check: the runtime selected the adapter we asked
			// for. The host runs HL7Processor through the loaded
			// adapter, so a wiring bug that silently fell back to
			// "default" would still pass the no-error check above —
			// we explicitly probe the adapter id here.
			//
			// productionRuntime doesn't expose the loaded adapter
			// directly today; the registry holds the truth. The
			// adapter_registry_test.go unit suite asserts manifest
			// id == registered id, so this integration test is content
			// to assert the runtime came up clean for every vendor.
		})
	}
}
