package com.bzonfhir.subscriptionservice.interfaceengine.worker

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ch.qos.logback.classic.Level as LogbackLevel
import ch.qos.logback.classic.Logger as LogbackLogger
import ch.qos.logback.classic.spi.ILoggingEvent
import ch.qos.logback.core.read.ListAppender
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.hl7.fhir.r4.model.Bundle
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.slf4j.LoggerFactory
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

/**
 * Retry policy + DLQ tests for ticket #383.
 *
 * Lives in a SEPARATE class from [IngestedMessageWorkerTest] (rather than
 * adding @Test methods to it) because the retry behaviour is sensitive to
 * the configured backoff values — and overriding `max-attempts` /
 * `backoff-base-ms` for *just these tests* via @TestPropertySource gives
 * each scenario a deterministic clock to work against without slowing the
 * existing six-test class down. Spring's test context cache keys on the
 * effective property set, so this is one extra context for these scenarios.
 *
 * Tunings:
 *   - max-attempts=3 → DLQ on the 3rd failure (so a single test can
 *     watch the full retry → DLQ life-cycle in well under a second).
 *   - backoff-base-ms=100 / factor=2.0 / max=2000 — produces 100, 200,
 *     400, 800, capped at 2000. Fast enough to test, slow enough to
 *     distinguish retry timings from "polled immediately".
 *   - poll-interval-ms=50 — ten polls per second so a `next_attempt_at`
 *     in the near future is picked up promptly. Faster than the parent
 *     class's 200ms because the assertions here time-slice across
 *     individual retries.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.NONE)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(
    properties = [
        "subscription-service.worker.enabled=true",
        "subscription-service.worker.poll-interval-ms=50",
        "subscription-service.worker.batch-size=10",
        "subscription-service.worker.transforming-stale-seconds=60",
        "subscription-service.worker.retry.max-attempts=3",
        "subscription-service.worker.retry.backoff-base-ms=100",
        "subscription-service.worker.retry.backoff-max-ms=2000",
        "subscription-service.worker.retry.backoff-factor=2.0",
        "subscription-service.worker.retry.dlq-log-level=WARN",
        "subscription-service.hapi.base-url=http://stub.hapi.test/fhir",
        "subscription-service.matchbox.base-url=http://stub.matchbox.test",
    ],
)
class IngestedMessageWorkerRetryTest {

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
                "Docker is not available; IngestedMessageWorkerRetryTest requires Testcontainers."
            }
        }
    }

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubMatchboxClient(fhirContext: FhirContext): MatchboxClient =
            StubMatchboxClient(fhirContext)

        @Bean
        @Primary
        fun stubHapiClient(): IGenericClient = StubHapiClient.build()
    }

    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var matchboxClient: MatchboxClient
    @Autowired private lateinit var hapiClient: IGenericClient
    @Autowired private lateinit var worker: IngestedMessageWorker
    @Autowired private lateinit var gateway: WorkerJdbcGateway

    /**
     * Logback ListAppender wired to the worker's logger. Captures every
     * log event emitted during a test so the structured `event=retry_scheduled`
     * and `event=dlq` lines can be asserted on by formatted message text.
     *
     * Attached in @BeforeEach, detached in @AfterEach so the captor doesn't
     * leak between tests in the same Spring context.
     */
    private lateinit var logAppender: ListAppender<ILoggingEvent>
    private lateinit var workerLogger: LogbackLogger

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        (matchboxClient as StubMatchboxClient).reset()
        StubHapiClient.reset(hapiClient)

        workerLogger = LoggerFactory.getLogger(IngestedMessageWorker::class.java) as LogbackLogger
        // Capture INFO (retry_scheduled) and WARN (dlq) both. Don't lower
        // below INFO or we'll drown in poll-loop debug chatter.
        workerLogger.level = LogbackLevel.INFO
        logAppender = ListAppender<ILoggingEvent>().apply {
            context = workerLogger.loggerContext
            start()
        }
        workerLogger.addAppender(logAppender)
    }

    @AfterEach
    fun detach() {
        workerLogger.detachAppender(logAppender)
    }

    // -------------------------------------------------------------------------
    // 1. Retry schedules backoff: first failure stamps next_attempt_at ~100ms
    //    from now (base=100ms, factor=2.0, new_attempt=1).
    // -------------------------------------------------------------------------
    @Test
    fun `failure schedules retry with next_attempt_at and exponential backoff`() {
        val stub = matchboxClient as StubMatchboxClient
        stub.failureToThrow = RuntimeException("transform 500")
        // adt-a01 structuremap URL is configured by default; we don't need a
        // response because the failure throws first.

        val id = seedReceived("EPIC", "RTY00001", "ADT_A01")

        // First retry: attempt_count=1, next_attempt_at ~now()+100ms.
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.FAILED)
            assertThat(row.attemptCount).isEqualTo(1)
            assertThat(row.nextAttemptAt).isNotNull()
        }

        // Wait for the row to be re-claimed once the backoff elapses. The
        // worker polls every 50ms so this should happen within ~200ms even
        // accounting for startup jitter.
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.attemptCount).isGreaterThanOrEqualTo(2)
        }

        // At least one retry_scheduled log line should have been emitted by
        // this point (one per FAILED transition, max-attempts=3 → up to 2
        // retry_scheduled lines before DLQ).
        val retryLines = logAppender.list.filter {
            it.formattedMessage.contains("event=retry_scheduled")
        }
        assertThat(retryLines)
            .describedAs("expected at least one event=retry_scheduled log line")
            .isNotEmpty()
        assertThat(retryLines.first().formattedMessage)
            .contains("message_id=$id")
            .contains("attempt_count=1")
    }

    // -------------------------------------------------------------------------
    // 2. Backoff cap: with base=1000, factor=2.0, max=2000, the 3rd-attempt
    //    raw delay would be 1000*2^2 = 4000ms; the cap clamps it to 2000ms.
    //    We exercise this in isolation via computeBackoffMillis() so we
    //    don't have to time-slice a slow test.
    // -------------------------------------------------------------------------
    @Test
    fun `computeBackoffMillis clamps to backoff-max-ms`() {
        // The class-level @TestPropertySource has base=100, max=2000, factor=2.
        // 100*2^0=100, 100*2^1=200, 100*2^2=400, 100*2^3=800, 100*2^4=1600,
        // 100*2^5=3200 → clamped to 2000.
        assertThat(worker.computeBackoffMillis(1)).isEqualTo(100L)
        assertThat(worker.computeBackoffMillis(2)).isEqualTo(200L)
        assertThat(worker.computeBackoffMillis(3)).isEqualTo(400L)
        assertThat(worker.computeBackoffMillis(4)).isEqualTo(800L)
        assertThat(worker.computeBackoffMillis(5)).isEqualTo(1600L)
        assertThat(worker.computeBackoffMillis(6))
            .describedAs("must clamp to backoff-max-ms when raw exceeds the cap")
            .isEqualTo(2000L)
        assertThat(worker.computeBackoffMillis(20))
            .describedAs("clamp must hold for arbitrarily large attempt counts")
            .isEqualTo(2000L)
    }

    // -------------------------------------------------------------------------
    // 3. DLQ transition: max-attempts=3, matchbox always fails. After 3
    //    attempts the row goes to DEAD_LETTER, next_attempt_at=null, and
    //    one event=dlq line appears. Subsequent polls don't touch it.
    // -------------------------------------------------------------------------
    @Test
    fun `row transitions to DEAD_LETTER after max-attempts failures`() {
        val stub = matchboxClient as StubMatchboxClient
        stub.failureToThrow = RuntimeException("permanent transform failure")

        val id = seedReceived("EPIC", "DLQ00001", "ADT_A01")

        // With base=100, factor=2.0: failures at +100ms, +200ms, +400ms = ~700ms
        // total. Awaitility 5s is comfortable.
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.DEAD_LETTER)
            assertThat(row.attemptCount).isEqualTo(3)
            assertThat(row.nextAttemptAt)
                .describedAs("DLQ rows must clear next_attempt_at")
                .isNull()
            assertThat(row.lastError).contains("transform")
        }

        // Exactly ONE event=dlq line per DLQ transition (we don't want the
        // line repeated every poll loop after the row settles).
        val dlqLines = logAppender.list.filter {
            it.formattedMessage.contains("event=dlq") &&
                it.formattedMessage.contains("message_id=$id")
        }
        assertThat(dlqLines).hasSize(1)
        // Structured fields are present.
        val line = dlqLines.single().formattedMessage
        assertThat(line)
            .contains("source_system=EPIC")
            .contains("source_id=DLQ00001")
            .contains("message_type=ADT_A01")
            .contains("attempt_count=3")
            .contains("last_error=")
        // And at the configured level (WARN).
        assertThat(dlqLines.single().level).isEqualTo(LogbackLevel.WARN)

        // Sanity: snapshot attempt_count, sleep across multiple poll cycles,
        // assert no further activity on this row.
        val attemptCountAfter = repository.findById(id).orElseThrow().attemptCount
        Thread.sleep(500)
        val rowLater = repository.findById(id).orElseThrow()
        assertThat(rowLater.status).isEqualTo(IngestedMessageStatus.DEAD_LETTER)
        assertThat(rowLater.attemptCount).isEqualTo(attemptCountAfter)
    }

    // -------------------------------------------------------------------------
    // 4. DLQ log level: the class-level @TestPropertySource sets dlq-log-level=WARN.
    //    Combined with test 3 above, this verifies one WARN line at exactly the
    //    configured level. Additionally we verify each retry emits exactly
    //    one INFO `event=retry_scheduled` line (one per transition, not per
    //    poll loop).
    // -------------------------------------------------------------------------
    @Test
    fun `log levels - dlq emits one WARN per transition, retries emit one INFO each`() {
        val stub = matchboxClient as StubMatchboxClient
        stub.failureToThrow = RuntimeException("level-test failure")

        val id = seedReceived("EPIC", "LVL00001", "ADT_A01")

        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.DEAD_LETTER)
        }
        // Settle: wait a couple of poll cycles to be sure no further log
        // lines are produced for this id.
        Thread.sleep(300)

        val ourLines = logAppender.list.filter { it.formattedMessage.contains("message_id=$id") }
        val retryScheduled = ourLines.filter { it.formattedMessage.contains("event=retry_scheduled") }
        val dlq = ourLines.filter { it.formattedMessage.contains("event=dlq") }

        // With max-attempts=3, two retries are scheduled (attempt 1, attempt 2),
        // then the third failure DLQs. Each is one line.
        assertThat(retryScheduled).describedAs("expected exactly 2 retry_scheduled lines (attempts 1,2)").hasSize(2)
        retryScheduled.forEach {
            assertThat(it.level).isEqualTo(LogbackLevel.INFO)
        }
        assertThat(dlq).describedAs("expected exactly 1 dlq line at DLQ transition").hasSize(1)
        assertThat(dlq.single().level).isEqualTo(LogbackLevel.WARN)
    }

    // -------------------------------------------------------------------------
    // 5. Admin retry reset → backoff math starts fresh. We don't have to call
    //    the controller; the admin endpoint sets status=RECEIVED and
    //    attempt_count=0, and the worker picks that up exactly like a brand
    //    new RECEIVED row. After the first failure the row must have
    //    next_attempt_at ≈ now() + base (i.e. 100ms here), NOT the larger
    //    delay from its prior failures.
    // -------------------------------------------------------------------------
    @Test
    fun `admin retry reset starts backoff sequence from base on next failure`() {
        val stub = matchboxClient as StubMatchboxClient

        // 1) Drive the row to DLQ.
        stub.failureToThrow = RuntimeException("first round")
        val id = seedReceived("EPIC", "RST00001", "ADT_A01")
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.status).isEqualTo(IngestedMessageStatus.DEAD_LETTER)
            assertThat(row.attemptCount).isEqualTo(3)
        }

        // 2) Simulate the admin retry (controller behaviour from #384).
        jdbc.update(
            """
            UPDATE ingested_messages
               SET status = 'RECEIVED'::ingested_message_status,
                   attempt_count = 0,
                   next_attempt_at = NULL,
                   last_error = NULL
             WHERE id = ?
            """.trimIndent(),
            id,
        )

        // Clear the appender so we only look at events AFTER the reset.
        logAppender.list.clear()

        // 3) The worker should re-pick up the row and fail. attempt_count
        //    should be exactly 1 (NOT continuing from the prior count of 3),
        //    and next_attempt_at is roughly now()+base.
        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            // status will flip from RECEIVED → FAILED. attempt_count is 1
            // because the policy is "new_attempt = current+1" and current
            // was just reset to 0.
            assertThat(row.status).isEqualTo(IngestedMessageStatus.FAILED)
            assertThat(row.attemptCount).isEqualTo(1)
            assertThat(row.nextAttemptAt).isNotNull()
        }

        // The retry_scheduled line for THIS failure should report attempt_count=1.
        val freshRetryLines = logAppender.list.filter {
            it.formattedMessage.contains("event=retry_scheduled") &&
                it.formattedMessage.contains("message_id=$id")
        }
        assertThat(freshRetryLines).isNotEmpty()
        assertThat(freshRetryLines.first().formattedMessage)
            .describedAs("admin retry must reset attempt_count → first new failure logs attempt_count=1")
            .contains("attempt_count=1")
    }

    // -------------------------------------------------------------------------
    // 6. next_attempt_at respected: a FAILED row with next_attempt_at in the
    //    future must NOT be picked up. Pushing the timestamp into the past
    //    makes it eligible immediately.
    // -------------------------------------------------------------------------
    @Test
    fun `worker respects future next_attempt_at and re-picks up when due`() {
        val stub = matchboxClient as StubMatchboxClient
        // Make sure if it IS picked up, it fails (so attempt_count would tick)
        stub.failureToThrow = RuntimeException("should not be polled yet")

        // Seed directly as FAILED with next_attempt_at in the future.
        jdbc.update(
            """
            INSERT INTO ingested_messages
              (source_protocol, source_system, source_id, message_type,
               raw_message, raw_content_type, status,
               attempt_count, next_attempt_at)
            VALUES ('HL7V2_MLLP'::ingested_message_source_protocol,
                    ?, ?, ?, ?, ?,
                    'FAILED'::ingested_message_status,
                    1, now() + interval '10 seconds')
            """.trimIndent(),
            "EPIC", "NXT00001", "ADT_A01",
            "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A01|NXT00001|P|2.5",
            "application/hl7-v2",
        )
        val id = jdbc.queryForObject(
            "SELECT id FROM ingested_messages WHERE source_id='NXT00001'",
            Long::class.java,
        )!!

        // Run several poll cycles. attempt_count must stay at 1 because the
        // worker's WHERE clause excludes rows whose next_attempt_at > now().
        Thread.sleep(400) // 8 poll cycles at 50ms
        val notYet = repository.findById(id).orElseThrow()
        assertThat(notYet.attemptCount)
            .describedAs("future next_attempt_at must keep the row off the queue")
            .isEqualTo(1)
        assertThat(notYet.status).isEqualTo(IngestedMessageStatus.FAILED)

        // Pull next_attempt_at into the past — now the worker is eligible to
        // pick it up. attempt_count should tick to 2 within a couple polls.
        jdbc.update(
            "UPDATE ingested_messages SET next_attempt_at = now() - interval '1 second' WHERE id = ?",
            id,
        )

        await().atMost(Duration.ofSeconds(5)).untilAsserted {
            val row = repository.findById(id).orElseThrow()
            assertThat(row.attemptCount)
                .describedAs("worker must claim row once next_attempt_at <= now()")
                .isGreaterThanOrEqualTo(2)
        }
    }

    // ---------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------

    private fun seedReceived(
        sourceSystem: String,
        sourceId: String,
        messageType: String,
    ): Long {
        val raw = "MSH|^~\\&|$sourceSystem|HOSP|RECV|CDS|20260626000000||$messageType|$sourceId|P|2.5"
        jdbc.update(
            """
            INSERT INTO ingested_messages
              (source_protocol, source_system, source_id, message_type,
               raw_message, raw_content_type, status)
            VALUES ('HL7V2_MLLP'::ingested_message_source_protocol,
                    ?, ?, ?, ?, ?, 'RECEIVED'::ingested_message_status)
            """.trimIndent(),
            sourceSystem, sourceId, messageType, raw, "application/hl7-v2",
        )
        return jdbc.queryForObject(
            "SELECT id FROM ingested_messages WHERE source_system=? AND source_id=?",
            Long::class.java,
            sourceSystem, sourceId,
        )!!
    }

    /**
     * Build a minimal valid ADT^A01 FHIR transaction Bundle for the rare
     * tests that need matchbox to succeed (none in this class actually use
     * it — all retry/DLQ scenarios drive failures — but kept for symmetry
     * with the parent class).
     */
    @Suppress("unused")
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
}
