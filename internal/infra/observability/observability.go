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
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/logging"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/metrics"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/tracing"
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
}

// AuditConfig is the audit-log config block.
type AuditConfig struct {
	Sink     string // "stdout" (default) | "file" | "syslog" | "otlp"
	FilePath string // used when Sink == "file"
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
	logger := logging.NewLogger(&logging.Options{
		Sink:   os.Stdout,
		Level:  level,
		Format: format,
		OnPHIDropped: func(field string) {
			inv.LoggingPHIDroppedTotal.Inc(prometheus.Labels{"field": field})
		},
	})

	// 4. Audit sink.
	sinkName := strings.ToLower(cfg.Audit.Sink)
	if sinkName == "" {
		sinkName = "stdout"
	}
	sink, file, err := buildAuditSink(sinkName, cfg.Audit.FilePath)
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
	}
	return mod, Handles{
		Metrics: em,
		Tracer:  tr,
		Logger:  logger,
		Audit:   &auditAdapter{w: w},
	}, nil
}

// Shutdown drains the tracer and closes any file-backed audit sink.
func (m *ObservabilityModule) Shutdown(ctx context.Context) error {
	var firstErr error
	if m.tracer != nil {
		if err := m.tracer.Shutdown(ctx); err != nil {
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
// it on Shutdown.
func buildAuditSink(sink, path string) (audit.Sink, *os.File, error) {
	switch sink {
	case "stdout", "":
		return audit.NewStdoutSink(), nil, nil
	case "file":
		if path == "" {
			return nil, nil, errors.New("observability: audit.file_path is required when sink is file")
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		return &fileSink{f: f}, f, nil
	default:
		return nil, nil, fmt.Errorf("observability: unsupported audit sink %q", sink)
	}
}

// fileSink writes JSON audit lines to a file. It defers serialization to
// audit.NewWriterSink so the wire format matches stdout.
type fileSink struct {
	f io.Writer
}

func (s *fileSink) Emit(ctx context.Context, evt audit.Event) error {
	return audit.NewWriterSink("file", nil, s.f).Emit(ctx, evt)
}
