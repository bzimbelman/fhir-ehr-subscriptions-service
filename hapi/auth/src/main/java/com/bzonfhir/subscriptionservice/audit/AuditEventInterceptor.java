package com.bzonfhir.subscriptionservice.audit;

import java.time.Instant;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

import org.hl7.fhir.instance.model.api.IBaseResource;
import org.hl7.fhir.instance.model.api.IIdType;
import org.hl7.fhir.r4.model.AuditEvent;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.rest.api.RestOperationTypeEnum;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.api.server.ResponseDetails;
import ca.uhn.fhir.rest.server.exceptions.BaseServerResponseException;
import com.bzonfhir.subscriptionservice.auth.OidcJwtAuthenticationInterceptor;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * HAPI interceptor that emits a FHIR {@code AuditEvent} resource for every
 * interesting {@code /fhir/*} REST operation (ticket #391, Epic #387).
 *
 * <h2>Refactored under ticket #432 (Epic #425)</h2>
 *
 * <p>The AuditEvent <strong>shape</strong> (type, subtype, action, agent,
 * entity, source, period, outcome) is now built by {@link AuditEventChain},
 * which mirrors the SPI-shaped Kotlin enricher in
 * {@code plugins-builtin/audit-event-fhir/}. This class is responsible for
 * the things only a HAPI interceptor can do:
 *
 * <ul>
 *   <li>Subscribe to HAPI pointcuts ({@code SERVER_INCOMING_REQUEST_POST_PROCESSED},
 *       {@code SERVER_OUTGOING_RESPONSE}, {@code SERVER_HANDLE_EXCEPTION}).
 *   <li>Stash a request-start timestamp on {@code RequestDetails.userData}.
 *   <li>Apply the skip-list ({@code /metadata}, {@code /AuditEvent},
 *       {@code /actuator}, {@code /admin}).
 *   <li>Gate reads/searches behind the capture-reads / capture-search
 *       properties.
 *   <li>Translate HAPI's {@link RequestDetails} + {@link ResponseDetails} +
 *       exception into a normalised {@link AuditContext}.
 *   <li>Persist the result via {@link AuditEventPersister}.
 * </ul>
 *
 * <p>Two implementations of the AuditEvent-building logic exist today —
 * this Java {@link AuditEventChain} and the canonical Kotlin
 * {@code AuditEventFhirEnricher}. They are behaviourally identical; both
 * are covered by tests. When {@code hapi/auth/} migrates from Maven to
 * Gradle (follow-up to ticket #432), the Java mirror is deleted and this
 * interceptor consumes the Kotlin enricher directly.
 *
 * <h2>Skip list</h2>
 *
 * <p>Some paths would dominate the audit log without telling us anything:
 * <ul>
 *   <li>{@code /metadata} — public capability statement; not PHI.
 *   <li>{@code /AuditEvent*} — viewing the audit log isn't itself audited
 *       (otherwise a {@code GET /fhir/AuditEvent} hot loop creates
 *       infinite recursion).
 *   <li>{@code /actuator/*} — Spring Boot endpoints; not the FHIR API.
 *   <li>{@code /admin/*} — interface-engine admin endpoints.
 * </ul>
 *
 * <h2>Persistence safety</h2>
 *
 * <p>If the audit-write itself fails (the AuditEvent DAO throws, the
 * database is down, ...) we LOG and SWALLOW. The audit trail records
 * what already happened on the FHIR side; failing the caller's request
 * because we couldn't write the audit row would be worse than missing one.
 */
@Interceptor
public class AuditEventInterceptor {

  /** {@code RequestDetails.userData} key for the request-start timestamp. */
  static final String USER_DATA_START_KEY = "subscription-service.audit.start";

  /** Fallback agent identifier when the request was unauthenticated. */
  static final String ANONYMOUS_AGENT = AuditEventChain.ANONYMOUS_AGENT;

  /** Skip-list prefixes (lower-cased, no leading slash). */
  private static final List<String> SKIP_PREFIXES =
      List.of("metadata", "auditevent", "actuator", "admin");

  private static final Logger log = LoggerFactory.getLogger(AuditEventInterceptor.class);

  private final AuditProperties props;
  private final AuditEventPersister persister;

  public AuditEventInterceptor(AuditProperties props, AuditEventPersister persister) {
    this.props = props;
    this.persister = persister;
  }

  // -- hooks -------------------------------------------------------------

  /**
   * Stamps a {@code System.currentTimeMillis()} into
   * {@code RequestDetails.userData}. Fires before
   * {@link OidcJwtAuthenticationInterceptor} so a request rejected at the
   * auth layer still has a start time to put into the AuditEvent
   * {@code period}.
   */
  @Hook(Pointcut.SERVER_INCOMING_REQUEST_POST_PROCESSED)
  public boolean stampStart(RequestDetails requestDetails) {
    if (!props.isEnabled()) {
      return true;
    }
    requestDetails.getUserData().put(USER_DATA_START_KEY, System.currentTimeMillis());
    return true;
  }

  /**
   * Successful operation — write an AuditEvent.
   * {@link Pointcut#SERVER_OUTGOING_RESPONSE} fires after the resource
   * provider returned but before the response is committed, so
   * {@code requestDetails}, {@code responseDetails}, and (when the
   * operation returned a resource) the resource itself are all populated.
   */
  @Hook(Pointcut.SERVER_OUTGOING_RESPONSE)
  public boolean afterOutgoingResponse(
      RequestDetails requestDetails, ResponseDetails responseDetails) {
    if (!props.isEnabled()) {
      return true;
    }
    if (requestDetails == null) {
      return true;
    }
    if (shouldSkip(requestDetails)) {
      return true;
    }
    RestOperationTypeEnum op = requestDetails.getRestOperationType();
    if (!shouldAudit(op)) {
      return true;
    }
    try {
      AuditEvent event =
          buildEvent(requestDetails, responseDetails, /*throwable=*/ null);
      persister.persist(event);
    } catch (RuntimeException e) {
      // Defense-in-depth: builder bug must not break the response.
      log.warn("AuditEvent build failed: {}", e.toString());
    }
    return true;
  }

  /**
   * Failed operation — write an AuditEvent with the outcome derived from
   * the exception's HTTP status code.
   */
  @Hook(Pointcut.SERVER_HANDLE_EXCEPTION)
  public boolean afterException(
      RequestDetails requestDetails, BaseServerResponseException exception) {
    if (!props.isEnabled()) {
      return true;
    }
    if (requestDetails == null) {
      return true;
    }
    if (shouldSkip(requestDetails)) {
      return true;
    }
    // For failures we audit ALL paths — including reads/searches — because
    // a failed read is itself a security-relevant signal (someone tried
    // to access something they shouldn't). captureReads/captureSearch gate
    // only the SUCCESS case.
    try {
      AuditEvent event = buildEvent(requestDetails, /*responseDetails=*/ null, exception);
      persister.persist(event);
    } catch (RuntimeException e) {
      log.warn("AuditEvent build failed during exception path: {}", e.toString());
    }
    return true;
  }

  // -- skip / gate -------------------------------------------------------

  /**
   * Is this request on the skip-list? Match against the request path
   * normalised to lower case, no leading slash. We check prefixes —
   * {@code AuditEvent}, {@code AuditEvent/123}, {@code AuditEvent?patient=...}
   * all match.
   */
  static boolean shouldSkip(RequestDetails requestDetails) {
    String path = normalize(requestDetails.getRequestPath());
    for (String prefix : SKIP_PREFIXES) {
      if (path.equals(prefix) || path.startsWith(prefix + "/") || path.startsWith(prefix + "?")) {
        return true;
      }
    }
    return false;
  }

  /**
   * Should this operation type be audited on a SUCCESS path? Writes always;
   * reads and searches gated by the props. Returns {@code false} for
   * operations we don't have an opinion on (e.g. {@code METADATA}, which
   * is also on the skip-list).
   */
  private boolean shouldAudit(RestOperationTypeEnum op) {
    if (op == null) {
      return false;
    }
    switch (op) {
      case CREATE:
      case UPDATE:
      case DELETE:
      case PATCH:
      case TRANSACTION:
      case BATCH:
        return true;
      case READ:
      case VREAD:
      case HISTORY_INSTANCE:
      case HISTORY_TYPE:
      case HISTORY_SYSTEM:
        return props.isCaptureReads();
      case SEARCH_SYSTEM:
      case SEARCH_TYPE:
      case GET_PAGE:
        return props.isCaptureSearch();
      default:
        // EXTENDED_OPERATION_*, VALIDATE, etc. — capture under "writes"
        // because they can mutate or affect server state. Cheap and the
        // safer default.
        return true;
    }
  }

  // -- AuditEvent builder ------------------------------------------------

  /**
   * Build an AuditEvent from a request + (optional) response + (optional)
   * exception. Pure-ish function — the only side effect is reading the
   * wall clock for {@code recorded} / {@code period.end}, mirroring what
   * the SPI-shaped enricher does. Unit tests call this directly with
   * mocked request/response.
   */
  AuditEvent buildEvent(
      RequestDetails requestDetails,
      ResponseDetails responseDetails,
      BaseServerResponseException throwable) {
    AuditContext ctx = toAuditContext(requestDetails, responseDetails, throwable);
    return AuditEventChain.applyBaseShape(new AuditEvent(), ctx);
  }

  /**
   * Adapt HAPI's request/response shape into the SPI-shaped
   * {@link AuditContext}. Centralised here so unit tests of
   * {@link AuditEventChain} (in the plugin module) can use a
   * hand-constructed AuditContext, while production goes through this
   * translation step.
   */
  AuditContext toAuditContext(
      RequestDetails requestDetails,
      ResponseDetails responseDetails,
      BaseServerResponseException throwable) {
    RestOperationTypeEnum op = requestDetails.getRestOperationType();

    // Resolve the resource id we'll write into entity.what:
    //   - Prefer the response resource's id (post-create has the new id).
    //   - Fall back to the request id (URL-encoded for reads/updates/deletes).
    IIdType idType = null;
    if (responseDetails != null) {
      IBaseResource res = responseDetails.getResponseResource();
      if (res != null && res.getIdElement() != null && !res.getIdElement().isEmpty()) {
        idType = res.getIdElement();
      }
    }
    if (idType == null) {
      idType = requestDetails.getId();
    }
    String resourceType = requestDetails.getResourceName();
    String resourceId = null;
    if (idType != null && !idType.isEmpty()) {
      // Full reference goes through entity-builder as ResourceType/id.
      // We split out the parts here so the AuditContext stays SPI-shaped.
      resourceId = idType.getIdPart();
      if (resourceType == null && idType.getResourceType() != null) {
        resourceType = idType.getResourceType();
      }
    }

    Map<String, String> attrs = new HashMap<>();
    if (op != null) {
      attrs.put("operation", op.name());
    }
    String fhirBase = requestDetails.getFhirServerBase();
    if (fhirBase != null && !fhirBase.isBlank()) {
      attrs.put("fhirServerBase", fhirBase);
    }
    if (responseDetails != null) {
      attrs.put("responseStatus", Integer.toString(responseDetails.getResponseCode()));
    }
    if (throwable != null) {
      attrs.put("exception.status", Integer.toString(throwable.getStatusCode()));
    }

    // JWT-derived identity.
    JWTClaimsSet claims = extractClaims(requestDetails);
    String principalName = null;
    if (claims != null) {
      String sub = claims.getSubject();
      if (sub != null && !sub.isBlank()) {
        principalName = sub;
      }
      Object preferredUsername = claims.getClaim("preferred_username");
      if (preferredUsername instanceof String pu && !pu.isBlank()) {
        attrs.put("preferredUsername", pu);
      }
      Object azp = claims.getClaim("azp");
      if (azp instanceof String s && !s.isBlank()) {
        attrs.put("azp", s);
      } else {
        Object clientId = claims.getClaim("client_id");
        if (clientId instanceof String cs && !cs.isBlank()) {
          attrs.put("azp", cs);
        }
      }
    }

    // Request start (stashed in pre-process) -> occurredAt.
    Object startStashed = requestDetails.getUserData().get(USER_DATA_START_KEY);
    long startMillis =
        (startStashed instanceof Long l) ? l : System.currentTimeMillis();

    String requestMethod = "";
    if (requestDetails.getRequestType() != null) {
      requestMethod = requestDetails.getRequestType().name();
    }

    return new AuditContext(
        Instant.ofEpochMilli(startMillis),
        /*correlationId=*/ "",
        /*tenantId=*/ null,
        principalName,
        requestDetails.getRequestPath() == null ? "" : requestDetails.getRequestPath(),
        requestMethod,
        resourceType,
        resourceId,
        /*sourceIp=*/ null,
        attrs);
  }

  /**
   * Static helper retained as the public surface for tests
   * ({@link AuditEventInterceptorTest#actionFor_mapsRestOperationCorrectly()}).
   * Delegates to the SPI-shaped {@link AuditEventChain#actionFor(String)}.
   */
  static AuditEventAction actionFor(RestOperationTypeEnum op) {
    if (op == null) {
      return AuditEventAction.E;
    }
    return AuditEventChain.actionFor(op.name());
  }

  private static JWTClaimsSet extractClaims(RequestDetails requestDetails) {
    Object stashed =
        requestDetails.getUserData().get(OidcJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY);
    if (stashed instanceof JWTClaimsSet c) {
      return c;
    }
    return null;
  }

  private static String normalize(String path) {
    if (path == null) {
      return "";
    }
    String p = path.trim().toLowerCase(Locale.ROOT);
    while (p.startsWith("/")) {
      p = p.substring(1);
    }
    while (p.endsWith("/") && p.length() > 1) {
      p = p.substring(0, p.length() - 1);
    }
    return p;
  }
}
