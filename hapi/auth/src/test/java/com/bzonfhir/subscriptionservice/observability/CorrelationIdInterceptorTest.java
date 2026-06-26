package com.bzonfhir.subscriptionservice.observability;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.slf4j.MDC;

import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * Unit tests for {@link CorrelationIdInterceptor} (Epic #387, ticket #388).
 *
 * Verifies the four observable surfaces of the interceptor:
 *
 *   1. Inbound request with no `X-Correlation-Id` header — MDC is set to a
 *      freshly generated UUID and the same value is echoed on the response.
 *   2. Inbound request with the header — MDC is set to that value and
 *      echoed back unchanged.
 *   3. Inbound request with a malformed header — MDC is set to a fresh
 *      UUID (the bad value is discarded).
 *   4. Outbound subscription delivery — the current MDC value is copied
 *      onto the CanonicalSubscription headers list so HAPI's REST-hook
 *      deliverer sends it on the wire.
 *
 * Cleanup hooks (SERVER_PROCESSING_COMPLETED_NORMALLY, SERVER_HANDLE_EXCEPTION)
 * are covered too — calling them clears the MDC.
 */
class CorrelationIdInterceptorTest {

  private final CorrelationIdInterceptor interceptor = new CorrelationIdInterceptor();

  @AfterEach
  void cleanup() {
    MDC.clear();
  }

  // -- inbound request ---------------------------------------------------

  @Test
  void generatesIdWhenHeaderMissing() {
    HttpServletRequest req = mock(HttpServletRequest.class);
    HttpServletResponse resp = mock(HttpServletResponse.class);
    when(req.getHeader(CorrelationIdInterceptor.HEADER)).thenReturn(null);

    boolean keepGoing = interceptor.preProcess(req, resp);
    assertThat(keepGoing).isTrue();

    // The generated id is on the MDC AND echoed on the response.
    String onMdc = MDC.get(CorrelationIdInterceptor.MDC_KEY);
    assertThat(onMdc).matches("[0-9a-fA-F-]{36}");

    ArgumentCaptor<String> hdr = ArgumentCaptor.forClass(String.class);
    org.mockito.Mockito.verify(resp)
        .setHeader(org.mockito.Mockito.eq(CorrelationIdInterceptor.HEADER), hdr.capture());
    assertThat(hdr.getValue()).isEqualTo(onMdc);
  }

  @Test
  void adoptsInboundHeaderValue() {
    HttpServletRequest req = mock(HttpServletRequest.class);
    HttpServletResponse resp = mock(HttpServletResponse.class);
    String inbound = "engine-supplied-id-xyz";
    when(req.getHeader(CorrelationIdInterceptor.HEADER)).thenReturn(inbound);

    interceptor.preProcess(req, resp);
    assertThat(MDC.get(CorrelationIdInterceptor.MDC_KEY)).isEqualTo(inbound);
    org.mockito.Mockito.verify(resp).setHeader(CorrelationIdInterceptor.HEADER, inbound);
  }

  @Test
  void sanitizesMalformedHeaderValue() {
    HttpServletRequest req = mock(HttpServletRequest.class);
    HttpServletResponse resp = mock(HttpServletResponse.class);
    String bad = "abc\ninjected";
    when(req.getHeader(CorrelationIdInterceptor.HEADER)).thenReturn(bad);

    interceptor.preProcess(req, resp);
    String onMdc = MDC.get(CorrelationIdInterceptor.MDC_KEY);
    assertThat(onMdc).isNotEqualTo(bad);
    assertThat(onMdc).matches("[0-9a-fA-F-]{36}");
  }

  @Test
  void cleanupOnSuccessRemovesMdc() {
    MDC.put(CorrelationIdInterceptor.MDC_KEY, "to-be-cleared");
    interceptor.cleanupOnSuccess(null);
    assertThat(MDC.get(CorrelationIdInterceptor.MDC_KEY)).isNull();
  }

  @Test
  void cleanupOnExceptionRemovesMdc() {
    MDC.put(CorrelationIdInterceptor.MDC_KEY, "to-be-cleared");
    interceptor.cleanupOnException(null);
    assertThat(MDC.get(CorrelationIdInterceptor.MDC_KEY)).isNull();
  }

  // -- outbound subscription delivery ------------------------------------

  @Test
  void beforeRestHookDeliveryAddsCorrelationHeader() {
    MDC.put(CorrelationIdInterceptor.MDC_KEY, "outbound-corr-1");

    CanonicalSubscription sub = new CanonicalSubscription();
    // Subscription pre-populated with a couple of existing headers so we
    // verify we're additive, not destructive. CanonicalSubscription
    // exposes `addHeader(String)` for the "Name: value" form.
    sub.addHeader("Authorization: Bearer xyz");
    sub.addHeader("Content-Type: application/fhir+json");

    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    boolean keepGoing = interceptor.beforeRestHookDelivery(sub, msg);
    assertThat(keepGoing).isTrue();
    assertThat(sub.getHeaders())
        .contains("Authorization: Bearer xyz")
        .contains("Content-Type: application/fhir+json")
        .contains("X-Correlation-Id: outbound-corr-1");
  }

  @Test
  void beforeRestHookDeliveryIsNoOpWhenMdcEmpty() {
    // No MDC value — typical for an async retry of a queued notification.
    // We should NOT fabricate one: better to omit and let the receiver
    // mint its own than imply a fresh request.
    CanonicalSubscription sub = new CanonicalSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();

    interceptor.beforeRestHookDelivery(sub, msg);
    assertThat(sub.getHeaders()).isEmpty();
  }

  @Test
  void beforeRestHookDeliveryIsIdempotent() {
    MDC.put(CorrelationIdInterceptor.MDC_KEY, "outbound-corr-2");

    CanonicalSubscription sub = new CanonicalSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();

    interceptor.beforeRestHookDelivery(sub, msg);
    interceptor.beforeRestHookDelivery(sub, msg);
    long count = sub.getHeaders().stream()
        .filter(h -> h.equals("X-Correlation-Id: outbound-corr-2"))
        .count();
    assertThat(count).isEqualTo(1);
  }

  // -- sanitize policy ---------------------------------------------------

  @Test
  void sanitizeOrGenerateAcceptsValidId() {
    assertThat(CorrelationIdInterceptor.sanitizeOrGenerate("abc-DEF_012.345"))
        .isEqualTo("abc-DEF_012.345");
  }

  @Test
  void sanitizeOrGenerateRejectsControlChars() {
    String result = CorrelationIdInterceptor.sanitizeOrGenerate("abc\n");
    assertThat(result).matches("[0-9a-fA-F-]{36}");
  }

  @Test
  void sanitizeOrGenerateRejectsOverlong() {
    String tooLong = "a".repeat(CorrelationIdInterceptor.MAX_HEADER_LENGTH + 1);
    String result = CorrelationIdInterceptor.sanitizeOrGenerate(tooLong);
    assertThat(result.length()).isLessThan(CorrelationIdInterceptor.MAX_HEADER_LENGTH);
  }
}
