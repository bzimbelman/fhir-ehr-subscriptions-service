// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/correlation"
)

// TracingMiddleware returns chi-friendly middleware that opens an
// OpenTelemetry span for every incoming request as required by LLD §9
// ("A trace span is opened for every request"). The span name is
// "<METHOD> <route-pattern>" and standard HTTP attributes are attached
// alongside the correlation_id pulled from the X-Correlation-ID header
// (auto-generated when absent).
//
// A nil tracer turns the middleware into a passthrough so the API can
// run with tracing disabled.
func TracingMiddleware(tracer trace.Tracer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if tracer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := correlation.ExtractFromHeaders(r.Context(), r.Header)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			// Defer span name resolution: the chi route pattern is only
			// known after the inner handler runs. We start the span with
			// a placeholder name and rename it before End() fires.
			ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.target", r.URL.Path),
					attribute.String("correlation_id", correlation.IDFromContext(ctx)),
				),
			)
			defer span.End()

			next.ServeHTTP(rec, r.WithContext(ctx))

			route := r.URL.Path
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pat := rctx.RoutePattern(); pat != "" {
					route = pat
				}
			}
			// chi resets RouteContext on the original request after
			// dispatch; we read from the wrapped one. As a fallback,
			// also try the request context that we passed downstream.
			if route == r.URL.Path {
				if rctx := chi.RouteContext(ctx); rctx != nil {
					if pat := rctx.RoutePattern(); pat != "" {
						route = pat
					}
				}
			}

			span.SetAttributes(
				attribute.String("http.route", route),
				attribute.Int("http.status_code", rec.status),
			)
			span.SetName(r.Method + " " + route)
			if rec.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
		})
	}
}

// statusRecorder is shared with the metrics middleware (declared in
// metrics.go could be a better home, but this package owns its own
// copy to avoid an import cycle with internal/api/metrics).
//
// Defined in this file separately so the package can be linted cleanly
// even when the metrics package is absent. The other declaration lives
// in metrics.go inside internal/api/metrics — these are distinct types.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteStatus bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteStatus {
		s.status = code
		s.wroteStatus = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteStatus {
		s.wroteStatus = true
	}
	return s.ResponseWriter.Write(b)
}
