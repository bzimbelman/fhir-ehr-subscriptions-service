// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package correlation is a placeholder.
package correlation

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

// ID is a placeholder.
type ID = uuid.UUID

// TraceContext is a placeholder.
type TraceContext struct {
	TraceID string
	SpanID  string
	Sampled bool
}

// NewID is a placeholder.
func NewID() ID { return uuid.UUID{} }

// ParseTraceparent is a placeholder.
func ParseTraceparent(string) (TraceContext, error) {
	return TraceContext{}, errors.New("not yet implemented")
}

// FormatTraceparent is a placeholder.
func FormatTraceparent(TraceContext) string { return "" }

// ExtractFromHeaders is a placeholder.
func ExtractFromHeaders(ctx context.Context, _ http.Header) context.Context { return ctx }

// IDFromContext is a placeholder.
func IDFromContext(context.Context) string { return "" }

// TraceparentFromContext is a placeholder.
func TraceparentFromContext(context.Context) (TraceContext, bool) { return TraceContext{}, false }

// WithID is a placeholder.
func WithID(ctx context.Context, _ string) context.Context { return ctx }

// WithTraceparent is a placeholder.
func WithTraceparent(ctx context.Context, _ TraceContext) context.Context { return ctx }

// InjectIntoHeaders is a placeholder.
func InjectIntoHeaders(context.Context, http.Header) {}
