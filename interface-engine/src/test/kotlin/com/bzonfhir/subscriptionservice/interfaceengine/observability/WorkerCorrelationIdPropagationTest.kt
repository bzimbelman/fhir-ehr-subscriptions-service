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
import java.time.Duration
import java.util.concurrent.atomic.AtomicReference

/**
 * Correlation-id propagation through the async worker (Epic #387, ticket #388).
 *
 * Two flavors covered here:
 *
 *   1. happy-path-with-stored-id — a row that arrives in the DB WITH a
 *      correlation_id (the post-#388 receive path persists one). The worker
 *      adopts that id, sets it on MDC for every log line, and forwards it
 *      as `X-Correlation-Id` on the matchbox call.
 *
 *   2. legacy-row-without-id — a row persisted before V003 (correlation_id
 *      = NULL). The worker mints a fresh UUID, back-fills it onto the row,
 *      and propagates it on the outbound matchbox call. The point of this
 *      case: a single grep on the new id pulls the worker's log lines for
 *      this row AND the matchbox-side server logs for the call we made.
 *
 * Uses the same Testcontainers + stub-client pattern as IngestedMessageWorkerTest.
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
    ],
)
class WorkerCorrelationIdPropagationTest {

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
                "Docker is not available; WorkerCorrelationIdPropagationTest requires Testcontainers."
            }
        }
    }

    /**
     * Capturing MatchboxClient stub — records the X-Correlation-Id header
     * the worker sends on the outbound `$transform`. We use a Spring
     * `@Primary` override rather than mocking RestTemplate so the worker's
     * MatchboxClient invocation contract (which IS what we want to assert)
     * is the contract under test.
     */
    class CapturingMatchboxClient(
        private val responseBundle: Bundle,
    ) : MatchboxClient {
        val lastCorrelationIdSeen = AtomicReference<String?>(null)
        var calls = 0

        override fun transformToBundle(structureMapCanonical: String, rawHl7: String): Bundle {
            calls++
            // The real impl reads CorrelationId.current() at the moment of
            // call. Mimic that: read MDC ourselves so we observe the value
            // the worker set up.
            lastCorrelationIdSeen.set(
                org.slf4j.MDC.get(CorrelationId.MDC_KEY),
            )
            return responseBundle
        }

        fun reset() {
            lastCorrelationIdSeen.set(null)
            calls = 0
        }
    }

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubMatchbox(): MatchboxClient = CapturingMatchboxClient(adtA01Bundle())

        @Bean
        @Primary
        fun stubHapi(fhirContext: FhirContext): IGenericClient =
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
        (matchboxClient as CapturingMatchboxClient).reset()
    }

    @Test
    fun `worker forwards the row's stored correlation_id to matchbox`() {
        val expectedId = "test-corr-from-receive-12345"
        val id = seedReceivedWithCorrelationId(
            sourceSystem = "EPIC",
            sourceId = "WORKERCORR00001",
            messageType = "ADT_A01",
            rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A01|WORKERCORR00001|P|2.5",
            correlationId = expectedId,
        )

        val stub = matchboxClient as CapturingMatchboxClient
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.DELIVERED)
        }
        assertThat(stub.calls).isEqualTo(1)
        assertThat(stub.lastCorrelationIdSeen.get())
            .describedAs("worker should propagate the row's correlation_id to matchbox")
            .isEqualTo(expectedId)
        // Row's correlation_id stays as it was — worker doesn't rewrite a
        // value the receive path already supplied.
        val row = repository.findById(id).orElseThrow()
        assertThat(row.correlationId).isEqualTo(expectedId)
    }

    @Test
    fun `worker mints and back-fills correlation_id for a legacy row`() {
        val id = seedReceivedWithCorrelationId(
            sourceSystem = "EPIC",
            sourceId = "LEGACY00001",
            messageType = "ADT_A01",
            rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A01|LEGACY00001|P|2.5",
            correlationId = null, // pre-#388 row
        )

        val stub = matchboxClient as CapturingMatchboxClient
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.DELIVERED)
        }
        // Worker should have generated a UUID, used it on the outbound
        // matchbox call, AND written it back to the row.
        val row = repository.findById(id).orElseThrow()
        assertThat(row.correlationId)
            .describedAs("worker should back-fill correlation_id on a legacy row")
            .isNotNull()
            .matches("[0-9a-fA-F-]{36}")
        assertThat(stub.lastCorrelationIdSeen.get()).isEqualTo(row.correlationId)
    }

    private fun seedReceivedWithCorrelationId(
        sourceSystem: String,
        sourceId: String,
        messageType: String,
        rawMessage: String,
        correlationId: String?,
    ): Long {
        jdbc.update(
            """
            INSERT INTO ingested_messages
              (source_protocol, source_system, source_id, message_type,
               raw_message, raw_content_type, status, correlation_id)
            VALUES ('HL7V2_MLLP'::ingested_message_source_protocol,
                    ?, ?, ?, ?, ?, 'RECEIVED'::ingested_message_status, ?)
            """.trimIndent(),
            sourceSystem, sourceId, messageType, rawMessage,
            "application/hl7-v2", correlationId,
        )
        return jdbc.queryForObject(
            "SELECT id FROM ingested_messages WHERE source_system=? AND source_id=?",
            Long::class.java,
            sourceSystem, sourceId,
        )!!
    }
}

/**
 * Minimal valid ADT^A01 Bundle for the matchbox stub. Identical shape to
 * `IngestedMessageWorkerTest.adtA01Bundle`; duplicated here so this test
 * class is independent.
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
