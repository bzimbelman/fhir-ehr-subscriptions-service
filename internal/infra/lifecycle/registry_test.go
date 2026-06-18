// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Tests cover LLD §7 (registration semantics) and LLD §3 (public surface):
//
//   - RegisterReadiness / RegisterShutdown are safe under concurrent calls.
//   - Registration is idempotent on (name) for readiness, and on (name, phase)
//     for shutdown — re-registering replaces.
//   - Once shutdown begins, registration is rejected.
//   - The registry exposes its readiness checks and the hooks-by-phase view
//     the sequencer needs.

func TestRegistry_RegisterReadinessConcurrent(t *testing.T) {
	t.Parallel()
	r := newRegistry()

	const goroutines = 32
	const perGoroutine = 16
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				name := registryName(g, i)
				err := r.registerReadiness(name, func(ctx context.Context) error { return nil })
				if err != nil {
					t.Errorf("registerReadiness(%q) err=%v", name, err)
				}
			}
		}(g)
	}
	wg.Wait()

	got := len(r.snapshotReadiness())
	want := goroutines * perGoroutine
	if got != want {
		t.Fatalf("readiness count: got %d want %d", got, want)
	}
}

func TestRegistry_ReadinessIdempotent(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	called := 0
	must(t, r.registerReadiness("postgres", func(ctx context.Context) error {
		called++
		return nil
	}))
	// Replace with new function.
	must(t, r.registerReadiness("postgres", func(ctx context.Context) error {
		return errors.New("flicker")
	}))
	checks := r.snapshotReadiness()
	if len(checks) != 1 {
		t.Fatalf("expected 1 readiness entry after replace, got %d", len(checks))
	}
	if got := checks[0].check(context.Background()); got == nil || got.Error() != "flicker" {
		t.Fatalf("replacement not applied; got %v", got)
	}
	if called != 0 {
		t.Fatalf("original callback should not have been invoked")
	}
}

func TestRegistry_ShutdownIdempotentOnNamePhase(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	must(t, r.registerShutdown(ShutdownHook{
		Name:  "engine",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return nil },
	}))
	must(t, r.registerShutdown(ShutdownHook{
		Name:  "engine",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return errors.New("replaced") },
	}))
	// Same name in a different phase is a separate registration.
	must(t, r.registerShutdown(ShutdownHook{
		Name:  "engine",
		Phase: PhaseDrainInFlight,
		Run:   func(ctx context.Context) error { return nil },
	}))

	stop := r.hooksInPhase(PhaseStopAccepting)
	drain := r.hooksInPhase(PhaseDrainInFlight)
	if len(stop) != 1 {
		t.Fatalf("expected 1 stop hook, got %d", len(stop))
	}
	if got := stop[0].Run(context.Background()); got == nil || got.Error() != "replaced" {
		t.Fatalf("replacement not applied; got %v", got)
	}
	if len(drain) != 1 {
		t.Fatalf("expected 1 drain hook, got %d", len(drain))
	}
}

func TestRegistry_RejectsAfterShutdownStarted(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	r.markShutdownInProgress()

	if err := r.registerReadiness("late", func(ctx context.Context) error { return nil }); err == nil {
		t.Fatalf("expected registerReadiness after shutdown to error, got nil")
	}
	if err := r.registerShutdown(ShutdownHook{
		Name:  "late",
		Phase: PhaseStopAccepting,
		Run:   func(ctx context.Context) error { return nil },
	}); err == nil {
		t.Fatalf("expected registerShutdown after shutdown to error, got nil")
	}
}

func TestRegistry_StartupAndShutdownFlags(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	if r.shutdownInProgress() {
		t.Fatalf("expected shutdownInProgress=false on fresh registry")
	}
	if r.startupComplete() {
		t.Fatalf("expected startupComplete=false on fresh registry")
	}
	r.markStartupComplete()
	if !r.startupComplete() {
		t.Fatalf("expected startupComplete=true after markStartupComplete")
	}
	r.markShutdownInProgress()
	if !r.shutdownInProgress() {
		t.Fatalf("expected shutdownInProgress=true after markShutdownInProgress")
	}
}

// helpers.

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func registryName(g, i int) string {
	return "check-" + itoa(g) + "-" + itoa(i)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
