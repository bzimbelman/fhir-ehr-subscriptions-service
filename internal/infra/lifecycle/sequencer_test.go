// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests cover LLD §6 (the sequencer) and the LifecycleModule contract:
//
//   - The five phases run in order: MarkUnready → StopAccepting → Drain →
//     CloseConnections → Exit.
//   - Hooks within a phase run concurrently.
//   - The total wall-clock is bounded by ShutdownGracePeriod even if a
//     hook hangs (ForcedExit=true on grace expiry).
//   - shutdown_in_progress flips at the very start (before any sleep or
//     hook), so /readyz starts returning 503 immediately.
//   - RequestShutdown is idempotent (only the first reason is recorded).
//   - HooksFailed lists the names of hooks that errored or timed out.
//   - PhaseDurations is populated for every executed phase.

// helper to build a module without starting the HTTP listener — pure
// in-process plumbing the tests can drive.
func newTestModule(t *testing.T, cfg LifecycleConfig) *LifecycleModule {
	t.Helper()
	if cfg.ShutdownGracePeriod == 0 {
		cfg.ShutdownGracePeriod = 200 * time.Millisecond
	}
	if cfg.ProbeObserveWindow == 0 {
		cfg.ProbeObserveWindow = time.Millisecond
	}
	if cfg.ReadinessCheckTimeout == 0 {
		cfg.ReadinessCheckTimeout = 50 * time.Millisecond
	}
	mod, err := newModuleForTest(cfg, LifecycleContext{Metrics: nopMetrics{}})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })
	return mod
}

func TestSequencer_PhasesRunInOrder(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{})

	var orderMu sync.Mutex
	var order []Phase
	record := func(p Phase) ShutdownHook {
		return ShutdownHook{
			Name:  "hook-" + p.String(),
			Phase: p,
			Run: func(ctx context.Context) error {
				orderMu.Lock()
				order = append(order, p)
				orderMu.Unlock()
				return nil
			},
		}
	}
	mod.RegisterShutdown(record(PhaseStopAccepting))
	mod.RegisterShutdown(record(PhaseDrainInFlight))
	mod.RegisterShutdown(record(PhaseCloseConnections))

	mod.RequestShutdown(context.Background(), "test")
	report := mod.WaitForExit(context.Background())

	want := []Phase{PhaseStopAccepting, PhaseDrainInFlight, PhaseCloseConnections}
	if !slices.Equal(order, want) {
		t.Fatalf("phase order: got %v want %v", order, want)
	}
	if report.ForcedExit {
		t.Fatalf("ForcedExit should be false on graceful shutdown")
	}
	if report.Reason != "test" {
		t.Fatalf("Reason: got %q want \"test\"", report.Reason)
	}
}

func TestSequencer_HooksWithinPhaseRunConcurrently(t *testing.T) {
	t.Parallel()
	cfg := LifecycleConfig{
		ShutdownGracePeriod: time.Second,
		ProbeObserveWindow:  time.Millisecond,
	}
	mod := newTestModule(t, cfg)

	const n = 6
	const sleep = 30 * time.Millisecond
	var done atomic.Int32
	for i := 0; i < n; i++ {
		i := i
		mod.RegisterShutdown(ShutdownHook{
			Name:  "drain-" + itoa(i),
			Phase: PhaseDrainInFlight,
			Run: func(ctx context.Context) error {
				time.Sleep(sleep)
				done.Add(1)
				return nil
			},
		})
	}

	mod.RequestShutdown(context.Background(), "test")
	report := mod.WaitForExit(context.Background())

	if got := done.Load(); got != n {
		t.Fatalf("only %d/%d drain hooks completed", got, n)
	}
	// Serial would be n*sleep = 180ms. Concurrent should be ~sleep.
	dur := report.PhaseDurations[PhaseDrainInFlight]
	if dur > time.Duration(n)*sleep/2 {
		t.Fatalf("drain phase did not run concurrently: %v", dur)
	}
}

func TestSequencer_TotalBudgetEnforcedOnHangingHook(t *testing.T) {
	t.Parallel()
	cfg := LifecycleConfig{
		ShutdownGracePeriod: 80 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	}
	mod := newTestModule(t, cfg)

	mod.RegisterShutdown(ShutdownHook{
		Name:  "hanger",
		Phase: PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})

	start := time.Now()
	mod.RequestShutdown(context.Background(), "test")
	report := mod.WaitForExit(context.Background())
	elapsed := time.Since(start)

	if elapsed > 400*time.Millisecond {
		t.Fatalf("sequencer ran past grace*5 — total budget not enforced: %v", elapsed)
	}
	if !report.ForcedExit {
		t.Fatalf("hanging hook past total grace must set ForcedExit=true")
	}
	if !slices.Contains(report.HooksFailed, "hanger") {
		t.Fatalf("HooksFailed should list hanger; got %v", report.HooksFailed)
	}
}

func TestSequencer_MarksUnreadyAtPhase1(t *testing.T) {
	t.Parallel()
	cfg := LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  20 * time.Millisecond,
	}
	mod := newTestModule(t, cfg)

	if mod.reg.shutdownInProgress() {
		t.Fatalf("shutdown_in_progress must be false before RequestShutdown")
	}
	flippedDuringPhase2 := atomic.Bool{}
	mod.RegisterShutdown(ShutdownHook{
		Name:  "observer",
		Phase: PhaseStopAccepting,
		Run: func(ctx context.Context) error {
			// By phase 2 the flag MUST already be set (LLD §6 phase 1).
			if mod.reg.shutdownInProgress() {
				flippedDuringPhase2.Store(true)
			}
			return nil
		},
	})

	mod.RequestShutdown(context.Background(), "test")
	mod.WaitForExit(context.Background())

	if !flippedDuringPhase2.Load() {
		t.Fatalf("shutdown_in_progress must be set before phase 2 hooks run (LLD §6)")
	}
}

func TestSequencer_RequestShutdownIsIdempotent(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	// Two callers race to request shutdown; only the first reason is
	// recorded.
	go mod.RequestShutdown(context.Background(), "first")
	go mod.RequestShutdown(context.Background(), "second")

	report := mod.WaitForExit(context.Background())

	if report.Reason != "first" && report.Reason != "second" {
		t.Fatalf("Reason: got %q want one of {first, second}", report.Reason)
	}
	// Second request must not re-trigger the sequencer or panic.
}

func TestSequencer_RegistrationRefusedAfterStart(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  10 * time.Millisecond,
	})

	mod.RequestShutdown(context.Background(), "test")
	// Allow phase 1 mark-unready to flip.
	time.Sleep(2 * time.Millisecond)
	err := mod.reg.registerReadiness("late", okCheck)
	if !errors.Is(err, errRegistrationAfterShutdown) {
		t.Fatalf("registerReadiness after shutdown: got %v want %v", err, errRegistrationAfterShutdown)
	}
	mod.WaitForExit(context.Background())
}

func TestSequencer_HookErrorListedInReport(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})
	mod.RegisterShutdown(ShutdownHook{
		Name:  "broken",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return errors.New("boom") },
	})
	mod.RegisterShutdown(ShutdownHook{
		Name:  "good",
		Phase: PhaseDrainInFlight,
		Run:   func(ctx context.Context) error { return nil },
	})

	mod.RequestShutdown(context.Background(), "test")
	report := mod.WaitForExit(context.Background())

	if !slices.Contains(report.HooksFailed, "broken") {
		t.Fatalf("HooksFailed should list broken; got %v", report.HooksFailed)
	}
	if slices.Contains(report.HooksFailed, "good") {
		t.Fatalf("HooksFailed should not list good; got %v", report.HooksFailed)
	}
	if report.ForcedExit {
		t.Fatalf("a hook error should not force exit; only grace expiry does")
	}
}

func TestSequencer_PhaseDurationsPopulated(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})
	mod.RegisterShutdown(ShutdownHook{
		Name:  "stop",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return nil },
	})

	mod.RequestShutdown(context.Background(), "test")
	report := mod.WaitForExit(context.Background())

	for _, p := range []Phase{PhaseMarkUnready, PhaseStopAccepting, PhaseDrainInFlight, PhaseCloseConnections} {
		if _, ok := report.PhaseDurations[p]; !ok {
			t.Fatalf("PhaseDurations missing %s", p)
		}
	}
}

func TestSequencer_WaitForExitWithoutRequestReturnsZeroReport(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 100 * time.Millisecond,
	})
	// WaitForExit must not block forever when the parent ctx fires.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	report := mod.WaitForExit(ctx)
	if report.Reason != "" && report.Reason != "ctx-cancelled" {
		t.Fatalf("Reason on cancelled wait: got %q want empty/ctx-cancelled", report.Reason)
	}
}
