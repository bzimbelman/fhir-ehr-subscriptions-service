// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Property tests for the load-bearing invariant LLD §6 calls out: no
// matter how the hooks are configured, the sequencer never runs past
// ShutdownGracePeriod by more than a small wall-clock slack window. This
// is the architectural promise the SIGTERM contract rests on.
//
// We use pgregory.net/rapid (already a project dep — see go.mod). Each
// trial generates a random set of (phase, behavior, latency) hooks,
// drives a shutdown, and asserts the wall-clock budget held.

type hookSpec struct {
	phase   Phase
	latency time.Duration
	// behavior: 0=return-nil, 1=return-error, 2=hang-on-ctx, 3=panic.
	behavior int
}

func TestProperty_AlwaysExitsWithinGrace(t *testing.T) {
	t.Parallel()
	// Tighten rapid's defaults so the property test runs in CI under
	// the package's race-detector pass without bloating wall-clock
	// time.
	rapid.Check(t, func(rt *rapid.T) {
		grace := time.Duration(rapid.IntRange(20, 60).Draw(rt, "grace_ms")) * time.Millisecond
		hookCount := rapid.IntRange(0, 8).Draw(rt, "hook_count")

		hooks := make([]hookSpec, 0, hookCount)
		for i := 0; i < hookCount; i++ {
			h := hookSpec{
				phase: Phase(rapid.IntRange(int(PhaseStopAccepting), int(PhaseCloseConnections)).Draw(rt, "phase")),
				// latency in [0, 2*grace) — half the time the hook
				// will exceed its phase budget.
				latency:  time.Duration(rapid.IntRange(0, int(grace.Milliseconds())*2).Draw(rt, "latency_ms")) * time.Millisecond,
				behavior: rapid.IntRange(0, 3).Draw(rt, "behavior"),
			}
			hooks = append(hooks, h)
		}

		mod := newTestModule(t, LifecycleConfig{
			ShutdownGracePeriod: grace,
			ProbeObserveWindow:  time.Millisecond,
		})
		for i, h := range hooks {
			i := i
			h := h
			mod.RegisterShutdown(ShutdownHook{
				Name:  "hook-" + itoa(i),
				Phase: h.phase,
				Run: func(ctx context.Context) error {
					switch h.behavior {
					case 0:
						select {
						case <-time.After(h.latency):
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					case 1:
						select {
						case <-time.After(h.latency):
							return errors.New("hook error")
						case <-ctx.Done():
							return ctx.Err()
						}
					case 2:
						<-ctx.Done()
						return ctx.Err()
					case 3:
						select {
						case <-time.After(h.latency):
							panic("hook panic")
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				},
			})
		}

		start := time.Now()
		mod.RequestShutdown(context.Background(), "property")
		// Cap WaitForExit at 4*grace as a backstop so a runaway test
		// doesn't deadlock the suite.
		ctx, cancel := context.WithTimeout(context.Background(), 4*grace+200*time.Millisecond)
		defer cancel()
		report := mod.WaitForExit(ctx)
		elapsed := time.Since(start)

		// Slack accounts for goroutine wakeup jitter on busy CI.
		// LLD §6 says the budget is a hard wall-clock cap; we allow
		// 200ms slack on top of the 50ms phase-deadline slack inside
		// the sequencer.
		slack := 250 * time.Millisecond
		if elapsed > grace+slack {
			rt.Fatalf("wall-clock exceeded grace+slack: elapsed=%v grace=%v slack=%v", elapsed, grace, slack)
		}
		// PhaseDurations is populated for every executed phase.
		for _, p := range []Phase{PhaseMarkUnready, PhaseStopAccepting, PhaseDrainInFlight, PhaseCloseConnections} {
			if _, ok := report.PhaseDurations[p]; !ok {
				rt.Fatalf("PhaseDurations missing %s", p)
			}
		}
		// CompletedAt - StartedAt is sane.
		if report.CompletedAt.Before(report.StartedAt) {
			rt.Fatalf("CompletedAt before StartedAt: %v < %v", report.CompletedAt, report.StartedAt)
		}
	})
}
