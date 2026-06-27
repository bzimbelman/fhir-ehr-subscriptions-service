package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import com.sun.net.httpserver.HttpExchange
import com.sun.net.httpserver.HttpHandler
import com.sun.net.httpserver.HttpServer
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.BeforeAll
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
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.time.Duration
import java.util.concurrent.atomic.AtomicInteger

/**
 * Web-layer tests for the actuator health indicators added in
 * Epic #387, ticket #393.
 *
 * # Test fixtures
 *
 * Two in-process [HttpServer] instances ([matchboxServer], [hapiServer])
 * back the matchbox + HAPI base URLs. Each can be configured per-test to
 * return 2xx (UP) or 5xx (DOWN). The interface-engine connects to them via
 * the actuator pipeline — same code path as production — so the assertions
 * exercise the real Spring Boot health-aggregator logic, not a mock.
 *
 * Postgres comes from Testcontainers so the built-in `db` indicator + the
 * `dlqBacklog` indicator both see a real datasource.
 *
 * # What's covered
 *
 *   1. `/actuator/health/liveness` returns UP when local DB is reachable,
 *      even if Matchbox + HAPI are returning 5xx.
 *   2. `/actuator/health/readiness` returns DOWN when Matchbox is 5xx.
 *   3. `/actuator/health/readiness` returns DOWN when HAPI is 5xx.
 *   4. DLQ DEGRADED status when >= 10 DEAD_LETTER rows; details.dlq_count=10.
 *   5. `/actuator/info` snapshot includes feature toggles + downstream URLs
 *      and masks secrets (audience, ipf_db.password).
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(
    properties = [
        // Disable the worker so it doesn't churn DEAD_LETTER rows we seed.
        "subscription-service.worker.enabled=false",
        // Feature toggles + auth values used by the InfoContributor.
        "subscription-service.auth.enabled=true",
        "subscription-service.auth.issuer=https://idp.example.com/realms/test",
        "subscription-service.auth.jwks-url=https://idp.example.com/realms/test/protocol/openid-connect/certs",
        "subscription-service.auth.audience=test-audience",
        "subscription-service.validation.mode=warn",
        "subscription-service.channel-security=strict",
        "subscription-service.multitenancy=disabled",
        // Lower the DLQ threshold default so the DEGRADED test doesn't have
        // to seed dozens of rows.
        "subscription-service.health.dlq.warn-threshold=10",
        // Tight downstream timeouts so a flapping fixture doesn't slow the test.
        "subscription-service.health.downstream-timeout-ms=1000",
        // Admin token left empty (masked output below validates the
        // "empty stays empty, present becomes ********" branch).
        "ipf.admin.auth-token=",
    ],
)
class HealthIndicatorsTest {

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

        /**
         * In-process matchbox stub. The next response status is taken from
         * [matchboxStatus]; tests flip it as needed. Path is `/metadata` —
         * matches the FHIR CapabilityStatement endpoint the indicator hits.
         */
        @JvmStatic
        lateinit var matchboxServer: HttpServer

        /** Same shape as matchboxServer — separate so the two are independent. */
        @JvmStatic
        lateinit var hapiServer: HttpServer

        @JvmStatic val matchboxStatus = AtomicInteger(200)

        @JvmStatic val hapiStatus = AtomicInteger(200)

        @JvmStatic var matchboxPort: Int = 0
        @JvmStatic var hapiPort: Int = 0

        @BeforeAll
        @JvmStatic
        fun startStubs() {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; HealthIndicatorsTest requires Testcontainers."
            }
            matchboxServer = startStubServer { matchboxStatus.get() }
            hapiServer = startStubServer { hapiStatus.get() }
            matchboxPort = matchboxServer.address.port
            hapiPort = hapiServer.address.port
        }

        @AfterAll
        @JvmStatic
        fun stopStubs() {
            matchboxServer.stop(0)
            hapiServer.stop(0)
        }

        private fun startStubServer(statusSupplier: () -> Int): HttpServer {
            val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
            server.createContext("/", object : HttpHandler {
                override fun handle(exchange: HttpExchange) {
                    val status = statusSupplier()
                    // Send a tiny FHIR-ish body so the BodyHandler doesn't choke
                    // on completely empty 200s. The indicator only reads the
                    // status code; body content is irrelevant.
                    val body = "{\"resourceType\":\"CapabilityStatement\"}".toByteArray()
                    exchange.responseHeaders.set("Content-Type", "application/fhir+json")
                    exchange.sendResponseHeaders(status, body.size.toLong())
                    exchange.responseBody.use { it.write(body) }
                }
            })
            server.start()
            return server
        }

        @JvmStatic
        @DynamicPropertySource
        fun props(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            registry.add("subscription-service.mllp.port") { mllpPort }
            // Point matchbox + HAPI at the local stubs.
            registry.add("subscription-service.matchbox.base-url") {
                "http://127.0.0.1:$matchboxPort"
            }
            registry.add("subscription-service.hapi.base-url") {
                "http://127.0.0.1:$hapiPort"
            }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var rest: TestRestTemplate
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var jdbc: JdbcTemplate

    private fun url(path: String) = "http://localhost:$port$path"

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        matchboxStatus.set(200)
        hapiStatus.set(200)
    }

    @Test
    fun `liveness UP even when matchbox and hapi are down`() {
        matchboxStatus.set(503)
        hapiStatus.set(503)

        val resp = rest.getForEntity(url("/actuator/health/liveness"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["status"]).isEqualTo("UP")
    }

    @Test
    fun `readiness DOWN when matchbox returns 5xx`() {
        matchboxStatus.set(503)
        hapiStatus.set(200)

        val resp = rest.getForEntity(url("/actuator/health/readiness"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE)
        assertThat(resp.body!!["status"]).isEqualTo("DOWN")
        @Suppress("UNCHECKED_CAST")
        val components = resp.body!!["components"] as Map<String, Map<String, Any>>
        assertThat(components["matchbox"]!!["status"]).isEqualTo("DOWN")
        assertThat(components["hapi"]!!["status"]).isEqualTo("UP")
    }

    @Test
    fun `readiness DOWN when hapi returns 5xx`() {
        matchboxStatus.set(200)
        hapiStatus.set(500)

        val resp = rest.getForEntity(url("/actuator/health/readiness"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.SERVICE_UNAVAILABLE)
        assertThat(resp.body!!["status"]).isEqualTo("DOWN")
        @Suppress("UNCHECKED_CAST")
        val components = resp.body!!["components"] as Map<String, Map<String, Any>>
        assertThat(components["hapi"]!!["status"]).isEqualTo("DOWN")
        assertThat(components["matchbox"]!!["status"]).isEqualTo("UP")
    }

    @Test
    fun `readiness UP when both downstreams healthy and no DLQ rows`() {
        val resp = rest.getForEntity(url("/actuator/health/readiness"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["status"]).isEqualTo("UP")
    }

    @Test
    fun `dlqBacklog shows DEGRADED when DEAD_LETTER count crosses threshold`() {
        repeat(10) {
            repository.saveAndFlush(deadRow("dlq-$it"))
        }

        val resp = rest.getForEntity(url("/actuator/health/readiness"), Map::class.java)
        // DEGRADED is a custom status — Spring's HealthAggregator maps it
        // through the default status->HTTP table which is silent on
        // DEGRADED, so the overall HTTP code falls back to 200 (UP) for
        // unknown statuses unless explicitly mapped. Our other indicators
        // are UP, so aggregate status is the worst of UP, UP, DEGRADED, UP, UP
        // → DEGRADED. We don't assert on the aggregate HTTP code (Spring's
        // mapping for custom statuses is intentionally not in the contract)
        // — only on the per-indicator detail.
        @Suppress("UNCHECKED_CAST")
        val components = resp.body!!["components"] as Map<String, Map<String, Any>>
        val dlq = components["dlqBacklog"]!!
        assertThat(dlq["status"]).isEqualTo("DEGRADED")
        @Suppress("UNCHECKED_CAST")
        val details = dlq["details"] as Map<String, Any>
        assertThat((details["dlq_count"] as Number).toLong()).isEqualTo(10L)
        assertThat((details["warn_threshold"] as Number).toLong()).isEqualTo(10L)
    }

    @Test
    fun `dlqBacklog stays UP below threshold`() {
        repeat(3) {
            repository.saveAndFlush(deadRow("dlq-low-$it"))
        }

        val resp = rest.getForEntity(url("/actuator/health/readiness"), Map::class.java)
        @Suppress("UNCHECKED_CAST")
        val components = resp.body!!["components"] as Map<String, Map<String, Any>>
        val dlq = components["dlqBacklog"]!!
        assertThat(dlq["status"]).isEqualTo("UP")
        @Suppress("UNCHECKED_CAST")
        val details = dlq["details"] as Map<String, Any>
        assertThat((details["dlq_count"] as Number).toLong()).isEqualTo(3L)
    }

    @Test
    fun `info snapshot includes feature toggles and masks secrets`() {
        val resp = rest.getForEntity(url("/actuator/info"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        @Suppress("UNCHECKED_CAST")
        val body = resp.body!! as Map<String, Any>
        @Suppress("UNCHECKED_CAST")
        val service = body["subscription_service"] as Map<String, Any>

        @Suppress("UNCHECKED_CAST")
        val toggles = service["feature_toggles"] as Map<String, Any>
        assertThat(toggles["auth_enabled"]).isEqualTo(true)
        assertThat(toggles["validation_mode"]).isEqualTo("warn")
        assertThat(toggles["channel_security"]).isEqualTo("strict")
        assertThat(toggles["multitenancy"]).isEqualTo("disabled")

        @Suppress("UNCHECKED_CAST")
        val auth = service["auth"] as Map<String, Any>
        assertThat(auth["issuer"]).isEqualTo("https://idp.example.com/realms/test")
        assertThat(auth["jwks_url"]).isEqualTo(
            "https://idp.example.com/realms/test/protocol/openid-connect/certs",
        )
        assertThat(auth["audience"]).isEqualTo("********")

        @Suppress("UNCHECKED_CAST")
        val downstream = service["downstream"] as Map<String, Any>
        assertThat(downstream["matchbox_base_url"] as String).startsWith("http://127.0.0.1:")
        assertThat(downstream["hapi_base_url"] as String).startsWith("http://127.0.0.1:")

        @Suppress("UNCHECKED_CAST")
        val db = service["ipf_db"] as Map<String, Any>
        assertThat(db["url"] as String).startsWith("jdbc:postgresql://")
        assertThat(db["user"]).isEqualTo("ipf")
        // Real Postgres password configured by Testcontainers → masked.
        assertThat(db["password"]).isEqualTo("********")

        @Suppress("UNCHECKED_CAST")
        val admin = service["admin"] as Map<String, Any>
        // Token left empty in @TestPropertySource → stays empty (NOT
        // ********, so operators can tell "unset" apart from "masked").
        assertThat(admin["auth_token"]).isEqualTo("")
    }

    private fun deadRow(sourceId: String): IngestedMessage =
        IngestedMessage(
            sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
            sourceSystem = "TEST",
            sourceId = sourceId,
            messageType = "ADT^A01",
            rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626120000||ADT^A01|$sourceId|P|2.5",
            rawContentType = "application/hl7-v2",
            status = IngestedMessageStatus.DEAD_LETTER,
        )
}
