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
 * Web-layer + persistence tests for the admin REST API (Epic #378, ticket #384).
 *
 * Uses a real Postgres in Testcontainers (a single JVM-wide instance shared
 * across both test classes) so JPQL pagination, status-enum binding, and
 * the 409/204 state-machine paths run against the real database.
 * RestTemplate exercises the full Spring MVC stack: interceptor → controller
 * → repo.
 *
 * Split into two top-level classes because @SpringBootTest binds properties
 * at class-load time — toggling the admin token between methods of the
 * same context isn't possible. AuthOff is the bare default (env var unset);
 * AuthOn forces a token in via @TestPropertySource.
 *
 * Sharing is via the [PostgresContainer] singleton, not @Testcontainers
 * field injection, because the framework would otherwise stop the
 * container at the end of one class's tests — and the next class needs
 * it still running.
 */
private fun registerDatasource(
    registry: DynamicPropertyRegistry,
    container: PostgreSQLContainer<*>,
) {
    registry.add("spring.datasource.url") { container.jdbcUrl }
    registry.add("spring.datasource.username") { container.username }
    registry.add("spring.datasource.password") { container.password }
}

/**
 * Pick a free TCP port for the MLLP listener. The two test classes here
 * each get a fresh Spring context (different `ipf.admin.auth-token`
 * property), and Spring's test context cache keeps the first one alive
 * while the second loads — so both would otherwise try to bind 2575 and
 * the second fails. Binding + immediately releasing a socket here finds
 * a free port; Camel's MLLP consumer grabs it ~100ms later.
 */
private fun pickFreePort(): Int = ServerSocket(0).use { it.localPort }

@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class AdminMessagesControllerAuthOffTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            // Wait for the Postgres TCP listener to accept a connection on
            // the mapped host port — the default LogMessageWaitStrategy
            // checks the container's stdout for "database system is ready
            // to accept connections" twice (which fires before the host
            // port-forward is open on Rancher Desktop, racing the JDBC
            // connect). HostPortWaitStrategy is closer to what we actually
            // care about and resolves the flake on first run.
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())
            .withStartupTimeout(java.time.Duration.ofSeconds(60))

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; AdminMessagesControllerTest requires Testcontainers."
            }
        }

        @JvmStatic
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun cleanTable() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `list on empty DB returns total=0 and empty items`() {
        val resp = rest.getForEntity(url("/admin/messages"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["total"]).isEqualTo(0)
        assertThat(body["items"] as List<*>).isEmpty()
    }

    @Test
    fun `list after seeding 5 rows returns total=5`() {
        repeat(5) { repository.saveAndFlush(newMsg("S", "list-$it")) }

        val resp = rest.getForEntity(url("/admin/messages"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["total"]).isEqualTo(5)
        val items = resp.body!!["items"] as List<*>
        assertThat(items).hasSize(5)
    }

    @Test
    fun `list with status filter returns only DEAD_LETTER rows`() {
        repeat(2) { repository.saveAndFlush(newMsg("S", "received-$it")) }
        repeat(3) {
            repository.saveAndFlush(
                newMsg("S", "dead-$it", status = IngestedMessageStatus.DEAD_LETTER),
            )
        }

        val resp = rest.getForEntity(
            url("/admin/messages?status=DEAD_LETTER"),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["total"]).isEqualTo(3)
        @Suppress("UNCHECKED_CAST")
        val items = resp.body!!["items"] as List<Map<String, Any>>
        assertThat(items).hasSize(3)
        assertThat(items).allSatisfy { item ->
            assertThat(item["status"]).isEqualTo("DEAD_LETTER")
        }
    }

    @Test
    fun `list with limit=2 offset=2 paginates correctly`() {
        repeat(5) { repository.saveAndFlush(newMsg("S", "p-$it")) }

        val resp = rest.getForEntity(
            url("/admin/messages?limit=2&offset=2"),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["total"]).isEqualTo(5)
        assertThat(resp.body!!["limit"]).isEqualTo(2)
        assertThat(resp.body!!["offset"]).isEqualTo(2)
        val items = resp.body!!["items"] as List<*>
        assertThat(items).hasSize(2)
    }

    @Test
    fun `get by id returns full row including raw_message`() {
        val saved = repository.saveAndFlush(newMsg("S", "get-1"))
        val resp = rest.getForEntity(url("/admin/messages/${saved.id}"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["raw_message"]).isNotNull()
        assertThat(resp.body!!["raw_message"] as String).contains("MSH")
        assertThat(resp.body!!["raw_content_type"]).isEqualTo("application/hl7-v2")
    }

    @Test
    fun `get by unknown id returns 404`() {
        val resp = rest.getForEntity(url("/admin/messages/99999"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
        assertThat(resp.body!!["error"]).isEqualTo("not_found")
    }

    @Test
    fun `retry on FAILED row resets to RECEIVED with attempt_count=0`() {
        val saved = repository.saveAndFlush(
            newMsg("S", "fail-1", status = IngestedMessageStatus.FAILED).also {
                it.attemptCount = 4
                it.lastError = "boom"
            },
        )
        val resp = rest.postForEntity(
            url("/admin/messages/${saved.id}/retry"),
            null,
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["status"]).isEqualTo("RECEIVED")
        assertThat(resp.body!!["attempt_count"]).isEqualTo(0)
        assertThat(resp.body!!["last_error"]).isNull()
        val reloaded = repository.findById(saved.id!!).orElseThrow()
        assertThat(reloaded.status).isEqualTo(IngestedMessageStatus.RECEIVED)
        assertThat(reloaded.attemptCount).isEqualTo(0)
        assertThat(reloaded.lastError).isNull()
    }

    @Test
    fun `retry on DELIVERED row returns 409`() {
        val saved = repository.saveAndFlush(
            newMsg("S", "ok-1", status = IngestedMessageStatus.DELIVERED),
        )
        val resp = rest.postForEntity(
            url("/admin/messages/${saved.id}/retry"),
            null,
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.CONFLICT)
        assertThat(resp.body!!["error"]).isEqualTo("invalid_state_for_retry")
    }

    @Test
    fun `delete on DEAD_LETTER row returns 204`() {
        val saved = repository.saveAndFlush(
            newMsg("S", "dead-1", status = IngestedMessageStatus.DEAD_LETTER),
        )
        val resp = rest.exchange(
            url("/admin/messages/${saved.id}"),
            HttpMethod.DELETE,
            null,
            Void::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NO_CONTENT)
        assertThat(repository.findById(saved.id!!)).isEmpty
    }

    @Test
    fun `delete on RECEIVED row returns 409`() {
        val saved = repository.saveAndFlush(newMsg("S", "live-1"))
        val resp = rest.exchange(
            url("/admin/messages/${saved.id}"),
            HttpMethod.DELETE,
            null,
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.CONFLICT)
        assertThat(resp.body!!["error"]).isEqualTo("invalid_state_for_delete")
    }

    @Test
    fun `auth OFF allows requests without Authorization header`() {
        val headers = HttpHeaders()
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

/**
 * Token configured → all `/admin/` glob requests must carry
 * `Authorization: Bearer secret-test-token`.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=secret-test-token"])
class AdminMessagesControllerAuthOnTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            // Wait for the Postgres TCP listener to accept a connection on
            // the mapped host port — the default LogMessageWaitStrategy
            // checks the container's stdout for "database system is ready
            // to accept connections" twice (which fires before the host
            // port-forward is open on Rancher Desktop, racing the JDBC
            // connect). HostPortWaitStrategy is closer to what we actually
            // care about and resolves the flake on first run.
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())
            .withStartupTimeout(java.time.Duration.ofSeconds(60))

        @JvmStatic
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun cleanTable() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `auth ON without Authorization header returns 401`() {
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON with wrong token returns 401`() {
        val headers = HttpHeaders().apply { set("Authorization", "Bearer wrong-token") }
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON with correct token returns 200`() {
        val headers = HttpHeaders().apply { set("Authorization", "Bearer secret-test-token") }
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

private fun newMsg(
    sourceSystem: String,
    sourceId: String,
    status: IngestedMessageStatus = IngestedMessageStatus.RECEIVED,
) = IngestedMessage(
    sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
    sourceSystem = sourceSystem,
    sourceId = sourceId,
    messageType = "ADT^A01",
    rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626120000||ADT^A01|$sourceId|P|2.5",
    rawContentType = "application/hl7-v2",
    status = status,
)
