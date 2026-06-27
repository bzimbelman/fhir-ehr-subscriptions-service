package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAgentComponent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventEntityComponent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome
import org.hl7.fhir.r4.model.AuditEvent.AuditEventSourceComponent
import org.hl7.fhir.r4.model.CodeableConcept
import org.hl7.fhir.r4.model.Coding
import org.hl7.fhir.r4.model.DateTimeType
import org.hl7.fhir.r4.model.Period
import org.hl7.fhir.r4.model.Reference
import java.net.InetAddress
import java.util.Date
import java.util.Locale

/**
 * Pure-Kotlin builders for the FHIR `AuditEvent` resource. Extracted from
 * the legacy `AuditEventInterceptor.buildEvent()` (in `hapi/auth/`) so the
 * exact same encoding lives in one place and can be unit-tested without
 * a HAPI server.
 *
 * The functions here are deliberately top-level (no `object` wrapper) so
 * Kotlin call sites read as plain function calls.
 *
 * ## Convention: AuditContext.attributes keys
 *
 * The hapi-auth runtime adapter is responsible for translating HAPI's
 * `RestOperationTypeEnum` / response code / exception status into a
 * normalized [AuditContext]. The builders here read the following
 * well-known keys from [AuditContext.attributes]:
 *
 *  - `operation` — uppercase operation name (CREATE, READ, UPDATE,
 *    DELETE, PATCH, SEARCH, HISTORY, TRANSACTION, BATCH). When absent,
 *    falls back to `requestMethod`.
 *  - `fhirServerBase` — value for `AuditEvent.source.site` (defaults to
 *    `"unknown"` when missing).
 *  - `azp` — OAuth client id, surfaced as a second non-requestor agent.
 *  - `preferredUsername` — falls back to [AuditContext.principalName].
 *  - `responseStatus` — successful HTTP status, used to derive outcome
 *    (0 / 4 / 8). When absent and `exception.status` is also absent,
 *    outcome defaults to 0.
 *  - `exception.status` — HTTP status from a thrown exception.
 *  - `enrichment.originatingUser` — practitioner id surfaced by an
 *    [com.bzonfhir.subscriptionservice.spi.meta.AuditEnrichmentRule] of
 *    field `"addOriginatingUser"`.
 */

/** {@code DCM} (DICOM) coding system. */
internal const val SYSTEM_DICOM = "http://dicom.nema.org/resources/ontology/dcm"
/** The DICOM coding for "Restful Operation". */
internal const val CODE_REST = "rest"
/** Fallback agent identifier when the request was unauthenticated. */
internal const val ANONYMOUS_AGENT = "anonymous"

/**
 * Apply the standard (non-vendor-specific) AuditEvent shape to
 * [baseEvent], using [ctx] as input. Mutates the argument and returns
 * the same instance for fluent chaining.
 */
internal fun applyBaseShape(baseEvent: AuditEvent, ctx: AuditContext): AuditEvent {
    // type = DICOM rest
    baseEvent.type = Coding(SYSTEM_DICOM, CODE_REST, "Restful Operation")

    val operation = operationOf(ctx)

    // subtype = FHIR restful-interaction code derived from the operation.
    baseEvent.addSubtype(subtypeFor(operation))

    // action = C/R/U/D/E
    baseEvent.action = actionFor(operation)

    // recorded = now
    baseEvent.recorded = Date()

    // outcome = derived from response code or exception
    baseEvent.outcome = outcomeFor(ctx)

    // agent: user (if any) + client app (if any). Falls back to anonymous.
    populateAgents(baseEvent, ctx)

    // source: server hostname + the FHIR base URL.
    baseEvent.source = buildSource(ctx)

    // entity: the resource(s) affected (best-effort).
    populateEntities(baseEvent, ctx)

    // period: occurredAt -> now (rough request duration).
    baseEvent.period = buildPeriod(ctx)

    return baseEvent
}

/** Map the normalized operation string to the FHIR restful-interaction subtype. */
internal fun subtypeFor(operation: String): Coding {
    val code = when (operation) {
        "CREATE" -> "create"
        "READ", "VREAD" -> "read"
        "UPDATE" -> "update"
        "PATCH" -> "patch"
        "DELETE" -> "delete"
        "SEARCH_TYPE", "SEARCH_SYSTEM", "SEARCH" -> "search"
        "HISTORY_INSTANCE", "HISTORY_TYPE", "HISTORY_SYSTEM", "HISTORY" -> "history"
        "TRANSACTION" -> "transaction"
        "BATCH" -> "batch"
        else -> operation.lowercase(Locale.ROOT)
    }
    return Coding("http://hl7.org/fhir/restful-interaction", code, code)
}

/** Map the normalized operation string to the AuditEvent.action enum. */
internal fun actionFor(operation: String): AuditEventAction = when (operation) {
    "CREATE" -> AuditEventAction.C
    "READ", "VREAD", "SEARCH_SYSTEM", "SEARCH_TYPE", "SEARCH",
    "HISTORY_INSTANCE", "HISTORY_TYPE", "HISTORY_SYSTEM", "HISTORY", "GET_PAGE" ->
        AuditEventAction.R
    "UPDATE", "PATCH" -> AuditEventAction.U
    "DELETE" -> AuditEventAction.D
    else -> AuditEventAction.E
}

/**
 * Outcome derivation. Mirrors the original interceptor's logic:
 *
 *  - `exception.status >= 500` -> 8 (serious failure)
 *  - `exception.status == 401` -> 8 (unauthenticated; per IHE profile)
 *  - `exception.status` 4xx    -> 4 (minor failure)
 *  - `responseStatus >= 500`   -> 8
 *  - `responseStatus >= 400`   -> 4
 *  - otherwise                 -> 0 (success)
 */
internal fun outcomeFor(ctx: AuditContext): AuditEventOutcome {
    val exStatus = ctx.attributes["exception.status"]?.toIntOrNull()
    if (exStatus != null) {
        return when {
            exStatus >= 500 -> AuditEventOutcome._8
            exStatus == 401 -> AuditEventOutcome._8
            else -> AuditEventOutcome._4
        }
    }
    val respStatus = ctx.attributes["responseStatus"]?.toIntOrNull()
        ?: return AuditEventOutcome._0
    return when {
        respStatus >= 500 -> AuditEventOutcome._8
        respStatus >= 400 -> AuditEventOutcome._4
        else -> AuditEventOutcome._0
    }
}

private fun operationOf(ctx: AuditContext): String =
    ctx.attributes["operation"] ?: ctx.requestMethod.uppercase(Locale.ROOT)

/**
 * Build the agent list. Adds:
 *  - One requestor agent for the authenticated user, when `principalName`
 *    is present (or `preferredUsername` from attributes).
 *  - One non-requestor agent for the OAuth client when `azp` is present.
 *  - A single anonymous placeholder when both above are absent.
 */
internal fun populateAgents(event: AuditEvent, ctx: AuditContext) {
    val principal = ctx.principalName
    val preferredUsername = ctx.attributes["preferredUsername"] ?: principal
    if (principal != null && principal.isNotBlank()) {
        event.addAgent(
            AuditEventAgentComponent().apply {
                requestor = true
                altId = principal
                name = preferredUsername ?: principal
                type = CodeableConcept().addCoding(
                    Coding(
                        "http://terminology.hl7.org/CodeSystem/v3-ParticipationType",
                        "AUT",
                        "author",
                    ),
                )
            },
        )
    }

    val azp = ctx.attributes["azp"]
    if (azp != null && azp.isNotBlank()) {
        event.addAgent(
            AuditEventAgentComponent().apply {
                requestor = false
                altId = azp
                name = azp
                type = CodeableConcept().addCoding(
                    Coding(
                        "http://terminology.hl7.org/CodeSystem/extra-security-role-type",
                        "dataprocessor",
                        "data processor",
                    ),
                )
            },
        )
    }

    if (event.agent.isEmpty()) {
        event.addAgent(
            AuditEventAgentComponent().apply {
                requestor = true
                altId = ANONYMOUS_AGENT
                name = ANONYMOUS_AGENT
            },
        )
    }
}

internal fun buildSource(ctx: AuditContext): AuditEventSourceComponent {
    val source = AuditEventSourceComponent()
    val base = ctx.attributes["fhirServerBase"]
    source.site = if (base.isNullOrBlank()) "unknown" else base
    source.observer = Reference().setDisplay(hostnameOrUnknown())
    source.addType(
        Coding(
            "http://terminology.hl7.org/CodeSystem/security-source-type",
            "3",
            "Web Server",
        ),
    )
    return source
}

private fun hostnameOrUnknown(): String = try {
    val h = InetAddress.getLocalHost().hostName
    if (h.isNullOrBlank()) "unknown" else h
} catch (_: Exception) {
    "unknown"
}

/**
 * Populate `entity.what` with the affected FHIR resource reference. When
 * we have both a resource type AND id, emit `ResourceType/id`. When we
 * have only a type (type-level/system-level op), emit a display-only
 * reference. Skip entirely when we have neither (system-level operation).
 */
internal fun populateEntities(event: AuditEvent, ctx: AuditContext) {
    val resourceType = ctx.resourceType
    val resourceId = ctx.resourceId

    if (resourceType.isNullOrBlank() && resourceId.isNullOrBlank()) {
        return
    }
    val entity = AuditEventEntityComponent()
    if (!resourceType.isNullOrBlank() && !resourceId.isNullOrBlank()) {
        entity.what = Reference().apply { reference = "$resourceType/$resourceId" }
    } else if (!resourceType.isNullOrBlank()) {
        entity.what = Reference().setDisplay(resourceType)
    }
    entity.type = Coding(
        "http://terminology.hl7.org/CodeSystem/audit-entity-type",
        "2",
        "System Object",
    )
    entity.role = Coding(
        "http://terminology.hl7.org/CodeSystem/object-role",
        "4",
        "Domain Resource",
    )
    entity.lifecycle = Coding(
        "http://terminology.hl7.org/CodeSystem/dicom-audit-lifecycle",
        "6",
        "Access / Use",
    )
    event.addEntity(entity)
}

internal fun buildPeriod(ctx: AuditContext): Period {
    val period = Period()
    period.startElement = DateTimeType(Date.from(ctx.occurredAt))
    period.endElement = DateTimeType(Date())
    return period
}
