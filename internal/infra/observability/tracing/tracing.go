// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package tracing is a placeholder.
package tracing

import (
	"context"
	"errors"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Options is a placeholder.
type Options struct {
	ServiceName  string
	OTLPEndpoint string
	SampleRate   float64
	SpanExporter sdktrace.SpanExporter
}

// Module is a placeholder.
type Module struct{}

// Tracer is a placeholder.
func (*Module) Tracer() trace.Tracer { return nil }

// Disabled is a placeholder.
func (*Module) Disabled() bool { return true }

// SampleRate is a placeholder.
func (*Module) SampleRate() float64 { return 0 }

// ForceFlush is a placeholder.
func (*Module) ForceFlush(context.Context) error { return nil }

// Shutdown is a placeholder.
func (*Module) Shutdown(context.Context) error { return nil }

// New is a placeholder.
func New(Options) (*Module, error) { return nil, errors.New("not yet implemented") }
