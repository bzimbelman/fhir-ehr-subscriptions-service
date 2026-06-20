// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package observability wires the metrics, tracing, logging, and audit
// sub-modules into a single ObservabilityModule that the host starts at
// process boot and shuts down during graceful exit.
//
// The module exposes typed handles to the rest of the service:
//
//   - Handles.Metrics — typed Prometheus emitter that enforces the
//     fhir_subs_ prefix and the LLD §4.2 label-cardinality rules.
//   - Handles.Tracer  — OTel tracer (no-op when no OTLP endpoint is
//     configured).
//   - Handles.Logger  — *slog.Logger with PHI redaction installed.
//   - Handles.Audit   — append-only, hash-chained audit writer.
//
// The module owns the lifecycle of these handles. Components consume
// them; they never reach into the registry / tracer-provider directly.
package observability

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/tracing"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// MetricsConfig is the metrics-layer config block.
type MetricsConfig struct {
	Bind string
	Path string
}

// TracingConfig is the tracing-layer config block.
type TracingConfig struct {
	OTLPEndpoint    string
	SampleRate      float64
	ExporterTimeout time.Duration
	Insecure        bool

	// TLSCertFile / TLSKeyFile / TLSCAFile point at PEM files used to
	// build the *tls.Config handed to the OTLP exporter. All three are
	// optional; supplying CertFile + KeyFile enables mTLS, supplying
	// CAFile pins a custom root pool. Story #94.
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string

	// Headers carry static HTTP headers forwarded with every OTLP
	// request (typically auth tokens). Story #94.
	Headers map[string]string
}

// LoggingConfig is the logging-layer config block.
type LoggingConfig struct {
	Level  string // "debug" | "info" (default) | "warn" | "error"
	Format string // "json" (default) | "text"
	// DebugLogPayloads opts in to retaining PHI-shaped fields when
	// Level is debug. Default false; PHI is redacted at every level.
	// Off by default because the LLD defaults Level to info, so this
	// flag is dead weight unless an operator deliberately turns the
	// firehose on for local diagnosis.
	DebugLogPayloads bool
	// Sink overrides the destination writer. Empty means os.Stdout.
	// The host's process supervisor captures stdout to the central
	// logging pipeline; tests inject a buffer.
	Sink io.Writer
}

// AuditConfig is the audit-log config block.
type AuditConfig struct {
	Sink     string // "stdout" (default) | "file" | "syslog" | "otlp"
	FilePath string // used when Sink == "file"

	// FileSyncMode controls fsync behavior of the file sink:
	//   - "every_write" (default): fsync after each Emit (safe; loses
	//     no rows under power loss).
	//   - "batched": fsync on a timer / at Close (higher throughput;
	//     trades durability — operators must opt in explicitly).
	// Empty string is treated as "every_write".
	FileSyncMode string

	// FileBatchInterval is the periodic fsync interval used in
	// batched mode. Defaults to 1s when batched mode is selected.
	FileBatchInterval time.Duration
}

// Config is the top-level observability config.
type Config struct {
	Metrics MetricsConfig
	Tracing TracingConfig
	Logging LoggingConfig
	Audit   AuditConfig
}

// AuditStore is the interface the audit module consumes from the host's
// storage layer.
type AuditStore = audit.Store

// AuditWriter is the audit-log front door exposed to other modules.
type AuditWriter interface {
	Emit(ctx context.Context, evt AuditEvent) error
}

// AuditEvent is the per-action audit event.
type AuditEvent struct {
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID uuid.UUID
	Payload       map[string]any
}

// Context is host-supplied at Start.
type Context struct {
	StoragePool AuditStore
	Clock       func() time.Time
}

// MetricsEmitter is the typed metrics handle.
type MetricsEmitter = *metrics.Emitter

// Tracer is the OTel-backed tracer handle.
type Tracer = *tracing.Module

// Handles is the bundle returned by Start.
type Handles struct {
	Metrics MetricsEmitter
	Tracer  Tracer
	Logger  *slog.Logger
	Audit   AuditWriter
}

// ObservabilityModule is the lifecycle owner.
//
//revive:disable-next-line:exported // canonical name per LLD §3 public surface
type ObservabilityModule struct {
	registry  *prometheus.Registry
	emitter   *metrics.Emitter
	inventory *metrics.Inventory
	tracer    *tracing.Module
	logger    *slog.Logger
	audit     *audit.Writer
	auditFile *os.File
	auditSink *fileSink // non-nil only when Sink == "file"; lifecycle owns Close
}

// matcherEmitterAdapter forwards matcher.MetricsEmitter calls into the
// observability inventory (P1.5).
type matcherEmitterAdapter struct {
	inv *metrics.Inventory
}

func (a *matcherEmitterAdapter) ResourceChangeClaimed(outcome string) {
	a.inv.MatcherResourceChangesClaimedTotal.Inc(prometheus.Labels{"outcome": outcome})
}
func (a *matcherEmitterAdapter) TopicEvaluated(t string) {
	a.inv.MatcherTopicsEvaluatedTotal.Inc(prometheus.Labels{"topic_id": t})
}
func (a *matcherEmitterAdapter) TopicMatch(t string) {
	a.inv.MatcherTopicMatchTotal.Inc(prometheus.Labels{"topic_id": t})
}
func (a *matcherEmitterAdapter) FHIRPathTimeout(t string) {
	a.inv.MatcherFHIRPathTimeoutsTotal.Inc(prometheus.Labels{"topic_id": t})
}
func (a *matcherEmitterAdapter) EvaluateDuration(t string, seconds float64) {
	a.inv.MatcherEvaluateDurationSeconds.Observe(seconds, prometheus.Labels{"topic_id": t})
}
func (a *matcherEmitterAdapter) EhrEventEmitted() {
	a.inv.MatcherEhrEventsEmittedTotal.Inc(nil)
}
func (a *matcherEmitterAdapter) RowAttempt(outcome string) {
	a.inv.MatcherRowAttemptsTotal.Inc(prometheus.Labels{"outcome": outcome})
}

// LifecycleMetricsAdapter forwards lifecycle.MetricsEmitter calls into
// the observability inventory so the production binary's lifecycle
// module surfaces phase-duration histograms / shutdown counters /
// probe-request counters via /metrics. OP #341 — pre-fix the lifecycle
// module wrote into a no-op emitter because lifecycle.Start ran before
// observability.Start was up. The host now constructs this adapter
// once observability is wired and calls lcMod.SetMetrics(adapter) so
// every subsequent emit lands on the registered prom collectors.
type LifecycleMetricsAdapter struct {
	inv *metrics.Inventory
}

// NewLifecycleMetricsAdapter returns an adapter bound to mod's
// inventory. Returns nil when mod or its inventory is nil; the caller
// guards against passing nil to lcMod.SetMetrics (a nil emitter would
// panic on Inc / Observe), so a nil-or-real return is the contract.
func NewLifecycleMetricsAdapter(mod *ObservabilityModule) *LifecycleMetricsAdapter {
	if mod == nil || mod.inventory == nil {
		return nil
	}
	return &LifecycleMetricsAdapter{inv: mod.inventory}
}

// Inc forwards a counter bump. Unknown / unlabeled metric names are
// dropped silently — operators see "metric not registered" via missing
// /metrics rows, NOT via a process panic.
func (a *LifecycleMetricsAdapter) Inc(name string, labels map[string]string) {
	if a == nil || a.inv == nil {
		return
	}
	switch name {
	case "fhir_subs_lifecycle_shutdown_initiated_total":
		a.inv.LifecycleShutdownInitiatedTotal.Inc(toPromLabels(labels))
	case "fhir_subs_lifecycle_shutdown_forced_total":
		a.inv.LifecycleShutdownForcedTotal.Inc(toPromLabels(labels))
	case "fhir_subs_lifecycle_shutdown_hook_outcome_total":
		a.inv.LifecycleShutdownHookOutcome.Inc(toPromLabels(labels))
	case "fhir_subs_lifecycle_probe_requests_total":
		a.inv.LifecycleProbeRequestsTotal.Inc(toPromLabels(labels))
	case "fhir_subs_lifecycle_readiness_check_failures_total":
		a.inv.LifecycleReadinessFailuresTotal.Inc(toPromLabels(labels))
	}
}

// Add forwards a counter add. Same dispatch shape as Inc.
func (a *LifecycleMetricsAdapter) Add(name string, delta float64, labels map[string]string) {
	if a == nil || a.inv == nil {
		return
	}
	switch name {
	case "fhir_subs_lifecycle_shutdown_initiated_total":
		a.inv.LifecycleShutdownInitiatedTotal.Add(delta, toPromLabels(labels))
	case "fhir_subs_lifecycle_shutdown_forced_total":
		a.inv.LifecycleShutdownForcedTotal.Add(delta, toPromLabels(labels))
	case "fhir_subs_lifecycle_shutdown_hook_outcome_total":
		a.inv.LifecycleShutdownHookOutcome.Add(delta, toPromLabels(labels))
	case "fhir_subs_lifecycle_probe_requests_total":
		a.inv.LifecycleProbeRequestsTotal.Add(delta, toPromLabels(labels))
	case "fhir_subs_lifecycle_readiness_check_failures_total":
		a.inv.LifecycleReadinessFailuresTotal.Add(delta, toPromLabels(labels))
	}
}

// Observe forwards a histogram observation. Only the phase-duration
// histogram is registered today — extend the switch when the lifecycle
// package ships another histogram.
func (a *LifecycleMetricsAdapter) Observe(name string, value float64, labels map[string]string) {
	if a == nil || a.inv == nil {
		return
	}
	if name == "fhir_subs_lifecycle_phase_duration_seconds" {
		a.inv.LifecyclePhaseDurationSeconds.Observe(value, toPromLabels(labels))
	}
}

// Set forwards a gauge value. Only the startup-complete gauge is
// registered today.
func (a *LifecycleMetricsAdapter) Set(name string, value float64, labels map[string]string) {
	if a == nil || a.inv == nil {
		return
	}
	if name == "fhir_subs_lifecycle_startup_complete" {
		a.inv.LifecycleStartupComplete.Set(value, toPromLabels(labels))
	}
}

// toPromLabels converts the lifecycle module's plain-map label form
// into the prometheus.Labels alias that the Inventory call sites
// expect. nil maps return an empty Labels map so the call site doesn't
// special-case nil.
func toPromLabels(in map[string]string) prometheus.Labels {
	if in == nil {
		return prometheus.Labels{}
	}
	out := make(prometheus.Labels, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// SubmatcherMetricsAdapter forwards submatcher.Metrics calls into the
// observability inventory so the host can wire one adapter per worker
// (story #61). The adapter is exported so cmd/fhir-subs can pass it via
// submatcher.WithMetrics — observability owns the inventory; the worker
// owns the call sites.
type SubmatcherMetricsAdapter struct {
	inv *metrics.Inventory
}

// NewSubmatcherMetricsAdapter returns an adapter bound to mod's
// inventory. Returns nil when mod is nil (mostly for tests that pass
// the nil module shape through).
func NewSubmatcherMetricsAdapter(mod *ObservabilityModule) *SubmatcherMetricsAdapter {
	if mod == nil {
		return nil
	}
	return &SubmatcherMetricsAdapter{inv: mod.inventory}
}

// FanoutOutcome is unused at present (the audit's S-12.3 row_attempts
// view subsumes the per-decision cardinality in dashboards), but the
// method is kept so the adapter satisfies submatcher.Metrics today.
func (a *SubmatcherMetricsAdapter) FanoutOutcome(_ string, _ submatcher.FanoutDecision) {}

// EventProcessed is similarly the row-level "matched count" view; we
// expose it as a no-op until a downstream dashboard wants it. The
// matcher's MatcherEhrEventsEmittedTotal already counts the per-row
// success.
func (a *SubmatcherMetricsAdapter) EventProcessed(_ string, _ int) {}

// FilterRuntimeError is the per-subscription runtime-error counter
// (S-12.3). subscriptionID is high-cardinality and forbidden as a
// label per LLD §4.2 — drop it to a global counter once we wire one;
// for now this is a no-op and the operator-facing signal is the
// matcher/submatcher row_attempts_total{outcome="error"} aggregation.
func (a *SubmatcherMetricsAdapter) FilterRuntimeError(_ uuid.UUID) {}

// RowAttempt forwards the per-tickOnce attempt to
// fhir_subs_submatcher_row_attempts_total{outcome}.
func (a *SubmatcherMetricsAdapter) RowAttempt(outcome string) {
	a.inv.SubmatcherRowAttemptsTotal.Inc(prometheus.Labels{"outcome": outcome})
}

// PublishTopicCatalogReport bumps the topics_rejected_total and
// topic_overridden_total counters once per startup/reload load
// (S-11.4). The catalog itself is pure; the host layer translates
// Catalog.Rejected() / Catalog.Overridden() into Prometheus emits.
func (m *ObservabilityModule) PublishTopicCatalogReport(report catalog.Report) {
	if m == nil || m.inventory == nil {
		return
	}
	for _, rej := range report.Rejected {
		origin := rej.Origin
		if origin == "" {
			origin = "_unknown"
		}
		reason := rej.Reason
		if reason == "" {
			reason = "_unknown"
		}
		m.inventory.TopicsRejectedTotal.Inc(prometheus.Labels{
			"origin": origin,
			"reason": reason,
		})
	}
	for _, ov := range report.Overridden {
		from := string(ov.FromSource)
		if from == "" {
			from = "_unknown"
		}
		to := string(ov.ToSource)
		if to == "" {
			to = "_unknown"
		}
		m.inventory.TopicOverriddenTotal.Inc(prometheus.Labels{
			"from": from,
			"to":   to,
		})
	}
}

// auditAdapter adapts the audit.Writer to the AuditWriter shape exposed
// to other modules.
type auditAdapter struct {
	w *audit.Writer
}

func (a *auditAdapter) Emit(ctx context.Context, evt AuditEvent) error {
	return a.w.Emit(ctx, audit.Event{
		OccurredAt:    evt.OccurredAt,
		ActorKind:     evt.ActorKind,
		ActorID:       evt.ActorID,
		Action:        evt.Action,
		TargetKind:    evt.TargetKind,
		TargetID:      evt.TargetID,
		Outcome:       evt.Outcome,
		CorrelationID: evt.CorrelationID,
		Payload:       evt.Payload,
	})
}

// Start wires every sub-module and returns its handles.
func Start(_ context.Context, cfg Config, octx Context) (*ObservabilityModule, Handles, error) {
	if octx.StoragePool == nil {
		return nil, Handles{}, errors.New("observability: StoragePool is required")
	}

	// 1. Metrics registry + startup inventory.
	reg := prometheus.NewRegistry()
	em := metrics.New(reg)
	inv, err := metrics.RegisterStartupInventory(em)
	if err != nil {
		return nil, Handles{}, fmt.Errorf("observability: register inventory: %w", err)
	}
	// Wire the dead-letter reporter: every successful repos.DeadLettersRepo
	// Insert bumps fhir_subs_dead_letters_total{reason} (P1.12). The
	// reporter is a process-global function pointer; we install it once
	// per Start and clear in Shutdown so a second Start (testing) gets
	// the correct counter handle.
	repos.SetDeadLetterReporter(func(reason string) {
		inv.DeadLettersTotal.Inc(prometheus.Labels{"reason": reason})
	})
	// Matcher metrics (P1.5).
	matcher.SetMetricsEmitter(&matcherEmitterAdapter{inv: inv})
	// FHIRPath fail-closed → fhir_subs_matcher_fhirpath_timeouts_total.
	// The matcher classifies any unrecognized expression as a sandbox
	// failure pending the real evaluator (P1.2); the metric tracks all
	// fail-closed evaluations so operators can spot misconfigured topics.
	matcher.SetUnknownFHIRPathReporter(func(_ string) {
		// We don't have the topic ID at the call site; emit with a
		// constant "unknown" label until P1.2 lands the proper sandbox
		// (which will know which topic owns the expression).
		inv.MatcherFHIRPathTimeoutsTotal.Inc(prometheus.Labels{"topic_id": "_unknown"})
	})

	// 2. Tracing.
	tlsCfg, err := buildTracingTLSConfig(cfg.Tracing)
	if err != nil {
		return nil, Handles{}, fmt.Errorf("observability: tracing tls: %w", err)
	}
	tr, err := tracing.New(tracing.Options{
		ServiceName:     "fhir-subs",
		OTLPEndpoint:    cfg.Tracing.OTLPEndpoint,
		SampleRate:      cfg.Tracing.SampleRate,
		ExporterTimeout: cfg.Tracing.ExporterTimeout,
		Insecure:        cfg.Tracing.Insecure,
		TLSConfig:       tlsCfg,
		Headers:         cfg.Tracing.Headers,
	})
	if err != nil {
		return nil, Handles{}, fmt.Errorf("observability: tracing: %w", err)
	}

	// 3. Logging.
	level := parseLevel(cfg.Logging.Level)
	format := cfg.Logging.Format
	if format == "" {
		format = "json"
	}
	logSink := cfg.Logging.Sink
	if logSink == nil {
		logSink = os.Stdout
	}
	logger := logging.NewLogger(&logging.Options{
		Sink:             logSink,
		Level:            level,
		Format:           format,
		DebugLogPayloads: cfg.Logging.DebugLogPayloads,
		OnPHIDropped: func(field string) {
			inv.LoggingPHIDroppedTotal.Inc(prometheus.Labels{"field": field})
		},
	})

	// 4. Audit sink.
	sinkName := strings.ToLower(cfg.Audit.Sink)
	if sinkName == "" {
		sinkName = "stdout"
	}
	sink, file, fSink, err := buildAuditSink(sinkName, cfg.Audit)
	if err != nil {
		return nil, Handles{}, fmt.Errorf("observability: audit sink: %w", err)
	}

	clock := octx.Clock
	if clock == nil {
		clock = time.Now
	}
	w, err := audit.NewWriter(audit.WriterOptions{
		Store:    octx.StoragePool,
		Sink:     sink,
		Clock:    clock,
		SinkName: sinkName,
		OnSinkFailure: func(s string) {
			inv.AuditSinkFailuresTotal.Inc(prometheus.Labels{"sink": s})
		},
	})
	if err != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, Handles{}, fmt.Errorf("observability: audit writer: %w", err)
	}

	mod := &ObservabilityModule{
		registry:  reg,
		emitter:   em,
		inventory: inv,
		tracer:    tr,
		logger:    logger,
		audit:     w,
		auditFile: file,
		auditSink: fSink,
	}
	return mod, Handles{
		Metrics: em,
		Tracer:  tr,
		Logger:  logger,
		Audit:   &auditAdapter{w: w},
	}, nil
}

// Shutdown drains the tracer and closes any file-backed audit sink.
// The file sink's Close flushes any pending writes (final fsync) before
// the underlying file handle is closed.
func (m *ObservabilityModule) Shutdown(ctx context.Context) error {
	// Clear the global dead-letter reporter so a subsequent test Start
	// installs its own handle without pointing at the prior process's
	// inventory (P1.12).
	repos.SetDeadLetterReporter(nil)
	// Clear the matcher emitter and FHIRPath reporter (P1.5).
	matcher.SetMetricsEmitter(nil)
	matcher.SetUnknownFHIRPathReporter(nil)
	var firstErr error
	if m.tracer != nil {
		if err := m.tracer.Shutdown(ctx); err != nil {
			firstErr = err
		}
	}
	if m.auditSink != nil {
		if err := m.auditSink.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if m.auditFile != nil {
		if err := m.auditFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PrometheusHandler returns the /metrics handler.
func (m *ObservabilityModule) PrometheusHandler() http.Handler {
	return m.emitter.Handler()
}

// Inventory returns the canonical startup metric set.
func (m *ObservabilityModule) Inventory() *metrics.Inventory { return m.inventory }

// Registry returns the underlying Prometheus registry. The host wires
// this into modules (e.g. internal/api/metrics.New) that register their
// own collectors so a single /metrics endpoint surfaces every series
// (story #94 AC #4).
func (m *ObservabilityModule) Registry() *prometheus.Registry { return m.registry }

// Tracer returns the underlying tracer module so callers can wire
// chi-friendly tracing middleware on their HTTP routers. Nil-safe.
// Story #94.
func (m *ObservabilityModule) Tracer() *tracing.Module {
	if m == nil {
		return nil
	}
	return m.tracer
}

// AuditWriter returns the underlying hash-chained audit.Writer so the
// host wiring can hand the same writer to both the API audit adapter
// (handlers.NewChainedAuditStore) and any sibling caller (e.g. boot
// audit events). The Handles.Audit interface is the loose coupling for
// pluggable consumers; this accessor is the tight coupling for the
// production wiring that needs the concrete *audit.Writer to satisfy
// handlers.NewChainedAuditStore (story #105 + #94 reconciliation).
func (m *ObservabilityModule) AuditWriter() *audit.Writer {
	if m == nil {
		return nil
	}
	return m.audit
}

// buildTracingTLSConfig translates the file-path knobs on TracingConfig
// into a *tls.Config for the OTLP HTTP exporter. Returns (nil, nil) when
// no TLS material is configured — the caller treats that as "use the
// transport defaults". Story #94.
func buildTracingTLSConfig(cfg TracingConfig) (*tls.Config, error) {
	if cfg.TLSCertFile == "" && cfg.TLSKeyFile == "" && cfg.TLSCAFile == "" {
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			return nil, errors.New("tracing tls: cert_file and key_file must both be set")
		}
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client keypair: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	if cfg.TLSCAFile != "" {
		caBytes, err := os.ReadFile(cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, errors.New("tracing tls: ca_file contained no PEM certificates")
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// buildAuditSink builds the configured sink. When sink is "file" the
// returned *os.File is the open append-mode handle; the module closes
// it on Shutdown. The returned *fileSink is non-nil only for file sinks
// — Shutdown calls Close on it before closing the file handle so any
// pending batched data is fsync'd.
func buildAuditSink(sink string, cfg AuditConfig) (audit.Sink, *os.File, *fileSink, error) {
	switch sink {
	case "stdout", "":
		return audit.NewStdoutSink(), nil, nil, nil
	case "file":
		if cfg.FilePath == "" {
			return nil, nil, nil, errors.New("observability: audit.file_path is required when sink is file")
		}
		f, err := os.OpenFile(cfg.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, nil, err
		}
		mode, err := parseFileSyncMode(cfg.FileSyncMode)
		if err != nil {
			_ = f.Close()
			return nil, nil, nil, err
		}
		fs := newFileSinkWithSyncer(f, fileSinkOptions{
			Mode:          mode,
			BatchInterval: cfg.FileBatchInterval,
		})
		return fs, f, fs, nil
	default:
		return nil, nil, nil, fmt.Errorf("observability: unsupported audit sink %q", sink)
	}
}

// fileSyncMode enumerates the fsync policies for the file sink.
type fileSyncMode int

const (
	fileSyncEveryWrite fileSyncMode = iota // safe default — fsync per Emit
	fileSyncBatched                        // periodic fsync; opt-in (LLD §observability)
)

// parseFileSyncMode resolves the configured string into a typed mode.
// Empty string defaults to every_write.
func parseFileSyncMode(s string) (fileSyncMode, error) {
	switch strings.ToLower(s) {
	case "", "every_write", "sync":
		return fileSyncEveryWrite, nil
	case "batched":
		return fileSyncBatched, nil
	default:
		return 0, fmt.Errorf("observability: unsupported audit.file_sync_mode %q (want every_write|batched)", s)
	}
}

// writeSyncer is the seam the file sink writes through. *os.File
// satisfies it in production; tests use a fake to count Sync calls.
type writeSyncer interface {
	io.Writer
	Sync() error
}

// fileSinkOptions configures fileSink construction.
type fileSinkOptions struct {
	Mode          fileSyncMode
	BatchInterval time.Duration // batched mode only; defaults to 1s
}

// fileSink writes JSON audit lines to a file with a configurable fsync
// policy (B-34).
//
// every_write mode fsyncs on each Emit so Emit-return implies the row
// is on disk. batched mode trades durability for throughput: writes go
// to the page cache and a background ticker fsyncs every BatchInterval.
// Close drains and fsyncs once before returning so a clean shutdown
// loses nothing even in batched mode.
type fileSink struct {
	mu       sync.Mutex
	w        writeSyncer
	inner    audit.Sink // pre-constructed once; avoids per-Emit allocation
	mode     fileSyncMode
	closed   bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	tickEach time.Duration
}

// newFileSinkWithSyncer wraps a writeSyncer (production: *os.File;
// tests: a fake counter). Caller owns the underlying handle's Close.
func newFileSinkWithSyncer(w writeSyncer, opts fileSinkOptions) *fileSink {
	tick := opts.BatchInterval
	if tick <= 0 {
		tick = time.Second
	}
	fs := &fileSink{
		w:        w,
		mode:     opts.Mode,
		tickEach: tick,
		// Construct the inner WriterSink once. fileSink.mu provides the
		// serialization the inner sink would otherwise need; passing
		// nil for the inner mutex keeps it lock-free per-call.
		inner: audit.NewWriterSink("file", nil, w),
	}
	if opts.Mode == fileSyncBatched {
		fs.stopCh = make(chan struct{})
		fs.doneCh = make(chan struct{})
		go fs.tickLoop()
	}
	return fs
}

func (s *fileSink) Emit(ctx context.Context, evt audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("observability: file sink is closed")
	}
	if err := s.inner.Emit(ctx, evt); err != nil {
		return err
	}
	if s.mode == fileSyncEveryWrite {
		return s.w.Sync()
	}
	return nil
}

// tickLoop runs in batched mode and fsyncs on each tick. Exits cleanly
// on stopCh.
func (s *fileSink) tickLoop() {
	defer close(s.doneCh)
	t := time.NewTicker(s.tickEach)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.mu.Lock()
			if !s.closed {
				_ = s.w.Sync()
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

// Close stops the batch ticker (if any) and performs a final fsync so
// the durability guarantee holds for a clean shutdown.
func (s *fileSink) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stop := s.stopCh
	done := s.doneCh
	syncErr := s.w.Sync()
	s.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	return syncErr
}
