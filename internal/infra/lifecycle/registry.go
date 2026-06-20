// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// readinessEntry pairs a check's name with its callable.
type readinessEntry struct {
	name  string
	check ReadinessCheck
}

// shutdownKey buckets a hook by (name, phase) so the same component can
// register one hook per phase (LLD §7).
type shutdownKey struct {
	name  string
	phase Phase
}

// registry holds the in-memory state of the lifecycle module:
//   - the set of readiness checks registered by components,
//   - the set of shutdown hooks bucketed by phase,
//   - the shutdown_in_progress, panic_signaled, and startup_complete flags.
//
// Mutating operations are guarded by mu; flag reads use atomics so the
// probe handlers stay lock-free on the hot path.
type registry struct {
	mu        sync.RWMutex
	readiness map[string]ReadinessCheck
	shutdown  map[shutdownKey]ShutdownHook

	shutdownStarted atomic.Bool
	startupDone     atomic.Bool
	panicked        atomic.Bool
}

// newRegistry constructs an empty registry.
func newRegistry() *registry {
	return &registry{
		readiness: make(map[string]ReadinessCheck),
		shutdown:  make(map[shutdownKey]ShutdownHook),
	}
}

// errRegistrationAfterShutdown is returned by registerReadiness /
// registerShutdown once the registry has been marked shutdown-in-progress.
// Per LLD §7, registering during shutdown is always a bug; the registry
// surfaces an error and lets callers decide.
var errRegistrationAfterShutdown = errors.New("lifecycle: registration after shutdown began")

// registerReadiness adds (or replaces) a readiness check by name. Returns
// errRegistrationAfterShutdown once the shutdown flag is set.
func (r *registry) registerReadiness(name string, check ReadinessCheck) error {
	if r.shutdownStarted.Load() {
		return errRegistrationAfterShutdown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Double-check after taking the lock — markShutdownInProgress holds
	// the write lock while flipping the flag, so this is the gate.
	if r.shutdownStarted.Load() {
		return errRegistrationAfterShutdown
	}
	r.readiness[name] = check
	return nil
}

// registerShutdown adds (or replaces) a shutdown hook keyed by
// (name, phase). Returns errRegistrationAfterShutdown once the shutdown
// flag is set, and rejects PhaseExit (terminal, not registerable).
func (r *registry) registerShutdown(hook ShutdownHook) error {
	if hook.Phase < PhaseMarkUnready || hook.Phase >= PhaseExit {
		return errors.New("lifecycle: shutdown hook must register against MarkUnready..CloseConnections")
	}
	if hook.Run == nil {
		return errors.New("lifecycle: shutdown hook Run is nil")
	}
	if hook.Name == "" {
		return errors.New("lifecycle: shutdown hook Name is empty")
	}
	if r.shutdownStarted.Load() {
		return errRegistrationAfterShutdown
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shutdownStarted.Load() {
		return errRegistrationAfterShutdown
	}
	r.shutdown[shutdownKey{name: hook.Name, phase: hook.Phase}] = hook
	return nil
}

// snapshotReadiness returns a deterministic, name-sorted snapshot of the
// registered readiness checks. Sorting keeps probe responses' failed[]
// list stable across runs (small but useful in tests and dashboards).
func (r *registry) snapshotReadiness() []readinessEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]readinessEntry, 0, len(r.readiness))
	for name, check := range r.readiness {
		out = append(out, readinessEntry{name: name, check: check})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// hooksInPhase returns the shutdown hooks registered for the given phase.
// Order within a phase is name-sorted for determinism; the sequencer runs
// them concurrently, so the order is informational.
func (r *registry) hooksInPhase(phase Phase) []ShutdownHook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ShutdownHook, 0)
	for k, h := range r.shutdown {
		if k.phase == phase {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// shutdownInProgress reports whether the sequencer has begun. Used by
// `/healthz` (returns 503 status="shutting_down") and `/readyz` (returns
// 503 status="shutting_down" with failed=["shutting_down"]).
func (r *registry) shutdownInProgress() bool { return r.shutdownStarted.Load() }

// startupComplete reports whether MarkStartupComplete has been called.
// Used by `/startup`.
func (r *registry) startupComplete() bool { return r.startupDone.Load() }

// panicSignaled reports whether the runtime panic handler has flipped the
// flag. Used by `/healthz`.
func (r *registry) panicSignaled() bool { return r.panicked.Load() }

// markShutdownInProgress flips the shutdown flag. Idempotent.
func (r *registry) markShutdownInProgress() {
	// Take the write lock while flipping so concurrent register* calls
	// observe a consistent gate.
	r.mu.Lock()
	r.shutdownStarted.Store(true)
	r.mu.Unlock()
}

// markStartupComplete flips the startup flag. Idempotent.
func (r *registry) markStartupComplete() { r.startupDone.Store(true) }

// markPanicSignaled flips the panic flag. Idempotent. Once set, the flag
// is not cleared at runtime — the operator either restarts or dies.
func (r *registry) markPanicSignaled() { r.panicked.Store(true) }

// runChecksConcurrently fans the readiness checks out, races each against
// per-check timeoutNanos, and returns a name-sorted []checkResult. The
// readiness aggregator (LLD §5.2) is the only caller.
//
// A panic inside a check is caught and reported as failed=true with
// reason="panic" — LLD §10 requires this so a buggy check does not take
// the probe handler down.
func runChecksConcurrently(ctx context.Context, entries []readinessEntry, perCheckTimeoutNanos int64) []checkResult {
	if len(entries) == 0 {
		return nil
	}
	out := make([]checkResult, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		i, e := i, e
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = runOneCheck(ctx, e, perCheckTimeoutNanos)
		}()
	}
	wg.Wait()
	return out
}

// runOneCheck runs a single readiness check with a per-check timeout
// derived from perCheckTimeoutNanos and the parent ctx.
func runOneCheck(ctx context.Context, e readinessEntry, perCheckTimeoutNanos int64) (cr checkResult) {
	cr.name = e.name
	defer func() {
		if rec := recover(); rec != nil {
			cr.failed = true
			cr.reason = "panic"
		}
	}()
	if e.check == nil {
		cr.failed = true
		cr.reason = "nil-check"
		return
	}
	checkCtx, cancel := withOptionalTimeout(ctx, perCheckTimeoutNanos)
	defer cancel()
	err := e.check(checkCtx)
	if err != nil {
		cr.failed = true
		cr.reason = err.Error()
	}
	return
}

// checkResult is the per-entry outcome the readiness aggregator collects.
// failed=false means the check passed.
type checkResult struct {
	name   string
	failed bool
	reason string
}
