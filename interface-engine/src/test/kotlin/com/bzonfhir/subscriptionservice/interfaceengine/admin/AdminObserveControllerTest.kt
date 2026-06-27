package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.web.client.TestRestTemplate
import org.springframework.boot.test.web.server.LocalServerPort
import org.springframework.http.HttpEntity
import org.springframework.http.HttpHeaders
import org.springframework.http.HttpMethod
import org.springframework.http.HttpStatus
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.springframework.test.context.TestPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket

/**
 * Agent-queryable observability API tests (Epic #387, ticket #396).
 *
 * Two classes — auth off and auth on — same pattern as
 * `AdminMessagesControllerTest`. Postgres-backed via Testcontainers
 * because the `/throughput` endpoint uses `date_trunc` which H2 doesn't
 * implement identically.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class AdminObserveControllerAuthOffTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker required for AdminObserveControllerTest."
            }
        }

        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @JvmStatic
        @DynamicPropertySource
        fun props(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            registry.add("subscription-service.mllp.port") { mllpPort }
            // Worker disabled — tests seed rows and read them back; running worker
            // would race on RECEIVED rows.
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun clean() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `system endpoint returns schema_version + queue counts + toggles`() {
        repository.saveAndFlush(seed("S", "sys-1", IngestedMessageStatus.RECEIVED))
        repository.saveAndFlush(seed("S", "sys-2", IngestedMessageStatus.DEAD_LETTER))
        repository.saveAndFlush(seed("S", "sys-3", IngestedMessageStatus.DELIVERED))

        val resp = rest.getForEntity(url("/admin/observe/system"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["schema_version"]).isEqualTo("1.0")
        assertThat(body["service"]).asString().isNotBlank()
        // Jackson decodes small JSON numbers as Integer; treat all numerics uniformly via Number.
        assertThat((body["uptime_seconds"] as Number).toLong()).isGreaterThanOrEqualTo(0L)

        val toggles = body["feature_toggles"] as Map<*, *>
        assertThat(toggles.keys).contains(
            "auth_enabled", "validation_mode",
            "channel_security_mode", "multitenancy_mode",
        )

        val queue = body["queue"] as Map<*, *>
        assertThat((queue["received"] as Number).toLong()).isEqualTo(1L)
        assertThat((queue["dead_letter"] as Number).toLong()).isEqualTo(1L)
        assertThat((queue["delivered"] as Number).toLong()).isEqualTo(1L)
    }

    @Test
    fun `throughput endpoint buckets by hour for 24h window`() {
        repeat(3) { repository.saveAndFlush(seed("S", "tp-$it", IngestedMessageStatus.RECEIVED)) }

        val resp = rest.getForEntity(url("/admin/observe/throughput?window=24h"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["bucket_width"]).isEqualTo("hour")
        assertThat(body["window"]).isEqualTo("24h")
        val buckets = body["buckets"] as List<*>
        assertThat(buckets).isNotEmpty
    }

    @Test
    fun `throughput endpoint buckets by day for 7d window`() {
        repository.saveAndFlush(seed("S", "tp-day", IngestedMessageStatus.RECEIVED))
        val resp = rest.getForEntity(url("/admin/observe/throughput?window=7d"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["bucket_width"]).isEqualTo("day")
    }

    @Test
    fun `throughput rejects malformed window`() {
        val resp = rest.getForEntity(url("/admin/observe/throughput?window=oops"), Map::class.java)
        assertThat(resp.statusCode.is4xxClientError || resp.statusCode.is5xxServerError).isTrue()
    }

    @Test
    fun `dlq endpoint returns only DEAD_LETTER messages`() {
        repository.saveAndFlush(seed("S", "live-1", IngestedMessageStatus.RECEIVED))
        repository.saveAndFlush(seed("S", "dead-1", IngestedMessageStatus.DEAD_LETTER))
        repository.saveAndFlush(seed("S", "dead-2", IngestedMessageStatus.DEAD_LETTER))

        val resp = rest.getForEntity(url("/admin/observe/dlq"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat((body["total"] as Number).toLong()).isEqualTo(2L)
        val items = body["items"] as List<*>
        assertThat(items).hasSize(2)
        items.forEach {
            val m = it as Map<*, *>
            assertThat(m["status"]).isEqualTo("DEAD_LETTER")
        }
    }

    @Test
    fun `dlq endpoint caps limit at 100`() {
        val resp = rest.getForEntity(url("/admin/observe/dlq?limit=9999"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["limit"]).isEqualTo(100)
    }

    @Test
    fun `trace by correlation_id returns matching messages`() {
        val saved = repository.saveAndFlush(seed("S", "tr-1", IngestedMessageStatus.RECEIVED)
            .apply { correlationId = "abc-123" })

        val resp = rest.getForEntity(url("/admin/observe/trace/abc-123"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["correlation_id"]).isEqualTo("abc-123")
        val messages = body["messages"] as List<*>
        assertThat(messages).hasSize(1)
        assertThat(((messages[0] as Map<*, *>)["id"] as Number).toLong()).isEqualTo(saved.id)
    }

    @Test
    fun `trace returns 404 when correlation_id unknown`() {
        val resp = rest.getForEntity(url("/admin/observe/trace/no-such-id"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
        assertThat(resp.body!!["error"]).isEqualTo("not_found")
    }

    @Test
    fun `openapi spec is served as JSON`() {
        val resp = rest.getForEntity(url("/admin/observe/openapi.json"), String::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.headers.contentType.toString()).startsWith("application/json")
        // Spot check the spec content
        val body = resp.body!!
        assertThat(body).contains("\"openapi\":")
        assertThat(body).contains("/admin/observe/system")
        assertThat(body).contains("/admin/observe/throughput")
        assertThat(body).contains("/admin/observe/dlq")
        assertThat(body).contains("/admin/observe/trace/{correlationId}")
    }

    @Test
    fun `auth OFF allows all endpoints without bearer token`() {
        val resp = rest.exchange(
            url("/admin/observe/system"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=observe-test-token"])
class AdminObserveControllerAuthOnTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())

        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @JvmStatic
        @DynamicPropertySource
        fun props(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            registry.add("subscription-service.mllp.port") { mllpPort }
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var rest: TestRestTemplate
    @Autowired private lateinit var jdbc: JdbcTemplate

    @BeforeEach
    fun clean() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    private fun url(p: String) = "http://localhost:$port$p"

    @Test
    fun `auth ON rejects requests with no bearer token`() {
        val resp = rest.exchange(
            url("/admin/observe/system"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON rejects requests with wrong bearer token`() {
        val headers = HttpHeaders().apply { setBearerAuth("not-the-token") }
        val resp = rest.exchange(
            url("/admin/observe/system"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON accepts requests with correct bearer token`() {
        val headers = HttpHeaders().apply { setBearerAuth("observe-test-token") }
        val resp = rest.exchange(
            url("/admin/observe/system"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["schema_version"]).isEqualTo("1.0")
    }
}

private fun seed(
    sourceSystem: String,
    sourceId: String,
    status: IngestedMessageStatus,
) = IngestedMessage(
    sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
    sourceSystem = sourceSystem,
    sourceId = sourceId,
    messageType = "ADT_A04",
    rawMessage = "MSH|^~\\&|TEST|FAC|RECV|CDS|20260626120000||ADT^A04|$sourceId|P|2.5",
    rawContentType = "application/hl7-v2",
    status = status,
)
