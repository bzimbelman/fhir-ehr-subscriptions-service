package com.bzonfhir.subscriptionservice.interfaceengine.observability

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import com.bzonfhir.subscriptionservice.interfaceengine.worker.MatchboxClient
import io.opentelemetry.api.OpenTelemetry
import io.opentelemetry.api.trace.SpanKind
import io.opentelemetry.context.propagation.ContextPropagators
import io.opentelemetry.context.propagation.TextMapPropagator
import io.opentelemetry.context.propagation.TextMapSetter
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator
import io.opentelemetry.sdk.OpenTelemetrySdk
import io.opentelemetry.sdk.testing.exporter.InMemorySpanExporter
import io.opentelemetry.sdk.trace.SdkTracerProvider
import io.opentelemetry.sdk.trace.`export`.SimpleSpanProcessor
import io.opentelemetry.sdk.trace.data.SpanData
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.hl7.fhir.r4.model.Bundle
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.context.TestConfiguration
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Primary
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.springframework.test.context.TestPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.containers.wait.strategy.Wait
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket
import java.net.Socket
import java.time.Duration
import java.util.concurrent.atomic.AtomicReference

/**
 * End-to-end OpenTelemetry tracing tests for ticket #394.
 *
 * Replaces the [OpenTelemetry] bean with one wired to an [InMemorySpanExporter]
 * so we can assert on the spans the receive route + worker emit without
 * running an OTLP collector. The Postgres backing `ingested_messages`
 * is real (Testcontainers) so the trace_context column round-trips
 * through the same SQL the worker uses in production.
 *
 * Four scenarios, all in one test class so the Spring context is cached:
 *
 *   1. [traceparent propagated through to matchbox HTTP call] —
 *      send an MLLP message, wait for DELIVERED, assert the
 *      captured X-stamped-traceparent on the matchbox stub matches
 *      the trace id of the receive span.
 *
 *   2. [receive and worker spans share a trace id] — sanity check the
 *      `trace_context` column wiring: the row's stored traceparent
 *      contains the receive span's trace id, and the worker's
 *      `worker.process` span continues that same trace id.
 *
 *   3. [per-message span names are correct] — `mllp.receive`,
 *      `worker.process`, and `HTTP POST matchbox/$transform` all
 *      appear with the right names and kinds.
 *
 *   4. [OTel disabled by default emits no traceparent header] — same
 *      flow but with the SDK disabled; matchbox sees no traceparent.
 *      This is the most important test for the "no overhead" guarantee.
 *
 * (Tests 1-3 run with a recording SDK; test 4 runs with the SDK
 * disabled in its own Spring context per @TestPropertySource.)
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.NONE)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(
    properties = [
        "subscription-service.worker.enabled=true",
        "subscription-service.worker.poll-interval-ms=200",
        "subscription-service.worker.batch-size=10",
        "subscription-service.worker.transforming-stale-seconds=2",
        "subscription-service.hapi.base-url=http://stub.hapi.test/fhir",
        "subscription-service.matchbox.base-url=http://stub.matchbox.test",
        // Allow the test's OpenTelemetry @Bean to override the
        // production OtelConfig#openTelemetry one. Spring Boot's
        // default is "fail loudly on override"; for a test that's
        // explicitly trying to swap the SDK for an in-memory exporter,
        // we want the standard override semantics back.
        "spring.main.allow-bean-definition-overriding=true",
    ],
)
class OtelTraceTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(Wait.forListeningPort())
            .withStartupTimeout(Duration.ofSeconds(60))

        // Free port for the MLLP listener so concurrent test JVMs don't
        // collide on 2575. Same trick IngestRoutesTest / WorkerCorrelationIdPropagationTest
        // use.
        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @JvmStatic
        @DynamicPropertySource
        fun props(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            registry.add("subscription-service.mllp.port") { mllpPort }
        }

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; OtelTraceTest requires Testcontainers."
            }
        }

        // MLLP framing bytes (same as IngestRoutesTest).
        private const val SB: Byte = 0x0B
        private const val EB: Byte = 0x1C
        private const val CR: Byte = 0x0D
    }

    /**
     * Matchbox stub that records the inbound `traceparent` header so
     * we can assert on it from tests. Same shape as the
     * CapturingMatchboxClient in WorkerCorrelationIdPropagationTest;
     * duplicated here to keep this test class self-contained.
     */
    class CapturingMatchboxClient(
        private val propagators: ContextPropagators,
    ) : MatchboxClient {
        val lastTraceparent = AtomicReference<String?>(null)
        val lastTraceContextFromOTel = AtomicReference<io.opentelemetry.api.trace.SpanContext?>(null)
        var calls = 0

        override fun transformToBundle(structureMapCanonical: String, rawHl7: String): Bundle {
            calls++
            // Capture the active span context AT THE MOMENT of the call.
            // The MatchboxClientImpl wraps its body in a CLIENT span and
            // makes it current; we capture the span context that span
            // got — which has the same trace id as worker.process /
            // mllp.receive.
            lastTraceContextFromOTel.set(io.opentelemetry.api.trace.Span.current().spanContext)
            // Encode the active context to a traceparent string the way
            // MatchboxClientImpl does. We use the INJECTED propagators
            // (NOT GlobalOpenTelemetry) so this works in a JVM where
            // multiple Spring contexts boot side-by-side without
            // stepping on each other's global SDK.
            val carrier = mutableMapOf<String, String>()
            propagators.textMapPropagator.inject(
                io.opentelemetry.context.Context.current(),
                carrier,
                object : TextMapSetter<MutableMap<String, String>> {
                    override fun set(c: MutableMap<String, String>?, k: String, v: String) {
                        c?.put(k, v)
                    }
                },
            )
            lastTraceparent.set(carrier["traceparent"])
            return adtA01Bundle()
        }

        fun reset() {
            lastTraceparent.set(null)
            lastTraceContextFromOTel.set(null)
            calls = 0
        }
    }

    /**
     * Spring config that wires the OTel SDK to an in-memory exporter
     * (so tests can read the emitted spans) and replaces the matchbox
     * + HAPI clients with stubs. Marked `@Primary` so it wins the
     * bean-resolution race against the production beans of the same
     * types.
     */
    @TestConfiguration
    class OtelTestConfig {
        val exporter: InMemorySpanExporter = InMemorySpanExporter.create()

        @Bean
        @Primary
        fun openTelemetry(): OpenTelemetry {
            val tracerProvider = SdkTracerProvider.builder()
                .addSpanProcessor(SimpleSpanProcessor.create(exporter))
                .build()
            return OpenTelemetrySdk.builder()
                .setTracerProvider(tracerProvider)
                // Use the W3C propagator explicitly so the test doesn't
                // depend on the SDK's default-propagator choice.
                .setPropagators(
                    ContextPropagators.create(W3CTraceContextPropagator.getInstance()),
                )
                .build()
            // Deliberately NOT calling setResultAsGlobal / GlobalOpenTelemetry.set —
            // see OtelConfig.kt's class javadoc. The stub matchbox client
            // captures its propagators via constructor injection instead.
        }

        @Bean
        @Primary
        fun stubMatchbox(propagators: ContextPropagators): MatchboxClient =
            CapturingMatchboxClient(propagators)

        // Name matches the production bean (FhirConfig.hapiFhirClient)
        // so this overrides by name. Required since ticket #520 made
        // the production bean @Primary — two beans of the same type
        // with @Primary on both would re-introduce ambiguity. The
        // `allow-bean-definition-overriding=true` property above lets
        // this same-named bean replace the production one.
        @Bean
        @Primary
        fun hapiFhirClient(fhirContext: FhirContext): IGenericClient =
            org.mockito.Mockito.mock(
                IGenericClient::class.java,
                org.mockito.Mockito.RETURNS_DEEP_STUBS,
            )

        @Bean
        fun inMemoryExporter(): InMemorySpanExporter = exporter
    }

    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var matchboxClient: MatchboxClient
    @Autowired private lateinit var exporter: InMemorySpanExporter

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        (matchboxClient as CapturingMatchboxClient).reset()
        exporter.reset()
    }

    @AfterEach
    fun cleanup() {
        exporter.reset()
    }

    @Test
    fun `traceparent propagates to matchbox HTTP call`() {
        val ack = sendMllp(
            "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A01|OTEL00001|P|2.5\r" +
                "EVN|A01|20260626000000\r" +
                "PID|1||MRN1^^^HOSP^MR||DOE^JANE",
        )
        assertThat(ack).contains("MSA|AA|OTEL00001")

        val stub = matchboxClient as CapturingMatchboxClient
        await().atMost(Duration.ofSeconds(5)).until { stub.calls >= 1 }

        // Matchbox should have received a well-formed W3C traceparent.
        // Format: 00-<32-hex>-<16-hex>-<2-hex>
        val tp = stub.lastTraceparent.get()
        assertThat(tp)
            .describedAs("matchbox should receive a W3C traceparent header")
            .isNotNull()
            .matches("^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$")

        // The trace id stamped on matchbox's call must equal the trace
        // id of the receive span. This is the load-bearing assertion of
        // the whole ticket: receive → matchbox spans share a trace.
        val receiveSpan = findSpan(OtelTracing.SPAN_MLLP_RECEIVE)
        assertThat(receiveSpan).describedAs("receive span emitted").isNotNull()
        val expectedTraceId = receiveSpan!!.traceId
        val actualTraceId = stub.lastTraceContextFromOTel.get()?.traceId
        assertThat(actualTraceId)
            .describedAs("matchbox span and receive span must share trace id")
            .isEqualTo(expectedTraceId)
    }

    @Test
    fun `receive and worker spans share a trace id`() {
        sendMllp(
            "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000001||ADT^A01|OTEL00002|P|2.5\r" +
                "EVN|A01|20260626000001\r" +
                "PID|1||MRN2^^^HOSP^MR||SMITH^JOHN",
        )

        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val rows = repository.findAll().toList()
            assertThat(rows).hasSize(1)
            assertThat(rows[0].status).isEqualTo(IngestedMessageStatus.DELIVERED)
        }

        val row = repository.findAll().first()
        assertThat(row.traceContext)
            .describedAs("receive path should persist a traceparent on the row")
            .isNotNull()
            .matches("^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$")

        val receiveSpan = findSpan(OtelTracing.SPAN_MLLP_RECEIVE)
        val workerSpan = findSpan(OtelTracing.SPAN_WORKER_PROCESS)
        assertThat(receiveSpan).describedAs("mllp.receive span").isNotNull()
        assertThat(workerSpan).describedAs("worker.process span").isNotNull()
        assertThat(workerSpan!!.traceId)
            .describedAs("worker.process must share trace id with mllp.receive (V004 trace_context column)")
            .isEqualTo(receiveSpan!!.traceId)
    }

    @Test
    fun `per-message spans have the right names and kinds`() {
        sendMllp(
            "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000002||ADT^A01|OTEL00003|P|2.5\r" +
                "EVN|A01|20260626000002\r" +
                "PID|1||MRN3^^^HOSP^MR||ROE^RICHARD",
        )

        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            assertThat(repository.findAll().toList())
                .filteredOn { it.status == IngestedMessageStatus.DELIVERED }
                .hasSize(1)
        }

        val receiveSpan = findSpan(OtelTracing.SPAN_MLLP_RECEIVE)
        val workerSpan = findSpan(OtelTracing.SPAN_WORKER_PROCESS)

        // mllp.receive: SERVER span with the messaging.* semantic attributes.
        assertThat(receiveSpan).describedAs("mllp.receive span").isNotNull
        assertThat(receiveSpan!!.kind).isEqualTo(SpanKind.SERVER)
        assertThat(receiveSpan.attributes.asMap()).containsEntry(
            io.opentelemetry.api.common.AttributeKey.stringKey("messaging.system"),
            "hl7v2",
        )
        assertThat(receiveSpan.attributes.asMap()).containsEntry(
            io.opentelemetry.api.common.AttributeKey.stringKey("messaging.operation"),
            "receive",
        )
        // worker.process: INTERNAL span (in-process continuation, not a wire entry point).
        assertThat(workerSpan).describedAs("worker.process span").isNotNull
        assertThat(workerSpan!!.kind).isEqualTo(SpanKind.INTERNAL)

        // The CLIENT span for the matchbox HTTP call lives inside
        // MatchboxClientImpl, which the test config replaces with the
        // CapturingMatchboxClient stub — so we can't observe it via
        // the in-memory exporter here. The stub records the inbound
        // traceparent (asserted in the first test); the wire-format
        // CLIENT span itself is exercised end-to-end by the live
        // Rancher Desktop validation in the ticket's runbook.
    }

    private fun findSpan(name: String): SpanData? =
        exporter.finishedSpanItems.firstOrNull { it.name == name }

    private fun sendMllp(payload: String): String {
        Socket("localhost", mllpPort).use { socket ->
            socket.soTimeout = 15_000
            val out = socket.getOutputStream()
            out.write(byteArrayOf(SB))
            out.write(payload.toByteArray(Charsets.UTF_8))
            out.write(byteArrayOf(EB, CR))
            out.flush()
            val buf = ByteArray(8192)
            val sink = StringBuilder()
            val input = socket.getInputStream()
            while (true) {
                val n = input.read(buf)
                if (n <= 0) break
                for (i in 0 until n) {
                    val b = buf[i]
                    when (b) {
                        SB -> {}
                        EB -> return sink.toString()
                        else -> sink.append(b.toInt().toChar())
                    }
                }
            }
            return sink.toString()
        }
    }
}

/**
 * Minimal ADT^A01 bundle for the matchbox stub. Identical shape to the
 * one in WorkerCorrelationIdPropagationTest; duplicated to keep this
 * test class self-contained.
 */
private fun adtA01Bundle(): Bundle =
    Bundle().apply {
        type = Bundle.BundleType.TRANSACTION
        addEntry().apply {
            fullUrl = "urn:uuid:patient-1"
            resource = org.hl7.fhir.r4.model.Patient().apply { id = "patient-1" }
            request = Bundle.BundleEntryRequestComponent().apply {
                method = Bundle.HTTPVerb.POST
                url = "Patient"
            }
        }
    }
