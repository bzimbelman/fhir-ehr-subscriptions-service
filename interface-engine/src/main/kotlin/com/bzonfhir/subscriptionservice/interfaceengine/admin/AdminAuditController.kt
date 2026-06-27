package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.api.SortOrderEnum
import ca.uhn.fhir.rest.api.SortSpec
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.gclient.DateClientParam
import ca.uhn.fhir.rest.gclient.ICriterion
import ca.uhn.fhir.rest.gclient.TokenClientParam
import ca.uhn.fhir.rest.server.exceptions.BaseServerResponseException
import ca.uhn.fhir.rest.server.exceptions.ResourceNotFoundException
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.ObjectMapper
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome
import org.hl7.fhir.r4.model.Bundle
import org.slf4j.LoggerFactory
import org.springframework.http.MediaType
import org.springframework.http.ResponseEntity
import org.springframework.stereotype.Component
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Operator audit log browser (Epic #398, ticket #407).
 *
 * AuditEvent resources are emitted by [AuditEventInterceptor] in the
 * hapi-auth JAR (ticket #391) and persisted as FHIR resources on HAPI
 * itself. The natural read path would be `GET /fhir/AuditEvent?...` --
 * but that endpoint has a different auth model (the FHIR API uses OIDC
 * bearer tokens from a per-user JWT) and the returned shape is a verbose
 * FHIR Bundle that the UI would have to re-normalise anyway.
 *
 * Instead, we expose `GET /admin/audit` here, which:
 *
 *   1. Stays under the existing `/admin/` glob bearer gate (so the
 *      operator UI's proxy keeps a single auth model).
 *   2. Hands HAPI the FHIR search params through the same
 *      [IGenericClient] used by the rest of the admin surface.
 *   3. Normalises the response into a flat row shape the UI can render
 *      without a second FHIR-parser dependency.
 *
 * A companion `GET /admin/audit/{id}` returns the full, raw FHIR
 * AuditEvent resource as JSON so the row-expansion view can show the
 * complete document.
 *
 * No paging-cursor / Bundle-link relations -- this surface uses
 * `_count` + `_offset` (we ask HAPI for `_total=accurate`) and tells the
 * UI how many total events match. Auditing is operator-scale traffic
 * (handfuls of pages, not millions); the simpler model is fine.
 */
@RestController
@RequestMapping("/admin/audit")
class AdminAuditController(
    private val client: HapiAuditClient,
) {

    private val log = LoggerFactory.getLogger(AdminAuditController::class.java)

    @GetMapping("", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun search(
        @RequestParam(required = false) type: String?,
        @RequestParam(required = false) subtype: String?,
        @RequestParam(required = false) outcome: String?,
        @RequestParam(required = false) agent: String?,
        @RequestParam(name = "date-from", required = false) dateFrom: String?,
        @RequestParam(name = "date-to", required = false) dateTo: String?,
        @RequestParam(defaultValue = "50") limit: Int,
        @RequestParam(defaultValue = "0") offset: Int,
    ): ResponseEntity<AuditSearchResponse> {
        // limit hard-capped at MAX_LIMIT (200) per the contract -- prevents
        // operators from accidentally pulling a five-figure result set
        // through the UI.
        val cappedLimit = limit.coerceIn(1, MAX_LIMIT)
        val safeOffset = offset.coerceAtLeast(0)
        val criteria = AuditSearchCriteria(
            type = type,
            subtype = subtype,
            outcome = outcome,
            agent = agent,
            dateFrom = dateFrom,
            dateTo = dateTo,
            limit = cappedLimit,
            offset = safeOffset,
        )
        return try {
            val bundle = client.search(criteria)
            val items = bundle.entry
                .mapNotNull { it.resource as? AuditEvent }
                .map { normalise(it) }
            // Bundle.total when the server populates it (HAPI does on
            // `_total=accurate` searches); fall back to items.size when
            // the server returned no total (HAPI sometimes does on
            // small result sets even with `_total=accurate`).
            val total = if (bundle.hasTotal()) bundle.total else items.size
            ResponseEntity.ok(
                AuditSearchResponse(
                    total = total,
                    limit = cappedLimit,
                    offset = safeOffset,
                    items = items,
                ),
            )
        } catch (ex: Exception) {
            log.warn("admin audit search failed: {}", ex.message)
            ResponseEntity.status(502).body(
                AuditSearchResponse(
                    total = 0,
                    limit = cappedLimit,
                    offset = safeOffset,
                    items = emptyList(),
                    error = ex.message ?: ex.javaClass.simpleName,
                ),
            )
        }
    }

    @GetMapping("/{id}", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun read(@PathVariable id: String): ResponseEntity<Any> {
        return try {
            val raw = client.read(id) ?: return ResponseEntity.status(404).body(
                mapOf(
                    "error" to "not_found",
                    "message" to "AuditEvent/$id not found",
                ),
            )
            ResponseEntity.ok(raw)
        } catch (ex: Exception) {
            log.warn("admin audit read id={} failed: {}", id, ex.message)
            ResponseEntity.status(502).body(
                mapOf(
                    "error" to "upstream_failed",
                    "message" to (ex.message ?: ex.javaClass.simpleName),
                ),
            )
        }
    }

    /**
     * FHIR AuditEvent -> our flat row shape. Pulled out so it can be
     * unit-tested in isolation if needed.
     */
    internal fun normalise(event: AuditEvent): AuditEventRow {
        val typeCoding = event.type
        val firstSubtype = event.subtype.firstOrNull()
        val recorded = event.recorded?.let {
            OffsetDateTime.ofInstant(it.toInstant(), ZoneOffset.UTC).toString()
        }
        val outcomeCode = event.outcome?.toCode()
        val outcomeDisplay = outcomeDisplayFor(event.outcome)
        val actionCode = event.action?.toCode()

        // Pick the first requestor agent for the headline "who" column,
        // falling back to the first agent of any kind. The full set is
        // available via the /admin/audit/{id} detail endpoint.
        val requestor = event.agent.firstOrNull { it.requestor }
            ?: event.agent.firstOrNull()
        val agentWho = requestor?.who?.reference
            ?: requestor?.altId
            ?: requestor?.who?.display
        val agentName = requestor?.name ?: requestor?.altId

        val firstEntity = event.entity.firstOrNull()
        val entityWhat = firstEntity?.what?.reference ?: firstEntity?.what?.display
        // Entity FHIR resource type, derived from the Reference target.
        // For "Patient/123", that's "Patient"; for a display-only
        // reference, we expose the display string itself as the type.
        val entityType = firstEntity?.what?.reference?.substringBefore('/')
            ?: firstEntity?.what?.display

        return AuditEventRow(
            id = event.idElement?.idPart?.let { "AuditEvent/$it" } ?: "",
            recorded = recorded,
            typeCode = typeCoding?.code,
            typeDisplay = typeCoding?.display ?: typeCoding?.code,
            subtypeCode = firstSubtype?.code,
            outcome = outcomeCode,
            outcomeDisplay = outcomeDisplay,
            action = actionCode,
            agentWho = agentWho,
            agentName = agentName,
            entityWhat = entityWhat,
            entityType = entityType,
        )
    }

    companion object {
        /**
         * Per the contract: limit is capped at 200. Anything higher is
         * silently clamped. The operator UI uses limit=50 by default.
         */
        const val MAX_LIMIT = 200
    }
}

// -- Search criteria + JSON shapes ----------------------------------------

/**
 * Input bundle for [HapiAuditClient.search]. Pulled out so test fakes
 * can capture the criteria verbatim.
 */
data class AuditSearchCriteria(
    val type: String?,
    val subtype: String?,
    val outcome: String?,
    val agent: String?,
    val dateFrom: String?,
    val dateTo: String?,
    val limit: Int,
    val offset: Int,
)

/**
 * One audit row -- the flattened shape the UI renders directly.
 * `null` on any field means "the underlying AuditEvent didn't have it";
 * the UI displays "—" rather than blanking the row.
 */
data class AuditEventRow(
    val id: String,
    val recorded: String?,
    @JsonProperty("type_code") val typeCode: String?,
    @JsonProperty("type_display") val typeDisplay: String?,
    @JsonProperty("subtype_code") val subtypeCode: String?,
    val outcome: String?,
    @JsonProperty("outcome_display") val outcomeDisplay: String?,
    val action: String?,
    @JsonProperty("agent_who") val agentWho: String?,
    @JsonProperty("agent_name") val agentName: String?,
    @JsonProperty("entity_what") val entityWhat: String?,
    @JsonProperty("entity_type") val entityType: String?,
)

data class AuditSearchResponse(
    val total: Int,
    val limit: Int,
    val offset: Int,
    val items: List<AuditEventRow>,
    val error: String? = null,
)

internal fun outcomeDisplayFor(outcome: AuditEventOutcome?): String? = when (outcome) {
    AuditEventOutcome._0 -> "Success"
    AuditEventOutcome._4 -> "Minor failure"
    AuditEventOutcome._8 -> "Serious failure"
    AuditEventOutcome._12 -> "Major failure"
    else -> null
}

// -- Client abstraction ---------------------------------------------------

/**
 * Thin abstraction over HAPI's AuditEvent search/read so the controller
 * can be tested without a live HAPI. Mirrors the
 * [HapiSubscriptionStatusClient] pattern used by ticket #404.
 */
interface HapiAuditClient {
    fun search(criteria: AuditSearchCriteria): Bundle
    /**
     * Returns the full FHIR resource as a generic JSON tree (Map / List)
     * so it can be re-serialized inside the admin envelope without
     * re-parsing on the client. Returns null when HAPI returns 404.
     */
    fun read(id: String): Any?
}

@Component
class HapiAuditClientImpl(
    private val hapiClient: IGenericClient,
    private val fhirContext: FhirContext,
) : HapiAuditClient {

    private val log = LoggerFactory.getLogger(HapiAuditClientImpl::class.java)
    private val objectMapper = ObjectMapper()

    override fun search(criteria: AuditSearchCriteria): Bundle {
        val criteriaList = mutableListOf<ICriterion<*>>()
        criteria.type?.takeUnless { it.isBlank() }?.let {
            criteriaList += TokenClientParam("type").exactly().code(it)
        }
        criteria.subtype?.takeUnless { it.isBlank() }?.let {
            criteriaList += TokenClientParam("subtype").exactly().code(it)
        }
        criteria.outcome?.takeUnless { it.isBlank() }?.let {
            criteriaList += TokenClientParam("outcome").exactly().code(it)
        }
        criteria.agent?.takeUnless { it.isBlank() }?.let {
            // FHIR R4 search param: `agent` matches Reference (agent.who.reference)
            // OR string (display). We pass it through unchanged and let HAPI
            // do the right thing. For free-text searches on agent names the
            // UI hits this with the username.
            criteriaList += TokenClientParam("agent").exactly().code(it)
        }
        criteria.dateFrom?.takeUnless { it.isBlank() }?.let {
            criteriaList += DateClientParam("date").afterOrEquals().day(it)
        }
        criteria.dateTo?.takeUnless { it.isBlank() }?.let {
            criteriaList += DateClientParam("date").beforeOrEquals().day(it)
        }

        var query = hapiClient.search<Bundle>().forResource(AuditEvent::class.java)
        for ((index, c) in criteriaList.withIndex()) {
            query = if (index == 0) query.where(c) else query.and(c)
        }
        // Newest first (matches the UI default) and ask HAPI for the
        // accurate total so pagination can render "X of N" correctly.
        query = query
            .sort(SortSpec("date", SortOrderEnum.DESC))
            .count(criteria.limit)
            .offset(criteria.offset)
            .totalMode(ca.uhn.fhir.rest.api.SearchTotalModeEnum.ACCURATE)

        return query.returnBundle(Bundle::class.java).execute()
    }

    override fun read(id: String): Any? {
        return try {
            val resource = hapiClient.read()
                .resource(AuditEvent::class.java)
                .withId(id)
                .execute()
            // Round-trip through Jackson so the controller envelope
            // contains a parsed JSON tree (Map/List) rather than a
            // FHIR model object Jackson doesn't know how to serialize
            // cleanly.
            val json = fhirContext.newJsonParser().encodeResourceToString(resource)
            objectMapper.readValue(json, Any::class.java)
        } catch (notFound: ResourceNotFoundException) {
            null
        } catch (ex: BaseServerResponseException) {
            log.warn("HAPI AuditEvent read id={} failed status={}", id, ex.statusCode)
            null
        }
    }
}
