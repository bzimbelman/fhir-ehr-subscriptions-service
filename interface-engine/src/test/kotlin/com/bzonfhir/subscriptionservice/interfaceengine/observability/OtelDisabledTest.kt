package com.bzonfhir.subscriptionservice.interfaceengine.observability

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import com.bzonfhir.subscriptionservice.interfaceengine.worker.MatchboxClient
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.hl7.fhir.r4.model.Bundle
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
 * The "OTel disabled by default emits zero overhead" test (ticket #394).
 *
 * Sets `OTEL_SDK_DISABLED=true` via @TestPropertySource so the
 * AutoConfiguredOpenTelemetrySdk builds a no-op SDK. Sends an MLLP
 * message, waits for delivery, and asserts:
 *
 *   - the matchbox stub saw no `traceparent` header (the propagator's
 *     inject is a no-op when the active context is the SDK's no-op root),
 *   - the row's `trace_context` column is NULL (encodeCurrentContext
 *     returns null in the disabled case).
 *
 * Lives in its own test class because the Spring context shape differs
 * from [OtelTraceTest] — Spring caches per @TestPropertySource hash, so
 * giving each disabled/enabled flavour its own class avoids context
 * reload mid-suite.
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
        // Disable the SDK at the env-var level the autoconfigure module
        // reads. AutoConfiguredOpenTelemetrySdk reads otel.* properties
        // via the same property source Spring exposes, so this works
        // without setting a real OS env var.
        "otel.sdk.disabled=true",
        // Ticket #520: stubs override the @Primary production
        // `hapiFhirClient` bean by name. See IngestedMessageWorkerTest
        // for the rationale.
        "spring.main.allow-bean-definition-overriding=true",
    ],
)
class OtelDisabledTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(Wait.forListeningPort())
            .withStartupTimeout(Duration.ofSeconds(60))

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
                "Docker is not available; OtelDisabledTest requires Testcontainers."
            }
        }

        private const val SB: Byte = 0x0B
        private const val EB: Byte = 0x1C
        private const val CR: Byte = 0x0D
    }

    class HeaderCapturingMatchbox(
        private val propagators: io.opentelemetry.context.propagation.ContextPropagators,
    ) : MatchboxClient {
        val lastTraceparent = AtomicReference<String?>(null)
        var calls = 0
        override fun transformToBundle(structureMapCanonical: String, rawHl7: String): Bundle {
            calls++
            // Use the INJECTED propagators (the same ones the production
            // ContextPropagators bean exposes). When the SDK is disabled
            // the active context is the no-op root and inject(...) is a
            // no-op — captured as null below.
            val carrier = mutableMapOf<String, String>()
            propagators.textMapPropagator.inject(
                io.opentelemetry.context.Context.current(),
                carrier,
                object : io.opentelemetry.context.propagation.TextMapSetter<MutableMap<String, String>> {
                    override fun set(c: MutableMap<String, String>?, k: String, v: String) {
                        c?.put(k, v)
                    }
                },
            )
            lastTraceparent.set(carrier["traceparent"])
            return Bundle().apply {
                type = Bundle.BundleType.TRANSACTION
                addEntry().apply {
                    resource = org.hl7.fhir.r4.model.Patient().apply { id = "p1" }
                    request = Bundle.BundleEntryRequestComponent().apply {
                        method = Bundle.HTTPVerb.POST
                        url = "Patient"
                    }
                }
            }
        }
    }

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubMatchbox(propagators: io.opentelemetry.context.propagation.ContextPropagators): MatchboxClient =
            HeaderCapturingMatchbox(propagators)

        // Name matches the production bean (FhirConfig.hapiFhirClient)
        // so this overrides by name (ticket #520).
        @Bean
        @Primary
        fun hapiFhirClient(fhirContext: FhirContext): IGenericClient =
            org.mockito.Mockito.mock(
                IGenericClient::class.java,
                org.mockito.Mockito.RETURNS_DEEP_STUBS,
            )
    }

    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var matchboxClient: MatchboxClient

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        (matchboxClient as HeaderCapturingMatchbox).lastTraceparent.set(null)
    }

    @Test
    fun `disabled SDK emits no traceparent and writes null trace_context`() {
        sendMllp(
            "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000010||ADT^A01|OTELDIS001|P|2.5\r" +
                "EVN|A01|20260626000010\r" +
                "PID|1||MRN9^^^HOSP^MR||DOE^JOHN",
        )

        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val rows = repository.findAll().toList()
            assertThat(rows).hasSize(1)
            assertThat(rows[0].status).isEqualTo(IngestedMessageStatus.DELIVERED)
        }

        val stub = matchboxClient as HeaderCapturingMatchbox
        assertThat(stub.calls).isEqualTo(1)
        assertThat(stub.lastTraceparent.get())
            .describedAs("disabled SDK must not emit traceparent (zero overhead requirement)")
            .isNull()

        val row = repository.findAll().first()
        assertThat(row.traceContext)
            .describedAs("disabled SDK must store NULL trace_context")
            .isNull()
    }

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
