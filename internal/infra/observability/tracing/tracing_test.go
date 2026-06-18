// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package tracing_test

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/tracing"
)

// LLD §5: tracer must support root spans at the four entry points
// and start_stage_span with parent context.
func TestNew_DisabledWhenEndpointEmpty(t *testing.T) {
	t.Parallel()
	tp, err := tracing.New(tracing.Options{
		ServiceName: "fhir-subs",
		// no OTLP endpoint -> tracing disabled but module returns nop tracer
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tp == nil {
		t.Fatalf("expected non-nil module")
	}
	if !tp.Disabled() {
		t.Fatalf("expected disabled when no OTLP endpoint")
	}

	tr := tp.Tracer()
	_, span := tr.Start(context.Background(), "noop")
	span.End()

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// New returns a real tracer when given an in-memory exporter.
func TestNew_WithInMemoryExporter(t *testing.T) {
	t.Parallel()
	exporter := tracetest.NewInMemoryExporter()
	tp, err := tracing.New(tracing.Options{
		ServiceName:  "fhir-subs",
		SampleRate:   1.0,
		SpanExporter: exporter,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer func() {
		_ = tp.Shutdown(context.Background())
	}()
	if tp.Disabled() {
		t.Fatalf("expected enabled")
	}

	tr := tp.Tracer()
	_, span := tr.Start(context.Background(), "mllp.receive")
	span.End()

	// Force flush so test sees the span.
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatalf("expected at least 1 exported span")
	}
	if spans[0].Name != "mllp.receive" {
		t.Fatalf("name: %s", spans[0].Name)
	}
}

// Sampler defaults to head-based at 0.1 when SampleRate is 0.
func TestNew_DefaultSampleRate(t *testing.T) {
	t.Parallel()
	tp, err := tracing.New(tracing.Options{
		ServiceName: "fhir-subs",
		// SampleRate left zero
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()
	if tp.SampleRate() != 0.1 {
		t.Fatalf("expected 0.1; got %f", tp.SampleRate())
	}
}

// SampleRate explicit value is honored.
func TestNew_ExplicitSampleRate(t *testing.T) {
	t.Parallel()
	tp, err := tracing.New(tracing.Options{
		ServiceName: "fhir-subs",
		SampleRate:  0.5,
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()
	if tp.SampleRate() != 0.5 {
		t.Fatalf("got %f", tp.SampleRate())
	}
}

// Shutdown returns even when the exporter blocks beyond the deadline.
func TestShutdown_RespectsContext(t *testing.T) {
	t.Parallel()
	tp, err := tracing.New(tracing.Options{
		ServiceName:  "fhir-subs",
		SampleRate:   1.0,
		SpanExporter: blockingExporter{},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = tp.Shutdown(ctx) // we just want it to return
}

type blockingExporter struct{}

func (blockingExporter) ExportSpans(ctx context.Context, _ []sdktrace.ReadOnlySpan) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingExporter) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
