// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// Phase A (RED) tests for OpenProject story #97: production binary MUST
// wire VendorAPIClient workers into cmd/fhir-subs.
//
// Each test pins one acceptance criterion. They fail today because
// buildProductionRuntime never calls loadedAdapter.BuildVendorAPIClient,
// never constructs a vendorclient.Worker, and never registers it with
// the supervisedPipeline. Stories #99 and #96 already supervise five
// pipeline workers (hl7-processor, matcher, submatcher, scheduler,
// fhir-scan-runner), so the test must specifically check that a
// *VendorAPIClient* supervisor entry exists alongside those — a generic
// "any supervisor" probe would falsely pass.
//
// AC reference (from the story):
//   (a) wiring.go MUST call loadedAdapter.BuildVendorAPIClient for every
//       adapter declaring Capabilities.VendorAPIClient: true.
//   (b) The returned client MUST be registered with the supervisor for
//       lifecycle + restart.
//   (d) Unit test asserting the client is reachable via the
//       supervisor's Status snapshot.
//
// AC (c) (manifest validation rejects VendorAPIClient=true with nil
// builder) is already covered by
// internal/adapter/registry.TestLoadRejectsCapabilityWithoutBuilder
// (case "VendorAPIClient declared but builder returns nil"). That
// coverage is intentionally NOT duplicated here — the registry layer
// is the single owner of the validation, per #65.
//
// AC (e) (e2e harness boots the binary with a synthetic change-feed
// vendor adapter and asserts the matcher receives the event) is owned
// by the e2e/orchestrator suite and is out of scope for Phase A unit
// tests, mirroring the precedent set by story #96.

// vendorClientSupervisorPattern is the canonical lowercase substring
// the vendor-client supervisor id must contain so this test can identify
// it among the existing pipeline supervisors. Phase B may use any of:
//
//	"vendor-api-client"
//	"vendorclient"
//	"<adapter-id>:vendorclient"
//
// All match this substring check; the only constraint is that the id
// distinguishes the vendor client from hl7-processor, matcher,
// submatcher, scheduler, and fhir-scan-runner.
const vendorClientSupervisorPattern = "vendor"

// findVendorClientSupervisor returns a non-nil *supervisor.Supervisor
// whose AdapterID matches the vendor-client pattern, or nil if none is
// reachable from the runtime. Walks the same shapes (struct, pointer,
// slice, map) as the scanrunner test's walker, but matches the
// "vendor" substring instead of "scan".
func findVendorClientSupervisor(rt *productionRuntime) *supervisor.Supervisor {
	v := reflect.ValueOf(rt).Elem()
	wantType := reflect.TypeOf((*supervisor.Supervisor)(nil))
	visited := map[uintptr]bool{}
	return walkFindVendor(v, wantType, visited, 0)
}

func walkFindVendor(v reflect.Value, want reflect.Type, visited map[uintptr]bool, depth int) *supervisor.Supervisor {
	if depth > 8 || !v.IsValid() {
		return nil
	}
	if v.Type() == want {
		if v.IsNil() {
			return nil
		}
		sv := v.Interface().(*supervisor.Supervisor)
		if strings.Contains(strings.ToLower(sv.Status().AdapterID), vendorClientSupervisorPattern) {
			return sv
		}
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		ptr := v.Pointer()
		if visited[ptr] {
			return nil
		}
		visited[ptr] = true
		return walkFindVendor(v.Elem(), want, visited, depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanInterface() && f.CanAddr() {
				//nolint:gosec // G103: reflect on unexported fields requires this idiom.
				f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
			}
			if got := walkFindVendor(f, want, visited, depth+1); got != nil {
				return got
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if got := walkFindVendor(v.Index(i), want, visited, depth+1); got != nil {
				return got
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if got := walkFindVendor(iter.Value(), want, visited, depth+1); got != nil {
				return got
			}
		}
	}
	return nil
}

// TestProductionRuntime_RegistersVendorClientSupervisor pins AC (a),
// (b), and (d): when the loaded adapter declares
// Capabilities.VendorAPIClient=true, buildProductionRuntime MUST call
// BuildVendorAPIClient, construct a vendorclient.Worker, wrap it in a
// supervisor.Supervisor with an identifying adapter id, and register
// it for lifecycle drain.
//
// FAILS today: wiring.go never calls BuildVendorAPIClient. Stories #99
// and #96 already wire five pipeline supervisors, so a generic "any
// supervisor exists" probe would falsely pass. This test specifically
// asserts a sixth supervisor whose adapter id contains "vendor".
//
// The default adapter is extended in Phase B to declare
// Capabilities.VendorAPIClient=true and return an idle vendor client
// (Consume blocks until ctx done; Translate is unreachable on the
// idle path), mirroring the precedent set by story #96 where the
// default adapter declares FhirScanRunner=true with an empty plan.
func TestProductionRuntime_RegistersVendorClientSupervisor(t *testing.T) {
	t.Parallel()

	url := dbURLForTest(t)
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

	ids := collectSupervisorAdapterIDs(reflect.ValueOf(rt).Elem())
	hasVendor := false
	for _, id := range ids {
		if strings.Contains(strings.ToLower(id), vendorClientSupervisorPattern) {
			hasVendor = true
			break
		}
	}
	if !hasVendor {
		t.Fatalf("productionRuntime has no supervisor whose adapter id contains %q. "+
			"Found supervisors: %v. Phase B MUST call loadedAdapter.BuildVendorAPIClient "+
			"for adapters declaring Capabilities.VendorAPIClient=true, construct a "+
			"vendorclient.Worker via vendorclient.New, and register it with the "+
			"supervisedPipeline (story #97 AC a, b, d). The default adapter must "+
			"declare VendorAPIClient=true so the wiring path is exercised by this fixture.",
			vendorClientSupervisorPattern, ids)
	}
}

// TestProductionRuntime_VendorClientSupervisorDrainsOnShutdown pins AC
// (b) lifecycle: once the lifecycle module's drain phase runs, the
// VendorAPIClient supervisor must reach StateStopped within the
// configured grace period. Without this, the vendor-client goroutine
// would hold up shutdown and ForcedExit would trip.
//
// FAILS today: there is no vendor-client supervisor to begin with —
// the findVendorClientSupervisor probe returns nil and the test fails
// at Fatalf, same RED signal as
// TestProductionRuntime_RegistersVendorClientSupervisor. After Phase B,
// when the supervisor exists, this test additionally asserts that
// shutdown drives it to StateStopped.
func TestProductionRuntime_VendorClientSupervisorDrainsOnShutdown(t *testing.T) {
	t.Parallel()

	url := dbURLForTest(t)
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

	vendorSv := findVendorClientSupervisor(rt)
	if vendorSv == nil {
		t.Fatalf("productionRuntime has no vendor-client supervisor (id containing %q). "+
			"Phase B MUST wire the VendorAPIClient supervisor before this test can "+
			"observe lifecycle behavior (story #97 AC b)",
			vendorClientSupervisorPattern)
	}

	// Drive shutdown. The pipeline.supervisors.drain hook
	// (PhaseDrainInFlight) must stop every supervisor — including the
	// new vendor-client one — as part of its normal completion; the
	// shutdown report must NOT report ForcedExit, and the vendor-client
	// supervisor's observed state must reach StateStopped within the
	// grace period.
	lcMod.RequestShutdown(context.Background(), "test")
	rep := lcMod.WaitForExit(ctx)

	if rep.ForcedExit {
		t.Errorf("shutdown reported ForcedExit=true; vendor-client supervisor "+
			"likely did not exit within the grace period: hooks_failed=%v",
			rep.HooksFailed)
	}

	// Allow a short tail for the supervisor goroutine to record its
	// StateStopped after Run returns. The supervisor sets state via
	// atomic store inside Start's defer, which runs after the worker's
	// Run unwinds, so a poll loop is more robust than a single read.
	deadline := time.Now().Add(2 * time.Second)
	var st supervisor.Status
	for time.Now().Before(deadline) {
		st = vendorSv.Status()
		if st.State == supervisor.StateStopped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("vendor-client supervisor did not reach StateStopped after "+
		"shutdown; final status=%+v", st)
}
