// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package lifecycle owns health probes and graceful shutdown.
//
// It exposes three Kubernetes-shaped probes (`/healthz`, `/readyz`,
// `/startup`), a registry where every long-running component registers a
// readiness check and a shutdown hook, and a SIGTERM-driven sequencer that
// marks the service unready, stops accepting new work, drains in-flight
// work bounded by `lifecycle.shutdown_grace_period`, closes connections,
// and exits.
//
// Two load-bearing invariants:
//
//  1. Liveness MUST NOT depend on Postgres. `/healthz` reads two in-memory
//     flags; nothing else.
//  2. Shutdown follows a strict five-phase order with the grace-period
//     budget enforced as a hard wall-clock deadline.
//
// See docs/low-level-design/lifecycle.md for the canonical design.
package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"log/slog"
)

// Phase identifies one of the five shutdown phases. Hooks register against
// a phase; the sequencer runs hooks within a phase concurrently and waits
// for the phase to complete (bounded by its budget) before proceeding.
type Phase int

// Shutdown phases, declared in execution order. PhaseExit is terminal and
// not registerable — it represents process exit.
const (
	PhaseMarkUnready Phase = iota
	PhaseStopAccepting
	PhaseDrainInFlight
	PhaseCloseConnections
	PhaseExit
)

// String returns the canonical lower-case name of the phase, used in
// metric labels and structured-log fields.
func (p Phase) String() string {
	switch p {
	case PhaseMarkUnready:
		return "mark_unready"
	case PhaseStopAccepting:
		return "stop_accepting"
	case PhaseDrainInFlight:
		return "drain_in_flight"
	case PhaseCloseConnections:
		return "close_connections"
	case PhaseExit:
		return "exit"
	default:
		return "unknown"
	}
}

// ReadinessCheck is a per-component readiness probe. The check returns nil
// when the component is ready to serve traffic; it returns a non-nil error
// whose Error() is reported in the `/readyz` failed-list when the
// component is unready. The check MUST honor ctx.Done() — the readiness
// aggregator races every check against a per-check timeout (LLD §5.2).
type ReadinessCheck func(ctx context.Context) error

// ShutdownHook is a per-component shutdown action. Run MUST honor
// ctx.Done() — the sequencer races each phase's hooks against the phase
// budget (LLD §6).
type ShutdownHook struct {
	Name  string
	Phase Phase
	Run   func(ctx context.Context) error
}

// LifecycleConfig holds the resolved configuration for the lifecycle
// module. Values come from `lifecycle.*` and `server.http.probe_bind` in
// the validated configuration; Start fills in defaults.
//
// The name intentionally repeats "Lifecycle" — the LLD §3 surface uses
// it explicitly so callers in cmd/fhir-subs read as
// `lifecycle.LifecycleConfig{...}`, mirroring how the configuration
// loader's typed config and the rest of the project's host bundles look.
//
//revive:disable-next-line:exported
type LifecycleConfig struct {
	// ProbeBind is the host:port for a dedicated probe listener. Empty
	// means "no dedicated listener — the host mounts ProbeHandlers on its
	// main HTTP listener" (LLD §8).
	ProbeBind string

	// ShutdownGracePeriod is the wall-clock budget for the shutdown
	// sequence. Default 30s.
	ShutdownGracePeriod time.Duration

	// PostgresProbeTimeout is the per-evaluation budget the storage
	// readiness check MAY consume. Default 2s. The lifecycle module does
	// not invoke this directly — it is plumbed through to the storage
	// module, which honors it inside its registered ReadinessCheck.
	PostgresProbeTimeout time.Duration

	// ReadinessCheckTimeout is the default per-check timeout used by the
	// readiness aggregator. Default 2s.
	ReadinessCheckTimeout time.Duration

	// PhaseBudgets is the soft per-phase share of ShutdownGracePeriod.
	// Defaults: 5/10/70/15 percent for phases 1..4.
	PhaseBudgets PhaseBudgets

	// ProbeObserveWindow caps the phase-1 sleep before stop-accepting
	// hooks run, so the orchestrator observes the 503 first. Default
	// min(5% of grace, 2s).
	ProbeObserveWindow time.Duration
}

// PhaseBudgets defines the per-phase wall-clock share of the grace
// period. Each value is a fraction in [0,1]. Defaults: 0.05 / 0.10 /
// 0.70 / 0.15.
type PhaseBudgets struct {
	MarkUnready      float64
	StopAccepting    float64
	DrainInFlight    float64
	CloseConnections float64
}

// LifecycleContext is the host-supplied dependency bundle.
//
//revive:disable-next-line:exported
type LifecycleContext struct {
	// Logger is the structured logger. Nil falls back to slog.Default().
	Logger *slog.Logger
	// Metrics is the metrics seam. Nil falls back to a no-op emitter.
	Metrics MetricsEmitter
	// Clock is a time source for deterministic tests. Nil falls back to
	// time.Now.
	Clock func() time.Time
}

// ProbeHandlers is the bundle of HTTP handlers the host mounts when
// ProbeBind is empty.
type ProbeHandlers struct {
	Healthz http.Handler
	Readyz  http.Handler
	Startup http.Handler
}

// ShutdownReport is the outcome of WaitForExit.
type ShutdownReport struct {
	Reason         string
	StartedAt      time.Time
	CompletedAt    time.Time
	PhaseDurations map[Phase]time.Duration
	HooksFailed    []string
	ForcedExit     bool
}

// LifecycleModule is the runtime handle returned by Start.
//
//revive:disable-next-line:exported
type LifecycleModule struct {
	cfg  LifecycleConfig
	lctx LifecycleContext
	reg  *registry

	probes ProbeHandlers
	server *http.Server

	requestCh   chan string
	exitDone    chan struct{}
	requestOnce sync.Once
	closeOnce   sync.Once
	reportMu    sync.Mutex
	report      ShutdownReport

	// reloadHandlerMu guards reloadHandler. SIGHUP routes through the
	// dispatcher to whatever handler the host has registered (typically
	// config.Module.Reload). nil means SIGHUP is a no-op (B-35).
	reloadHandlerMu sync.Mutex
	reloadHandler   func(context.Context)
}

// SetReloadHandler registers (or replaces) the SIGHUP-driven reload
// handler. nil clears the handler. Safe under concurrent calls.
// Established for B-35.
func (m *LifecycleModule) SetReloadHandler(fn func(context.Context)) {
	m.reloadHandlerMu.Lock()
	defer m.reloadHandlerMu.Unlock()
	m.reloadHandler = fn
}

// invokeReloadHandler invokes the registered handler if any. Used by
// the signal dispatcher; nil-safe.
func (m *LifecycleModule) invokeReloadHandler(ctx context.Context) {
	m.reloadHandlerMu.Lock()
	fn := m.reloadHandler
	m.reloadHandlerMu.Unlock()
	if fn != nil {
		fn(ctx)
	}
}

// Start initializes the lifecycle module: builds the registry, mounts the
// probe handlers, optionally binds a dedicated probe listener, and
// installs signal handlers. The returned module is reachable for
// registration and the probes are reachable as soon as Start returns.
func Start(ctx context.Context, cfg LifecycleConfig, lctx LifecycleContext) (*LifecycleModule, error) {
	mod, err := newModule(cfg, lctx, true)
	if err != nil {
		return nil, err
	}
	if err := mod.installSignalHandlers(ctx); err != nil {
		return nil, err
	}
	if err := mod.maybeStartProbeListener(); err != nil {
		return nil, err
	}
	return mod, nil
}

// RegisterReadiness registers (or replaces) a readiness check by name.
// Safe under concurrent calls. Errors (registration after shutdown, etc.)
// are logged and dropped — see LLD §7 ("registration after shutdown is
// always a bug"). Components that need the explicit error reach into the
// registry helpers, but the documented surface is the silent-on-error
// form.
func (m *LifecycleModule) RegisterReadiness(name string, check ReadinessCheck) {
	if err := m.reg.registerReadiness(name, check); err != nil {
		m.lctx.Logger.Error("lifecycle: registerReadiness failed",
			"name", name, "error", err.Error())
	}
}

// RegisterShutdown registers (or replaces) a shutdown hook keyed by
// (name, phase). Safe under concurrent calls. Same error semantics as
// RegisterReadiness.
func (m *LifecycleModule) RegisterShutdown(hook ShutdownHook) {
	if err := m.reg.registerShutdown(hook); err != nil {
		m.lctx.Logger.Error("lifecycle: registerShutdown failed",
			"name", hook.Name, "phase", hook.Phase.String(), "error", err.Error())
	}
}

// MarkStartupComplete flips the startup-complete flag and bumps the
// startup-complete gauge. Until called, `/startup` returns 503.
func (m *LifecycleModule) MarkStartupComplete() {
	m.reg.markStartupComplete()
	m.lctx.Metrics.Set(MetricStartupComplete, 1, nil)
}

// RequestShutdown begins the shutdown sequence. Idempotent — only the
// first caller's reason is recorded; subsequent calls return without
// effect. Safe under concurrent calls.
//
// Note: the ctx parameter is currently advisory. The sequencer runs on a
// dedicated context so a cancelled caller does not abandon the drain.
// Callers that need to cap the entire sequence pass ShutdownGracePeriod
// instead.
func (m *LifecycleModule) RequestShutdown(ctx context.Context, reason string) {
	_ = ctx
	m.requestOnce.Do(func() {
		// Buffered channel of size 1 — first send always succeeds.
		m.requestCh <- reason
	})
}

// WaitForExit blocks until shutdown completes or until the parent ctx
// fires. Returns the recorded ShutdownReport.
func (m *LifecycleModule) WaitForExit(ctx context.Context) ShutdownReport {
	select {
	case <-m.exitDone:
	case <-ctx.Done():
	}
	m.reportMu.Lock()
	rep := m.report
	m.reportMu.Unlock()
	return rep
}

// Probes returns the ProbeHandlers bundle the host mounts when ProbeBind
// is empty.
func (m *LifecycleModule) Probes() ProbeHandlers {
	return m.probes
}

// installSignalHandlers wires SIGTERM and SIGINT to RequestShutdown. The
// implementation lives in signals.go; this method exists to keep Start
// readable.
func (m *LifecycleModule) installSignalHandlers(ctx context.Context) error {
	return installSignalHandlers(ctx, m)
}

// maybeStartProbeListener binds the dedicated probe HTTP listener when
// cfg.ProbeBind is non-empty. Failure to bind is a startup error per
// LLD §10. Implementation lives in probe_server.go (created in the
// metrics-wiring commit so the dependency surface stays small here).
func (m *LifecycleModule) maybeStartProbeListener() error {
	return maybeStartProbeListener(m)
}

// errProbeListenerNotImplemented is a sentinel kept to avoid the unused
// import warning when the probe listener glue is wired up. It is replaced
// by the real binder.
var _ = errors.New
