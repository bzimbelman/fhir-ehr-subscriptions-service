// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package correlation_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/correlation"
)

// ADR 0010 #1: correlation_id is UUIDv4, lower-case 36-char hyphenated form.
func TestNewID_IsUUIDv4(t *testing.T) {
	t.Parallel()
	for i := 0; i < 10; i++ {
		id := correlation.NewID()
		parsed, err := uuid.Parse(id.String())
		if err != nil {
			t.Fatalf("NewID returned invalid uuid %q: %v", id, err)
		}
		if parsed.Version() != 4 {
			t.Fatalf("expected v4 uuid; got version %d", parsed.Version())
		}
		s := id.String()
		if len(s) != 36 {
			t.Fatalf("expected 36-char string; got %d (%q)", len(s), s)
		}
		if s != strings.ToLower(s) {
			t.Fatalf("expected lower case; got %q", s)
		}
	}
}

// IDs are unique under load.
func TestNewID_Unique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		id := correlation.NewID().String()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iteration %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// ParseTraceparent reads a W3C traceparent header.
func TestParseTraceparent(t *testing.T) {
	t.Parallel()
	const tp = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx, err := correlation.ParseTraceparent(tp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ctx.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id: %s", ctx.TraceID)
	}
	if ctx.SpanID != "b7ad6b7169203331" {
		t.Fatalf("span id: %s", ctx.SpanID)
	}
	if !ctx.Sampled {
		t.Fatalf("expected sampled flag")
	}
}

func TestParseTraceparent_InvalidLength(t *testing.T) {
	t.Parallel()
	_, err := correlation.ParseTraceparent("not-valid")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestFormatTraceparent(t *testing.T) {
	t.Parallel()
	formatted := correlation.FormatTraceparent(correlation.TraceContext{
		TraceID: "0af7651916cd43dd8448eb211c80319c",
		SpanID:  "b7ad6b7169203331",
		Sampled: true,
	})
	if formatted != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Fatalf("got %q", formatted)
	}
}

// HTTP middleware extracts traceparent and correlation-id headers and
// places them on the request context.
func TestExtractFromHeaders_PrefersExistingCorrelationID(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("X-Correlation-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	ctx := correlation.ExtractFromHeaders(req.Context(), req.Header)
	got := correlation.IDFromContext(ctx)
	if got != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("got %q", got)
	}
	tp, ok := correlation.TraceparentFromContext(ctx)
	if !ok || tp.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("traceparent missing or wrong: %+v", tp)
	}
}

// When no correlation-id header is present, ExtractFromHeaders generates a
// fresh one.
func TestExtractFromHeaders_GeneratesIfMissing(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest("GET", "/", nil)
	ctx := correlation.ExtractFromHeaders(req.Context(), req.Header)
	got := correlation.IDFromContext(ctx)
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("expected fresh uuid; got %q (err %v)", got, err)
	}
}

// InjectIntoHeaders writes the correlation_id and traceparent onto outbound headers.
func TestInjectIntoHeaders(t *testing.T) {
	t.Parallel()
	ctx := correlation.WithID(context.Background(), "abc-123")
	ctx = correlation.WithTraceparent(ctx, correlation.TraceContext{
		TraceID: "0af7651916cd43dd8448eb211c80319c",
		SpanID:  "b7ad6b7169203331",
		Sampled: true,
	})
	h := http.Header{}
	correlation.InjectIntoHeaders(ctx, h)
	if h.Get("X-Correlation-ID") != "abc-123" {
		t.Fatalf("X-Correlation-ID: %q", h.Get("X-Correlation-ID"))
	}
	if !strings.HasPrefix(h.Get("traceparent"), "00-") {
		t.Fatalf("traceparent: %q", h.Get("traceparent"))
	}
}
