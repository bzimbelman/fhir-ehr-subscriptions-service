// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package correlation provides the correlation_id and W3C traceparent
// helpers shared across observability sub-modules.
//
// ADR 0010 #1 pins correlation_id as a UUIDv4 stored as Postgres uuid
// end-to-end. Wire form is the canonical 36-character lower-case
// hyphenated string (xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx).
package correlation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// ID is the correlation identifier alias.
type ID = uuid.UUID

// TraceContext is the parsed W3C traceparent carry payload.
type TraceContext struct {
	TraceID string // 32 lower-case hex chars
	SpanID  string // 16 lower-case hex chars
	Sampled bool
}

// NewID generates a fresh UUIDv4 correlation id.
func NewID() ID { return uuid.New() }

// ParseTraceparent parses the W3C traceparent header value:
//
//	"00-<trace-id 32 hex>-<span-id 16 hex>-<flags 2 hex>"
func ParseTraceparent(s string) (TraceContext, error) {
	parts := strings.Split(s, "-")
	if len(parts) != 4 {
		return TraceContext{}, fmt.Errorf("correlation: traceparent expected 4 parts; got %d", len(parts))
	}
	if parts[0] != "00" {
		return TraceContext{}, fmt.Errorf("correlation: traceparent unsupported version %q", parts[0])
	}
	if len(parts[1]) != 32 {
		return TraceContext{}, errors.New("correlation: traceparent trace-id must be 32 hex chars")
	}
	if len(parts[2]) != 16 {
		return TraceContext{}, errors.New("correlation: traceparent span-id must be 16 hex chars")
	}
	if len(parts[3]) != 2 {
		return TraceContext{}, errors.New("correlation: traceparent flags must be 2 hex chars")
	}
	return TraceContext{
		TraceID: strings.ToLower(parts[1]),
		SpanID:  strings.ToLower(parts[2]),
		Sampled: parts[3] != "00",
	}, nil
}

// FormatTraceparent renders a TraceContext to the W3C wire form.
func FormatTraceparent(tc TraceContext) string {
	flags := "00"
	if tc.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", tc.TraceID, tc.SpanID, flags)
}

// HTTP header names per W3C and project convention.
const (
	HeaderCorrelationID = "X-Correlation-ID"
	HeaderTraceparent   = "traceparent"
)

// context keys.
type idCtxKey struct{}
type tpCtxKey struct{}

// WithID returns a copy of ctx carrying id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, idCtxKey{}, id)
}

// WithTraceparent returns a copy of ctx carrying tc.
func WithTraceparent(ctx context.Context, tc TraceContext) context.Context {
	return context.WithValue(ctx, tpCtxKey{}, tc)
}

// IDFromContext returns the correlation id stored in ctx, or "" if none.
func IDFromContext(ctx context.Context) string {
	if v := ctx.Value(idCtxKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// TraceparentFromContext returns the traceparent stored in ctx.
func TraceparentFromContext(ctx context.Context) (TraceContext, bool) {
	if v := ctx.Value(tpCtxKey{}); v != nil {
		if tc, ok := v.(TraceContext); ok {
			return tc, true
		}
	}
	return TraceContext{}, false
}

// ExtractFromHeaders pulls the X-Correlation-ID and traceparent headers
// off h and returns a context carrying them. If the correlation header
// is missing, a fresh UUIDv4 is generated.
func ExtractFromHeaders(ctx context.Context, h http.Header) context.Context {
	id := h.Get(HeaderCorrelationID)
	if id == "" {
		id = NewID().String()
	}
	ctx = WithID(ctx, id)
	if tp := h.Get(HeaderTraceparent); tp != "" {
		if tc, err := ParseTraceparent(tp); err == nil {
			ctx = WithTraceparent(ctx, tc)
		}
	}
	return ctx
}

// InjectIntoHeaders writes the correlation id (always) and traceparent (if
// present in ctx) onto the outbound headers.
func InjectIntoHeaders(ctx context.Context, h http.Header) {
	if id := IDFromContext(ctx); id != "" {
		h.Set(HeaderCorrelationID, id)
	}
	if tp, ok := TraceparentFromContext(ctx); ok {
		h.Set(HeaderTraceparent, FormatTraceparent(tp))
	}
}
