package com.bzonfhir.subscriptionservice.audit;

import java.time.Instant;
import java.util.Map;

/**
 * Normalised request context handed to {@link AuditEventChain}.
 *
 * <p>Mirrors the Kotlin
 * {@code com.bzonfhir.subscriptionservice.spi.meta.AuditContext} record from
 * {@code plugins-spi/} (ticket #430). When {@code hapi/auth/} migrates from
 * Maven to Gradle (follow-up story to ticket #432), this Java class is
 * deleted and the SPI's Kotlin {@code AuditContext} becomes the sole shape.
 *
 * <p>Today both classes coexist because the Docker build of the HAPI image
 * runs {@code mvn package} on this Maven module in isolation — it can't
 * consume Gradle-published artifacts. See the audit-event-fhir plugin
 * README for the long-term plan.
 *
 * <p>Field semantics match the Kotlin counterpart 1:1. Keys recognised
 * inside {@link #attributes}:
 *
 * <ul>
 *   <li>{@code operation} — uppercase operation name (CREATE, READ, ...).
 *   <li>{@code fhirServerBase} — value for {@code source.site}.
 *   <li>{@code azp} — OAuth client id, surfaced as a non-requestor agent.
 *   <li>{@code preferredUsername} — falls back to {@code principalName}.
 *   <li>{@code responseStatus} — successful HTTP status (drives outcome).
 *   <li>{@code exception.status} — HTTP status from an exception path.
 *   <li>{@code enrichment.originatingUser} — value the
 *       {@code addOriginatingUser} vendor rule stamps as an extra agent.
 * </ul>
 */
public record AuditContext(
    Instant occurredAt,
    String correlationId,
    String tenantId,
    String principalName,
    String requestPath,
    String requestMethod,
    String resourceType,
    String resourceId,
    String sourceIp,
    Map<String, String> attributes) {

  /** Convenience accessor for an attribute key. */
  public String attribute(String key) {
    return attributes == null ? null : attributes.get(key);
  }
}
