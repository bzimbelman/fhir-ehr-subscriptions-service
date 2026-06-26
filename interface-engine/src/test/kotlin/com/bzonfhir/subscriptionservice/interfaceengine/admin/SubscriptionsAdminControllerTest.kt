package com.bzonfhir.subscriptionservice.interfaceengine.admin

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.autoconfigure.jdbc.AutoConfigureTestDatabase
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.context.TestConfiguration
import org.springframework.boot.test.web.client.TestRestTemplate
import org.springframework.boot.test.web.server.LocalServerPort
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Primary
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
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Web-layer tests for the `/admin/subscriptions/` admin endpoints (Epic #387, ticket #390).
 *
 * Same scaffolding as [AdminMessagesControllerAuthOffTest]:
 *   - Real Postgres in Testcontainers so JPA/Flyway autoconfig is happy.
 *   - Random Spring web port via @SpringBootTest.WebEnvironment.RANDOM_PORT.
 *   - Two top-level classes — one auth-off, one auth-on — because
 *     @TestPropertySource binds at class-load time.
 *
 * Critical difference from the messages controller test: those tests use
 * the real [IngestedMessageRepository] (seeded by JDBC) because that
 * controller talks straight to Postgres. *This* controller talks to HAPI
 * via [HapiSubscriptionStatusClient]; we replace the implementation with
 * a [FakeHapiSubscriptionStatusClient] via @TestConfiguration so the
 * tests don't need a live HAPI server.
 */
private fun registerDatasource(
    registry: DynamicPropertyRegistry,
    container: PostgreSQLContainer<*>,
) {
    registry.add("spring.datasource.url") { container.jdbcUrl }
    registry.add("spring.datasource.username") { container.username }
    registry.add("spring.datasource.password") { container.password }
}

private fun pickFreePort(): Int = ServerSocket(0).use { it.localPort }

@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class SubscriptionsAdminControllerAuthOffTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubStatusClient(): HapiSubscriptionStatusClient = FakeHapiSubscriptionStatusClient()
    }

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())
            .withStartupTimeout(java.time.Duration.ofSeconds(60))

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; SubscriptionsAdminControllerTest requires Testcontainers."
            }
        }

        @JvmStatic
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
            // Disable the async worker — these tests don't touch the
            // ingest pipeline and we don't want a running worker thread
            // racing the bean graph.
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var statusClient: HapiSubscriptionStatusClient
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        (statusClient as FakeHapiSubscriptionStatusClient).reset()
    }

    private fun url(path: String) = "http://localhost:$port$path"
    private fun fake() = statusClient as FakeHapiSubscriptionStatusClient

    // ---------------------------------------------------------------------
    // Health endpoint
    // ---------------------------------------------------------------------

    @Test
    fun `health on empty HAPI returns total=0 and empty items`() {
        val resp = rest.getForEntity(url("/admin/subscriptions/health"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["total"]).isEqualTo(0)
        assertThat(resp.body!!["items"] as List<*>).isEmpty()
    }

    @Test
    fun `health with 3 subscriptions returns 3 items with correct summary fields`() {
        fake().addSubscription(
            id = "123",
            active = true,
            channelType = "rest-hook",
            endpoint = "https://example.com/notify",
            successCount = 1247,
            failureCount = 3,
            lastAttempt = OffsetDateTime.of(2026, 6, 26, 18, 0, 0, 0, ZoneOffset.UTC),
            lastOutcome = "success",
            lastError = null,
        )
        fake().addSubscription(
            id = "456",
            active = true,
            channelType = "rest-hook",
            endpoint = "https://other.example.com/hook",
            successCount = 5,
            failureCount = 0,
            lastAttempt = null,
            lastOutcome = null,
            lastError = null,
        )
        fake().addSubscription(
            id = "789",
            active = false,
            channelType = "email",
            endpoint = "mailto:alerts@example.com",
            successCount = 0,
            failureCount = 12,
            lastAttempt = OffsetDateTime.of(2026, 6, 25, 12, 0, 0, 0, ZoneOffset.UTC),
            lastOutcome = "failure",
            lastError = "550 mailbox unavailable",
        )

        val resp = rest.getForEntity(url("/admin/subscriptions/health"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["total"]).isEqualTo(3)

        @Suppress("UNCHECKED_CAST")
        val items = resp.body!!["items"] as List<Map<String, Any?>>
        assertThat(items).hasSize(3)
        val first = items[0]
        assertThat(first["id"]).isEqualTo("Subscription/123")
        assertThat(first["active"]).isEqualTo(true)
        assertThat(first["channel_type"]).isEqualTo("rest-hook")
        assertThat(first["endpoint"]).isEqualTo("https://example.com/notify")
        assertThat((first["delivery_success_count"] as Number).toLong()).isEqualTo(1247L)
        assertThat((first["delivery_failure_count"] as Number).toLong()).isEqualTo(3L)
        assertThat(first["last_attempt_outcome"]).isEqualTo("success")
        assertThat(first["last_error"]).isNull()

        val third = items[2]
        assertThat(third["id"]).isEqualTo("Subscription/789")
        assertThat(third["active"]).isEqualTo(false)
        assertThat(third["channel_type"]).isEqualTo("email")
        assertThat(third["last_attempt_outcome"]).isEqualTo("failure")
        assertThat(third["last_error"]).isEqualTo("550 mailbox unavailable")
    }

    // ---------------------------------------------------------------------
    // History endpoint
    // ---------------------------------------------------------------------

    @Test
    fun `history for unknown id returns 404`() {
        val resp = rest.getForEntity(url("/admin/subscriptions/does-not-exist/history"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
        assertThat(resp.body!!["error"]).isEqualTo("not_found")
    }

    @Test
    fun `history for known id returns delivery attempts`() {
        val events = listOf(
            DeliveryEvent(
                attemptedAt = OffsetDateTime.of(2026, 6, 26, 12, 0, 30, 0, ZoneOffset.UTC),
                outcome = "success",
                httpStatus = 200,
                error = null,
                durationMs = 142L,
            ),
            DeliveryEvent(
                attemptedAt = OffsetDateTime.of(2026, 6, 26, 12, 0, 0, 0, ZoneOffset.UTC),
                outcome = "failure",
                httpStatus = 503,
                error = "connection refused",
                durationMs = 30000L,
            ),
        )
        fake().addSubscription(
            id = "123",
            active = true,
            channelType = "rest-hook",
            endpoint = "https://example.com/notify",
            successCount = 1,
            failureCount = 1,
            lastAttempt = events[0].attemptedAt,
            lastOutcome = "success",
            lastError = null,
            events = events,
        )

        val resp = rest.getForEntity(url("/admin/subscriptions/123/history"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["subscription_id"]).isEqualTo("Subscription/123")
        assertThat((body["total"] as Number).toLong()).isEqualTo(2L)
        assertThat(body["limit"]).isEqualTo(50)
        assertThat(body["offset"]).isEqualTo(0)

        @Suppress("UNCHECKED_CAST")
        val items = body["items"] as List<Map<String, Any?>>
        assertThat(items).hasSize(2)
        assertThat(items[0]["outcome"]).isEqualTo("success")
        assertThat((items[0]["http_status"] as Number).toInt()).isEqualTo(200)
        assertThat(items[0]["error"]).isNull()
        assertThat((items[0]["duration_ms"] as Number).toLong()).isEqualTo(142L)

        assertThat(items[1]["outcome"]).isEqualTo("failure")
        assertThat((items[1]["http_status"] as Number).toInt()).isEqualTo(503)
        assertThat(items[1]["error"]).isEqualTo("connection refused")
    }

    @Test
    fun `history pagination respects limit and offset`() {
        val events = (1..5).map { n ->
            DeliveryEvent(
                attemptedAt = OffsetDateTime.of(2026, 6, 26, 12, n, 0, 0, ZoneOffset.UTC),
                outcome = "success",
                httpStatus = 200,
                error = null,
                durationMs = (n * 10L),
            )
        }
        fake().addSubscription(
            id = "p1",
            active = true,
            channelType = "rest-hook",
            endpoint = "https://example.com/p",
            successCount = 5,
            failureCount = 0,
            lastAttempt = events.first().attemptedAt,
            lastOutcome = "success",
            lastError = null,
            events = events,
        )

        val resp = rest.getForEntity(
            url("/admin/subscriptions/p1/history?limit=2&offset=2"),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat((body["total"] as Number).toLong()).isEqualTo(5L)
        assertThat(body["limit"]).isEqualTo(2)
        assertThat(body["offset"]).isEqualTo(2)
        @Suppress("UNCHECKED_CAST")
        val items = body["items"] as List<Map<String, Any?>>
        assertThat(items).hasSize(2)
        // Slice should be events[2] and events[3] in the as-added order.
        assertThat((items[0]["duration_ms"] as Number).toLong()).isEqualTo(30L)
        assertThat((items[1]["duration_ms"] as Number).toLong()).isEqualTo(40L)
    }

    @Test
    fun `auth OFF allows requests without Authorization header`() {
        val resp = rest.exchange(
            url("/admin/subscriptions/health"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

/**
 * Token configured → admin endpoints under `/admin/subscriptions/` require Bearer
 * auth. The same AdminAuthInterceptor handles it because that interceptor is
 * mounted on the `/admin/` glob, so no additional wiring is needed when a new
 * `/admin/...` controller is added.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=secret-test-token-390"])
class SubscriptionsAdminControllerAuthOnTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubStatusClient(): HapiSubscriptionStatusClient = FakeHapiSubscriptionStatusClient()
    }

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(org.testcontainers.containers.wait.strategy.Wait.forListeningPort())
            .withStartupTimeout(java.time.Duration.ofSeconds(60))

        @JvmStatic
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var rest: TestRestTemplate

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `auth ON without Authorization header returns 401 on health`() {
        val resp = rest.exchange(
            url("/admin/subscriptions/health"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON without Authorization header returns 401 on history`() {
        val resp = rest.exchange(
            url("/admin/subscriptions/anything/history"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON with correct token returns 200`() {
        val headers = HttpHeaders().apply { set("Authorization", "Bearer secret-test-token-390") }
        val resp = rest.exchange(
            url("/admin/subscriptions/health"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

/**
 * In-memory stub for [HapiSubscriptionStatusClient]. The real impl
 * round-trips to HAPI; here we just hold a map and return what tests
 * configure. Keeping the fake in this file (rather than a separate
 * package) avoids needing to make the production interface public.
 */
internal class FakeHapiSubscriptionStatusClient : HapiSubscriptionStatusClient {
    private val views = linkedMapOf<String, SubscriptionStatusView>()

    fun reset() {
        views.clear()
    }

    fun addSubscription(
        id: String,
        active: Boolean,
        channelType: String,
        endpoint: String?,
        successCount: Long,
        failureCount: Long,
        lastAttempt: OffsetDateTime?,
        lastOutcome: String?,
        lastError: String?,
        events: List<DeliveryEvent> = emptyList(),
    ) {
        views[id] = SubscriptionStatusView(
            subscriptionId = "Subscription/$id",
            active = active,
            channelType = channelType,
            endpoint = endpoint,
            deliverySuccessCount = successCount,
            deliveryFailureCount = failureCount,
            lastAttemptAt = lastAttempt,
            lastAttemptOutcome = lastOutcome,
            lastError = lastError,
            events = events,
        )
    }

    override fun listSubscriptions(): List<org.hl7.fhir.r4.model.Subscription> {
        // The controller only ever pulls .idElement?.idPart off these,
        // so a minimally populated Subscription resource is enough.
        return views.keys.map { id ->
            org.hl7.fhir.r4.model.Subscription().also { it.setId(id) }
        }
    }

    override fun readSubscription(id: String): org.hl7.fhir.r4.model.Subscription? =
        if (views.containsKey(id)) {
            org.hl7.fhir.r4.model.Subscription().also { it.setId(id) }
        } else {
            null
        }

    override fun statusFor(id: String): SubscriptionStatusView? = views[id]
}
