package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import ca.uhn.fhir.context.FhirContext
import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpServer
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Observation
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import java.net.InetSocketAddress
import java.time.Duration
import java.time.Instant
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

/**
 * Wire-level integration test for the FHIR polling plugin
 * (ticket #434, TDD step 4).
 *
 * Stands up a tiny embedded FHIR server using the JDK's
 * [com.sun.net.httpserver.HttpServer] and serves hand-crafted
 * FHIR+JSON Bundle responses based on the `_lastUpdated` query
 * parameter the plugin sends. We then construct a real
 * [FhirPollingIngestSource] pointing at the embedded server, start
 * it, and verify:
 *
 *   - the SPI callback fires exactly 3 times,
 *   - the [PipelineMessage] envelope has the right
 *     sourceProtocol / sourceSystem / contentType / attributes,
 *   - the `raw` bytes are valid FHIR+JSON of each Observation,
 *   - subsequent polls return empty bundles (no further callbacks),
 *   - the HTTP server actually received the polling requests with
 *     the advanced `_lastUpdated` value on the second poll.
 *
 * Test runs in ~2-3s. The JDK HTTP server has no external
 * dependencies, no version conflict with HAPI's transitives, and no
 * Spring or Testcontainers overhead.
 *
 * Why not HAPI's `RestfulServer` + embedded Jetty: it works
 * conceptually, but the HAPI 8.10 + Jetty 12 + Spring Boot 3.5 BOM
 * combination ends up resolving incompatible versions on the test
 * classpath (jetty-server 11 stays pinned while jetty-io / jetty-util
 * get upgraded to 12 by Spring's BOM). The JDK HttpServer sidesteps
 * the dep tangle entirely while still exercising the real HAPI
 * IGenericClient -> wire -> response -> Bundle parsing path.
 */
class FhirPollingEndToEndTest {

    private lateinit var server: HttpServer
    private var port: Int = 0
    private lateinit var handler: TestFhirHandler
    private var ingestSource: FhirPollingIngestSource? = null

    @BeforeEach
    fun setUp() {
        server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        handler = TestFhirHandler()
        server.createContext("/fhir/Observation", handler)
        // HAPI's IGenericClient does a capabilities probe (GET
        // /metadata) on first use unless we disabled validation in the
        // context. We DO disable it (ServerValidationModeEnum.NEVER),
        // so the /metadata endpoint isn't strictly needed — but
        // registering a tiny stub makes the test more resilient to
        // future HAPI versions that probe regardless of the setting.
        server.createContext("/fhir/metadata") { exchange ->
            val body = """{"resourceType":"CapabilityStatement"}""".toByteArray(Charsets.UTF_8)
            exchange.responseHeaders.set("Content-Type", "application/fhir+json")
            exchange.sendResponseHeaders(200, body.size.toLong())
            exchange.responseBody.use { it.write(body) }
        }
        server.start()
        port = server.address.port
    }

    @AfterEach
    fun tearDown() {
        ingestSource?.stop()
        if (::server.isInitialized) {
            // Stop with a small grace period so any in-flight request
            // finishes; otherwise the HAPI client sees a connection
            // reset and the test's tearDown logs a noisy stack trace.
            server.stop(1)
        }
    }

    @Test
    fun `polls the FHIR server, emits PipelineMessages, then stops emitting after empty bundles`() {
        // Seed 3 Observations into the handler. The handler filters
        // them by the `_lastUpdated=gt...` query param the plugin
        // sends — first poll returns all 3, subsequent polls return
        // [].
        val fhirContext = FhirContext.forR4()
        handler.seed(
            listOf(
                makeObservation("obs-1", "2026-06-25T14:00:00Z"),
                makeObservation("obs-2", "2026-06-25T15:00:00Z"),
                makeObservation("obs-3", "2026-06-25T16:00:00Z"),
            ),
            fhirContext,
        )

        val captured = ConcurrentLinkedQueue<PipelineMessage>()

        val client = fhirContext.newRestfulGenericClient("http://127.0.0.1:$port/fhir")
        val executor = FhirSearchExecutor(client, fhirContext)
        val store = HighWaterMarkStore()

        val config = FhirPollingSourceConfig(
            id = "e2e-obs",
            enabled = true,
            baseUrl = "http://127.0.0.1:$port/fhir",
            pollIntervalSeconds = 1L,
            search = "Observation?_lastUpdated=gt{{lastRun}}",
            sourceSystem = "test-fhir",
        )

        val source = FhirPollingIngestSource(
            config = config,
            searchExecutor = executor,
            highWaterMarkStore = store,
        ).also { ingestSource = it }

        source.start { msg -> captured.add(msg) }

        await().atMost(Duration.ofSeconds(10)).until { captured.size == 3 }

        // Verify no additional callbacks fire after the high-water
        // mark advances past obs-3's lastUpdated.
        Thread.sleep(2_000)
        assertThat(captured).hasSize(3)

        val msgs = captured.toList().sortedBy { it.sourceId }
        val ids = msgs.map { it.sourceId }
        assertThat(ids).containsExactly("obs-1", "obs-2", "obs-3")

        msgs.forEach { msg ->
            assertThat(msg.sourceProtocol).isEqualTo("fhir-r4-polling")
            assertThat(msg.sourceSystem).isEqualTo("test-fhir")
            assertThat(msg.contentType).isEqualTo("application/fhir+json")
            assertThat(msg.attributes)
                .containsEntry("fhir.resourceType", "Observation")
                .containsEntry("fhir.pollingSourceId", "e2e-obs")
                .containsKey("fhir.lastUpdated")
                .containsKey("fhir.resourceId")
            // Compatibility shim for the engine's IngestSourceRegistry
            // (see KDoc on FhirPollingScheduler.buildPipelineMessage).
            assertThat(msg.attributes["hl7.messageType"]).isEqualTo("Observation")
            assertThat(msg.correlationId).matches("[0-9a-fA-F-]{36}")
            // The raw bytes are valid FHIR+JSON for this Observation.
            val parsed = fhirContext.newJsonParser()
                .parseResource(Observation::class.java, String(msg.raw, Charsets.UTF_8))
            assertThat(parsed.idElement.idPart).isEqualTo(msg.sourceId)
            assertThat(parsed.status).isEqualTo(Observation.ObservationStatus.FINAL)
        }

        // The handler must have been hit at least twice: once for the
        // initial poll (which returned 3 entries), at least once more
        // for a subsequent poll (which returned 0).
        assertThat(handler.requestCount.get()).isGreaterThanOrEqualTo(2)

        // On the FIRST request the search URL must use the sentinel
        // (epoch); on later requests it must use the advanced mark
        // (the newest lastUpdated of the seeded data,
        // 2026-06-25T16:00:00Z). HAPI's IGenericClient URL-encodes
        // colons in the path (`:` -> `%3A`), so we compare against
        // the URL-decoded form to keep the assertion readable. The
        // handler's URLDecoder ran before the value landed in
        // `requestUrls`, but the raw query is what we captured here
        // — decode for the assertion.
        val urls = handler.requestUrls.toList()
            .map { java.net.URLDecoder.decode(it, Charsets.UTF_8) }
        assertThat(urls.first())
            .describedAs("first poll uses the sentinel high-water mark (epoch)")
            .contains("_lastUpdated=gt1970-01-01T00:00:00Z")
        assertThat(urls.last())
            .describedAs("subsequent polls use the advanced mark")
            .contains("_lastUpdated=gt2026-06-25T16:00:00Z")
    }

    private fun makeObservation(id: String, lastUpdatedIso: String): Observation =
        Observation().apply {
            this.id = id
            status = Observation.ObservationStatus.FINAL
            meta.lastUpdated = java.util.Date.from(Instant.parse(lastUpdatedIso))
        }

    /**
     * Minimal FHIR server handler. Serves
     * `Observation?_lastUpdated=gt<iso>` queries by filtering the
     * seeded list and returning a searchset Bundle.
     *
     * Lives at class scope so the test fixture can configure seeded
     * data and inspect captured request URLs.
     */
    class TestFhirHandler : com.sun.net.httpserver.HttpHandler {
        private val observations: MutableList<Observation> = mutableListOf()
        private var fhirContextRef: FhirContext? = null

        /** Captured request paths + queries, in arrival order. */
        val requestUrls: CopyOnWriteArrayList<String> = CopyOnWriteArrayList()

        /** How many times the search endpoint has been hit. */
        val requestCount: AtomicInteger = AtomicInteger(0)

        fun seed(items: List<Observation>, fhirContext: FhirContext) {
            synchronized(this) {
                observations.clear()
                observations.addAll(items)
                fhirContextRef = fhirContext
            }
        }

        override fun handle(exchange: HttpExchange) {
            requestCount.incrementAndGet()
            val pathAndQuery = exchange.requestURI.let { uri ->
                if (uri.rawQuery != null) "${uri.path}?${uri.rawQuery}" else uri.path
            }
            requestUrls.add(pathAndQuery)

            val ctx = synchronized(this) { fhirContextRef }
                ?: return respondError(exchange, 500, "handler not seeded")

            // Parse `_lastUpdated=gt<iso>` out of the query. The
            // plugin sends EXACTLY this shape (it substitutes
            // `{{lastRun}}` into the literal `gt` prefix).
            val sinceParam = parseSince(exchange.requestURI.rawQuery)
            val since: Instant? = sinceParam?.let { runCatching { Instant.parse(it) }.getOrNull() }

            val matching = synchronized(this) {
                observations.filter { obs ->
                    val lastUpdated = obs.meta?.lastUpdated?.toInstant() ?: return@filter false
                    since == null || lastUpdated.isAfter(since)
                }
            }

            val bundle = Bundle().apply {
                type = Bundle.BundleType.SEARCHSET
                total = matching.size
                matching.forEach { obs ->
                    addEntry().apply {
                        fullUrl = "Observation/${obs.idElement.idPart}"
                        resource = obs
                    }
                }
            }

            val body = ctx.newJsonParser().encodeResourceToString(bundle).toByteArray(Charsets.UTF_8)
            exchange.responseHeaders.set("Content-Type", "application/fhir+json")
            exchange.sendResponseHeaders(200, body.size.toLong())
            exchange.responseBody.use { it.write(body) }
        }

        /**
         * Pull the `_lastUpdated` value (after the `gt` prefix) out
         * of a raw query string. Returns null when not present.
         * Permissive — the test only ever sends the `gt` form.
         */
        private fun parseSince(rawQuery: String?): String? {
            if (rawQuery.isNullOrEmpty()) return null
            for (pair in rawQuery.split('&')) {
                val idx = pair.indexOf('=')
                if (idx <= 0) continue
                val key = java.net.URLDecoder.decode(pair.substring(0, idx), Charsets.UTF_8)
                val value = java.net.URLDecoder.decode(pair.substring(idx + 1), Charsets.UTF_8)
                if (key == "_lastUpdated" && value.startsWith("gt")) {
                    return value.removePrefix("gt")
                }
            }
            return null
        }

        private fun respondError(exchange: HttpExchange, status: Int, message: String) {
            val body = message.toByteArray(Charsets.UTF_8)
            exchange.responseHeaders.set("Content-Type", "text/plain")
            exchange.sendResponseHeaders(status, body.size.toLong())
            exchange.responseBody.use { it.write(body) }
        }
    }
}
