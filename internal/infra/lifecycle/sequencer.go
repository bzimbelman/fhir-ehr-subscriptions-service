// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// slogDefault returns the package-level default slog.Logger; isolated in a
// helper so tests can swap it out if they need to.
func slogDefault() *slog.Logger { return slog.Default() }

// newModuleForTest builds a LifecycleModule wired for in-process tests:
// the registry and the sequencer goroutine are live, but the probe HTTP
// listener is not bound. Production callers go through Start.
func newModuleForTest(cfg LifecycleConfig, lctx LifecycleContext) (*LifecycleModule, error) {
	return newModule(cfg, lctx, false)
}

// newModule constructs the LifecycleModule and starts the sequencer
// goroutine. When bindProbeListener is true, Start also binds the HTTP
// listener; tests pass false.
func newModule(cfg LifecycleConfig, lctx LifecycleContext, bindProbeListener bool) (*LifecycleModule, error) {
	cfg = applyConfigDefaults(cfg)
	lctx = applyContextDefaults(lctx)

	reg := newRegistry()
	mod := &LifecycleModule{
		cfg:       cfg,
		lctx:      lctx,
		reg:       reg,
		requestCh: make(chan string, 1),
		exitDone:  make(chan struct{}),
	}
	mod.probes = ProbeHandlers{
		Healthz: newLivenessHandler(reg, lctx.Metrics),
		Readyz:  newReadinessHandler(reg, lctx.Metrics, cfg.ReadinessCheckTimeout),
		Startup: newStartupHandler(reg, lctx.Metrics, cfg.ReadinessCheckTimeout),
	}

	// Initialize the startup-complete gauge to 0 so dashboards see the
	// metric immediately at boot. MarkStartupComplete flips it to 1.
	lctx.Metrics.Set(MetricStartupComplete, 0, nil)

	// Sequencer goroutine: waits for a request, runs the phases, signals
	// exitDone. Any panic inside the sequencer flips panic_signaled and
	// still closes exitDone so WaitForExit unblocks.
	go mod.sequencerLoop()

	// Production probe listener wiring lives in Start (which calls
	// maybeStartProbeListener). Tests use newModuleForTest with
	// bindProbeListener=false; the parameter is kept here so production
	// and test code paths stay aligned even though Start owns the
	// actual bind.
	_ = bindProbeListener

	return mod, nil
}

// applyConfigDefaults fills in the LLD-specified defaults for any
// zero-valued field. Defaults match LLD §3 and §11.
func applyConfigDefaults(cfg LifecycleConfig) LifecycleConfig {
	if cfg.ShutdownGracePeriod <= 0 {
		cfg.ShutdownGracePeriod = 30 * time.Second
	}
	if cfg.PostgresProbeTimeout <= 0 {
		cfg.PostgresProbeTimeout = 2 * time.Second
	}
	if cfg.ReadinessCheckTimeout <= 0 {
		cfg.ReadinessCheckTimeout = 2 * time.Second
	}
	if cfg.PhaseBudgets == (PhaseBudgets{}) {
		cfg.PhaseBudgets = PhaseBudgets{
			MarkUnready:      0.05,
			StopAccepting:    0.10,
			DrainInFlight:    0.70,
			CloseConnections: 0.15,
		}
	}
	if cfg.ProbeObserveWindow <= 0 {
		// min(5% of grace, 2s).
		w := time.Duration(float64(cfg.ShutdownGracePeriod) * cfg.PhaseBudgets.MarkUnready)
		if w > 2*time.Second {
			w = 2 * time.Second
		}
		if w <= 0 {
			w = 100 * time.Millisecond
		}
		cfg.ProbeObserveWindow = w
	}
	return cfg
}

// applyContextDefaults fills in nop fallbacks for nil dependencies so the
// module is nil-safe on the hot path.
func applyContextDefaults(lctx LifecycleContext) LifecycleContext {
	if lctx.Metrics == nil {
		lctx.Metrics = nopMetrics{}
	}
	if lctx.Clock == nil {
		lctx.Clock = time.Now
	}
	if lctx.Logger == nil {
		lctx.Logger = slogDefault()
	}
	return lctx
}

// stopForTest is the test-only equivalent of WaitForExit + cleanup.
// Idempotent. Production callers do not invoke it; use WaitForExit.
func (m *LifecycleModule) stopForTest() {
	// Best-effort: ensure the sequencer goroutine has exited so the
	// race detector does not flag goroutine leaks.
	select {
	case <-m.exitDone:
		return
	default:
	}
	m.RequestShutdown(context.Background(), "test-cleanup")
	select {
	case <-m.exitDone:
	case <-time.After(2 * time.Second):
	}
}

// sequencerLoop is the single-goroutine driver. It blocks until a shutdown
// request arrives, runs the phases, and closes exitDone. Wrapped in a
// recover so a panic inside the sequencer flips panic_signaled and still
// unblocks WaitForExit.
func (m *LifecycleModule) sequencerLoop() {
	defer func() {
		if rec := recover(); rec != nil {
			m.reg.markPanicSignaled()
			m.lctx.Logger.Error("lifecycle sequencer panicked",
				"recover", fmt.Sprint(rec))
		}
		// Signal WaitForExit one way or another.
		m.closeOnce.Do(func() { close(m.exitDone) })
	}()

	reason := <-m.requestCh
	m.runShutdown(context.Background(), reason)
}

// runShutdown executes the five-phase sequence. It is exported only at
// the package level so the test helper newModuleForTest can build a
// module that uses the same code path.
func (m *LifecycleModule) runShutdown(parent context.Context, reason string) {
	startedAt := m.lctx.Clock()
	totalDeadline := startedAt.Add(m.cfg.ShutdownGracePeriod)

	m.lctx.Logger.Info("lifecycle shutdown initiated", "reason", reason)
	m.lctx.Metrics.Inc(MetricShutdownInitiatedTotal, map[string]string{
		"reason": reason,
	})

	// Phase 1 — mark unready. The flag flips before any sleep so probes
	// see 503 immediately; we then sleep one probe-observe window so
	// the orchestrator drops this pod from the LB.
	phaseStart := m.lctx.Clock()
	m.reg.markShutdownInProgress()
	probeWindow := m.cfg.ProbeObserveWindow
	if remaining := totalDeadline.Sub(m.lctx.Clock()); probeWindow > remaining {
		probeWindow = remaining
	}
	if probeWindow > 0 {
		select {
		case <-time.After(probeWindow):
		case <-parent.Done():
		}
	}
	p1 := m.lctx.Clock().Sub(phaseStart)
	m.lctx.Metrics.Observe(MetricPhaseDurationSeconds, p1.Seconds(), map[string]string{
		"phase": PhaseMarkUnready.String(),
	})

	// Phases 2-4 share the racePhase helper. Each phase has a soft
	// budget; remaining grace rolls forward. The sequencer respects the
	// total grace deadline as a hard wall-clock cap.
	p2, failed2, timed2 := m.racePhase(parent, PhaseStopAccepting, totalDeadline,
		fractionOf(m.cfg.ShutdownGracePeriod, m.cfg.PhaseBudgets.StopAccepting))
	p3, failed3, timed3 := m.racePhase(parent, PhaseDrainInFlight, totalDeadline,
		fractionOf(m.cfg.ShutdownGracePeriod, m.cfg.PhaseBudgets.DrainInFlight))
	p4, failed4, timed4 := m.racePhase(parent, PhaseCloseConnections, totalDeadline,
		fractionOf(m.cfg.ShutdownGracePeriod, m.cfg.PhaseBudgets.CloseConnections))
	anyTimedOut := timed2 || timed3 || timed4

	completedAt := m.lctx.Clock()
	// "Forced" means the shutdown abandoned in-flight work. Two
	// triggers, both representing operator-visible "not clean":
	//   (a) wall-clock elapsed reached the total grace deadline;
	//   (b) any phase timed out a hook (the sequencer left a hook
	//       running and proceeded — the row reverts to pending for the
	//       next incarnation, LLD §10).
	// Both are true under "the deadline was hit" semantically; the
	// metric `fhir_subs_lifecycle_shutdown_forced_total` increments on
	// either.
	forced := completedAt.Sub(startedAt) >= m.cfg.ShutdownGracePeriod || anyTimedOut
	if forced {
		m.lctx.Metrics.Inc(MetricShutdownForcedTotal, nil)
	}

	hooksFailed := append(append(append([]string(nil), failed2...), failed3...), failed4...)

	report := ShutdownReport{
		Reason:      reason,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		PhaseDurations: map[Phase]time.Duration{
			PhaseMarkUnready:      p1,
			PhaseStopAccepting:    p2,
			PhaseDrainInFlight:    p3,
			PhaseCloseConnections: p4,
		},
		HooksFailed: hooksFailed,
		ForcedExit:  forced,
	}

	m.reportMu.Lock()
	m.report = report
	m.reportMu.Unlock()

	kind := "graceful"
	if forced {
		kind = "forced"
	}
	m.lctx.Logger.Info("lifecycle shutdown complete",
		"kind", kind,
		"elapsed_ms", strconv.FormatInt(completedAt.Sub(startedAt).Milliseconds(), 10),
		"hooks_failed", hooksFailed,
	)
}

// racePhase runs every hook for the given phase concurrently, capped by
// the lesser of (now + softBudget) and totalDeadline. Returns the phase's
// wall-clock duration and the names of failed hooks. A hook is "failed"
// when its Run returns an error or the phase deadline fires before Run
// returns (LLD §10).
func (m *LifecycleModule) racePhase(parent context.Context, phase Phase, totalDeadline time.Time, softBudget time.Duration) (time.Duration, []string, bool) {
	phaseStart := m.lctx.Clock()
	phaseDeadline := phaseStart.Add(softBudget)
	if phaseDeadline.After(totalDeadline) {
		phaseDeadline = totalDeadline
	}
	hooks := m.reg.hooksInPhase(phase)
	if len(hooks) == 0 {
		dur := m.lctx.Clock().Sub(phaseStart)
		m.lctx.Metrics.Observe(MetricPhaseDurationSeconds, dur.Seconds(), map[string]string{
			"phase": phase.String(),
		})
		return dur, nil, false
	}

	phaseCtx, cancel := withDeadline(parent, phaseDeadline)
	defer cancel()

	type hookOutcome struct {
		name    string
		failed  bool
		outcome string
	}
	results := make([]hookOutcome, len(hooks))
	// resultsMu guards results — both the hook goroutines and the
	// phase-deadline path write into the same backing slice. The
	// previous code read `results[i].name` from the deadline path
	// while hook goroutines were still writing, which the race
	// detector flagged. S-15 #7.
	var resultsMu sync.Mutex

	var wg sync.WaitGroup
	for i, h := range hooks {
		i, h := i, h
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := runOneShutdownHook(phaseCtx, h)
			resultsMu.Lock()
			// Don't clobber a timeout written by the deadline path.
			if results[i].name == "" {
				results[i] = out
			}
			resultsMu.Unlock()
		}()
	}

	// Wait for either every hook to return, or the phase deadline to
	// fire. We do not abandon goroutines on deadline expiry — Go's
	// context cancellation gives well-behaved hooks the signal. A
	// hook that ignores ctx.Done() will keep running in the background
	// until the process exits; that is the contract.
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	deadlineTimer := time.NewTimer(time.Until(phaseDeadline))
	defer deadlineTimer.Stop()
	select {
	case <-doneCh:
	case <-deadlineTimer.C:
		// Phase deadline fired. Mark all not-yet-completed hooks as
		// failed; their goroutines are already cancelled via phaseCtx.
		// We still wait for them to return so we never report on a
		// not-yet-written slot, but bound that wait to a small slack
		// window so a truly hung hook does not stall WaitForExit.
		slack := time.NewTimer(50 * time.Millisecond)
		defer slack.Stop()
		select {
		case <-doneCh:
		case <-slack.C:
			// Mark every result entry that has not been written by
			// the hook goroutine yet as a timeout, by name. Done
			// under resultsMu so it can never race with a hook
			// finishing concurrently.
			resultsMu.Lock()
			for i, h := range hooks {
				if results[i].name == "" {
					results[i] = hookOutcome{name: h.Name, failed: true, outcome: "timed_out"}
				}
			}
			resultsMu.Unlock()
		}
	}

	dur := m.lctx.Clock().Sub(phaseStart)
	m.lctx.Metrics.Observe(MetricPhaseDurationSeconds, dur.Seconds(), map[string]string{
		"phase": phase.String(),
	})

	// Snapshot under the same mutex used by the hook + deadline writers
	// so the metric/report loop never observes torn state.
	resultsMu.Lock()
	snapshot := make([]hookOutcome, len(results))
	copy(snapshot, results)
	resultsMu.Unlock()

	var failed []string
	var anyTimedOut bool
	for _, r := range snapshot {
		m.lctx.Metrics.Inc(MetricShutdownHookOutcomeTotal, map[string]string{
			"hook":    r.name,
			"outcome": r.outcome,
		})
		if r.failed {
			failed = append(failed, r.name)
		}
		if r.outcome == "timed_out" {
			anyTimedOut = true
		}
	}
	return dur, failed, anyTimedOut
}

// runOneShutdownHook invokes a single hook. The hook's Run is given the
// phase context whose deadline is the phase's hard cap. A returned error
// is reported as failed=true with outcome="errored"; ctx.DeadlineExceeded
// is reported as outcome="timed_out"; nil is "drained".
func runOneShutdownHook(phaseCtx context.Context, h ShutdownHook) (out struct {
	name    string
	failed  bool
	outcome string
}) {
	out.name = h.Name
	defer func() {
		if rec := recover(); rec != nil {
			out.failed = true
			out.outcome = "errored"
		}
	}()
	err := h.Run(phaseCtx)
	switch {
	case err == nil:
		out.outcome = "drained"
	case isDeadlineExceeded(err):
		out.failed = true
		out.outcome = "timed_out"
	default:
		out.failed = true
		out.outcome = "errored"
	}
	return
}

// isDeadlineExceeded is a small helper so the sequencer is independent of
// the exact error chain shape components return on cancellation.
//
// Uses errors.Is so wrapped sentinels (e.g., fmt.Errorf("%w", ctx.Err()))
// are classified the same as the bare context error. The previous
// pointer-equality form misclassified any wrapped chain as "errored",
// which mislabeled cleanly-cancelled hooks. S-15 #8.
func isDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// fractionOf returns max(0, total*frac), capped at total.
func fractionOf(total time.Duration, frac float64) time.Duration {
	if frac <= 0 {
		return 0
	}
	if frac >= 1 {
		return total
	}
	return time.Duration(float64(total) * frac)
}
