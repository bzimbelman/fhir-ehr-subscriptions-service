// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// Story #99 RED Phase A — these tests prove the production wiring does
// NOT yet host every adapter Build* result under a supervisor.Supervisor.
// They fail until Phase B teaches buildSupervisedPipeline to:
//
//   - construct a supervisor.Supervisor for each pipeline worker;
//   - own the worker goroutine via Start (replacing the bare `go w.Run()`
//     pattern at cmd/fhir-subs/wiring.go:429-432);
//   - register a single shutdown hook in PhaseDrainInFlight that calls
//     Stop(ctx) on every supervisor;
//   - expose a SupervisorStatusReader handle to /admin/supervisor/status;
//   - emit OnHealthTick callbacks on cadence so a metrics layer observes
//     restart counts;
//   - recover panics, restart with backoff, and respect a hung-worker
//     stop deadline.
//
// The supervisor package itself is well-tested; these tests cover the
// host-side wiring contract in cmd/fhir-subs.

// fakeWorker is a deterministic Worker for the supervisor wiring tests.
// Behavior is dictated by the run func; calls counts each Run invocation.
type fakeWorker struct {
	calls atomic.Int64
	run   func(ctx context.Context, attempt int64) error
}

func (w *fakeWorker) Run(ctx context.Context) error {
	n := w.calls.Add(1)
	if w.run == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return w.run(ctx, n)
}

// TestPipelineSupervisor_RegistersDrainHookInPhaseDrainInFlight asserts
// that buildSupervisedPipeline registers a single shutdown hook in
// PhaseDrainInFlight whose name is greppable as a supervisor drain
// (callers typically look for the `pipeline.supervisors.drain` literal
// in operator runbooks).
func TestPipelineSupervisor_RegistersDrainHookInPhaseDrainInFlight(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	deps := pipelineSupervisorDeps{
		HL7:        &fakeWorker{},
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     50 * time.Millisecond,
			HealthInterval: 0,
		},
	}

	pl, err := buildSupervisedPipeline(deps)
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	names := lcMod.RegisteredShutdownNames(lifecycle.PhaseDrainInFlight)
	found := false
	for _, n := range names {
		if n == "pipeline.supervisors.drain" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected `pipeline.supervisors.drain` in PhaseDrainInFlight; got %v", names)
	}
}

// TestPipelineSupervisor_OwnsWorkerGoroutines asserts that Start invokes
// each Worker.Run at least once (proving the supervisor — not a bare
// `go w.Run()` — owns the goroutine).
func TestPipelineSupervisor_OwnsWorkerGoroutines(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	hl7 := &fakeWorker{}
	matcher := &fakeWorker{}
	submatcher := &fakeWorker{}
	scheduler := &fakeWorker{}

	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        hl7,
		Matcher:    matcher,
		Submatcher: submatcher,
		Scheduler:  scheduler,
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     50 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	// Workers run on supervisor-owned goroutines launched by the
	// helper. Wait until each has been invoked at least once.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hl7.calls.Load() > 0 && matcher.calls.Load() > 0 &&
			submatcher.calls.Load() > 0 && scheduler.calls.Load() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workers never invoked: hl7=%d matcher=%d sub=%d sched=%d",
		hl7.calls.Load(), matcher.calls.Load(), submatcher.calls.Load(), scheduler.calls.Load())
}

// TestPipelineSupervisor_RestartsOnPanic asserts that a panicking worker
// is restarted by its supervisor and that Status reports the restart
// count via Pipeline.Status (the slice the /admin/supervisor/status
// endpoint serializes).
func TestPipelineSupervisor_RestartsOnPanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	// HL7 worker panics on the first three Runs, then blocks on ctx.
	hl7 := &fakeWorker{
		run: func(ctx context.Context, attempt int64) error {
			if attempt <= 3 {
				panic("synthetic panic from fake hl7 worker")
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        hl7,
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	// Wait for the supervisor to record at least 3 restarts on hl7.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statuses := pl.Status()
		for _, st := range statuses {
			if st.AdapterID == "hl7-processor" && st.RestartCount >= 3 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	statuses := pl.Status()
	t.Fatalf("supervisor never recorded 3 restarts on hl7-processor; got %+v", statuses)
}

// TestPipelineSupervisor_StopKillsHungWorker asserts that Stop(ctx)
// returns within the budget even when a worker hangs on Run, and that
// supervisors honor the deadline.
func TestPipelineSupervisor_StopKillsHungWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	// Hung worker: ignores ctx for `hangFor` to simulate a stuck claim
	// loop. The supervisor must give up at the stop deadline rather than
	// blocking the host process indefinitely.
	hangFor := 2 * time.Second
	stuck := &fakeWorker{
		run: func(_ context.Context, _ int64) error {
			time.Sleep(hangFor)
			return errors.New("never observed cancel")
		},
	}
	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        stuck,
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}

	// Give the worker time to enter Run before Stop fires.
	time.Sleep(20 * time.Millisecond)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()

	start := time.Now()
	err = pl.Stop(stopCtx)
	elapsed := time.Since(start)

	// Stop must return within the deadline; a hang at process shutdown
	// is the failure mode this story exists to fix.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Stop blocked for %v (budget 200ms); supervisor did not kill hung worker", elapsed)
	}
	if err == nil {
		t.Fatalf("Stop with 200ms budget against 2s-hung worker must surface a deadline error; got nil")
	}
}

// TestPipelineSupervisor_OnHealthTickFires asserts the supervisor fires
// the host-supplied OnHealthTick callback on cadence so a metrics
// emitter can publish per-supervisor restart counters.
func TestPipelineSupervisor_OnHealthTickFires(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	var (
		mu    sync.Mutex
		seen  = make(map[string]int)
		ticks = make(chan struct{}, 16)
	)
	onHealth := func(name string, _ supervisor.Status) {
		mu.Lock()
		seen[name]++
		mu.Unlock()
		select {
		case ticks <- struct{}{}:
		default:
		}
	}

	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        &fakeWorker{},
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
			HealthInterval: 20 * time.Millisecond,
		},
		OnHealth: onHealth,
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	// Wait for at least one tick from each of the four supervisors.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(seen)
		mu.Unlock()
		if got >= 4 {
			break
		}
		select {
		case <-ticks:
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("expected health ticks from all 4 supervisors; got %v", seen)
		}
	}
}

// TestPipelineSupervisor_RestartCallableExposed asserts the host-side
// Pipeline handle exposes a Restart(adapterID) operator entry point so
// /admin/supervisor/status (or a future POST) can manually kick a
// stuck supervisor without a process restart. The contract: Restart
// signals the named supervisor to drop its current Run and rerun
// immediately (skipping the backoff sleep).
func TestPipelineSupervisor_RestartCallableExposed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	hl7 := &fakeWorker{
		run: func(ctx context.Context, _ int64) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        hl7,
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	// Wait for first Run.
	deadline := time.Now().Add(time.Second)
	for hl7.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hl7.calls.Load() == 0 {
		t.Fatalf("hl7 never ran")
	}

	before := hl7.calls.Load()
	if err := pl.Restart("hl7-processor"); err != nil {
		t.Fatalf("Restart hl7-processor: %v", err)
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if hl7.calls.Load() > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Restart did not re-invoke worker: before=%d after=%d", before, hl7.calls.Load())
}

// TestPipelineSupervisor_UnknownRestartReturnsError asserts Restart
// surfaces a not-found error for an unknown adapter ID rather than
// silently no-op. Operator-facing surfaces require loud errors.
func TestPipelineSupervisor_UnknownRestartReturnsError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        &fakeWorker{},
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	if err := pl.Restart("does-not-exist"); err == nil {
		t.Fatalf("Restart unknown adapter: expected error, got nil")
	}
}

// TestPipelineSupervisor_StatusListsAllFour asserts the Status snapshot
// surfaces every wired pipeline supervisor by name so the admin endpoint
// has something to render for operators.
func TestPipelineSupervisor_StatusListsAllFour(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{})
	if err != nil {
		t.Fatalf("lifecycle start: %v", err)
	}
	defer func() {
		lcMod.RequestShutdown(context.Background(), "test_cleanup")
		exitCtx, exitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer exitCancel()
		_ = lcMod.WaitForExit(exitCtx)
	}()

	pl, err := buildSupervisedPipeline(pipelineSupervisorDeps{
		HL7:        &fakeWorker{},
		Matcher:    &fakeWorker{},
		Submatcher: &fakeWorker{},
		Scheduler:  &fakeWorker{},
		Lifecycle:  lcMod,
		Backoff: PipelineSupervisorConfig{
			BackoffInitial: 5 * time.Millisecond,
			BackoffMax:     20 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("buildSupervisedPipeline: %v", err)
	}
	defer func() { _ = pl.Stop(context.Background()) }()

	want := map[string]bool{
		"hl7-processor": false,
		"matcher":       false,
		"submatcher":    false,
		"scheduler":     false,
	}
	for _, st := range pl.Status() {
		if _, ok := want[st.AdapterID]; ok {
			want[st.AdapterID] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("supervisor %q not present in Status snapshot: %+v", k, pl.Status())
		}
	}
}
