package com.bzonfhir.subscriptionservice.observability;

import java.util.Collections;
import java.util.HashMap;
import java.util.Map;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanKind;
import io.opentelemetry.api.trace.Tracer;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.Scope;
import io.opentelemetry.context.propagation.TextMapGetter;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * HAPI interceptor that propagates W3C trace context (Epic #387, ticket #394).
 *
 * <p>On every inbound FHIR request:
 *
 * <ol>
 *   <li>Extract the W3C {@code traceparent} (+ optional {@code tracestate}) headers using
 *       the OTel SDK's {@link io.opentelemetry.context.propagation.TextMapPropagator}.
 *       This restores the trace context the upstream service (interface-engine) started.
 *   <li>Start a SERVER-kind span named {@code http GET /fhir/Patient} (or similar) as a
 *       child of the extracted context. If no inbound traceparent is present the span
 *       becomes a new root.
 *   <li>Make the span current for the duration of the request handling so any nested
 *       work (the auth interceptor, the storage layer, the subscription matcher) runs
 *       with the right context.
 *   <li>End the span in the cleanup hooks (success OR exception).
 * </ol>
 *
 * <p>On {@link Pointcut#SUBSCRIPTION_BEFORE_REST_HOOK_DELIVERY}, when HAPI fires an
 * outbound notification webhook, we INJECT the current trace context onto the
 * subscription headers list so the outbound HTTP request carries
 * {@code traceparent} on the wire. The subscriber on the other end continues the
 * trace — third hop of the pipeline.
 *
 * <p>Sibling to {@link CorrelationIdInterceptor}: that class manages the
 * {@code X-Correlation-Id} header (single id per message); this one manages the
 * span-aware OTel context. They live next to each other in the same package
 * because they share the same lifecycle pattern (SERVER_INCOMING_REQUEST_PRE_PROCESSED
 * → SERVER_PROCESSING_COMPLETED_NORMALLY / SERVER_HANDLE_EXCEPTION) and are
 * naturally bound to the same auto-config gate.
 *
 * <h2>Storage</h2>
 *
 * <p>The active {@link Span} and its {@link Scope} are stashed in a {@link
 * ThreadLocal} pair so the cleanup hooks (which run on the same Tomcat worker thread
 * as the pre-process hook for each request) can read them back. The alternative
 * (stashing on the HttpServletRequest attributes) doesn't work cleanly because
 * HAPI's Pointcut.SERVER_PROCESSING_COMPLETED_NORMALLY signature doesn't carry the
 * servlet request — only the RequestDetails — and Tomcat's request-recycling can
 * make `request.getAttribute(...)` unreliable after the request body has been read.
 *
 * <p>The ThreadLocal is cleared on cleanup so a worker thread that handles two
 * requests back-to-back doesn't leak state from the first into the second.
 */
@Interceptor
public class OtelTraceInterceptor {

  /**
   * ThreadLocal holding the active span (created at pre-process, ended at cleanup).
   * One slot per Tomcat worker thread; HAPI runs every request handler synchronously
   * on a single thread so this is safe. Cleared in {@link #finish} so a worker
   * thread that recycles between requests doesn't leak state.
   */
  static final ThreadLocal<Span> ACTIVE_SPAN = new ThreadLocal<>();

  /** Companion to {@link #ACTIVE_SPAN} — the scope returned by Span.makeCurrent(). */
  static final ThreadLocal<Scope> ACTIVE_SCOPE = new ThreadLocal<>();

  private static final Logger log = LoggerFactory.getLogger(OtelTraceInterceptor.class);

  /**
   * Getter for {@link HttpServletRequest} headers — reads case-insensitively (servlet API
   * is case-insensitive on header lookups by spec) so {@code traceparent} matches the
   * lowercased W3C wire format too.
   */
  private static final TextMapGetter<HttpServletRequest> REQUEST_GETTER =
      new TextMapGetter<>() {
        @Override
        public Iterable<String> keys(HttpServletRequest carrier) {
          if (carrier == null) {
            return Collections.emptyList();
          }
          // HttpServletRequest.getHeaderNames returns an Enumeration; we
          // wrap it in a manual list because the OTel API expects an
          // Iterable<String>.
          java.util.Enumeration<String> e = carrier.getHeaderNames();
          java.util.List<String> names = new java.util.ArrayList<>();
          while (e.hasMoreElements()) {
            names.add(e.nextElement());
          }
          return names;
        }

        @Override
        public String get(HttpServletRequest carrier, String key) {
          return carrier == null ? null : carrier.getHeader(key);
        }
      };

  private final OpenTelemetry openTelemetry;
  private final Tracer tracer;

  public OtelTraceInterceptor(OpenTelemetry openTelemetry) {
    this.openTelemetry = openTelemetry;
    // Scope name is the same shape interface-engine uses. Operators
    // filtering by `otel.scope.name=subscription-service-hapi` see only
    // HAPI-emitted spans.
    this.tracer = openTelemetry.getTracer("subscription-service-hapi");
  }

  /**
   * Extract the inbound trace context, start the server-side span, and make it current
   * for the request handler. Returns true so HAPI continues processing.
   *
   * <p>Runs BEFORE {@link CorrelationIdInterceptor#preProcess} (no explicit ordering —
   * HAPI invokes registered interceptors in registration order, and our
   * {@link ObservabilityAutoConfiguration} registers OTel first). The order doesn't
   * actually matter for correctness; both interceptors are read-only on the request
   * carrier.
   */
  @Hook(Pointcut.SERVER_INCOMING_REQUEST_PRE_PROCESSED)
  public boolean preProcess(HttpServletRequest request, HttpServletResponse response) {
    Context extracted =
        openTelemetry
            .getPropagators()
            .getTextMapPropagator()
            .extract(Context.current(), request, REQUEST_GETTER);

    // Span name: `<METHOD> <uri>` per OTel HTTP semantic conventions.
    // Operators see this exact shape in Jaeger.
    String spanName = request.getMethod() + " " + request.getRequestURI();
    Span span =
        tracer
            .spanBuilder(spanName)
            .setSpanKind(SpanKind.SERVER)
            .setParent(extracted)
            .setAttribute("http.method", request.getMethod())
            .setAttribute("http.target", request.getRequestURI())
            .startSpan();
    Scope scope = span.makeCurrent();

    ACTIVE_SPAN.set(span);
    ACTIVE_SCOPE.set(scope);

    if (log.isDebugEnabled()) {
      log.debug(
          "otel span started traceId={} spanId={} parent={}",
          span.getSpanContext().getTraceId(),
          span.getSpanContext().getSpanId(),
          Span.fromContextOrNull(extracted) == null
              ? "none"
              : Span.fromContextOrNull(extracted).getSpanContext().getTraceId());
    }
    return true;
  }

  /**
   * End the span (success path) and close the scope. Order matters: scope.close() must
   * run before span.end(), otherwise the parent context never gets restored — the next
   * request handled by this Tomcat worker thread would see the just-ended span as
   * its current context.
   */
  @Hook(Pointcut.SERVER_PROCESSING_COMPLETED_NORMALLY)
  public void cleanupOnSuccess(ca.uhn.fhir.rest.api.server.RequestDetails requestDetails) {
    finish(requestDetails, /* isError= */ false);
  }

  /**
   * End the span on the exception path. Marks the span ERROR before ending so the trace
   * in Jaeger shows red. Same scope-then-span ordering as the success hook.
   */
  @Hook(Pointcut.SERVER_HANDLE_EXCEPTION)
  public boolean cleanupOnException(
      ca.uhn.fhir.rest.api.server.RequestDetails requestDetails) {
    finish(requestDetails, /* isError= */ true);
    return true;
  }

  /**
   * Shared cleanup body. Pulled out so both pointcut hooks share one implementation
   * (HAPI's Pointcut signatures differ — success takes RequestDetails and returns
   * void; exception takes RequestDetails and returns boolean).
   */
  private void finish(
      ca.uhn.fhir.rest.api.server.RequestDetails requestDetails, boolean isError) {
    Scope scope = ACTIVE_SCOPE.get();
    Span span = ACTIVE_SPAN.get();
    try {
      if (scope != null) {
        scope.close();
      }
      if (span != null) {
        if (isError) {
          span.setStatus(io.opentelemetry.api.trace.StatusCode.ERROR);
        }
        span.end();
      }
    } finally {
      // Always clear, even on partial failure during close. Otherwise
      // a Tomcat worker thread reused for a second request would see
      // stale ThreadLocal values from the first.
      ACTIVE_SPAN.remove();
      ACTIVE_SCOPE.remove();
    }
  }

  /**
   * Inject the current trace context into the outbound subscription notification
   * headers. Same Pointcut as {@link CorrelationIdInterceptor#beforeRestHookDelivery} —
   * we add `traceparent` (and `tracestate` when present) alongside the existing
   * `X-Correlation-Id`. Operators tracing a subscription notification end-to-end see
   * the receiver's HTTP server span as a CHILD of HAPI's outbound delivery span,
   * which is itself a child of the original /fhir/* server span.
   */
  @Hook(Pointcut.SUBSCRIPTION_BEFORE_REST_HOOK_DELIVERY)
  public boolean beforeRestHookDelivery(
      CanonicalSubscription subscription, ResourceDeliveryMessage deliveryMessage) {
    Map<String, String> carrier = new HashMap<>();
    openTelemetry
        .getPropagators()
        .getTextMapPropagator()
        .inject(Context.current(), carrier, (c, k, v) -> c.put(k, v));
    for (Map.Entry<String, String> e : carrier.entrySet()) {
      String headerLine = e.getKey() + ": " + e.getValue();
      if (!subscription.getHeaders().contains(headerLine)) {
        subscription.addHeader(headerLine);
      }
    }
    return true;
  }
}
