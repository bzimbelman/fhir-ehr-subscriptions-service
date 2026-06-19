// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"sort"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	adapterspi "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// expectedAdapterIDs is the full list of vendor IDs the production binary
// must register at startup. Story #113 (epic #91) — every adapter package
// under adapters/* with a manifest must be reachable via cfg.Adapter.ID.
var expectedAdapterIDs = []string{
	"allscripts",
	"athena",
	"cerner",
	"default",
	"demo",
	"direct",
	"epic",
	"meditech",
	"nextgen",
}

// TestRegisterAllAdapters_ContainsEveryVendor pins the wiring contract
// for OP #113: registerAllAdapters MUST add every vendor adapter to the
// registry so an operator config `adapter.id: <vendor>` resolves
// successfully. Before this fix only "default" was registered and any
// other id failed with UnknownAdapterError at startup.
func TestRegisterAllAdapters_ContainsEveryVendor(t *testing.T) {
	t.Parallel()

	adReg := registry.New()
	if err := registerAllAdapters(adReg); err != nil {
		t.Fatalf("registerAllAdapters: %v", err)
	}

	got := adReg.IDs()
	sort.Strings(got)

	if len(got) != len(expectedAdapterIDs) {
		t.Fatalf("registered ids: got %v, want %v", got, expectedAdapterIDs)
	}
	for i, want := range expectedAdapterIDs {
		if got[i] != want {
			t.Errorf("ids[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestRegisterAllAdapters_LoadEachID asserts every registered id can be
// loaded through the host registry's normal Load path. This proves the
// factory→manifest wiring is consistent for every vendor adapter, not
// just that the id is in the map: Load runs the manifest-id-mismatch
// check (#65), so a copy-paste bug where e.g. cerner's factory returned
// the epic adapter would surface here.
func TestRegisterAllAdapters_LoadEachID(t *testing.T) {
	t.Parallel()

	adReg := registry.New()
	if err := registerAllAdapters(adReg); err != nil {
		t.Fatalf("registerAllAdapters: %v", err)
	}
	if err := adReg.ValidateAll(context.Background(), adapterspi.HostSPIVersion); err != nil {
		t.Fatalf("ValidateAll: %v", err)
	}
	for _, id := range expectedAdapterIDs {
		loaded, err := adReg.Load(context.Background(), registry.LoadConfig{
			AdapterID:  id,
			HostSpiVer: adapterspi.HostSPIVersion,
		})
		if err != nil {
			t.Errorf("Load(%q): %v", id, err)
			continue
		}
		if got := loaded.Manifest().ID; got != id {
			t.Errorf("Load(%q).Manifest().ID = %q; manifest must match registered id", id, got)
		}
	}
}

// TestRegisterAllAdapters_UnknownIDFailsLoud asserts that a config
// pointing at an id that does NOT exist still fails with the registry's
// UnknownAdapterError — the wiring change must not silently fall back
// to "default" or any other adapter on a typo.
func TestRegisterAllAdapters_UnknownIDFailsLoud(t *testing.T) {
	t.Parallel()

	adReg := registry.New()
	if err := registerAllAdapters(adReg); err != nil {
		t.Fatalf("registerAllAdapters: %v", err)
	}
	_, err := adReg.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "definitely-not-a-real-vendor",
		HostSpiVer: adapterspi.HostSPIVersion,
	})
	if err == nil {
		t.Fatal("Load with unknown id: expected error, got nil (wiring must fail loud, not silently fall back)")
	}
}
