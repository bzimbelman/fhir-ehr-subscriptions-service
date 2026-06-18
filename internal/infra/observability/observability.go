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

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/tracing"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// MetricsConfig is the metrics-layer config block.
type MetricsConfig struct {
	Bind string
	Path string
}

// TracingConfig is the tracing-layer config block.
type TracingConfig struct {
	OTLPEndpoint string
	SampleRate   float64
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

	// 2. Tracing.
	tr, err := tracing.New(tracing.Options{
		ServiceName:  "fhir-subs",
		OTLPEndpoint: cfg.Tracing.OTLPEndpoint,
		SampleRate:   cfg.Tracing.SampleRate,
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
