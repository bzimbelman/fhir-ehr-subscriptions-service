package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.context.FhirContext
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.ObjectMapper
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.CapabilityStatement
import org.hl7.fhir.r4.model.StructureMap
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.web.client.RestTemplateBuilder
import org.springframework.http.HttpEntity
import org.springframework.http.HttpHeaders
import org.springframework.http.MediaType
import org.springframework.http.ResponseEntity
import org.springframework.stereotype.Component
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PostMapping
import org.springframework.web.bind.annotation.RequestBody
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RestController
import org.springframework.web.client.RestTemplate
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.time.Duration
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Operator admin REST API for the upstream Matchbox FHIR-mapping engine
 * (Epic #398, ticket #405).
 *
 * Three endpoints, mounted under the same `/admin/` glob the rest of the
 * admin API uses (so the existing [AdminAuthInterceptor] bearer-token
 * gate covers them with no extra wiring):
 *
 *   GET  /admin/matchbox/health           - liveness probe of the upstream
 *   GET  /admin/matchbox/structuremaps    - normalised list of loaded SMs
 *   POST /admin/matchbox/transform        - run a $transform interactively
 *
 * Why a separate gateway from the worker's [MatchboxClient]: that client
 * is purpose-built for the async pipeline - it knows about correlation
 * IDs, OTel CLIENT spans, and the v2-ER7 content-type the worker needs.
 * The operator inspector needs different verbs (metadata, search), wants
 * to surface errors in detail rather than throw, and shouldn't pull in
 * tracing/context machinery on every request. Splitting keeps each
 * surface narrow.
 *
 * Auth: gated by [AdminAuthInterceptor] on the `/admin/` glob. No
 * additional Matchbox-side auth is set up today (Matchbox runs in the
 * same docker network and only exposes itself to the interface engine);
 * if Matchbox grows an auth gate later this controller will need to
 * forward a token the same way the worker would.
 */
@RestController
@RequestMapping("/admin/matchbox")
class AdminMatchboxController(
    private val gateway: MatchboxAdminGateway,
    @Value("\${subscription-service.matchbox.base-url}") private val matchboxBaseUrl: String,
    @Value("\${subscription-service.matchbox.structuremap.adt-a01:}")
    private val defaultStructureMap: String,
) {

    private val log = LoggerFactory.getLogger(AdminMatchboxController::class.java)

    @GetMapping("/health", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun health(): ResponseEntity<MatchboxHealthResponse> {
        val started = System.nanoTime()
        val checkedAt = OffsetDateTime.now(ZoneOffset.UTC)
        return try {
            val version = gateway.fetchMetadataVersion()
            val ms = (System.nanoTime() - started) / 1_000_000
            ResponseEntity.ok(
                MatchboxHealthResponse(
                    reachable = true,
                    version = version,
                    baseUrl = matchboxBaseUrl,
                    checkedAt = checkedAt,
                    responseTimeMs = ms,
                    error = null,
                ),
            )
        } catch (ex: Exception) {
            val ms = (System.nanoTime() - started) / 1_000_000
            log.warn("matchbox health failed: {}", ex.message)
            ResponseEntity.ok(
                MatchboxHealthResponse(
                    reachable = false,
                    version = null,
                    baseUrl = matchboxBaseUrl,
                    checkedAt = checkedAt,
                    responseTimeMs = ms,
                    error = ex.message ?: ex.javaClass.simpleName,
                ),
            )
        }
    }

    @GetMapping("/structuremaps", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun structureMaps(): ResponseEntity<StructureMapsResponse> {
        return try {
            val items = gateway.listStructureMaps()
            ResponseEntity.ok(StructureMapsResponse(total = items.size, items = items, error = null))
        } catch (ex: Exception) {
            log.warn("matchbox structuremaps failed: {}", ex.message)
            ResponseEntity.ok(
                StructureMapsResponse(
                    total = 0,
                    items = emptyList(),
                    error = ex.message ?: ex.javaClass.simpleName,
                ),
            )
        }
    }

    @PostMapping(
        "/transform",
        consumes = [MediaType.APPLICATION_JSON_VALUE],
        produces = [MediaType.APPLICATION_JSON_VALUE],
    )
    fun transform(@RequestBody body: TransformRequest): ResponseEntity<TransformResponse> {
        val raw = body.rawMessage?.takeIf { it.isNotBlank() }
            ?: return ResponseEntity.status(400).body(
                TransformResponse(
                    success = false,
                    bundle = null,
                    error = "raw_message is required",
                ),
            )
        val mapUrl = body.mapUrl?.takeIf { it.isNotBlank() } ?: defaultStructureMap
        if (mapUrl.isBlank()) {
            return ResponseEntity.status(400).body(
                TransformResponse(
                    success = false,
                    bundle = null,
                    error = "no map_url given and no server-side default configured",
                ),
            )
        }
        return try {
            val bundle = gateway.transform(mapUrl, raw)
            ResponseEntity.ok(
                TransformResponse(
                    success = true,
                    bundle = bundle,
                    error = null,
                ),
            )
        } catch (ex: Exception) {
            log.warn("matchbox transform failed: {}", ex.message)
            ResponseEntity.ok(
                TransformResponse(
                    success = false,
                    bundle = null,
                    error = ex.message ?: ex.javaClass.simpleName,
                ),
            )
        }
    }
}

// -- JSON shapes -----------------------------------------------------------

data class MatchboxHealthResponse(
    val reachable: Boolean,
    val version: String?,
    @JsonProperty("base_url") val baseUrl: String,
    @JsonProperty("checked_at") val checkedAt: OffsetDateTime,
    @JsonProperty("response_time_ms") val responseTimeMs: Long,
    val error: String?,
)

data class StructureMapItem(
    val id: String,
    val url: String?,
    val name: String?,
    val title: String?,
    val status: String?,
    val version: String?,
)

data class StructureMapsResponse(
    val total: Int,
    val items: List<StructureMapItem>,
    val error: String?,
)

data class TransformRequest(
    @JsonProperty("source_format") val sourceFormat: String? = "hl7v2",
    @JsonProperty("raw_message") val rawMessage: String? = null,
    @JsonProperty("map_url") val mapUrl: String? = null,
)

data class TransformResponse(
    val success: Boolean,
    /**
     * The FHIR Bundle as a parsed JSON tree (so the UI can render it
     * pretty-printed without re-parsing on the client). `null` on
     * failure.
     */
    val bundle: Any?,
    val error: String?,
)

// -- Gateway abstraction --------------------------------------------------

/**
 * Internal abstraction over Matchbox HTTP so the controller can be tested
 * without standing up a real Matchbox server. The default implementation
 * is [MatchboxAdminGatewayImpl] below; tests replace it via
 * `@TestConfiguration` + `@Primary` (same pattern as
 * [HapiSubscriptionStatusClient] in [AdminSubscriptionsController]).
 */
interface MatchboxAdminGateway {
    /** Returns the Matchbox version string from `/fhir/metadata`, or null if unknown. */
    fun fetchMetadataVersion(): String?

    /** Returns the loaded StructureMaps as a normalised list. */
    fun listStructureMaps(): List<StructureMapItem>

    /** POSTs to Matchbox `$transform` and returns the resulting Bundle as a parsed JSON tree. */
    fun transform(structureMapCanonical: String, rawMessage: String): Any
}

@Component
class MatchboxAdminGatewayImpl(
    @Value("\${subscription-service.matchbox.base-url}") private val matchboxBaseUrl: String,
    @Value("\${subscription-service.matchbox.timeout-ms:30000}") private val timeoutMs: Int,
    private val fhirContext: FhirContext,
) : MatchboxAdminGateway {

    private val restTemplate: RestTemplate = RestTemplateBuilder()
        .connectTimeout(Duration.ofMillis(timeoutMs.toLong()))
        .readTimeout(Duration.ofMillis(timeoutMs.toLong()))
        .build()

    // Stock Jackson ObjectMapper - we re-parse the Bundle JSON into a
    // Map tree so Spring/Jackson re-serializes it inside our envelope.
    // Same approach AdminSubscriptionsController takes for FHIR resources.
    private val objectMapper = ObjectMapper()

    override fun fetchMetadataVersion(): String? {
        val url = "${matchboxBaseUrl.trimEnd('/')}/metadata"
        val headers = HttpHeaders().apply {
            accept = listOf(MediaType.parseMediaType("application/fhir+json"))
        }
        val entity: ResponseEntity<String> = restTemplate.exchange(
            url,
            org.springframework.http.HttpMethod.GET,
            HttpEntity<Void>(headers),
            String::class.java,
        )
        val body = entity.body ?: error("matchbox /metadata returned empty body")
        // Parse only enough to pull the version out. CapabilityStatement.software.version
        // is the canonical place; fall back to fhirVersion.
        val cs: CapabilityStatement = try {
            fhirContext.newJsonParser().parseResource(CapabilityStatement::class.java, body)
        } catch (_: Exception) {
            return null
        }
        return cs.software?.version
            ?: cs.implementation?.description
            ?: cs.fhirVersion?.toCode()
    }

    override fun listStructureMaps(): List<StructureMapItem> {
        val url = "${matchboxBaseUrl.trimEnd('/')}/StructureMap?_count=100"
        val headers = HttpHeaders().apply {
            accept = listOf(MediaType.parseMediaType("application/fhir+json"))
        }
        val entity: ResponseEntity<String> = restTemplate.exchange(
            url,
            org.springframework.http.HttpMethod.GET,
            HttpEntity<Void>(headers),
            String::class.java,
        )
        val body = entity.body ?: error("matchbox StructureMap search returned empty body")
        val bundle = fhirContext.newJsonParser().parseResource(Bundle::class.java, body)
        return bundle.entry.mapNotNull { entry ->
            val sm = entry.resource as? StructureMap ?: return@mapNotNull null
            StructureMapItem(
                id = sm.idElement?.idPart ?: "",
                url = sm.url,
                name = sm.name,
                title = sm.title,
                status = sm.status?.toCode(),
                version = sm.version,
            )
        }
    }

    override fun transform(structureMapCanonical: String, rawMessage: String): Any {
        val sourceParam = URLEncoder.encode(structureMapCanonical, StandardCharsets.UTF_8)
        val url = "${matchboxBaseUrl.trimEnd('/')}/StructureMap/\$transform?source=$sourceParam"
        val headers = HttpHeaders().apply {
            // Same content-type the worker's MatchboxClientImpl uses for
            // HL7 v2 ER7 input. v1 of this controller only supports v2;
            // when a second source format is added, branch on
            // request.sourceFormat.
            contentType = MediaType.parseMediaType("x-application/hl7-v2+er7")
            accept = listOf(MediaType.parseMediaType("application/fhir+json"))
        }
        val request = HttpEntity(rawMessage, headers)
        val response = restTemplate.postForEntity(url, request, String::class.java)
        val body = response.body
            ?: error("matchbox returned empty body (status=${response.statusCode})")
        // Return as a generic JSON tree so the controller envelope
        // contains the parsed Bundle directly (no double-encoded
        // string). Jackson handles Maps/Lists transparently.
        return objectMapper.readValue(body, Any::class.java)
    }
}
