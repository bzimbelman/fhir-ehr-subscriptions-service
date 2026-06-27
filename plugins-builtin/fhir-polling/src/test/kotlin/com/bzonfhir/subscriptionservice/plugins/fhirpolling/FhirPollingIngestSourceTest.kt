package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.gclient.IQuery
import ca.uhn.fhir.rest.gclient.IUntypedQuery
import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Observation
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Test
import org.mockito.kotlin.any
import org.mockito.kotlin.mock
import org.mockito.kotlin.whenever
import java.time.Duration
import java.time.Instant
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicInteger

/**
 * SPI-lifecycle contract tests for [FhirPollingIngestSource].
 *
 * These pin the SPI shape — `meta`, `protocol`, the start/stop
 * lifecycle. The wire-level "did HAPI actually fetch from a real FHIR
 * server" check is in `FhirPollingEndToEndTest`.
 *
 * Each test uses a mocked HAPI [IGenericClient] so the scheduler tick
 * stays deterministic — we can control exactly what the search
 * returns, when the bundle has 0 vs N entries, etc.
 */
class FhirPollingIngestSourceTest {

    private var ingestSource: FhirPollingIngestSource? = null

    @AfterEach
    fun tearDown() {
        ingestSource?.stop()
    }

    private fun configuredSource(
        id: String = "test-obs",
        pollIntervalSeconds: Long = 1L,
        search: String = "Observation?_lastUpdated=gt{{lastRun}}",
        sourceSystem: String = "test",
    ) = FhirPollingSourceConfig(
        id = id,
        enabled = true,
        baseUrl = "https://example.test/fhir",
        pollIntervalSeconds = pollIntervalSeconds,
        search = search,
        sourceSystem = sourceSystem,
    )

    /**
     * Build a mock client whose search().byUrl(...).returnBundle(...)
     * .execute() returns [bundle]. The bundle is recomputed on each
     * call so a test can mutate it between ticks (e.g. "first tick
     * returns 3 entries, subsequent ticks return 0").
     */
    private fun mockClient(bundleSupplier: () -> Bundle): IGenericClient {
        val client = mock<IGenericClient>()
        val untyped = mock<IUntypedQuery<Bundle>>()
        val query = mock<IQuery<Bundle>>()
        whenever(client.search<Bundle>()).thenReturn(untyped)
        whenever(untyped.byUrl(any())).thenReturn(query)
        whenever(query.returnBundle(Bundle::class.java)).thenReturn(query)
        whenever(query.execute()).thenAnswer { bundleSupplier() }
        return client
    }

    private fun build(
        config: FhirPollingSourceConfig,
        client: IGenericClient,
    ): FhirPollingIngestSource {
        val executor = FhirSearchExecutor(client, FhirContext.forR4())
        val store = HighWaterMarkStore()
        return FhirPollingIngestSource(
            config = config,
            searchExecutor = executor,
            highWaterMarkStore = store,
        ).also { ingestSource = it }
    }

    @Test
    fun `meta is the canonical fhir-polling first-party plugin descriptor`() {
        val source = build(configuredSource(), mockClient { Bundle() })

        // id is suffixed with the configured source id so multiple
        // FhirPollingIngestSource beans co-exist in the operator UI's
        // plugin listing without colliding.
        assertThat(source.meta.id).isEqualTo("fhir-polling:test-obs")
        assertThat(source.meta.supplier).isEqualTo(PluginSupplier.FIRST_PARTY)
        assertThat(source.meta.schemaVersion).isEqualTo(1)
        assertThat(source.meta.version).matches("\\d+\\.\\d+\\.\\d+(-.+)?")
    }

    @Test
    fun `protocol is fhir-r4-polling`() {
        val source = build(configuredSource(), mockClient { Bundle() })
        assertThat(source.protocol).isEqualTo("fhir-r4-polling")
    }

    @Test
    fun `start fires the callback once per bundle entry`() {
        val captured = ConcurrentLinkedQueue<PipelineMessage>()
        val firstPollDone = AtomicInteger(0)
        val client = mockClient {
            if (firstPollDone.getAndIncrement() == 0) {
                Bundle().apply {
                    addEntry().apply {
                        resource = Observation().apply {
                            id = "obs-1"
                            meta = meta.apply {
                                lastUpdated = java.util.Date.from(
                                    Instant.parse("2026-06-25T14:00:00Z"),
                                )
                            }
                        }
                    }
                    addEntry().apply {
                        resource = Observation().apply {
                            id = "obs-2"
                            meta = meta.apply {
                                lastUpdated = java.util.Date.from(
                                    Instant.parse("2026-06-25T15:00:00Z"),
                                )
                            }
                        }
                    }
                    addEntry().apply {
                        resource = Observation().apply {
                            id = "obs-3"
                            meta = meta.apply {
                                lastUpdated = java.util.Date.from(
                                    Instant.parse("2026-06-25T16:00:00Z"),
                                )
                            }
                        }
                    }
                }
            } else {
                Bundle()
            }
        }
        val source = build(configuredSource(), client)

        source.start { msg -> captured.add(msg) }

        // Awaitility-poll until all three messages have arrived. The
        // scheduler runs on a 1s cadence so this is well-bounded.
        await().atMost(Duration.ofSeconds(10)).until { captured.size == 3 }

        val ids = captured.map { it.sourceId }.sorted()
        assertThat(ids).containsExactly("obs-1", "obs-2", "obs-3")

        // Verify the message envelope is right.
        val msg = captured.first { it.sourceId == "obs-2" }
        assertThat(msg.sourceProtocol).isEqualTo("fhir-r4-polling")
        assertThat(msg.sourceSystem).isEqualTo("test")
        assertThat(msg.contentType).isEqualTo("application/fhir+json")
        assertThat(msg.attributes)
            .containsEntry("fhir.resourceType", "Observation")
            .containsEntry("fhir.resourceId", "obs-2")
            .containsEntry("fhir.lastUpdated", "2026-06-25T15:00:00Z")
            // hl7.messageType is set as a compatibility shim so the
            // engine's IngestSourceRegistry, which today requires that
            // attribute, accepts the message. Value is the FHIR
            // resource type. See plugin README for the rationale.
            .containsEntry("hl7.messageType", "Observation")
        // raw bytes are HAPI's JSON encoding of the resource.
        assertThat(String(msg.raw, Charsets.UTF_8))
            .contains("\"resourceType\":\"Observation\"")
            .contains("\"id\":\"obs-2\"")
        assertThat(msg.correlationId).matches("[0-9a-fA-F-]{36}")
    }

    @Test
    fun `stop halts the scheduler`() {
        val callbackCount = AtomicInteger(0)
        val client = mockClient { Bundle() }
        val source = build(configuredSource(pollIntervalSeconds = 1L), client)

        source.start { _ -> callbackCount.incrementAndGet() }

        // Give the scheduler a moment to tick. Empty bundle -> zero
        // callbacks. Either way, the scheduler should keep ticking
        // until we stop it.
        Thread.sleep(1_500)

        source.stop()

        val countAtStop = callbackCount.get()
        // After stop(), the scheduler must NOT tick again. Wait
        // generously and verify the count hasn't changed.
        Thread.sleep(2_000)
        assertThat(callbackCount.get()).isEqualTo(countAtStop)
    }

    @Test
    fun `stop is idempotent`() {
        val source = build(configuredSource(), mockClient { Bundle() })
        source.start { /* unused */ }
        source.stop()
        // Second stop must not throw — SPI requires idempotent stop().
        source.stop()
    }

    @Test
    fun `disabled source does not poll`() {
        val callbackCount = AtomicInteger(0)
        val client = mockClient {
            Bundle().apply {
                addEntry().resource = Observation().apply { id = "obs-1" }
            }
        }
        // Per-source disabled — bean still constructed but the start()
        // call is a no-op.
        val source = build(
            configuredSource().copy(enabled = false),
            client,
        )

        source.start { _ -> callbackCount.incrementAndGet() }

        Thread.sleep(2_000)
        assertThat(callbackCount.get()).isEqualTo(0)
    }

    @Test
    fun `high-water mark advances after a successful poll`() {
        // Behavioural pin: when the first poll returns entries, the
        // next poll's URL contains the newest lastUpdated, not the
        // sentinel epoch. We catch the byUrl argument on the SECOND
        // call to verify the substitution.
        val byUrlCalls = ConcurrentLinkedQueue<String>()
        val client = mock<IGenericClient>()
        val untyped = mock<IUntypedQuery<Bundle>>()
        val query = mock<IQuery<Bundle>>()
        val pollCounter = AtomicInteger(0)
        whenever(client.search<Bundle>()).thenReturn(untyped)
        whenever(untyped.byUrl(any())).thenAnswer { invocation ->
            byUrlCalls.add(invocation.getArgument<String>(0))
            query
        }
        whenever(query.returnBundle(Bundle::class.java)).thenReturn(query)
        whenever(query.execute()).thenAnswer {
            if (pollCounter.getAndIncrement() == 0) {
                Bundle().apply {
                    addEntry().apply {
                        resource = Observation().apply {
                            id = "obs-1"
                            meta = meta.apply {
                                lastUpdated = java.util.Date.from(
                                    Instant.parse("2026-06-25T14:30:01Z"),
                                )
                            }
                        }
                    }
                }
            } else {
                Bundle()
            }
        }

        val source = build(configuredSource(pollIntervalSeconds = 1L), client)
        source.start { /* unused */ }

        // Wait until we've made at least 2 byUrl calls.
        await().atMost(Duration.ofSeconds(10)).until { byUrlCalls.size >= 2 }

        val urls = byUrlCalls.toList()
        // First call substitutes the sentinel.
        assertThat(urls[0])
            .isEqualTo("Observation?_lastUpdated=gt1970-01-01T00:00:00Z")
        // Subsequent call uses the advanced mark.
        assertThat(urls[1])
            .isEqualTo("Observation?_lastUpdated=gt2026-06-25T14:30:01Z")
    }

    @Test
    fun `start after stop spins the scheduler back up`() {
        // Restart cycle: start() after stop() must re-run polls. SPI
        // doesn't forbid the cycle so we support it for parity with
        // the HL7 v2 plugin's behaviour.
        val callbackCount = AtomicInteger(0)
        val client = mockClient {
            Bundle().apply {
                addEntry().apply {
                    resource = Observation().apply {
                        id = "obs-restart"
                        meta = meta.apply {
                            lastUpdated = java.util.Date.from(Instant.now())
                        }
                    }
                }
            }
        }
        val source = build(configuredSource(pollIntervalSeconds = 1L), client)

        source.start { _ -> callbackCount.incrementAndGet() }
        await().atMost(Duration.ofSeconds(5)).until { callbackCount.get() > 0 }
        source.stop()

        val firstCount = callbackCount.get()
        Thread.sleep(1_500) // No callbacks expected here.
        assertThat(callbackCount.get()).isEqualTo(firstCount)

        source.start { _ -> callbackCount.incrementAndGet() }
        await().atMost(Duration.ofSeconds(5)).until { callbackCount.get() > firstCount }
    }
}
