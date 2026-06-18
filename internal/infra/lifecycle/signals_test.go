// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// Tests cover LLD §9: SIGTERM and SIGINT enter the sequencer's input
// channel exactly once. SIGKILL is non-recoverable and never reaches the
// process; we cannot test it directly, but the contract is documented.
//
// Strategy: install signal handlers against an isolated *signalDispatcher
// (not the process-wide signal.Notify), then drive it via the dispatcher's
// internal channel so the test does not actually raise OS signals into
// the test runner. The exact wiring is verified through dispatchSignal,
// which the production handler also uses.

func TestSignals_SIGTERMTriggersShutdown(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	d := newSignalDispatcher(mod)
	d.dispatchSignal(syscall.SIGTERM)

	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	report := mod.WaitForExit(wctx)
	if report.Reason != "sigterm" {
		t.Fatalf("Reason: got %q want \"sigterm\"", report.Reason)
	}
}

func TestSignals_SIGINTTriggersShutdown(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	d := newSignalDispatcher(mod)
	d.dispatchSignal(syscall.SIGINT)

	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	report := mod.WaitForExit(wctx)
	if report.Reason != "sigint" {
		t.Fatalf("Reason: got %q want \"sigint\"", report.Reason)
	}
}

func TestSignals_SecondSignalIsNoop(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	d := newSignalDispatcher(mod)
	d.dispatchSignal(syscall.SIGTERM)
	// Second SIGTERM after sequencer started must coalesce — no panic,
	// no double-trigger, Reason stays the first one.
	d.dispatchSignal(syscall.SIGTERM)
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	report := mod.WaitForExit(wctx)
	if report.Reason != "sigterm" {
		t.Fatalf("Reason: got %q want \"sigterm\"", report.Reason)
	}
}

func TestSignals_UnknownSignalIsNoop(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 100 * time.Millisecond,
	})

	d := newSignalDispatcher(mod)
	// SIGUSR1 is not a shutdown signal; the dispatcher must ignore it.
	d.dispatchSignal(syscall.SIGUSR1)

	// Sequencer should not have started; WaitForExit with a short ctx
	// returns the zero report.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	report := mod.WaitForExit(ctx)
	if report.Reason == "sigusr1" {
		t.Fatalf("SIGUSR1 must not trigger shutdown; Reason=%q", report.Reason)
	}
}
