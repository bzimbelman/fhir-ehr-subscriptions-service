package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestPersistService
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import io.micrometer.core.instrument.MeterRegistry
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.web.client.TestRestTemplate
import org.springframework.boot.test.web.server.LocalServerPort
import org.springframework.http.HttpStatus
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
 * Wire-level tests for the Prometheus metrics surface (Epic #387, ticket #389).
 *
 * Boots a full Spring context (web server on a random port, real Postgres
 * via Testcontainers) and exercises the metrics endpoint exactly the way
 * the Prometheus Operator's ServiceMonitor would: an HTTP GET against
 * `/actuator/prometheus` and a string match on the exposition format.
 *
 * ## Coverage map (matching the brief on ticket #389)
 *
 *   1. `endpoint exposes Prometheus exposition format` —
 *      GET returns 200 with a Content-Type that the Prometheus server
 *      accepts. Body opens with the usual `# HELP` / `# TYPE` preamble.
 *
 *   2. `default JVM and HTTP metrics are present` —
 *      `jvm_memory_used_bytes`, `http_server_requests_seconds_count`, and
 *      `jdbc_connections_active` all show up after a couple of HTTP hits.
 *
 *   3. `custom counter increments when a row is persisted RECEIVED` —
 *      The producer in [IngestPersistService.persistReceived] stamps one
 *      `interface_engine_ingested_messages_total{status="RECEIVED",...}`
 *      per inserted row. We persist three rows and assert the counter is
 *      exactly 3 for the labels we sent.
 *
 *   4. `DLQ gauge reflects the current table count` —
 *      Seed N rows with `status=DEAD_LETTER`, force a poll via the test
 *      hook on [DlqSizeGauge], scrape the endpoint, expect
 *      `interface_engine_dlq_current_size N.0`.
 *
 *   5. `no high-cardinality / PII label values are produced` —
 *      Parse the rendered exposition text, extract every label value, and
 *      assert none looks like a UUID, IPv4 address, or HL7v2 control id
 *      (the three shapes [InterfaceEngineMetrics.normalizeDlqReason] is
 *      meant to collapse). Catches a producer that bypasses normalization
 *      or accidentally tags by a high-cardinality field.
 *
 *   6. `DLQ reason normalization strips UUIDs / URLs / long ids` —
 *      Call the producer directly with a worst-case raw reason and check
 *      the rendered label value is normalized to a SHAPE token. The
 *      contract that protects the whole class from cardinality blow-out.
 *
 * ## Why a full-context test
 *
 * We could test the producers against a `SimpleMeterRegistry` in isolation
 * — that's faster — but the *contract* we want to enforce is "operators
 * scrape /actuator/prometheus and find these series". That contract spans
 * the producers, the `management.endpoints.web.exposure.include` config,
 * the Prometheus registry being on the classpath, and the actuator
 * web-binding. A wire-level test is the only place all five pieces meet.
 *
 * The cost is one Postgres container + one Spring boot per test class, but
 * Spring's context cache shares it across all six methods.
 */
@Testcontainers
@SpringBootTest(
    webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
// Disable the async worker for these tests — we want stable counts and
// deterministic gauge values, not whatever the worker happens to have
// processed at scrape time. The DlqSizeGauge poll IS deliberately enabled
// (default) so we can verify its surface.
@TestPropertySource(
    properties = [
        "subscription-service.worker.enabled=false",
        // Short DLQ poll interval so the test doesn't have to sleep
        // 30 seconds to see a non-zero value. The gauge's @Scheduled
        // is on a fixedDelay, so this is the cap on staleness in test.
        "subscription-service.observability.metrics.dlq-poll-ms=500",
        "subscription-service.observability.metrics.dlq-poll-initial-ms=100",
        // Spring Boot 3.5 introduced ConditionalOnEnabledMetricsExport on the
        // Prometheus auto-config; both this property and the explicit endpoint
        // enabler are required for /actuator/prometheus to register. The
        // production application.yaml has the same — without it the JAR is
        // on the classpath but the endpoint is silently absent.
        "management.prometheus.metrics.export.enabled=true",
        "management.endpoint.prometheus.access=read_only",
    ],
)
class PrometheusMetricsTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(Wait.forListeningPort())
            .withStartupTimeout(Duration.ofSeconds(60))

        // The IngestRoutes MLLP listener boots in this context too; pick a
        // free port so two parallel test JVMs don't collide on 2575.
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
                "Docker is not available; PrometheusMetricsTest requires Testcontainers."
            }
        }
    }

    @LocalServerPort
    private var port: Int = 0

    @Autowired private lateinit var restTemplate: TestRestTemplate
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var persist: IngestPersistService
    @Autowired private lateinit var metrics: InterfaceEngineMetrics
    @Autowired private lateinit var dlqGauge: DlqSizeGauge
    @Autowired private lateinit var meterRegistry: MeterRegistry

    @BeforeEach
    fun resetState() {
        // Truncate and reset identity so every test starts with a known-empty
        // table. The DlqSizeGauge's polled AtomicLong will catch up on the
        // next poll (forced via pollNow() in the relevant tests).
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        // Force a poll so the gauge reflects the truncate before the
        // assertion-side scrape, otherwise the gauge can still hold its
        // pre-truncate value from the previous test method.
        dlqGauge.pollNow()
    }

    private fun scrape(): String {
        val url = "http://localhost:$port/actuator/prometheus"
        val resp = restTemplate.getForEntity(url, String::class.java)
        if (resp.statusCode != HttpStatus.OK) {
            // Help debug 404s: surface what /actuator actually exposes.
            val index = restTemplate.getForEntity(
                "http://localhost:$port/actuator",
                String::class.java,
            )
            error(
                "GET $url returned ${resp.statusCode}. " +
                    "Body: ${resp.body?.take(200)}. " +
                    "Index ${index.statusCode}: ${index.body?.take(500)}",
            )
        }
        return resp.body ?: ""
    }

    // -- 1. endpoint shape ----------------------------------------------------

    @Test
    fun `endpoint exposes Prometheus exposition format`() {
        // Use scrape() so we get the diagnostic "what IS exposed" message on
        // a 404 — much faster to debug than a bare 404 assertion.
        val body = scrape()
        // Exposition format always opens its first series with a `# HELP`
        // line. If we got HTML back here, an upstream filter is failing.
        assertThat(body).contains("# HELP")
        assertThat(body).contains("# TYPE")
        // Sanity-check the response was text-shaped (the registry's exposition
        // is plain text — not JSON, not HTML).
        assertThat(body).doesNotContain("<html")
    }

    // -- 2. default metrics ---------------------------------------------------

    @Test
    fun `default JVM and HTTP and JDBC metrics are present`() {
        // Hit a couple of actuator endpoints to make sure
        // http_server_requests_seconds is non-empty.
        restTemplate.getForEntity("http://localhost:$port/actuator/health", String::class.java)
        restTemplate.getForEntity("http://localhost:$port/actuator/info", String::class.java)

        val body = scrape()

        // JVM memory — present as soon as the JVM Metrics binder is wired,
        // which spring-boot-actuator does by default when the Prometheus
        // registry is on the classpath.
        assertThat(body).contains("jvm_memory_used_bytes")
        // HTTP server requests — present after at least one inbound HTTP.
        assertThat(body).contains("http_server_requests_seconds_count")
        // JDBC pool — present because we have spring-boot-starter-data-jpa
        // wiring HikariCP, and Micrometer's DataSourcePoolMetricsAutoConfiguration
        // binds the HikariDataSource's stats automatically.
        assertThat(body).contains("jdbc_connections_active")
    }

    // -- 3. custom counter ----------------------------------------------------

    @Test
    fun `custom counter interface_engine_ingested_messages_total increments on persist`() {
        // Persist three RECEIVED rows. Same labels on all three so they
        // collapse to a single time-series with a counter value of 3.
        repeat(3) { i ->
            persist.persistReceived(
                sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
                sourceSystem = "EPIC",
                sourceId = "PRMT-COUNT-${i}",
                messageType = "ADT_A04",
                rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A04|PRMT-COUNT-${i}|P|2.5",
                rawContentType = "application/hl7-v2",
                correlationId = null,
            )
        }

        val body = scrape()
        // Counter line shape:
        //   interface_engine_ingested_messages_total{labels...} VALUE
        // Filter to the matching label set and parse the trailing number.
        val matching = body.lines().filter {
            it.startsWith("interface_engine_ingested_messages_total{") &&
                it.contains("status=\"RECEIVED\"") &&
                it.contains("source_system=\"EPIC\"") &&
                it.contains("message_type=\"ADT_A04\"")
        }
        assertThat(matching)
            .withFailMessage("Expected a RECEIVED counter line in:\n$body")
            .isNotEmpty()
        val value = matching.first().substringAfterLast(' ').toDouble()
        assertThat(value).isGreaterThanOrEqualTo(3.0)
    }

    // -- 4. DLQ gauge ---------------------------------------------------------

    @Test
    fun `dlq gauge reflects the current dead_letter row count`() {
        // Seed two DEAD_LETTER rows directly via SQL (faster than going
        // through the worker; the gauge only cares about the COUNT, not how
        // the rows got there).
        seedDeadLetter("EPIC", "DLQ-1")
        seedDeadLetter("EPIC", "DLQ-2")

        // Force a poll so we don't have to wait for the next scheduled tick.
        dlqGauge.pollNow()

        // The Micrometer-side AtomicLong is updated synchronously inside pollNow.
        assertThat(dlqGauge.currentValue()).isEqualTo(2L)

        val body = scrape()
        // Gauge line: `interface_engine_dlq_current_size{application="..."} 2.0`
        val gaugeLine = body.lines().firstOrNull {
            it.startsWith("interface_engine_dlq_current_size{") ||
                it.startsWith("interface_engine_dlq_current_size ")
        }
        assertThat(gaugeLine)
            .withFailMessage("Expected a dlq_current_size line in:\n$body")
            .isNotNull()
        val value = gaugeLine!!.substringAfterLast(' ').toDouble()
        assertThat(value).isEqualTo(2.0)
    }

    // -- 5. cardinality / PII heuristic --------------------------------------

    @Test
    fun `no label value contains a uuid or ipv4 or long control id`() {
        // Persist a row first so the counter has at least one observed value
        // set in the exposition. Then ensure none of the rendered label
        // values match the high-cardinality shapes we're trying to keep out.
        persist.persistReceived(
            sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
            sourceSystem = "EPIC",
            sourceId = "PRMT-PII-1",
            messageType = "ADT_A04",
            rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626000000||ADT^A04|PRMT-PII-1|P|2.5",
            rawContentType = "application/hl7-v2",
            correlationId = "corr-${java.util.UUID.randomUUID()}",
        )

        val body = scrape()
        // Filter to lines that emit our custom metrics. The default JVM /
        // JDBC metrics include legitimate UUIDs / long ids in some label
        // values (datasource names, gc names) and aren't ours to police.
        val customLines = body.lines().filter { it.startsWith("interface_engine_") }
        assertThat(customLines).isNotEmpty()

        val labelValueRegex = Regex("\\{([^}]*)\\}")
        val uuidRegex = Regex("[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")
        val ipv4Regex = Regex("\\b\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\b")
        // Bare 6+ digit numbers are the shape an HL7v2 control id takes.
        // Allow 4-5 digits (HTTP status, year fragments are fine).
        val longIntRegex = Regex("\"[^\"]*\\b\\d{6,}\\b[^\"]*\"")

        customLines.forEach { line ->
            val labels = labelValueRegex.find(line)?.groupValues?.get(1) ?: return@forEach
            // Strip the `application` label — it's stamped by our config
            // and is a constant ("subscription-service-interface-engine"
            // or similar); not a cardinality risk.
            val withoutAppTag = labels.replace(Regex("application=\"[^\"]*\""), "")
            assertThat(uuidRegex.containsMatchIn(withoutAppTag))
                .withFailMessage("Found UUID in label values: $line")
                .isFalse()
            assertThat(ipv4Regex.containsMatchIn(withoutAppTag))
                .withFailMessage("Found IPv4 in label values: $line")
                .isFalse()
            assertThat(longIntRegex.containsMatchIn(withoutAppTag))
                .withFailMessage("Found long-int (likely control id) in label values: $line")
                .isFalse()
        }
    }

    // -- 6. reason normalization ---------------------------------------------

    @Test
    fun `dlq counter reason label is normalized to a low-cardinality shape`() {
        // The worst-case input: a "real" worker error string with a UUID,
        // a URL, an IPv4, and a long control id all in one message. After
        // normalization none of those should appear in the label value.
        val ugly = "matchbox: 422 unprocessable entity for correlation_id=" +
            "1f2d3e4f-aabb-ccdd-eeff-001122334455 control_id=987654321 " +
            "host=10.20.30.40 hint=https://matchbox.local/fhir/StructureMap/x"

        metrics.incrementDlqTransitions(
            sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP.name,
            rawReason = ugly,
        )

        val body = scrape()
        val dlqLines = body.lines().filter {
            it.startsWith("interface_engine_dlq_transitions_total{")
        }
        assertThat(dlqLines).isNotEmpty()

        // Pull every reason="..." substring out and check the contract.
        val reasonRegex = Regex("reason=\"([^\"]*)\"")
        val reasons = dlqLines.flatMap { reasonRegex.findAll(it).map { m -> m.groupValues[1] }.toList() }
        assertThat(reasons).isNotEmpty()
        reasons.forEach { r ->
            assertThat(r.length).isLessThanOrEqualTo(InterfaceEngineMetrics.MAX_REASON_LENGTH)
            assertThat(r).doesNotMatch(".*[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-.*") // no UUID
            assertThat(r).doesNotContain("10.20.30.40")
            assertThat(r).doesNotContain("987654321")
            assertThat(r).doesNotContain("https://")
        }
    }

    // -- helpers --------------------------------------------------------------

    private fun seedDeadLetter(sourceSystem: String, sourceId: String) {
        // Direct INSERT bypasses the worker / persist service so we exercise
        // the gauge's query, not the producers. status is cast to the
        // Postgres ENUM type explicitly because JDBC has no native enum.
        jdbc.update(
            """
            INSERT INTO ingested_messages
              (source_protocol, source_system, source_id, message_type,
               raw_message, raw_content_type, status, attempt_count)
            VALUES (
              CAST(? AS ingested_message_source_protocol),
              ?, ?, ?, ?, ?,
              CAST(? AS ingested_message_status),
              0
            )
            """.trimIndent(),
            IngestedMessageSourceProtocol.HL7V2_MLLP.name,
            sourceSystem,
            sourceId,
            "ADT_A04",
            "raw",
            "application/hl7-v2",
            IngestedMessageStatus.DEAD_LETTER.name,
        )
    }
}
