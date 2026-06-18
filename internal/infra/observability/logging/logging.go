// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package logging is a placeholder.
package logging

import (
	"context"
	"io"
	"log/slog"
)

// Options is a placeholder.
type Options struct {
	Sink             io.Writer
	Level            slog.Level
	Format           string
	DebugLogPayloads bool
	OnPHIDropped     func(field string)
}

// NewLogger is a placeholder; returns nil to force tests to fail.
func NewLogger(*Options) *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// WithCorrelationID is a placeholder.
func WithCorrelationID(ctx context.Context, _ string) context.Context { return ctx }
