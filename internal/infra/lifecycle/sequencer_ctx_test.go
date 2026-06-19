// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Story #206 — RED Phase A. The sequencer must propagate the caller's
// context into the shutdown phases so a parent cancellation aborts the
// drain at the next phase boundary, and so a WaitForExit caller whose
// own ctx fires can force the sequencer to give up.
//
// Today sequencerLoop drops the parent and runs runShutdown with
// context.Background(). Probes for parent.Done() inside runShutdown
// never fire. These tests pin the new contract.

// metricsRecorder records (metric, labels) tuples emitted by the
// sequencer so the tests can assert that the forced-exit metric fires
// when ctx-cancellation triggers a force-exit.
type metricsRecorder struct {
	forcedTotal atomic.Int64
}

func (r *metricsRecorder) Inc(name string, _ map[string]string) {
	if name == MetricShutdownForcedTotal {
		r.forcedTotal.Add(1)
	}
}
func (r *metricsRecorder) Set(_ string, _ float64, _ map[string]string)             {}
func (r *metricsRecorder) Observe(_ string, _ float64, _ map[string]string)         {}
func (r *metricsRecorder) Add(_ string, _ float64, _ map[string]string)             {}
func (r *metricsRecorder) Counter(_ string, _ map[string]string) func(float64)      { return func(float64) {} }
func (r *metricsRecorder) Histogram(_ string, _ map[string]string) func(float64)    { return func(float64) {} }
func (r *metricsRecorder) Gauge(_ string, _ map[string]string) func(float64)        { return func(float64) {} }

// TestSequencer_RequestShutdownCtxCancelAbortsPhase asserts that the
// ctx supplied to RequestShutdown is honored by the sequencer — when
// the caller cancels that ctx mid-shutdown, a hook blocked on its phase
// context observes ctx.Done() (Canceled) and the sequencer reports a
// forced exit.
//
// Under the old wiring (parent==context.Background) the hook below
// would only see DeadlineExceeded after the phase budget, never
// Canceled, and ForcedExit would track the budget — not the caller's
// cancel.
func TestSequencer_RequestShutdownCtxCancelAbortsPhase(t *testing.T) {
	t.Parallel()

	rec := &metricsRecorder{}
	cfg := LifecycleConfig{
		ShutdownGracePeriod:   2 * time.Second, // long, so test cancel — not deadline — drives exit
		ProbeObserveWindow:    time.Millisecond,
		ReadinessCheckTimeout: 50 * time.Millisecond,
	}
	mod, err := newModuleForTest(cfg, LifecycleContext{Metrics: rec})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	// Hook hangs until ctx fires; reports the ctx error so the test can
	// assert WHY the hook was unblocked.
	hookErr := make(chan error, 1)
	mod.RegisterShutdown(ShutdownHook{
		Name:  "drain.blocking",
		Phase: PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			hookErr <- ctx.Err()
			return ctx.Err()
		},
	})

	// Cancellable parent: the test cancels it shortly after the
	// sequencer starts, simulating a caller who decides "give up now".
	parentCtx, parentCancel := context.WithCancel(context.Background())
	defer parentCancel()

	mod.RequestShutdown(parentCtx, "test_cancel_propagates")

	// Give the sequencer time to enter the drain phase.
	time.Sleep(50 * time.Millisecond)
	parentCancel()

	report := mod.WaitForExit(context.Background())

	select {
	case err := <-hookErr:
		// The hook must observe Canceled (parent ctx) — not
		// DeadlineExceeded (phase deadline). If parent ctx was dropped
		// by the sequencer (as today), the hook would block until the
		// full phase budget fires and report DeadlineExceeded instead.
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("hook ctx error = %v; want context.Canceled (parent ctx propagation broken)", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("hook never returned after parent ctx cancel; sequencer ignored caller cancellation")
	}

	if !report.ForcedExit {
		t.Fatalf("ForcedExit=false after parent-ctx-driven hook unblock; want true (LLD: ctx-cancel mid-shutdown is forced)")
	}
	if rec.forcedTotal.Load() == 0 {
		t.Fatalf("MetricShutdownForcedTotal never incremented; ctx-cancel force-exit metric is dead")
	}
}

// TestSequencer_WaitForExitCtxCancelTriggersForceExit asserts that
// cancellation of the WaitForExit caller's ctx triggers a force-exit
// path — the sequencer aborts in-flight phases rather than blocking
// the caller until the full grace period elapses.
//
// Today WaitForExit's ctx-fire path returns the in-progress (zero or
// partial) report but the sequencer keeps running on Background. This
// pins the new behavior: WaitForExit cancel signals the sequencer to
// abort.
func TestSequencer_WaitForExitCtxCancelTriggersForceExit(t *testing.T) {
	t.Parallel()

	rec := &metricsRecorder{}
	cfg := LifecycleConfig{
		ShutdownGracePeriod:   5 * time.Second, // long — cancel must beat the budget
		ProbeObserveWindow:    time.Millisecond,
		ReadinessCheckTimeout: 50 * time.Millisecond,
	}
	mod, err := newModuleForTest(cfg, LifecycleContext{Metrics: rec})
	if err != nil {
		t.Fatalf("newModuleForTest: %v", err)
	}
	t.Cleanup(func() { mod.stopForTest() })

	// A hook that hangs until ctx fires — the sequencer must signal it.
	hookErr := make(chan error, 1)
	mod.RegisterShutdown(ShutdownHook{
		Name:  "drain.hangs_until_ctx",
		Phase: PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			hookErr <- ctx.Err()
			return ctx.Err()
		},
	})

	mod.RequestShutdown(context.Background(), "wait_cancel_test")

	// Let the sequencer enter the drain phase, then cancel the
	// WaitForExit ctx. Under the new contract, this aborts the phase.
	time.Sleep(50 * time.Millisecond)

	waitCtx, waitCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		waitCancel()
	}()

	start := time.Now()
	_ = mod.WaitForExit(waitCtx)
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Fatalf("WaitForExit blocked %v after caller cancel; want abort within ~100ms (ShutdownGracePeriod=5s drives the budget)", elapsed)
	}

	// The hook must have been signalled by the ctx-cancel path. Without
	// the new wiring, the hook would only unblock on the phase deadline.
	select {
	case <-hookErr:
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("hook never signalled after WaitForExit cancel; sequencer not honoring force-exit")
	}

	// Drain the sequencer goroutine to keep the cleanup deterministic.
	mod.stopForTest()

	if rec.forcedTotal.Load() == 0 {
		t.Fatalf("MetricShutdownForcedTotal never incremented; force-exit metric not wired to WaitForExit cancel")
	}
}
