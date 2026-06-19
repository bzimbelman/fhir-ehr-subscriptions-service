// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package main

import (
	"context"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// dbURLForTest returns the test postgres connection URL: TEST_PG_URL when
// set (a developer running an external pg via `docker run`), otherwise
// startTestPostgres which spins one up via testcontainers. The env-var
// path lets developers exercise these tests on a Mac with Rancher
// Desktop where testcontainers' reaper cannot mount the docker socket.
func dbURLForTest(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("TEST_PG_URL"); u != "" {
		return u
	}
	return startTestPostgres(t)
}

// Phase A (RED) tests for OpenProject story #96: production binary MUST
// wire FhirScanRunner workers into cmd/fhir-subs.
//
// Each test pins one acceptance criterion. They fail today because
// buildProductionRuntime never calls loadedAdapter.BuildFhirScanRunner,
// never constructs a scanrunner.Worker, and never registers it with the
// supervisedPipeline. Story #99 already supervises the four pipeline
// workers (hl7-processor, matcher, submatcher, scheduler), so the test
// must specifically check that a *FhirScanRunner* supervisor entry exists
// alongside those — a generic "any supervisor" probe would falsely pass.
//
// AC reference (from the story):
//   (a) wiring.go MUST call loadedAdapter.BuildFhirScanRunner for every
//       adapter declaring Capabilities.FhirScanRunner: true.
//   (b) The returned runner MUST be registered with the supervisor for
//       lifecycle + restart.
//   (d) Unit test in cmd/fhir-subs/wiring_test.go asserting that a fake
//       adapter declaring scan capability ends up with a runner under
//       supervision.
//
// AC (c) (manifest validation rejects FhirScanRunner=true with nil
// builder) is already covered by
// internal/adapter/registry.TestLoadRejectsCapabilityWithoutBuilder
// (case "FhirScanRunner declared but builder returns nil"). That
// coverage is intentionally NOT duplicated here — the registry layer is
// the single owner of the validation, per #65.
//
// AC (e) (e2e harness boots the binary and asserts a synthetic Bundle
// reaches the matcher within 90s) is owned by the e2e/orchestrator
// suite and is out of scope for Phase A unit tests.

// scanRunnerSupervisorPattern is the canonical lowercase substring the
// scan-runner supervisor id must contain so this test can identify it
// among the existing pipeline supervisors. Phase B may use any of:
//
//	"fhir-scan-runner"
//	"scanrunner"
//	"<adapter-id>:scanrunner"
//
// All match this substring check; the only constraint is that the id
// distinguishes the scan runner from hl7-processor, matcher, submatcher,
// and scheduler.
const scanRunnerSupervisorPattern = "scan"

// collectSupervisorAdapterIDs walks *productionRuntime via reflection
// and collects the AdapterID of every reachable *supervisor.Supervisor.
// The status snapshot is the canonical surface (supervisor.Supervisor
// stamps AdapterID into every Status) so we read via Status() rather
// than poking at unexported supervisor fields.
//
// The walk descends into struct, pointer, slice, and map fields — that
// is enough to reach the supervisedPipeline.supervisors map (#99) and
// any new field Phase B adds (single supervisor, slice, or map).
func collectSupervisorAdapterIDs(v reflect.Value) []string {
	visited := map[uintptr]bool{}
	var out []string
	walkCollect(v, &out, visited, 0)
	return out
}

func walkCollect(v reflect.Value, out *[]string, visited map[uintptr]bool, depth int) {
	if depth > 8 || !v.IsValid() {
		return
	}
	wantType := reflect.TypeOf((*supervisor.Supervisor)(nil))
	if v.Type() == wantType {
		if !v.IsNil() {
			sv := v.Interface().(*supervisor.Supervisor)
			*out = append(*out, sv.Status().AdapterID)
		}
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		ptr := v.Pointer()
		if visited[ptr] {
			return
		}
		visited[ptr] = true
		walkCollect(v.Elem(), out, visited, depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanInterface() && f.CanAddr() {
				//nolint:gosec // G103: reflect on unexported fields requires this idiom.
				f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
			}
			walkCollect(f, out, visited, depth+1)
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkCollect(v.Index(i), out, visited, depth+1)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkCollect(iter.Value(), out, visited, depth+1)
		}
	}
}

// findScanRunnerSupervisor returns a non-nil *supervisor.Supervisor whose
// AdapterID matches the scan-runner pattern, or nil if none is reachable
// from the runtime.
func findScanRunnerSupervisor(rt *productionRuntime) *supervisor.Supervisor {
	v := reflect.ValueOf(rt).Elem()
	wantType := reflect.TypeOf((*supervisor.Supervisor)(nil))
	visited := map[uintptr]bool{}
	return walkFindScan(v, wantType, visited, 0)
}

func walkFindScan(v reflect.Value, want reflect.Type, visited map[uintptr]bool, depth int) *supervisor.Supervisor {
	if depth > 8 || !v.IsValid() {
		return nil
	}
	if v.Type() == want {
		if v.IsNil() {
			return nil
		}
		sv := v.Interface().(*supervisor.Supervisor)
		if strings.Contains(strings.ToLower(sv.Status().AdapterID), scanRunnerSupervisorPattern) {
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
		return walkFindScan(v.Elem(), want, visited, depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanInterface() && f.CanAddr() {
				//nolint:gosec // G103: reflect on unexported fields requires this idiom.
				f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
			}
			if got := walkFindScan(f, want, visited, depth+1); got != nil {
				return got
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if got := walkFindScan(v.Index(i), want, visited, depth+1); got != nil {
				return got
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if got := walkFindScan(iter.Value(), want, visited, depth+1); got != nil {
				return got
			}
		}
	}
	return nil
}

// TestProductionRuntime_RegistersScanRunnerSupervisor pins AC (a), (b),
// and (d): when the default adapter (which declares
// Capabilities.FhirScanRunner=true) is loaded, buildProductionRuntime
// MUST construct a scanrunner.Worker, wrap it in a supervisor.Supervisor
// with an identifying adapter id, and register it for lifecycle drain.
//
// FAILS today: wiring.go never calls BuildFhirScanRunner. Story #99
// already wires four pipeline supervisors (hl7-processor, matcher,
// submatcher, scheduler), so a generic "any supervisor exists" probe
// would falsely pass. This test specifically asserts a fifth supervisor
// whose adapter id contains "scan".
func TestProductionRuntime_RegistersScanRunnerSupervisor(t *testing.T) {
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
	hasScan := false
	for _, id := range ids {
		if strings.Contains(strings.ToLower(id), scanRunnerSupervisorPattern) {
			hasScan = true
			break
		}
	}
	if !hasScan {
		t.Fatalf("productionRuntime has no supervisor whose adapter id contains %q. "+
			"Found supervisors: %v. Phase B MUST call loadedAdapter.BuildFhirScanRunner "+
			"for adapters declaring Capabilities.FhirScanRunner=true, construct a "+
			"scanrunner.Worker via scanrunner.New, and register it with the "+
			"supervisedPipeline (story #96 AC a, b, d). The default adapter declares "+
			"FhirScanRunner=true so the wiring path is exercised by this fixture.",
			scanRunnerSupervisorPattern, ids)
	}
}

// TestProductionRuntime_ScanSupervisorDrainsOnShutdown pins AC (b)
// lifecycle: once the lifecycle module's drain phase runs, the
// FhirScanRunner supervisor must reach StateStopped within the configured
// grace period. Without this, the scan-runner goroutine would hold up
// shutdown and ForcedExit would trip.
//
// FAILS today: there is no scan-runner supervisor to begin with — the
// findScanRunnerSupervisor probe returns nil and the test fails at
// Fatalf, same RED signal as TestProductionRuntime_RegistersScanRunnerSupervisor.
// After Phase B, when the supervisor exists, this test additionally asserts
// that shutdown drives it to StateStopped.
func TestProductionRuntime_ScanSupervisorDrainsOnShutdown(t *testing.T) {
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

	scanSv := findScanRunnerSupervisor(rt)
	if scanSv == nil {
		t.Fatalf("productionRuntime has no scan-runner supervisor (id containing %q). "+
			"Phase B MUST wire the FhirScanRunner supervisor before this test can "+
			"observe lifecycle behavior (story #96 AC b)",
			scanRunnerSupervisorPattern)
	}

	// Drive shutdown. The pipeline.supervisors.drain hook (PhaseDrainInFlight)
	// must stop every supervisor — including the new scan-runner one — as
	// part of its normal completion; the shutdown report must NOT report
	// ForcedExit, and the scan-runner supervisor's observed state must
	// reach StateStopped within the grace period.
	lcMod.RequestShutdown(context.Background(), "test")
	rep := lcMod.WaitForExit(ctx)

	if rep.ForcedExit {
		t.Errorf("shutdown reported ForcedExit=true; scanrunner supervisor "+
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
		st = scanSv.Status()
		if st.State == supervisor.StateStopped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("scanrunner supervisor did not reach StateStopped after "+
		"shutdown; final status=%+v", st)
}
