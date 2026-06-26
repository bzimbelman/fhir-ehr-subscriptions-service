package com.bzonfhir.subscriptionservice.interfaceengine.routes

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.apache.camel.CamelContext
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.containers.wait.strategy.Wait
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket
import java.net.Socket
import java.time.Duration
import javax.sql.DataSource

/**
 * Receive-route contract tests for ticket #381.
 *
 * After ticket #381 the route's surface is exactly:
 *
 *   parse v2 → snapshot raw text → INSERT into `ingested_messages` with
 *   status=RECEIVED → ACK AA;  or, on DB failure, ACK AE.
 *
 * These tests boot a real Spring context with a Testcontainers Postgres
 * (same pattern as `IngestedMessageRepositoryTest`) and exercise the full
 * receive path end-to-end via a raw MLLP TCP socket. Each test sends an
 * HL7v2 message framed with MLLP block markers (0x0B / 0x1C 0x0D) — exactly
 * the bytes `nc` would write — and asserts on:
 *
 *   - the ACK code returned to the sender,
 *   - the row(s) that ended up in `ingested_messages`.
 *
 * Cases covered:
 *
 *   1. Happy path (`route persists a new ADT_A04 …`) — one message in, one
 *      row out with the expected source_system / source_id / message_type /
 *      raw_content_type / status, AA ACK echoing MSH-10.
 *   2. Idempotency (`duplicate replay produces …`) — send the same message
 *      twice, get two AA ACKs, end up with exactly one row.
 *   3. Distinct control ids (`two messages with different MSH-10 …`) —
 *      different control ids = different rows.
 *   4. DB-down (`when the database is unreachable …`) — kill the
 *      Testcontainer mid-test, send a message, expect AE and no row.
 *
 * Why Testcontainers and not the in-memory H2 pattern: the entity uses
 * Postgres-native ENUM types (mapped via `@JdbcTypeCode(SqlTypes.NAMED_ENUM)`)
 * and the route relies on the (source_system, source_id) UNIQUE constraint
 * to detect duplicates. Both behaviours need a real Postgres to exercise.
 */
@Testcontainers
@SpringBootTest(
    webEnvironment = SpringBootTest.WebEnvironment.NONE,
)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
// Order matters here: the DB-down test pauses Postgres mid-run. We want it
// last so a hung un-pause / late connection reset can't affect the happy-path
// tests. @Order on each method + this OrderAnnotation orderer enforces it.
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class IngestRoutesTest {

    companion object {
        // MLLP framing bytes per HL7 v2 spec.
        private const val SB: Byte = 0x0B  // start block
        private const val EB: Byte = 0x1C  // end block
        private const val CR: Byte = 0x0D  // carriage return (segment + post-EB)

        // Pick a free port once per test class. Bound + immediately released
        // so Camel's MLLP consumer can grab it ~100ms later. Held in a JVM
        // static so both @DynamicPropertySource and the test methods see the
        // same value.
        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            // Rancher Desktop is occasionally slower than the default
            // PostgreSQLContainer log-based wait strategy expects: the
            // "database system is ready to accept connections" log message
            // fires before the host-side port forwarder is actually
            // routing traffic, and Spring/Flyway then sees a connection
            // refused on its first attempt. Layering a "listening port"
            // wait on top of the default log wait gives the port a real
            // chance to start forwarding before we hand control to Spring.
            .waitingFor(Wait.forListeningPort())

        @JvmStatic
        @DynamicPropertySource
        fun properties(registry: DynamicPropertyRegistry) {
            registry.add("subscription-service.mllp.port") { mllpPort }
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            // Tighten the Hikari connect timeout so the DB-down test's
            // persist call gives up quickly (within ~4s) instead of waiting
            // the production default of 10s — the test's MLLP socket
            // soTimeout is 10s, and the Camel route adds another second of
            // overhead, so we want the DB call to bail with time to spare.
            // We deliberately do NOT shorten validation-timeout here:
            // shorter validation timeouts caused HikariPool to evict
            // perfectly good connections under Rancher Desktop's slightly-
            // laggy networking, which broke the duplicate-replay path
            // (which needs a 2nd connection for the REQUIRES_NEW lookup).
            registry.add("spring.datasource.hikari.connection-timeout") { "4000" }
            // The receive route doesn't talk to HAPI or Matchbox anymore, but
            // the @Value-bound beans in FhirConfig still construct on startup
            // (the async worker in #382 will consume them). Point them at
            // unreachable URLs to make it obvious if anything starts trying
            // to talk to them — the receive route should NEVER trigger an
            // outbound call on the sync path.
            registry.add("subscription-service.hapi.base-url") { "http://invalid.hapi.test/fhir" }
            registry.add("subscription-service.matchbox.base-url") { "http://invalid.matchbox.test" }
            // Disable the async worker (#382) — these tests only verify the
            // synchronous receive path. Leaving the worker enabled would mean
            // a background thread is polling the same table the tests
            // truncate before each test, and racing to mutate the RECEIVED
            // rows the happy-path test then asserts on.
            registry.add("subscription-service.worker.enabled") { "false" }
        }

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; IngestRoutesTest requires Testcontainers."
            }
        }
    }

    @Autowired
    private lateinit var camelContext: CamelContext

    @Autowired
    private lateinit var repository: IngestedMessageRepository

    @Autowired
    private lateinit var jdbc: JdbcTemplate

    @Autowired
    private lateinit var dataSource: DataSource

    @BeforeEach
    fun setUp() {
        // Each test starts with an empty table; Flyway history stays put.
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        // Wait for Camel route to be running before each test — Spring
        // hands us the context before Camel's async startup completes.
        await().atMost(Duration.ofSeconds(10)).until {
            camelContext.getRoute(IngestRoutes.ROUTE_MLLP_INGEST) != null &&
                camelContext.routeController.getRouteStatus(IngestRoutes.ROUTE_MLLP_INGEST).isStarted
        }
    }

    @Test
    @Order(1)
    fun `route persists a new ADT_A04 with status=RECEIVED and ACKs AA`() {
        val controlId = "MSGCTRL00001"
        val msg = adtA04(controlId = controlId, sendingApp = "EPIC")

        val ack = sendMllp(msg)

        // ACK assertions: AA echoing our control id, message type ACK^A04.
        assertThat(ack)
            .describedAs("ACK should be AA echoing MSH-10")
            .contains("MSA|AA|$controlId")
            .contains("ACK^A04")

        // Row assertions: exactly one row with the fields we expect.
        val rows = repository.findAll()
        assertThat(rows).hasSize(1)
        val row = rows.single()
        assertThat(row.sourceProtocol).isEqualTo(IngestedMessageSourceProtocol.HL7V2_MLLP)
        assertThat(row.sourceSystem).isEqualTo("EPIC")
        assertThat(row.sourceId).isEqualTo(controlId)
        assertThat(row.messageType).isEqualTo("ADT_A04")
        assertThat(row.rawContentType).isEqualTo("application/hl7-v2")
        assertThat(row.status).isEqualTo(IngestedMessageStatus.RECEIVED)
        // rawMessage should look like an HL7 message (starts with MSH|).
        assertThat(row.rawMessage)
            .describedAs("raw_message should be the re-encoded v2 wire payload")
            .startsWith("MSH|")
            .contains("ADT^A04")
            .contains(controlId)
        // DB DEFAULT now() should have populated received_at.
        assertThat(row.receivedAt).isNotNull()
        // Worker fields are untouched on initial insert.
        assertThat(row.attemptCount).isEqualTo(0)
        assertThat(row.deliveredAt).isNull()
        assertThat(row.lastError).isNull()
        // Correlation id (Epic #387, ticket #388): server-assigned at
        // receive time; HL7 v2 has no transport-level header, so the
        // route generates a UUID. We don't pin the EXACT value (it's
        // random), just that it looks like a UUID v4.
        assertThat(row.correlationId)
            .describedAs("correlation_id should be a UUID set by the receive route")
            .isNotNull()
            .matches("[0-9a-fA-F-]{36}")
    }

    @Test
    @Order(2)
    fun `duplicate replay produces one row and both responses ACK AA`() {
        val controlId = "DUP00001"
        val msg = adtA04(controlId = controlId, sendingApp = "EPIC")

        val firstAck = sendMllp(msg)
        val secondAck = sendMllp(msg)

        // Both responses must be AA — replaying a message we've already
        // received is benign and the sender shouldn't be told otherwise.
        assertThat(firstAck).contains("MSA|AA|$controlId")
        assertThat(secondAck)
            .describedAs("duplicate replay should ACK AA, not AE")
            .contains("MSA|AA|$controlId")

        // Exactly one DB row. The unique-violation path in
        // IngestPersistService.persistReceived should have looked up the
        // existing row rather than inserting a second one.
        val rows = repository.findAll()
        assertThat(rows).hasSize(1)
        assertThat(rows.single().sourceId).isEqualTo(controlId)
    }

    @Test
    @Order(3)
    fun `two messages with different MSH-10 produce two rows`() {
        val ack1 = sendMllp(adtA04(controlId = "DIFF00001", sendingApp = "EPIC"))
        val ack2 = sendMllp(adtA04(controlId = "DIFF00002", sendingApp = "EPIC"))

        assertThat(ack1).contains("MSA|AA|DIFF00001")
        assertThat(ack2).contains("MSA|AA|DIFF00002")

        val rows = repository.findAll().sortedBy { it.sourceId }
        assertThat(rows).hasSize(2)
        assertThat(rows.map { it.sourceId }).containsExactly("DIFF00001", "DIFF00002")
    }

    @Test
    @Order(4)
    fun `when the database is unreachable the route ACKs AE and persists nothing`() {
        // Sanity-check before we cut connectivity: the row count baseline is
        // zero (TRUNCATE in @BeforeEach). We can't query through HikariCP
        // while the DB is paused, so we check it again after un-pausing.
        assertThat(repository.count()).isZero()

        val dockerClient = postgres.dockerClient
        val containerId = postgres.containerId

        try {
            // `docker pause` freezes the container's processes (SIGSTOP-style)
            // without releasing the port mapping. This is strictly better than
            // `stop()` for testing DB outage: the port stays bound — Postgres
            // doesn't even ACCEPT new TCP connections (kernel queues them up
            // until the container un-pauses or the connect-timeout trips).
            // From HikariCP's perspective this looks like "DB is up but
            // hanging" — a real-world failure mode. `stop()` would reassign
            // the port on restart and invalidate the Spring-cached datasource.
            dockerClient.pauseContainerCmd(containerId).exec()

            // Drop any idle connections HikariCP already had in its pool —
            // those would still appear "valid" by checkout until they try
            // an actual statement. softEvictConnections marks each one for
            // disposal on next return; combined with connect-timeout on new
            // checkouts (10s from application.yaml), the next persist hits
            // a SQLException quickly.
            (dataSource as? com.zaxxer.hikari.HikariDataSource)?.hikariPoolMXBean?.softEvictConnections()

            val ack = sendMllp(adtA04(controlId = "DBDOWN00001", sendingApp = "EPIC"))

            // The route's onException handler converts the DB failure into
            // an AE ACK. We don't pin the exact wording of the AE reason
            // (HikariCP / Postgres JDBC driver messages vary by version),
            // just that the code is AE and the control id echoes through.
            assertThat(ack)
                .describedAs("DB-down failure should ACK AE, not AA")
                .contains("MSA|AE|DBDOWN00001")
        } finally {
            // Always un-pause, even if the assertion above failed, so the
            // rest of the test class (and subsequent test runs in the same
            // JVM) see a healthy Postgres again. Port mapping is unchanged
            // by pause/unpause, so the Spring-cached datasource just works
            // once Postgres starts processing again.
            dockerClient.unpauseContainerCmd(containerId).exec()
            // Wait for connectivity to come back before asserting on row
            // counts — the un-pause is async at the kernel level.
            await().atMost(Duration.ofSeconds(10)).until {
                runCatching { repository.count() }.isSuccess
            }
        }

        // Postgres is back; the row count should still be zero. The route
        // must not have persisted anything during the outage.
        assertThat(repository.count())
            .describedAs("DB-down path must not persist any row")
            .isZero()
    }

    // -- Helpers ---------------------------------------------------------

    private fun adtA04(controlId: String, sendingApp: String): String =
        listOf(
            "MSH|^~\\&|$sendingApp|HOSP|RECEIVER|CDS|20260625120000||ADT^A04|$controlId|P|2.5",
            "EVN|A04|20260625120000",
            "PID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M|||123 MAIN ST^^ANYTOWN^CA^94000",
            "PV1|1|O|2000^2012^01||||NPI001^WELBY^MARCUS|||AMB||||REG|A0",
        ).joinToString("\r") + "\r"

    /**
     * Open a TCP socket to the MLLP listener, frame and send `payload`, read
     * the response until we see the MLLP end-block byte. Returns the ACK
     * body (everything between the start- and end-block markers).
     */
    private fun sendMllp(payload: String): String {
        Socket("localhost", mllpPort).use { socket ->
            // 15s soTimeout > Hikari connection-timeout (4s set in
            // @DynamicPropertySource) so the DB-down test reliably reads
            // the AE ACK back instead of the test socket timing out first.
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
                        SB -> { /* drop start block */ }
                        EB -> return sink.toString()  // end block — done
                        else -> sink.append(b.toInt().toChar())
                    }
                }
            }
            return sink.toString()
        }
    }
}
