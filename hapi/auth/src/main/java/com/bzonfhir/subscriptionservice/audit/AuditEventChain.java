package com.bzonfhir.subscriptionservice.audit;

import java.util.Date;
import java.util.List;
import java.util.Locale;

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

/**
 * SPI-shaped AuditEvent builder chain for the HAPI image (ticket #432,
 * Epic #425).
 *
 * <p>Behaviourally identical to the Kotlin
 * {@code com.bzonfhir.subscriptionservice.plugins.auditeventfhir.AuditEventFhirEnricher}
 * in {@code plugins-builtin/audit-event-fhir/}. Both consume an
 * {@link AuditContext} and mutate a fresh {@link AuditEvent}; both are
 * pure functions over their input. The plugin module's tests
 * ({@code AuditEventFhirEnricherTest}, {@code AuditEventBuildersTest})
 * cover the same encodings as this module's
 * {@code AuditEventInterceptorTest}, which keeps the two implementations
 * provably equivalent.
 *
 * <p>Why two implementations: the {@code hapi/auth/} Docker build uses
 * Maven in isolation and can't consume Gradle-published JARs. When
 * {@code hapi/auth/} migrates to Gradle (follow-up to ticket #432), this
 * class is deleted and the Kotlin enricher becomes the sole owner.
 */
public final class AuditEventChain {

  /** {@code DCM} (DICOM) coding system. */
  static final String SYSTEM_DICOM = "http://dicom.nema.org/resources/ontology/dcm";
  /** The DICOM coding for "Restful Operation". */
  static final String CODE_REST = "rest";
  /** Fallback agent identifier when the request was unauthenticated. */
  static final String ANONYMOUS_AGENT = "anonymous";

  /** No instances — pure functions only. */
  private AuditEventChain() {}

  /**
   * Apply the standard (non-vendor-specific) AuditEvent shape to
   * {@code baseEvent} using {@code ctx} as input. Mutates the argument
   * and returns the same instance for fluent chaining.
   */
  public static AuditEvent applyBaseShape(AuditEvent baseEvent, AuditContext ctx) {
    // type = DICOM rest
    baseEvent.setType(new Coding(SYSTEM_DICOM, CODE_REST, "Restful Operation"));

    String operation = operationOf(ctx);

    // subtype = FHIR restful-interaction code derived from the operation.
    baseEvent.addSubtype(subtypeFor(operation));

    // action = C/R/U/D/E
    baseEvent.setAction(actionFor(operation));

    // recorded = now
    baseEvent.setRecorded(new Date());

    // outcome = derived from response code or exception
    baseEvent.setOutcome(outcomeFor(ctx));

    // agent: user (if any) + client app (if any); anonymous fallback.
    populateAgents(baseEvent, ctx);

    // source: server hostname + the FHIR base URL.
    baseEvent.setSource(buildSource(ctx));

    // entity: the resource(s) affected (best-effort).
    populateEntities(baseEvent, ctx);

    // period: occurredAt -> now.
    baseEvent.setPeriod(buildPeriod(ctx));

    return baseEvent;
  }

  /** Map the normalised operation string to the FHIR restful-interaction subtype. */
  static Coding subtypeFor(String operation) {
    String code;
    switch (operation) {
      case "CREATE":
        code = "create";
        break;
      case "READ":
      case "VREAD":
        code = "read";
        break;
      case "UPDATE":
        code = "update";
        break;
      case "PATCH":
        code = "patch";
        break;
      case "DELETE":
        code = "delete";
        break;
      case "SEARCH_TYPE":
      case "SEARCH_SYSTEM":
      case "SEARCH":
        code = "search";
        break;
      case "HISTORY_INSTANCE":
      case "HISTORY_TYPE":
      case "HISTORY_SYSTEM":
      case "HISTORY":
        code = "history";
        break;
      case "TRANSACTION":
        code = "transaction";
        break;
      case "BATCH":
        code = "batch";
        break;
      default:
        code = operation.toLowerCase(Locale.ROOT);
    }
    return new Coding("http://hl7.org/fhir/restful-interaction", code, code);
  }

  /** Map the normalised operation string to the AuditEvent.action enum. */
  static AuditEventAction actionFor(String operation) {
    switch (operation) {
      case "CREATE":
        return AuditEventAction.C;
      case "READ":
      case "VREAD":
      case "SEARCH_SYSTEM":
      case "SEARCH_TYPE":
      case "SEARCH":
      case "HISTORY_INSTANCE":
      case "HISTORY_TYPE":
      case "HISTORY_SYSTEM":
      case "HISTORY":
      case "GET_PAGE":
        return AuditEventAction.R;
      case "UPDATE":
      case "PATCH":
        return AuditEventAction.U;
      case "DELETE":
        return AuditEventAction.D;
      default:
        return AuditEventAction.E;
    }
  }

  /**
   * Outcome derivation (mirrors the legacy interceptor's logic):
   *
   * <ul>
   *   <li>{@code exception.status >= 500} -> 8 (serious failure)
   *   <li>{@code exception.status == 401} -> 8 (unauthenticated)
   *   <li>{@code exception.status} 4xx    -> 4 (minor failure)
   *   <li>{@code responseStatus >= 500}   -> 8
   *   <li>{@code responseStatus >= 400}   -> 4
   *   <li>otherwise                       -> 0 (success)
   * </ul>
   */
  static AuditEventOutcome outcomeFor(AuditContext ctx) {
    Integer exStatus = parseIntOrNull(ctx.attribute("exception.status"));
    if (exStatus != null) {
      if (exStatus >= 500) {
        return AuditEventOutcome._8;
      }
      if (exStatus == 401) {
        return AuditEventOutcome._8;
      }
      return AuditEventOutcome._4;
    }
    Integer respStatus = parseIntOrNull(ctx.attribute("responseStatus"));
    if (respStatus == null) {
      return AuditEventOutcome._0;
    }
    if (respStatus >= 500) {
      return AuditEventOutcome._8;
    }
    if (respStatus >= 400) {
      return AuditEventOutcome._4;
    }
    return AuditEventOutcome._0;
  }

  private static String operationOf(AuditContext ctx) {
    String op = ctx.attribute("operation");
    if (op != null && !op.isBlank()) {
      return op;
    }
    return ctx.requestMethod() == null
        ? "UNKNOWN"
        : ctx.requestMethod().toUpperCase(Locale.ROOT);
  }

  /**
   * Build the agent list. Adds:
   * <ul>
   *   <li>One requestor agent for the authenticated user when
   *       {@code principalName} is present.
   *   <li>One non-requestor agent for the OAuth client when {@code azp}
   *       attribute is present.
   *   <li>A single anonymous placeholder when both above are absent.
   * </ul>
   */
  static void populateAgents(AuditEvent event, AuditContext ctx) {
    String principal = ctx.principalName();
    String preferredUsername = ctx.attribute("preferredUsername");
    if (preferredUsername == null) {
      preferredUsername = principal;
    }
    if (principal != null && !principal.isBlank()) {
      AuditEventAgentComponent userAgent = new AuditEventAgentComponent();
      userAgent.setRequestor(true);
      userAgent.setAltId(principal);
      userAgent.setName(preferredUsername != null ? preferredUsername : principal);
      userAgent.setType(
          new CodeableConcept()
              .addCoding(
                  new Coding(
                      "http://terminology.hl7.org/CodeSystem/v3-ParticipationType",
                      "AUT",
                      "author")));
      event.addAgent(userAgent);
    }

    String azp = ctx.attribute("azp");
    if (azp != null && !azp.isBlank()) {
      AuditEventAgentComponent appAgent = new AuditEventAgentComponent();
      appAgent.setRequestor(false);
      appAgent.setAltId(azp);
      appAgent.setName(azp);
      appAgent.setType(
          new CodeableConcept()
              .addCoding(
                  new Coding(
                      "http://terminology.hl7.org/CodeSystem/extra-security-role-type",
                      "dataprocessor",
                      "data processor")));
      event.addAgent(appAgent);
    }

    if (event.getAgent().isEmpty()) {
      AuditEventAgentComponent anon = new AuditEventAgentComponent();
      anon.setRequestor(true);
      anon.setAltId(ANONYMOUS_AGENT);
      anon.setName(ANONYMOUS_AGENT);
      event.addAgent(anon);
    }
  }

  static AuditEventSourceComponent buildSource(AuditContext ctx) {
    AuditEventSourceComponent source = new AuditEventSourceComponent();
    String base = ctx.attribute("fhirServerBase");
    source.setSite((base == null || base.isBlank()) ? "unknown" : base);
    source.setObserver(new Reference().setDisplay(hostnameOrUnknown()));
    source.addType(
        new Coding(
            "http://terminology.hl7.org/CodeSystem/security-source-type", "3", "Web Server"));
    return source;
  }

  /**
   * Populate {@code entity.what} with the affected FHIR resource reference.
   * When both type and id are known, emit {@code ResourceType/id}; when only
   * the type is known, emit a display-only Reference; otherwise skip.
   */
  static void populateEntities(AuditEvent event, AuditContext ctx) {
    String resourceType = ctx.resourceType();
    String resourceId = ctx.resourceId();

    if ((resourceType == null || resourceType.isBlank())
        && (resourceId == null || resourceId.isBlank())) {
      return;
    }
    AuditEventEntityComponent entity = new AuditEventEntityComponent();
    if (resourceType != null
        && !resourceType.isBlank()
        && resourceId != null
        && !resourceId.isBlank()) {
      entity.setWhat(new Reference().setReference(resourceType + "/" + resourceId));
    } else if (resourceType != null && !resourceType.isBlank()) {
      entity.setWhat(new Reference().setDisplay(resourceType));
    }
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

  static Period buildPeriod(AuditContext ctx) {
    Period period = new Period();
    long start = ctx.occurredAt() == null
        ? System.currentTimeMillis()
        : ctx.occurredAt().toEpochMilli();
    long end = System.currentTimeMillis();
    period.setStartElement(new DateTimeType(new Date(start)));
    period.setEndElement(new DateTimeType(new Date(end)));
    return period;
  }

  private static String hostnameOrUnknown() {
    try {
      String h = java.net.InetAddress.getLocalHost().getHostName();
      return (h == null || h.isBlank()) ? "unknown" : h;
    } catch (Exception e) {
      return "unknown";
    }
  }

  private static Integer parseIntOrNull(String value) {
    if (value == null) {
      return null;
    }
    try {
      return Integer.parseInt(value);
    } catch (NumberFormatException e) {
      return null;
    }
  }

  // Reserved for future Epic / vendor profile integration — invoked when
  // a HL7VendorProfile surfaces AuditEnrichmentRule values for the active
  // request. Kept package-visible so the interceptor can call it once
  // the vendor profile pipeline (a later ticket in Epic #425) lands.
  static void applyVendorRules(
      AuditEvent event, AuditContext ctx, List<VendorRule> rules) {
    if (rules == null || rules.isEmpty()) {
      return;
    }
    for (VendorRule rule : rules) {
      if ("addOriginatingUser".equals(rule.field())) {
        String value = ctx.attribute("enrichment.originatingUser");
        if (value != null && !value.isBlank()) {
          AuditEventAgentComponent agent = new AuditEventAgentComponent();
          agent.setRequestor(false);
          agent.setWho(new Reference("Practitioner/" + value));
          event.addAgent(agent);
        }
      }
      // Other rule keys (addPatientFacility, addPracticeId, ...) wired
      // when concrete vendor profiles land.
    }
  }

  /** Java mirror of {@code spi.meta.AuditEnrichmentRule}. */
  public record VendorRule(String field, String source) {}
}
