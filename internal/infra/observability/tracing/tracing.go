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
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
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

// DefaultExporterTimeout caps how long the OTLP exporter waits for the
// collector before failing. Exposed as a knob; defaults to 10s.
const DefaultExporterTimeout = 10 * time.Second

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

	// ExporterTimeout caps the OTLP exporter's per-call deadline.
	// Zero means use DefaultExporterTimeout. S-14 #10.
	ExporterTimeout time.Duration

	// TLSConfig pins the TLS settings used for the OTLP HTTP transport.
	// Nil means use the system defaults; a non-nil value enables mTLS
	// when ClientCertificates / RootCAs are populated. S-14 #10.
	TLSConfig *tls.Config

	// Headers carry static auth credentials forwarded with every OTLP
	// request (e.g., {"authorization": "Bearer <static>"}). Use
	// TLSConfig + a private collector network in preference; this hook
	// covers shared collectors gated by a header check. S-14 #10.
	Headers map[string]string

	// Insecure disables transport security. Default is to require TLS
	// for any non-loopback OTLP endpoint. S-14 #10.
	Insecure bool
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
	timeout := opts.ExporterTimeout
	if timeout <= 0 {
		timeout = DefaultExporterTimeout
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
		// Refuse to ship spans over plaintext to a non-loopback
		// collector unless the operator opts in explicitly. Spans
		// can carry path / status / error information; they are not
		// PHI but they are operational telemetry that benefits from
		// transport security as a baseline.
		if !opts.Insecure && !isLoopbackEndpoint(opts.OTLPEndpoint) && opts.TLSConfig == nil {
			return nil, errors.New("tracing: OTLP endpoint is non-loopback and Insecure=false but no TLSConfig was provided")
		}

		clientOpts := []otlptracehttp.Option{
			otlptracehttp.WithEndpointURL(opts.OTLPEndpoint),
			otlptracehttp.WithTimeout(timeout),
		}
		if opts.TLSConfig != nil {
			clientOpts = append(clientOpts, otlptracehttp.WithTLSClientConfig(opts.TLSConfig))
		}
		if opts.Insecure {
			clientOpts = append(clientOpts, otlptracehttp.WithInsecure())
		}
		if len(opts.Headers) > 0 {
			hcopy := make(map[string]string, len(opts.Headers))
			for k, v := range opts.Headers {
				hcopy[k] = v
			}
			clientOpts = append(clientOpts, otlptracehttp.WithHeaders(hcopy))
		}
		// Bound the exporter-build call so a hung collector cannot
		// pin process startup (S-14 #9).
		buildCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var err error
		exporter, err = otlptrace.New(buildCtx, otlptracehttp.NewClient(clientOpts...))
		if err != nil {
			return nil, fmt.Errorf("tracing: build OTLP exporter: %w", err)
		}
	}

	resCtx, resCancel := context.WithTimeout(context.Background(), timeout)
	defer resCancel()
	res, err := resource.New(resCtx, resource.WithAttributes(
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

// isLoopbackEndpoint reports whether url's host resolves to localhost.
// We accept "http://localhost", "http://127.0.0.1", "http://[::1]"
// without TLS; everything else is rejected unless TLSConfig or
// Insecure=true is set.
func isLoopbackEndpoint(url string) bool {
	low := strings.ToLower(url)
	for _, prefix := range []string{
		"http://localhost", "http://127.0.0.1", "http://[::1]",
		"https://localhost", "https://127.0.0.1", "https://[::1]",
	} {
		if strings.HasPrefix(low, prefix) {
			return true
		}
	}
	return false
}

// SafeAttribute returns a span attribute with the value redacted if it
// resembles PII. Use this from callers that want the same key shape
// the LLD documents but cannot guarantee the value is operator data.
//
// The classifier is intentionally narrow: anything that *might* carry
// PII (long string, structured payload, anything matching a sensitive
// key name) becomes "[redacted]". Numeric, boolean, and short
// id-shaped values pass through.
//
// Use this for: anything reaching span.SetAttributes from request
// bodies, FHIR resources, HL7 segments, or bound subscription state.
// Do NOT use it for fixed structural metadata (route, method, status).
func SafeAttribute(key, value string) attribute.KeyValue {
	if value == "" {
		return attribute.String(key, "")
	}
	if isSensitiveKey(key) || len(value) > 256 {
		return attribute.String(key, "[redacted]")
	}
	return attribute.String(key, value)
}

func isSensitiveKey(k string) bool {
	low := strings.ToLower(k)
	for _, suffix := range []string{
		"resource", "bundle", "body", "payload", "raw", "hl7",
		"patient", "mrn", "ssn", "dob", "name", "address",
		"phone", "email", "identifier", "callback", "webhook",
		"token", "secret", "password", "authorization",
	} {
		if strings.Contains(low, suffix) {
			return true
		}
	}
	return false
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
