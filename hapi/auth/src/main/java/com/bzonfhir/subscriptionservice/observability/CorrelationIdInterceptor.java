package com.bzonfhir.subscriptionservice.observability;

import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.slf4j.MDC;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * HAPI interceptor that propagates a {@code correlation_id} through every FHIR request
 * (Epic #387, ticket #388).
 *
 * <p>On every inbound request:
 *
 * <ol>
 *   <li>Read {@value #HEADER} from the request headers.
 *   <li>If missing or malformed, generate a fresh UUID v4.
 *   <li>Put the value into the SLF4J MDC under {@value #MDC_KEY} so every log line
 *       emitted while this request is being processed carries it as a top-level field in
 *       the JSON log layout.
 *   <li>Echo the value back on the response header {@value #HEADER} so the caller can
 *       correlate its side of the conversation with server-side logs.
 * </ol>
 *
 * <p>On request completion (success OR failure) the MDC value is cleared. Without this,
 * a Tomcat worker thread that finished handling one request and starts a new one would
 * still log the previous request's id from its ThreadLocal MDC. The {@link
 * Pointcut#SERVER_PROCESSING_COMPLETED_NORMALLY} and {@link
 * Pointcut#SERVER_HANDLE_EXCEPTION} hooks together cover both terminal states.
 *
 * <p>Why this lives in the hapi-auth JAR: the same JAR already layers Spring Boot
 * autoconfig + interceptors onto the upstream HAPI image (ticket #359). Adding one more
 * interceptor here keeps the deploy story unchanged — one JAR copied into
 * {@code /app/extra-classes/} — and the JSON log encoder configured by
 * {@code logback-spring-subsvc.xml} sees the MDC value because both pieces live in the
 * same JVM.
 *
 * <p>Header name {@value #HEADER} matches the interface-engine's filter so a request that
 * the engine made into HAPI lands here with the engine's MDC value, joining the two
 * services' logs by one shared id.
 */
@Interceptor
public class CorrelationIdInterceptor {

  /** HTTP header carrying the correlation id between services. */
  public static final String HEADER = "X-Correlation-Id";

  /** SLF4J MDC key — matches the JSON field name in the log layout. */
  public static final String MDC_KEY = "correlation_id";

  /**
   * Hard ceiling on a correlation id length. UUID v4 is 36 chars; a 96-char cap gives
   * callers headroom for prefixed ids (e.g. {@code epic-2026-06-26-<uuid>}). Anything
   * longer is suspicious and discarded in favour of a freshly generated id.
   */
  static final int MAX_HEADER_LENGTH = 96;

  /**
   * RequestDetails userData key used as a fallback to find the correlation id during
   * cleanup. RequestDetails.getRequestId() exists but is not stable across HAPI versions;
   * stashing our own copy keeps the implementation independent of HAPI's request-id
   * helper.
   */
  static final String USER_DATA_KEY = "subscription-service.observability.correlation_id";

  private static final Logger log = LoggerFactory.getLogger(CorrelationIdInterceptor.class);

  /**
   * Pre-processed pointcut — fires earlier than {@link Pointcut#SERVER_INCOMING_REQUEST_POST_PROCESSED}
   * (used by {@link com.bzonfhir.subscriptionservice.auth.OidcJwtAuthenticationInterceptor})
   * so the MDC value is set BEFORE the auth interceptor logs its own audit lines. Order
   * matters: any log line emitted between these two pointcuts (and there ARE some inside
   * HAPI's request pipeline) is silently un-correlated otherwise.
   *
   * <p>HAPI passes the raw servlet request/response into this hook, which is exactly what
   * we need to:
   *
   * <ul>
   *   <li>read the inbound header without going through RequestDetails (which doesn't
   *       expose headers at this pointcut on all HAPI versions), and
   *   <li>add the echo-back response header before the response is committed.
   * </ul>
   */
  @Hook(Pointcut.SERVER_INCOMING_REQUEST_PRE_PROCESSED)
  public boolean preProcess(HttpServletRequest request, HttpServletResponse response) {
    String inbound = request.getHeader(HEADER);
    String correlationId = sanitizeOrGenerate(inbound);

    // Echo the id back BEFORE the response is committed. Tomcat commits the response
    // when the first byte is written; we're well before that here.
    response.setHeader(HEADER, correlationId);

    MDC.put(MDC_KEY, correlationId);
    request.setAttribute(USER_DATA_KEY, correlationId);

    if (log.isDebugEnabled()) {
      log.debug(
          "request entered method={} uri={} correlationIdSource={}",
          request.getMethod(),
          request.getRequestURI(),
          (inbound == null || inbound.isBlank()) ? "generated" : "inbound");
    }
    return true; // continue processing
  }

  /**
   * Clear MDC on successful completion. {@link
   * Pointcut#SERVER_PROCESSING_COMPLETED_NORMALLY} fires from HAPI's RestfulServer's
   * finally-style cleanup after a successful response has been written.
   */
  @Hook(Pointcut.SERVER_PROCESSING_COMPLETED_NORMALLY)
  public void cleanupOnSuccess(RequestDetails requestDetails) {
    MDC.remove(MDC_KEY);
  }

  /**
   * Clear MDC when the request raised an exception. Without this hook the MDC value
   * would leak onto whatever request the same Tomcat worker thread handles next.
   * Returning {@code null} keeps HAPI's default exception handling in place — this hook
   * is for cleanup only, not for changing the response.
   */
  @Hook(Pointcut.SERVER_HANDLE_EXCEPTION)
  public boolean cleanupOnException(RequestDetails requestDetails) {
    MDC.remove(MDC_KEY);
    return true;
  }

  /**
   * Subscription REST-hook delivery — outbound side. When HAPI fires a Subscription
   * notification triggered by a resource change, we want the outbound HTTP request to
   * carry the SAME {@value #HEADER} that was set on the request which caused the change.
   *
   * <p>Today: when the interface-engine POSTs a Bundle to HAPI to write resources, the
   * inbound request's correlation id is currently held on the MDC (set by this
   * interceptor's pre-process hook). The subscription matcher fires synchronously on the
   * commit path, so the MDC value at this hook's invocation IS the same id that caused
   * the change. We pull it from MDC and stamp it on the {@link CanonicalSubscription}'s
   * {@code headers} list so HAPI's REST-hook deliverer (which reads that list when
   * building the outbound request) sends it on the wire.
   *
   * <p>Edge case: an empty MDC (e.g. an async resubmission of a queued notification) — in
   * that case we leave the headers untouched. Generating a NEW correlation id here would
   * misleadingly imply a fresh request; better to omit the header and let the receiver
   * fall back to its own id.
   *
   * <p>Pointcut name on HAPI 7.6.0 is {@code SUBSCRIPTION_BEFORE_REST_HOOK_DELIVERY}; see
   * {@link Pointcut} javadoc.
   */
  @Hook(Pointcut.SUBSCRIPTION_BEFORE_REST_HOOK_DELIVERY)
  public boolean beforeRestHookDelivery(
      CanonicalSubscription subscription, ResourceDeliveryMessage deliveryMessage) {
    String correlationId = MDC.get(MDC_KEY);
    if (correlationId == null || correlationId.isBlank()) {
      // No id on the MDC — likely an async retry. Don't fabricate one;
      // the receiver should mint its own.
      return true;
    }
    // CanonicalSubscription.getHeaders() returns a mutable list of "Name: value"
    // strings (HAPI's encoding); appending our header here is enough — HAPI's
    // REST-hook deliverer parses the list when building the outbound HttpClient
    // request. We use the canonical "Name: value" form to match the rest of
    // the list's contents.
    String headerLine = HEADER + ": " + correlationId;
    if (!subscription.getHeaders().contains(headerLine)) {
      subscription.addHeader(headerLine);
    }
    return true;
  }

  /**
   * Sanitize an inbound header value. Tight rules: ASCII letters/digits/dash/underscore/dot,
   * with a length cap. Anything else gets a fresh UUID. The cap defends against a
   * malicious sender attempting to blow up log records with a multi-megabyte value.
   *
   * <p>Package-private so {@link CorrelationIdInterceptorTest} can exercise the edge
   * cases directly.
   */
  static String sanitizeOrGenerate(String headerValue) {
    if (headerValue == null || headerValue.isBlank()) {
      return UUID.randomUUID().toString();
    }
    if (headerValue.length() > MAX_HEADER_LENGTH) {
      return UUID.randomUUID().toString();
    }
    for (int i = 0; i < headerValue.length(); i++) {
      if (!isAcceptable(headerValue.charAt(i))) {
        return UUID.randomUUID().toString();
      }
    }
    return headerValue;
  }

  private static boolean isAcceptable(char c) {
    return Character.isLetterOrDigit(c) || c == '-' || c == '_' || c == '.';
  }
}
