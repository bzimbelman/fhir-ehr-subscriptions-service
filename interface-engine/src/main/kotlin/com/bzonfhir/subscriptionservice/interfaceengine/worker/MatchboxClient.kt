package com.bzonfhir.subscriptionservice.interfaceengine.worker

import ca.uhn.fhir.context.FhirContext
import org.hl7.fhir.r4.model.Bundle
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.web.client.RestTemplateBuilder
import org.springframework.http.HttpEntity
import org.springframework.http.HttpHeaders
import org.springframework.http.MediaType
import org.springframework.stereotype.Component
import org.springframework.web.client.RestTemplate
import java.net.URLEncoder
import java.nio.charset.StandardCharsets
import java.time.Duration

/**
 * Thin HTTP client for Matchbox's `$transform` operation.
 *
 * Pulled out of the worker class so it can be swapped for a mock or stub
 * in tests (`@Primary` or `@MockBean`) without bringing in `RestTemplate`
 * mocking infrastructure. The interface-versus-implementation split is
 * deliberate — the worker depends on the interface, and the worker test
 * supplies a simple in-class implementation.
 *
 * The original `IngestRoutes` (pre-#381) called matchbox over Camel's
 * `toD()` so the response would participate in the route's error handler.
 * The async worker doesn't have a Camel pipeline anymore — `@Scheduled`
 * + JdbcTemplate is plenty — so a plain RestTemplate is the right tool.
 * Timeouts come from the same `subscription-service.matchbox.timeout-ms`
 * env var the Camel http component used to read; the legacy
 * `camel.component.http.*` block in `application.yaml` is now dead code
 * but is left in place because removing it is unrelated cleanup and
 * doesn't affect runtime (the worker isn't a Camel route).
 */
interface MatchboxClient {
    /**
     * POST `rawHl7` to matchbox's `StructureMap/$transform?source=<sm>`
     * and return the resulting R4 [Bundle]. Throws on any HTTP error,
     * timeout, or unparseable response.
     */
    fun transformToBundle(structureMapCanonical: String, rawHl7: String): Bundle
}

@Component
class MatchboxClientImpl(
    @Value("\${subscription-service.matchbox.base-url}") private val matchboxBaseUrl: String,
    @Value("\${subscription-service.matchbox.timeout-ms:30000}") private val timeoutMs: Int,
    private val fhirContext: FhirContext,
) : MatchboxClient {

    private val log = LoggerFactory.getLogger(MatchboxClientImpl::class.java)

    // Built lazily and held for the lifetime of the bean so the connection
    // pool / timeouts settle once. RestTemplateBuilder + Boot autoconfig
    // would also work, but doing it ourselves here keeps the bean
    // wiring explicit and easy to override in tests.
    private val restTemplate: RestTemplate = RestTemplateBuilder()
        .connectTimeout(Duration.ofMillis(timeoutMs.toLong()))
        .readTimeout(Duration.ofMillis(timeoutMs.toLong()))
        .build()

    override fun transformToBundle(structureMapCanonical: String, rawHl7: String): Bundle {
        val sourceParam = URLEncoder.encode(structureMapCanonical, StandardCharsets.UTF_8)
        val url = "$matchboxBaseUrl/StructureMap/\$transform?source=$sourceParam"
        val headers = HttpHeaders().apply {
            // Matchbox's StructureMap engine recognizes this content-type
            // as "interpret the body as HL7 v2 ER7 and parse before
            // applying the source StructureMap". Same content-type the
            // old Camel transform route used (see ticket #361 commit
            // b81da654's IngestRoutes.kt prepareMatchboxCall()).
            contentType = MediaType.parseMediaType("x-application/hl7-v2+er7")
            accept = listOf(MediaType.parseMediaType("application/fhir+json"))
        }
        val request = HttpEntity(rawHl7, headers)

        log.debug("matchbox transform url={} bytes={}", url, rawHl7.length)
        val response = restTemplate.postForEntity(url, request, String::class.java)
        val body = response.body
            ?: error("matchbox returned empty body (status=${response.statusCode})")

        // FhirContext.newJsonParser() is thread-safe per HAPI's own docs as
        // long as we don't configure it mid-parse; we use the defaults so a
        // single shared parser per call is fine. (Creating one per call is
        // also cheap enough — the expensive part is the FhirContext init,
        // which is a singleton bean.)
        return fhirContext.newJsonParser().parseResource(Bundle::class.java, body)
    }
}
