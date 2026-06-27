package com.bzonfhir.subscriptionservice.audit;

import java.util.Date;
import java.util.List;
import java.util.Locale;

import org.hl7.fhir.instance.model.api.IBaseResource;
import org.hl7.fhir.instance.model.api.IIdType;
import org.hl7.fhir.r4.model.AuditEvent;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAgentComponent;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventEntityComponent;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome;
import org.hl7.fhir.r4.model.AuditEvent.AuditEventSourceComponent;
import org.hl7.fhir.r4.model.CodeableConcept;
import org.hl7.fhir.r4.model.Coding;
import org.hl7.fhir.r4.model.DateTimeType;
import org.hl7.fhir.r4.model.Period;
import org.hl7.fhir.r4.model.Reference;
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
 * HAPI interceptor that emits a FHIR {@code AuditEvent} resource for every interesting
 * {@code /fhir/*} REST operation (ticket #391, Epic #387).
 *
 * <p>Why this exists: compliance frameworks (HIPAA, SOC 2, ONC) require an audit trail
 * of who accessed/modified what. Today the HAPI image emits operational lines into
 * stdout JSON logs (epic #387 / ticket #388 set that up), but those are unstructured
 * for compliance review and disappear on container restart. By writing
 * {@code AuditEvent} resources INTO HAPI itself, we get a standards-compliant trail
 * that's queryable via {@code GET /fhir/AuditEvent} and retained alongside the data
 * it audits.
 *
 * <h2>Hook layout</h2>
 *
 * <ul>
 *   <li>{@link Pointcut#SERVER_INCOMING_REQUEST_PRE_PROCESSED} — stash a request-start
 *       timestamp on {@code RequestDetails.userData} so the AuditEvent's
 *       {@code period} carries both start and end. Cheap; one allocation per request.
 *   <li>{@link Pointcut#SERVER_OUTGOING_RESPONSE} — the operation succeeded; build the
 *       AuditEvent from the request/response context and hand it to the
 *       {@link AuditEventPersister}.
 *   <li>{@link Pointcut#SERVER_HANDLE_EXCEPTION} — the operation FAILED; build an
 *       AuditEvent with {@code outcome=4} (minor failure, e.g. 401/403) or
 *       {@code outcome=8} (serious failure, 5xx) depending on the exception's
 *       status code.
 * </ul>
 *
 * <h2>Skip list</h2>
 *
 * <p>Some paths would dominate the audit log without telling us anything:
 * <ul>
 *   <li>{@code /metadata} — public capability statement; not PHI.
 *   <li>{@code /AuditEvent*} — viewing the audit log isn't itself audited (otherwise
 *       a single {@code GET /fhir/AuditEvent} on a hot loop creates infinite recursion).
 *   <li>{@code /actuator/*} — Spring Boot endpoints; not the FHIR API.
 *   <li>{@code /admin/*} — interface-engine admin endpoints; not handled by HAPI but
 *       belt-and-braces.
 * </ul>
 *
 * <h2>Persistence safety</h2>
 *
 * <p>If the audit-write itself fails (the AuditEvent DAO throws, the database is
 * down, ...) we LOG and SWALLOW. The audit trail records what already happened on
 * the FHIR side; failing the caller's request because we couldn't write the audit
 * row would be worse than missing one row. {@link DaoRegistryAuditEventPersister}
 * handles the catch-and-log internally so this interceptor stays focused on what
 * to record.
 */
@Interceptor
public class AuditEventInterceptor {

  /** {@code DCM} (DICOM) coding system, the FHIR-standard system for the "rest" type code. */
  static final String SYSTEM_DICOM = "http://dicom.nema.org/resources/ontology/dcm";

  /** The DICOM coding for "Restful Operation". */
  static final String CODE_REST = "rest";

  /** {@code RequestDetails.userData} key under which we stash the request-start timestamp. */
  static final String USER_DATA_START_KEY = "subscription-service.audit.start";

  /** Fallback agent identifier when the request was unauthenticated. */
  static final String ANONYMOUS_AGENT = "anonymous";

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
   * Stamps a {@code System.currentTimeMillis()} into {@code RequestDetails.userData}.
   * Fires before {@link OidcJwtAuthenticationInterceptor} so a request rejected at the
   * auth layer still has a start time to put into the AuditEvent {@code period}.
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
   * Successful operation — write an AuditEvent. {@link Pointcut#SERVER_OUTGOING_RESPONSE}
   * fires after the resource provider returned but before the response is committed
   * to the wire, so {@code requestDetails}, {@code responseDetails}, and (when the
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
   * Failed operation — write an AuditEvent with the outcome derived from the
   * exception's HTTP status code.
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
    // For failures we audit ALL paths — including reads/searches — because a failed
    // read is itself a security-relevant signal (someone tried to access something
    // they shouldn't). captureReads/captureSearch gate only the SUCCESS case.
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
   * Is this request on the skip-list? Match against the request path normalized
   * to lower case, no leading slash. We check prefixes — {@code AuditEvent},
   * {@code AuditEvent/123}, {@code AuditEvent?patient=...} all match.
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
   * Should this operation type be audited on a SUCCESS path? Writes always; reads
   * and searches gated by the props. Returns {@code false} for operations we don't
   * have an opinion on (e.g. {@code METADATA}, which is also on the skip-list above).
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
        // EXTENDED_OPERATION_*, VALIDATE, etc. — capture under "writes" because they
        // can mutate or affect server state. Cheap and the safer default.
        return true;
    }
  }

  // -- AuditEvent builder ------------------------------------------------

  /**
   * Build an AuditEvent from a request + (optional) response + (optional) exception.
   * Pure function — no side effects, no persistence — so the unit tests can call it
   * directly with mocked request/response.
   */
  AuditEvent buildEvent(
      RequestDetails requestDetails,
      ResponseDetails responseDetails,
      BaseServerResponseException throwable) {
    AuditEvent event = new AuditEvent();

    // type = DICOM rest
    event.setType(new Coding(SYSTEM_DICOM, CODE_REST, "Restful Operation"));

    // subtype = HTTP method + resource type code (mapped from the REST operation)
    RestOperationTypeEnum op = requestDetails.getRestOperationType();
    event.addSubtype(subtypeFor(op));

    // action = C/R/U/D/E
    event.setAction(actionFor(op));

    // recorded = now
    event.setRecorded(new Date());

    // outcome = derived from response code or exception
    event.setOutcome(outcomeFor(responseDetails, throwable));

    // agent: the authenticated user (if any) + the client application (if any).
    addAgents(event, requestDetails);

    // source: server hostname + the FHIR base URL.
    event.setSource(buildSource(requestDetails));

    // entity: the resource(s) affected (best-effort — read from response or request).
    addEntities(event, requestDetails, responseDetails);

    // period: request start (stashed in pre-process) -> now.
    event.setPeriod(buildPeriod(requestDetails));

    return event;
  }

  private static Coding subtypeFor(RestOperationTypeEnum op) {
    if (op == null) {
      return new Coding("http://hl7.org/fhir/restful-interaction", "unknown", "unknown");
    }
    // Map HAPI's RestOperationTypeEnum to the FHIR restful-interaction code system.
    String code;
    switch (op) {
      case CREATE:
        code = "create";
        break;
      case READ:
      case VREAD:
        code = "read";
        break;
      case UPDATE:
        code = "update";
        break;
      case PATCH:
        code = "patch";
        break;
      case DELETE:
        code = "delete";
        break;
      case SEARCH_SYSTEM:
      case SEARCH_TYPE:
        code = "search";
        break;
      case HISTORY_INSTANCE:
      case HISTORY_TYPE:
      case HISTORY_SYSTEM:
        code = "history";
        break;
      case TRANSACTION:
        code = "transaction";
        break;
      case BATCH:
        code = "batch";
        break;
      default:
        code = op.getCode() == null ? op.name().toLowerCase(Locale.ROOT) : op.getCode();
    }
    return new Coding("http://hl7.org/fhir/restful-interaction", code, code);
  }

  static AuditEventAction actionFor(RestOperationTypeEnum op) {
    if (op == null) {
      return AuditEventAction.E;
    }
    switch (op) {
      case CREATE:
        return AuditEventAction.C;
      case READ:
      case VREAD:
      case SEARCH_SYSTEM:
      case SEARCH_TYPE:
      case HISTORY_INSTANCE:
      case HISTORY_TYPE:
      case HISTORY_SYSTEM:
      case GET_PAGE:
        return AuditEventAction.R;
      case UPDATE:
      case PATCH:
        return AuditEventAction.U;
      case DELETE:
        return AuditEventAction.D;
      default:
        // Extended ops, batch, transaction — "E" (Execute) is the DICOM/IHE
        // convention for "something happened that doesn't map to CRUD."
        return AuditEventAction.E;
    }
  }

  private static AuditEventOutcome outcomeFor(
      ResponseDetails responseDetails, BaseServerResponseException throwable) {
    if (throwable != null) {
      int sc = throwable.getStatusCode();
      if (sc >= 500) {
        return AuditEventOutcome._8; // serious failure
      }
      if (sc == 401) {
        // Unauthenticated — there's no identified caller, which is what serious-failure
        // means in the IHE audit profile. Spec says outcome=8 for "the action was not
        // permitted and the event was not recorded as expected."
        return AuditEventOutcome._8;
      }
      // 4xx — caller was authenticated but couldn't perform the operation. Minor.
      return AuditEventOutcome._4;
    }
    if (responseDetails == null) {
      return AuditEventOutcome._0;
    }
    int sc = responseDetails.getResponseCode();
    if (sc >= 500) {
      return AuditEventOutcome._8;
    }
    if (sc >= 400) {
      return AuditEventOutcome._4;
    }
    return AuditEventOutcome._0;
  }

  /**
   * Populate the {@code agent} array. We add ONE agent for the authenticated user
   * (when JWT claims are present on the request) and ONE for the client application
   * (the OAuth client / {@code azp} claim). When the request is unauthenticated, a
   * single placeholder "anonymous" agent is added — this matches the IHE profile's
   * requirement that AuditEvent always has at least one agent.
   */
  private void addAgents(AuditEvent event, RequestDetails requestDetails) {
    JWTClaimsSet claims = extractClaims(requestDetails);
    if (claims == null) {
      // Unauthenticated. Add an anonymous agent so the resource is still valid
      // per the AuditEvent profile (agent: 1..*).
      AuditEventAgentComponent agent = new AuditEventAgentComponent();
      agent.setRequestor(true);
      agent.setAltId(ANONYMOUS_AGENT);
      agent.setName(ANONYMOUS_AGENT);
      event.addAgent(agent);
      return;
    }

    // User agent (`sub` claim).
    String sub = claims.getSubject();
    if (sub != null && !sub.isBlank()) {
      AuditEventAgentComponent userAgent = new AuditEventAgentComponent();
      userAgent.setRequestor(true);
      userAgent.setAltId(sub);
      Object preferredUsername = claims.getClaim("preferred_username");
      if (preferredUsername instanceof String pu && !pu.isBlank()) {
        userAgent.setName(pu);
      } else {
        userAgent.setName(sub);
      }
      // type: "person" (or "user") — DICOM coding.
      userAgent.setType(
          new CodeableConcept()
              .addCoding(
                  new Coding(
                      "http://terminology.hl7.org/CodeSystem/v3-ParticipationType",
                      "AUT",
                      "author")));
      event.addAgent(userAgent);
    }

    // Client-application agent (`azp` / `client_id`).
    Object azp = claims.getClaim("azp");
    if (!(azp instanceof String s) || s.isBlank()) {
      Object clientId = claims.getClaim("client_id");
      if (clientId instanceof String cs && !cs.isBlank()) {
        azp = cs;
      } else {
        azp = null;
      }
    }
    if (azp instanceof String clientStr && !clientStr.isBlank()) {
      AuditEventAgentComponent appAgent = new AuditEventAgentComponent();
      appAgent.setRequestor(false);
      appAgent.setAltId(clientStr);
      appAgent.setName(clientStr);
      appAgent.setType(
          new CodeableConcept()
              .addCoding(
                  new Coding(
                      "http://terminology.hl7.org/CodeSystem/extra-security-role-type",
                      "dataprocessor",
                      "data processor")));
      event.addAgent(appAgent);
    }

    // If neither user nor client could be derived (truly empty token), still emit
    // a single anonymous agent so the resource is valid.
    if (event.getAgent().isEmpty()) {
      AuditEventAgentComponent agent = new AuditEventAgentComponent();
      agent.setRequestor(true);
      agent.setAltId(ANONYMOUS_AGENT);
      agent.setName(ANONYMOUS_AGENT);
      event.addAgent(agent);
    }
  }

  private static AuditEventSourceComponent buildSource(RequestDetails requestDetails) {
    AuditEventSourceComponent source = new AuditEventSourceComponent();
    String base = requestDetails.getFhirServerBase();
    if (base != null && !base.isBlank()) {
      source.setSite(base);
    } else {
      source.setSite("unknown");
    }
    // observer = a Reference to "this server" — we use a Device-shaped Reference
    // pointing to the FHIR base URL. The receiver knows the server identity from
    // the source.site anyway, so this is informational.
    source.setObserver(new Reference().setDisplay(hostnameOrUnknown()));
    // type: "Web Server"
    source.addType(
        new Coding(
            "http://terminology.hl7.org/CodeSystem/security-source-type", "3", "Web Server"));
    return source;
  }

  private static String hostnameOrUnknown() {
    try {
      String h = java.net.InetAddress.getLocalHost().getHostName();
      return (h == null || h.isBlank()) ? "unknown" : h;
    } catch (Exception e) {
      return "unknown";
    }
  }

  private static void addEntities(
      AuditEvent event, RequestDetails requestDetails, ResponseDetails responseDetails) {
    // Best-effort: prefer the response resource's id (post-create has the new id);
    // fall back to the request id (URL-encoded for reads/updates/deletes).
    IIdType id = null;
    if (responseDetails != null) {
      IBaseResource res = responseDetails.getResponseResource();
      if (res != null && res.getIdElement() != null && !res.getIdElement().isEmpty()) {
        id = res.getIdElement();
      }
    }
    if (id == null) {
      id = requestDetails.getId();
    }
    String resourceName = requestDetails.getResourceName();
    if (id == null && (resourceName == null || resourceName.isBlank())) {
      // Type-level or system-level operation with no concrete id. Skip the entity
      // entry — AuditEvent.entity is 0..*.
      return;
    }
    AuditEventEntityComponent entity = new AuditEventEntityComponent();
    if (id != null && !id.isEmpty()) {
      Reference what = new Reference();
      // IIdType.getValue() is the full "ResourceType/id" form; HAPI is happy to
      // accept that as a Reference target.
      what.setReference(id.getValue());
      entity.setWhat(what);
    } else if (resourceName != null) {
      // Type-level operation: record the type as a display-only entity so the audit
      // row at least says WHICH resource type was hit.
      entity.setWhat(new Reference().setDisplay(resourceName));
    }
    // type: "2" = System Object; role: "4" = Domain Resource; lifecycle: "6" = Access/Use
    entity.setType(
        new Coding(
            "http://terminology.hl7.org/CodeSystem/audit-entity-type", "2", "System Object"));
    entity.setRole(
        new Coding(
            "http://terminology.hl7.org/CodeSystem/object-role", "4", "Domain Resource"));
    entity.setLifecycle(
        new Coding(
            "http://terminology.hl7.org/CodeSystem/dicom-audit-lifecycle",
            "6",
            "Access / Use"));
    event.addEntity(entity);
  }

  private static Period buildPeriod(RequestDetails requestDetails) {
    Period period = new Period();
    Object startStashed = requestDetails.getUserData().get(USER_DATA_START_KEY);
    long start =
        (startStashed instanceof Long l) ? l : System.currentTimeMillis();
    long end = System.currentTimeMillis();
    period.setStartElement(new DateTimeType(new Date(start)));
    period.setEndElement(new DateTimeType(new Date(end)));
    return period;
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
