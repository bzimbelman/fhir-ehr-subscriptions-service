package com.bzonfhir.subscriptionservice.observability;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.util.Collections;
import java.util.Enumeration;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.context.Context;
import io.opentelemetry.sdk.OpenTelemetrySdk;
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter;
import io.opentelemetry.sdk.trace.SdkTracerProvider;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SimpleSpanProcessor;
import io.opentelemetry.context.propagation.ContextPropagators;
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * Unit tests for {@link OtelTraceInterceptor} (Epic #387, ticket #394).
 *
 * Covers:
 *
 * <ol>
 *   <li>Inbound request with a W3C traceparent → server span shares the upstream
 *       trace id (the receive→HAPI continuity case mandated by the ticket).
 *   <li>Inbound request without traceparent → server span starts a fresh trace.
 *   <li>Subscription delivery (SUBSCRIPTION_BEFORE_REST_HOOK_DELIVERY) → outbound
 *       headers carry traceparent, so the downstream subscriber continues the trace.
 *   <li>Cleanup hooks end the span (success / exception paths both work).
 * </ol>
 *
 * Sibling of {@link CorrelationIdInterceptorTest} — same Mockito-based servlet
 * stubbing pattern, no Spring context.
 */
class OtelTraceInterceptorTest {

  private InMemorySpanExporter exporter;
  private OpenTelemetry openTelemetry;
  private OtelTraceInterceptor interceptor;

  @BeforeEach
  void setUp() {
    exporter = InMemorySpanExporter.create();
    openTelemetry =
        OpenTelemetrySdk.builder()
            .setTracerProvider(
                SdkTracerProvider.builder()
                    .addSpanProcessor(SimpleSpanProcessor.create(exporter))
                    .build())
            .setPropagators(
                ContextPropagators.create(W3CTraceContextPropagator.getInstance()))
            .build();
    interceptor = new OtelTraceInterceptor(openTelemetry);
  }

  @AfterEach
  void tearDown() {
    // ThreadLocals are cleared by finish() in the production code; if a
    // test leaks one (a missed cleanup hook), explicitly remove it here
    // so the next test in the class doesn't inherit a half-active span.
    OtelTraceInterceptor.ACTIVE_SPAN.remove();
    OtelTraceInterceptor.ACTIVE_SCOPE.remove();
  }

  // -- inbound trace context extraction ---------------------------------

  @Test
  void preProcessExtractsInboundTraceparent() {
    // A well-formed W3C traceparent (v00 format).
    String inboundTraceparent =
        "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01";
    HttpServletRequest req = mockRequest("GET", "/fhir/Patient/1", Map.of("traceparent", inboundTraceparent));
    HttpServletResponse resp = mock(HttpServletResponse.class);

    interceptor.preProcess(req, resp);
    interceptor.cleanupOnSuccess(mock(RequestDetails.class));

    List<SpanData> spans = exporter.getFinishedSpanItems();
    assertThat(spans).hasSize(1);
    SpanData span = spans.get(0);
    // The trace id in the span's context must equal the trace id from
    // the inbound traceparent — proving the SDK extracted the context.
    assertThat(span.getTraceId()).isEqualTo("0123456789abcdef0123456789abcdef");
    assertThat(span.getKind()).isEqualTo(io.opentelemetry.api.trace.SpanKind.SERVER);
    // And the span's parent should be the inbound span id.
    assertThat(span.getParentSpanContext().getSpanId()).isEqualTo("fedcba9876543210");
  }

  @Test
  void preProcessStartsNewTraceWhenNoInboundHeader() {
    HttpServletRequest req = mockRequest("POST", "/fhir/Bundle", Collections.emptyMap());
    HttpServletResponse resp = mock(HttpServletResponse.class);

    interceptor.preProcess(req, resp);
    interceptor.cleanupOnSuccess(mock(RequestDetails.class));

    List<SpanData> spans = exporter.getFinishedSpanItems();
    assertThat(spans).hasSize(1);
    SpanData span = spans.get(0);
    // No inbound parent — span is a root.
    assertThat(span.getParentSpanContext().isValid()).isFalse();
    assertThat(span.getKind()).isEqualTo(io.opentelemetry.api.trace.SpanKind.SERVER);
  }

  @Test
  void cleanupOnExceptionMarksSpanError() {
    HttpServletRequest req = mockRequest("DELETE", "/fhir/Patient/1", Collections.emptyMap());
    HttpServletResponse resp = mock(HttpServletResponse.class);

    interceptor.preProcess(req, resp);
    interceptor.cleanupOnException(mock(RequestDetails.class));

    List<SpanData> spans = exporter.getFinishedSpanItems();
    assertThat(spans).hasSize(1);
    assertThat(spans.get(0).getStatus().getStatusCode())
        .isEqualTo(io.opentelemetry.api.trace.StatusCode.ERROR);
  }

  // -- outbound subscription delivery -----------------------------------

  @Test
  void beforeRestHookDeliveryInjectsTraceparent() {
    // Set up an active span so the propagator has a context to inject.
    HttpServletRequest req = mockRequest("PUT", "/fhir/Subscription/1", Collections.emptyMap());
    HttpServletResponse resp = mock(HttpServletResponse.class);
    interceptor.preProcess(req, resp);

    CanonicalSubscription sub = new CanonicalSubscription();
    sub.addHeader("Authorization: Bearer xyz");
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();

    boolean kept = interceptor.beforeRestHookDelivery(sub, msg);
    assertThat(kept).isTrue();

    // After the hook runs, the headers list should contain a traceparent
    // line whose value matches the current span context. The header
    // shape is "traceparent: 00-<32hex>-<16hex>-<2hex>".
    assertThat(sub.getHeaders())
        .anyMatch(h -> h.startsWith("traceparent: 00-"))
        .contains("Authorization: Bearer xyz");

    // Clean up the active span so other tests start fresh.
    interceptor.cleanupOnSuccess(mock(RequestDetails.class));
  }

  @Test
  void beforeRestHookDeliveryIsIdempotent() {
    HttpServletRequest req = mockRequest("PUT", "/fhir/Subscription/1", Collections.emptyMap());
    HttpServletResponse resp = mock(HttpServletResponse.class);
    interceptor.preProcess(req, resp);

    CanonicalSubscription sub = new CanonicalSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();

    interceptor.beforeRestHookDelivery(sub, msg);
    interceptor.beforeRestHookDelivery(sub, msg);

    long count =
        sub.getHeaders().stream().filter(h -> h.startsWith("traceparent: ")).count();
    assertThat(count).isEqualTo(1);

    interceptor.cleanupOnSuccess(mock(RequestDetails.class));
  }

  // -- helpers ----------------------------------------------------------

  private static HttpServletRequest mockRequest(
      String method, String uri, Map<String, String> headers) {
    HttpServletRequest req = mock(HttpServletRequest.class);
    when(req.getMethod()).thenReturn(method);
    when(req.getRequestURI()).thenReturn(uri);
    when(req.getHeaderNames())
        .thenAnswer(invocation -> Collections.enumeration(headers.keySet()));
    for (Map.Entry<String, String> e : headers.entrySet()) {
      when(req.getHeader(e.getKey())).thenReturn(e.getValue());
    }
    return req;
  }
}
