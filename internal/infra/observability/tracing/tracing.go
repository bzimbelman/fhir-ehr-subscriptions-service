// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package tracing implements the OpenTelemetry tracing layer of the
// observability module. It exposes a tracer rooted at the four entry
// points called out in LLD §5 (mllp.receive / api.request /
// fhir_scan.tick / vendor_feed.event) and exports spans over OTLP HTTP
// to the configured collector.
//
// Sampling is head-based at the configured SampleRate; LLD §5 defaults
// SampleRate to 0.1 when the operator has not pinned it.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// DefaultSampleRate is the head sampling rate when the operator does not
// pin one (LLD §5).
const DefaultSampleRate = 0.1

// Options configures the tracing module.
type Options struct {
	// ServiceName is the service.name attribute attached to every span.
	ServiceName string
	// OTLPEndpoint is the OTLP HTTP collector URL. Empty disables tracing
	// entirely (LLD §11: tracer is a no-op when disabled).
	OTLPEndpoint string
	// SampleRate is the head sampling rate, 0.0–1.0. Zero means use the
	// default (0.1).
	SampleRate float64
	// SpanExporter overrides the OTLP exporter. Used by tests with an
	// in-memory exporter.
	SpanExporter sdktrace.SpanExporter
}

// Module is the tracing handle.
type Module struct {
	tp         *sdktrace.TracerProvider
	tracer     trace.Tracer
	disabled   bool
	sampleRate float64
}

// New constructs a tracing module from cfg. If both OTLPEndpoint and
// SpanExporter are empty, the module is disabled (returns a no-op tracer).
func New(opts Options) (*Module, error) {
	rate := opts.SampleRate
	if rate == 0 {
		rate = DefaultSampleRate
	}

	if opts.OTLPEndpoint == "" && opts.SpanExporter == nil {
		return &Module{
			tracer:     noop.NewTracerProvider().Tracer("fhir-subs"),
			disabled:   true,
			sampleRate: rate,
		}, nil
	}

	exporter := opts.SpanExporter
	if exporter == nil {
		var err error
		exporter, err = otlptrace.New(context.Background(), otlptracehttp.NewClient(
			otlptracehttp.WithEndpointURL(opts.OTLPEndpoint),
		))
		if err != nil {
			return nil, fmt.Errorf("tracing: build OTLP exporter: %w", err)
		}
	}

	res, err := resource.New(context.Background(), resource.WithAttributes(
		semconv.ServiceNameKey.String(opts.ServiceName),
	))
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate))),
		sdktrace.WithResource(res),
	)

	return &Module{
		tp:         tp,
		tracer:     tp.Tracer(opts.ServiceName),
		disabled:   false,
		sampleRate: rate,
	}, nil
}

// Tracer returns the tracer used to create spans.
func (m *Module) Tracer() trace.Tracer { return m.tracer }

// Disabled reports whether tracing is a no-op.
func (m *Module) Disabled() bool { return m.disabled }

// SampleRate returns the head sampling rate.
func (m *Module) SampleRate() float64 { return m.sampleRate }

// ForceFlush flushes all pending spans to the exporter.
func (m *Module) ForceFlush(ctx context.Context) error {
	if m.tp == nil {
		return nil
	}
	return m.tp.ForceFlush(ctx)
}

// Shutdown drains the exporter or returns when ctx expires.
func (m *Module) Shutdown(ctx context.Context) error {
	if m.tp == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- m.tp.Shutdown(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
