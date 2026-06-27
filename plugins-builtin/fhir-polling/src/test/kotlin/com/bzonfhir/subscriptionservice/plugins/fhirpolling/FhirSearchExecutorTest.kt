package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.gclient.IQuery
import ca.uhn.fhir.rest.gclient.IUntypedQuery
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Observation
import org.junit.jupiter.api.Test
import org.mockito.kotlin.any
import org.mockito.kotlin.mock
import org.mockito.kotlin.verify
import org.mockito.kotlin.whenever
import org.mockito.kotlin.argumentCaptor
import java.time.Instant

/**
 * Tests for [FhirSearchExecutor].
 *
 * The executor's job is the narrow one of "substitute {{lastRun}}, ask
 * HAPI's IGenericClient to run the search, return the Bundle." We mock
 * IGenericClient here — the wire-level "did HAPI actually call our
 * test server" exercise is in `FhirPollingEndToEndTest` which stands
 * up an embedded FHIR REST server.
 *
 * Why a separate test from the e2e: the substitution logic deserves
 * pinning by itself. The e2e covers the happy path but not the dozen
 * variants of "what if the search has multiple `{{lastRun}}`s, or no
 * `{{lastRun}}` at all, or a malformed placeholder." Those are unit
 * concerns.
 */
class FhirSearchExecutorTest {

    private val fhirContext: FhirContext = FhirContext.forR4()

    @Test
    fun `replaces {{lastRun}} with the current high-water mark`() {
        val client = mock<IGenericClient>()
        val untyped = mock<IUntypedQuery<Bundle>>()
        val query = mock<IQuery<Bundle>>()
        val bundle = Bundle()
        whenever(client.search<Bundle>()).thenReturn(untyped)
        // HAPI's IUntypedQuery has a `byUrl(...).returnBundle(Bundle.class)`
        // shape. We stub the chain end-to-end so the executor's call
        // returns our canned Bundle.
        whenever(untyped.byUrl(any<String>())).thenReturn(query)
        whenever(query.returnBundle(Bundle::class.java)).thenReturn(query)
        whenever(query.execute()).thenReturn(bundle)

        val executor = FhirSearchExecutor(client)
        val mark = Instant.parse("2026-06-25T14:30:01Z")

        val result = executor.execute(
            "Observation?_lastUpdated=gt{{lastRun}}",
            mark,
        )

        // The Bundle the executor returned is the one HAPI's client gave us.
        assertThat(result).isSameAs(bundle)

        // Verify the URL HAPI was asked to fetch. byUrl() takes the
        // substituted URL — we want exact equality so a regression
        // that double-substitutes or quotes the timestamp shows up
        // immediately.
        val urlCaptor = argumentCaptor<String>()
        verify(untyped).byUrl(urlCaptor.capture())
        assertThat(urlCaptor.firstValue)
            .isEqualTo("Observation?_lastUpdated=gt2026-06-25T14:30:01Z")
    }

    @Test
    fun `search without placeholder passes through unchanged`() {
        val client = mock<IGenericClient>()
        val untyped = mock<IUntypedQuery<Bundle>>()
        val query = mock<IQuery<Bundle>>()
        whenever(client.search<Bundle>()).thenReturn(untyped)
        whenever(untyped.byUrl(any<String>())).thenReturn(query)
        whenever(query.returnBundle(Bundle::class.java)).thenReturn(query)
        whenever(query.execute()).thenReturn(Bundle())

        val executor = FhirSearchExecutor(client)

        executor.execute(
            "Patient?identifier=urn:oid:1.2.3|MRN123",
            Instant.parse("2026-06-25T14:30:01Z"),
        )

        val urlCaptor = argumentCaptor<String>()
        verify(untyped).byUrl(urlCaptor.capture())
        // No placeholder = no substitution. The literal pipe in the
        // search expression survives — HAPI's URL builder handles
        // any percent-encoding the FHIR server cares about.
        assertThat(urlCaptor.firstValue).isEqualTo("Patient?identifier=urn:oid:1.2.3|MRN123")
    }

    @Test
    fun `replaces multiple placeholders with the same mark`() {
        // Edge case: a search with `_lastUpdated=gt{{lastRun}}` AND
        // also `&date=gt{{lastRun}}`. Both should resolve to the same
        // value (one poll, one mark). Behaviour pinned so a future
        // refactor doesn't accidentally rewrite only the first one.
        val client = mock<IGenericClient>()
        val untyped = mock<IUntypedQuery<Bundle>>()
        val query = mock<IQuery<Bundle>>()
        whenever(client.search<Bundle>()).thenReturn(untyped)
        whenever(untyped.byUrl(any<String>())).thenReturn(query)
        whenever(query.returnBundle(Bundle::class.java)).thenReturn(query)
        whenever(query.execute()).thenReturn(Bundle())

        val executor = FhirSearchExecutor(client)
        val mark = Instant.parse("2026-06-25T14:30:01Z")

        executor.execute(
            "Encounter?_lastUpdated=gt{{lastRun}}&date=gt{{lastRun}}",
            mark,
        )

        val urlCaptor = argumentCaptor<String>()
        verify(untyped).byUrl(urlCaptor.capture())
        assertThat(urlCaptor.firstValue)
            .isEqualTo("Encounter?_lastUpdated=gt2026-06-25T14:30:01Z&date=gt2026-06-25T14:30:01Z")
    }

    @Test
    fun `extracts newest lastUpdated from bundle entries`() {
        // The extractor's role: walk the bundle's entries, find the
        // newest `Resource.meta.lastUpdated`, return it. Used by the
        // scheduler to advance the high-water mark after a successful
        // poll. Edge cases:
        //   - empty bundle -> null (caller doesn't advance the mark).
        //   - entry with no meta.lastUpdated -> skipped.
        //   - multi-entry, unsorted -> max wins.
        val executor = FhirSearchExecutor(mock())

        val bundle = Bundle().apply {
            addEntry().apply {
                resource = Observation().apply {
                    meta = meta.apply {
                        lastUpdated = Instant.parse("2026-06-25T14:00:00Z").toJavaUtilDate()
                    }
                }
            }
            addEntry().apply {
                resource = Observation().apply {
                    meta = meta.apply {
                        lastUpdated = Instant.parse("2026-06-25T15:30:01Z").toJavaUtilDate()
                    }
                }
            }
            addEntry().apply {
                resource = Observation().apply {
                    meta = meta.apply {
                        lastUpdated = Instant.parse("2026-06-25T13:00:00Z").toJavaUtilDate()
                    }
                }
            }
        }

        assertThat(executor.newestLastUpdated(bundle))
            .isEqualTo(Instant.parse("2026-06-25T15:30:01Z"))
    }

    @Test
    fun `newestLastUpdated returns null for empty bundle`() {
        val executor = FhirSearchExecutor(mock())
        assertThat(executor.newestLastUpdated(Bundle())).isNull()
    }

    @Test
    fun `newestLastUpdated skips entries without meta lastUpdated`() {
        val executor = FhirSearchExecutor(mock())
        val bundle = Bundle().apply {
            // One entry with no lastUpdated (meta will be null on a
            // bare Observation). The extractor must not NPE; it must
            // simply skip the entry.
            addEntry().apply { resource = Observation() }
        }

        assertThat(executor.newestLastUpdated(bundle)).isNull()
    }

    @Test
    fun `serializeEntry round-trips a resource through HAPI's JSON parser`() {
        // The plugin emits one PipelineMessage per Bundle entry, and
        // PipelineMessage.raw is the JSON of the resource as bytes.
        // We use HAPI's JsonParser, which is what the engine's mapping
        // layer expects on the read side. Verifying the round-trip
        // here is the cheap proof that the SerializerFamily we picked
        // is interoperable with the rest of the system.
        val executor = FhirSearchExecutor(mock(), fhirContext = fhirContext)
        val obs = Observation().apply {
            id = "obs-123"
            status = Observation.ObservationStatus.FINAL
        }

        val bytes = executor.serializeResource(obs)
        val parsed = fhirContext.newJsonParser().parseResource(String(bytes)) as Observation

        assertThat(parsed.idElement.idPart).isEqualTo("obs-123")
        assertThat(parsed.status).isEqualTo(Observation.ObservationStatus.FINAL)
    }

    /**
     * HAPI's Bundle.entry.resource.meta.lastUpdated is stored as a
     * java.util.Date. The helper converts our test Instants to that
     * type without leaking Kotlin's `Instant.toEpochMilli().let { ... }`
     * boilerplate through every test setup.
     */
    private fun Instant.toJavaUtilDate(): java.util.Date = java.util.Date.from(this)
}
