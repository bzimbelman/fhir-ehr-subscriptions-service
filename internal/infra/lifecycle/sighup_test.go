// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// B-35: SIGHUP must not trigger shutdown. Instead, the dispatcher must
// route it to a registered reload handler. Without this, secret rotation
// is unreachable: ${file:...} placeholders are read once and the
// process keeps the old value indefinitely.
func TestSignals_SIGHUPInvokesReloadHandler(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	var calls atomic.Int64
	mod.SetReloadHandler(func(_ context.Context) {
		calls.Add(1)
	})

	d := newSignalDispatcher(mod)
	d.dispatchSignal(syscall.SIGHUP)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls.Load() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reload handler invoked %d times; want 1", got)
	}

	// SIGHUP must NOT trigger shutdown. WaitForExit with a short ctx
	// returns the zero report.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	report := mod.WaitForExit(ctx)
	if report.Reason == "sighup" {
		t.Fatalf("SIGHUP must not initiate shutdown; Reason=%q", report.Reason)
	}
}

// B-35: SIGHUP without a registered handler is a no-op; in particular
// it must not panic.
func TestSignals_SIGHUPWithoutHandlerIsNoop(t *testing.T) {
	t.Parallel()
	mod := newTestModule(t, LifecycleConfig{
		ShutdownGracePeriod: 200 * time.Millisecond,
		ProbeObserveWindow:  time.Millisecond,
	})

	d := newSignalDispatcher(mod)
	d.dispatchSignal(syscall.SIGHUP) // must not panic

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	report := mod.WaitForExit(ctx)
	if report.Reason != "" {
		t.Fatalf("SIGHUP should not initiate shutdown; Reason=%q", report.Reason)
	}
}
