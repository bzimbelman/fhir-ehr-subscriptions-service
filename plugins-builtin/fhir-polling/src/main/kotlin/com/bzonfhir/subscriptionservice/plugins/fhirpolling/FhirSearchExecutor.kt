package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Resource
import java.time.Instant

/**
 * Executes one FHIR search via HAPI's [IGenericClient], substituting
 * `{{lastRun}}` placeholders with the supplied high-water mark.
 *
 * Lives at the seam between the [HighWaterMarkStore] (Instant in /
 * Instant out) and the FHIR server's wire surface (HTTP URLs + JSON
 * Bundles). Splitting this off from [FhirPollingScheduler] keeps the
 * scheduler purely about "when to tick" and lets the executor be
 * unit-tested in isolation.
 *
 * ## Substitution rules
 *
 * The placeholder is the literal seven-character string `{{lastRun}}`.
 * It's replaced with [Instant.toString] applied to the supplied mark,
 * which produces a FHIR-friendly ISO-8601 instant (e.g.
 * `2026-06-25T14:30:01Z`). FHIR's `_lastUpdated` parameter expects
 * exactly this shape; no quoting or URL-encoding is needed.
 *
 * Multiple placeholders are all replaced with the same mark — there's
 * one mark per source, so it would be ambiguous for them to mean
 * different things. The (rare) case of "search uses lastRun more than
 * once" is exercised by [FhirSearchExecutorTest].
 *
 * Searches without the placeholder are passed through unchanged. This
 * lets operators configure stateless searches (e.g.
 * `Patient?identifier=urn:oid:1.2.3|MRN123`) when they don't need
 * incremental polling.
 *
 * ## Why byUrl(...) and not the typed search DSL
 *
 * HAPI's `client.search().forResource(Observation.class)
 * .where(Observation.SUBJECT.hasId("Patient/123"))` builder is
 * type-safe but only works for "canonical" FHIR search params known
 * at compile time. Vendor profiles (Athena, Epic) regularly extend
 * FHIR with custom search parameters that aren't in the HAPI R4
 * model. `byUrl(...)` accepts an arbitrary URL string the server
 * understands — operators can configure any search they want without
 * waiting for HAPI to model the vendor's params.
 *
 * ## Newest lastUpdated extraction
 *
 * [newestLastUpdated] walks the Bundle's entries and returns the max
 * `Resource.meta.lastUpdated`. The scheduler uses this to advance
 * the high-water mark after a successful poll. Returns null for an
 * empty bundle so the caller knows not to advance the mark (advancing
 * to "now" in that case would skip resources the next poll should
 * have picked up).
 */
class FhirSearchExecutor(
    private val client: IGenericClient,
    private val fhirContext: FhirContext = FhirContext.forR4(),
) {

    /**
     * Substitute [Instant.toString] of [mark] into every `{{lastRun}}`
     * occurrence in [searchTemplate], then execute the search and
     * return the Bundle.
     *
     * Errors bubble. HAPI throws `ca.uhn.fhir.rest.server.exceptions.BaseServerResponseException`
     * subclasses for HTTP-level failures (auth rejected, malformed
     * search). The caller (the scheduler tick) catches and logs them
     * without advancing the high-water mark — the next tick will
     * retry the same search.
     */
    fun execute(searchTemplate: String, mark: Instant): Bundle {
        val resolvedUrl = searchTemplate.replace(LAST_RUN_PLACEHOLDER, mark.toString())
        return client.search<Bundle>()
            .byUrl(resolvedUrl)
            .returnBundle(Bundle::class.java)
            .execute()
    }

    /**
     * Walk [bundle.entry] and return the maximum `Resource.meta.lastUpdated`,
     * or null if the bundle is empty / no entry has a lastUpdated.
     *
     * Used by the scheduler to advance the high-water mark. The
     * scheduler treats null as "don't advance" — important because an
     * empty bundle is legit (server has nothing new since lastRun)
     * but we mustn't jump the mark to "now," which would skip
     * resources that might still be in flight server-side and arrive
     * before the next tick.
     */
    fun newestLastUpdated(bundle: Bundle): Instant? =
        bundle.entry
            .asSequence()
            .mapNotNull { it.resource?.meta?.lastUpdated }
            .map { it.toInstant() }
            .maxOrNull()

    /**
     * Serialize [resource] to JSON bytes using HAPI's R4 JsonParser.
     *
     * The plugin emits one PipelineMessage per Bundle entry, and the
     * `raw` field of each is the resource as JSON. Using HAPI's
     * canonical serializer matches what the engine's downstream
     * mapping layer expects — re-parsing this with
     * `fhirContext.newJsonParser().parseResource(...)` produces the
     * same object graph the plugin started with (modulo any
     * server-side fields the FHIR server stamped before returning).
     */
    fun serializeResource(resource: Resource): ByteArray =
        fhirContext.newJsonParser().encodeResourceToString(resource).toByteArray(Charsets.UTF_8)

    companion object {
        /**
         * The placeholder token operators write in their search
         * expression. Kept here (not inlined) so a future change of
         * the syntax (e.g. supporting `{{lastRun:iso8601}}` for
         * format-specific substitution) has a single place to edit.
         */
        const val LAST_RUN_PLACEHOLDER: String = "{{lastRun}}"
    }
}
