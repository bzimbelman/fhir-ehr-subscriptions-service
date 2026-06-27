package com.bzonfhir.subscriptionservice.spi.meta

import java.time.Instant

/**
 * Information the runtime hands an
 * [com.bzonfhir.subscriptionservice.spi.AuditEventEnricher] when it's
 * about to emit a FHIR `AuditEvent`.
 *
 * The runtime first builds a base `AuditEvent` from the request/response
 * it just observed (see `hapi/auth/.../audit/AuditEventInterceptor.java`),
 * then walks through each registered enricher giving it a chance to
 * add fields. Enrichers receive both the [AuditContext] (what we know
 * about the request) and the `AuditEventBuilder` (the in-progress event
 * they can mutate).
 *
 * Why a context object instead of passing the raw HAPI `RequestDetails`:
 * we want third-party enrichers to be testable without standing up a
 * HAPI request. The runtime extracts the fields enrichers care about,
 * normalizes them, and hands over this neutral structure.
 *
 * @property occurredAt When the underlying request happened.
 * @property correlationId The MDC correlation id for the request, so an
 *   enricher can stamp it on the AuditEvent for forensic queries.
 * @property tenantId The active partition id when multi-tenancy is on
 *   (see `hapi/auth/.../multitenancy/TenantPartitionInterceptor.java`);
 *   `null` on single-tenant deployments.
 * @property principalName The authenticated subject's name (typically
 *   the OIDC `sub` claim). `null` for anonymous/unauthenticated paths.
 * @property requestPath The REST path being audited (`"/fhir/Patient"`).
 * @property requestMethod HTTP method (`"POST"`, `"GET"`...).
 * @property resourceType Optional — the FHIR resource type being acted
 *   on, when discernible from the request.
 * @property resourceId Optional — the FHIR resource logical id, when
 *   discernible.
 * @property sourceIp The originating client IP (best-effort, may be a
 *   proxy IP depending on deployment).
 * @property sourceMessage Optional — when the audit pertains to an
 *   inbound HL7 v2 / FHIR / other ingested message, the
 *   [PipelineMessage] that originated it. Profile-aware enrichers
 *   (e.g. Epic's) walk into [PipelineMessage.attributes] to copy
 *   vendor-specific fields (PV1-7 originating user, etc.) onto the
 *   emitted AuditEvent. See master plan §4.3.
 * @property attributes Loose key/value map for additional context the
 *   runtime gathered (e.g. `"http.statusCode"`, `"operation.name"`).
 */
data class AuditContext(
    val occurredAt: Instant,
    val correlationId: String,
    val tenantId: String?,
    val principalName: String?,
    val requestPath: String,
    val requestMethod: String,
    val resourceType: String? = null,
    val resourceId: String? = null,
    val sourceIp: String? = null,
    val sourceMessage: PipelineMessage? = null,
    val attributes: Map<String, String> = emptyMap(),
)

/**
 * Information available to an
 * [com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher].
 *
 * Mirrors [AuditContext] in spirit but is fired in a much hotter path
 * (every log line / every metric increment). Kept narrow so enrichers
 * don't accidentally do expensive lookups on the request critical path.
 *
 * The runtime offers this shape both for log-field enrichment and for
 * metric-label enrichment. Implementations should NOT branch on which
 * call invoked them — the same context shape is reused.
 *
 * @property correlationId MDC correlation id for the current request /
 *   pipeline stage.
 * @property tenantId Active partition when multitenancy is on.
 * @property pipelineStage Name of the stage emitting the log/metric
 *   (`"mllp.receive"`, `"worker.transform"`, `"hapi.write"`).
 * @property attributes Loose key/value map for stage-specific fields.
 *   Enrichers may read from here and write back additional fields to
 *   their return map.
 */
data class ObservabilityContext(
    val correlationId: String,
    val tenantId: String?,
    val pipelineStage: String,
    val attributes: Map<String, String> = emptyMap(),
)

/**
 * Vendor-supplied rule for how a piece of inbound metadata should land
 * on the emitted `AuditEvent`. Carried inside an
 * [com.bzonfhir.subscriptionservice.spi.HL7VendorProfile.auditEnrichments]
 * list and applied by the runtime's audit pipeline when the matching
 * profile is active.
 *
 * Example (Epic): `AuditEnrichmentRule(field = "addOriginatingUser",
 * source = "pv1.7")` tells the runtime to copy the PV1-7 field
 * (attending doctor) into the AuditEvent's agent.who slot. The mapping
 * from `field` → AuditEvent slot is owned by the runtime; the rule
 * itself stays declarative so vendor profile YAML stays human-readable.
 *
 * @property field A well-known rule key the runtime knows how to apply
 *   (`addOriginatingUser`, `addPatientFacility`, `addPracticeId`).
 *   Unknown keys fail profile loading.
 * @property source A profile-specific source expression. For HL7 v2 this
 *   is a Terser path (`"pv1.7"`, `"msh.4"`). For REST sources it's
 *   typically `"query.<param>"` or `"response-header.<name>"`.
 */
data class AuditEnrichmentRule(
    val field: String,
    val source: String,
)

/**
 * Outcome of mapping an inbound message into one or more FHIR resources
 * via [com.bzonfhir.subscriptionservice.spi.HL7VendorProfile.mapMessageToFhir].
 *
 * @property bundleJson The resulting FHIR Bundle serialized to JSON.
 *   Plugin authors return JSON rather than `IBaseResource` to keep
 *   HAPI off the SPI's hot classpath; the runtime parses it once
 *   before persisting.
 * @property warnings Non-fatal mapping warnings (missing optional
 *   fields, deprecated quirks). Surfaced in the operator UI's
 *   message detail page.
 */
data class FhirMappingResult(
    val bundleJson: String,
    val warnings: List<String> = emptyList(),
)

/**
 * Generic create outcome for [com.bzonfhir.subscriptionservice.spi.StorageBackend.create].
 *
 * @property id The logical id the backend assigned (or the one the
 *   caller proposed, if accepted).
 * @property versionId The resource version after the create
 *   (typically `"1"` for a fresh insert).
 */
data class CreateOutcome(
    val id: String,
    val versionId: String,
)

/**
 * Generic update outcome for [com.bzonfhir.subscriptionservice.spi.StorageBackend.update].
 *
 * @property id Logical id of the updated resource.
 * @property versionId Version after the update.
 * @property created `true` when this update created a new resource
 *   (upsert semantics); `false` when it updated an existing one.
 */
data class UpdateOutcome(
    val id: String,
    val versionId: String,
    val created: Boolean,
)

/**
 * A minimal FHIR-resource envelope for the
 * [com.bzonfhir.subscriptionservice.spi.StorageBackend] surface.
 *
 * Like [SubscriptionEvent.resourceJson], this is intentionally a
 * `(type, id, json)` triple rather than a parsed `IBaseResource`. A
 * `StorageBackend` plugin gets to choose how it parses (or doesn't —
 * an Elasticsearch backend might index the JSON directly without
 * touching HAPI).
 */
data class FhirResource(
    val type: String,
    val id: String?,
    val json: String,
)

/**
 * Search criteria handed to
 * [com.bzonfhir.subscriptionservice.spi.StorageBackend.search].
 *
 * @property resourceType The FHIR resource type being searched.
 * @property parameters Search parameters as the FHIR client constructed
 *   them (`{"identifier": "mrn|123"}`). The backend implements the
 *   same FHIR search semantics as HAPI's JPA store.
 * @property count Optional `_count` paging hint.
 * @property offset Optional offset for paging.
 */
data class SearchCriteria(
    val resourceType: String,
    val parameters: Map<String, String> = emptyMap(),
    val count: Int? = null,
    val offset: Int? = null,
)

/**
 * Search result from a [com.bzonfhir.subscriptionservice.spi.StorageBackend].
 *
 * @property total Total matching resources (server-side count).
 * @property resources The resources in this page.
 */
data class SearchResult(
    val total: Int,
    val resources: List<FhirResource>,
)
