// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

func newRecordingTracerProvider() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	return tp, rec
}

func TestTracingMiddleware_OpensSpanPerRequest(t *testing.T) {
	t.Parallel()
	tp, rec := newRecordingTracerProvider()
	tracer := tp.Tracer("test")

	r := chi.NewRouter()
	r.Use(handlers.TracingMiddleware(tracer))
	r.Get("/Subscription/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/Subscription/abc", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if got := len(rec.Ended()); got != 1 {
		t.Fatalf("ended spans = %d; want 1", got)
	}
	span := rec.Ended()[0]
	if !strings.Contains(span.Name(), "GET") {
		t.Errorf("span name %q should contain method", span.Name())
	}
	if !strings.Contains(span.Name(), "/Subscription/{id}") {
		t.Errorf("span name %q should contain route pattern", span.Name())
	}
}

func TestTracingMiddleware_AttachesCorrelationID(t *testing.T) {
	t.Parallel()
	tp, rec := newRecordingTracerProvider()
	tracer := tp.Tracer("test")

	r := chi.NewRouter()
	r.Use(handlers.TracingMiddleware(tracer))
	r.Get("/metadata", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	req.Header.Set("X-Correlation-ID", "00000000-0000-4000-8000-000000000abc")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if len(rec.Ended()) == 0 {
		t.Fatalf("no spans recorded")
	}
	span := rec.Ended()[0]
	found := false
	for _, kv := range span.Attributes() {
		if string(kv.Key) == "correlation_id" && kv.Value.AsString() == "00000000-0000-4000-8000-000000000abc" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected correlation_id attribute on span; attrs=%v", span.Attributes())
	}
}

func TestTracingMiddleware_RecordsHTTPStatusAttribute(t *testing.T) {
	t.Parallel()
	tp, rec := newRecordingTracerProvider()
	tracer := tp.Tracer("test")

	r := chi.NewRouter()
	r.Use(handlers.TracingMiddleware(tracer))
	r.Get("/Subscription/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	req := httptest.NewRequest(http.MethodGet, "/Subscription/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if len(rec.Ended()) == 0 {
		t.Fatalf("no spans recorded")
	}
	span := rec.Ended()[0]
	found := false
	for _, kv := range span.Attributes() {
		if string(kv.Key) == "http.status_code" && kv.Value.AsInt64() == 404 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected http.status_code=404 attribute; attrs=%v", span.Attributes())
	}
}

func TestTracingMiddleware_NilTracerIsNoOp(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(handlers.TracingMiddleware(nil))
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200; got %d", rr.Code)
	}
}
