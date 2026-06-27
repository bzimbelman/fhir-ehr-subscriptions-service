package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.Subscription
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
import org.testcontainers.containers.wait.strategy.Wait
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket
import java.time.Duration
import java.time.OffsetDateTime

/**
 * Web-layer + persistence tests for the per-message effects admin endpoint
 * (Epic #387, ticket #392).
 *
 * Follows the [AdminMessagesControllerTest] pattern: real Postgres via
 * Testcontainers (so V005's TEXT[] column actually round-trips through
 * the JDBC array path), a stub [HapiSubscriptionStatusClient] for the HAPI
 * side data (no real HAPI in scope), and TestRestTemplate against the
 * full Spring MVC stack so the [AdminAuthInterceptor] glob covers the
 * new endpoint just like the existing ones.
 *
 * Split into two classes: AuthOff for the bulk of behaviour assertions,
 * AuthOn for the bearer-token gate (mirroring the pattern in
 * AdminMessagesControllerTest — @SpringBootTest binds the property at
 * class-load time, so two contexts are needed).
 */
private fun registerDatasource(
    registry: DynamicPropertyRegistry,
    container: PostgreSQLContainer<*>,
) {
    registry.add("spring.datasource.url") { container.jdbcUrl }
    registry.add("spring.datasource.username") { container.username }
    registry.add("spring.datasource.password") { container.password }
}

/** See [AdminMessagesControllerTest.pickFreePort] for the rationale. */
private fun pickFreePort(): Int = ServerSocket(0).use { it.localPort }

/**
 * Stub [HapiSubscriptionStatusClient] that returns whatever lists the
 * tests have configured. Threadsafe enough for the single test thread
 * pattern. @Primary lets it win bean resolution over the real impl
 * without us having to suppress the real bean.
 */
class StubHapiSubscriptionStatusClient : HapiSubscriptionStatusClient {
    @Volatile var subscriptions: List<Subscription> = emptyList()
    @Volatile var statusViews: Map<String, SubscriptionStatusView> = emptyMap()

    override fun listSubscriptions(): List<Subscription> = subscriptions
    override fun readSubscription(id: String): Subscription? =
        subscriptions.firstOrNull { it.idElement?.idPart == id }
    override fun statusFor(id: String): SubscriptionStatusView? = statusViews[id]
    // Ticket #404 added this; this stub is read-only so a no-op is fine.
    override fun setStatus(id: String, newStatus: String): SubscriptionStatusView? = statusViews[id]

    fun reset() {
        subscriptions = emptyList()
        statusViews = emptyMap()
    }
}

@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class MessageEffectsControllerAuthOffTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(Wait.forListeningPort())
            .withStartupTimeout(Duration.ofSeconds(60))

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; MessageEffectsControllerTest requires Testcontainers."
            }
        }

        @JvmStatic
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
            // Disable the async worker — effects-API tests seed rows
            // directly via the repository.
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    /**
     * Replace the real HAPI-side client with a stub so subscription
     * matching + notification listing run deterministically against
     * what the tests configure.
     */
    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubStatusClient(): HapiSubscriptionStatusClient = StubHapiSubscriptionStatusClient()
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate
    @Autowired private lateinit var statusClient: HapiSubscriptionStatusClient

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
        (statusClient as StubHapiSubscriptionStatusClient).reset()
    }

    private fun url(path: String) = "http://localhost:$port$path"

    // -----------------------------------------------------------------------
    // 1. Happy path: DELIVERED with two created resources.
    // -----------------------------------------------------------------------
    @Test
    fun `happy path - DELIVERED row returns the two FHIR resources`() {
        val saved = repository.saveAndFlush(
            newMsg(
                sourceSystem = "EPIC",
                sourceId = "HAPPY00001",
                status = IngestedMessageStatus.DELIVERED,
                createdResourceRefs = arrayOf("Patient/123", "Encounter/456"),
            ).also {
                it.deliveredAt = OffsetDateTime.now()
                it.attemptCount = 1
            },
        )

        val resp = rest.getForEntity(url("/admin/messages/${saved.id}/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["effects_status"]).isEqualTo("delivered")

        @Suppress("UNCHECKED_CAST")
        val msg = body["message"] as Map<String, Any?>
        assertThat(msg["status"]).isEqualTo("DELIVERED")
        assertThat(msg["source_system"]).isEqualTo("EPIC")
        assertThat(msg["source_id"]).isEqualTo("HAPPY00001")

        @Suppress("UNCHECKED_CAST")
        val transform = body["transform"] as Map<String, Any?>
        assertThat(transform["delivered_at"]).isNotNull()
        assertThat(transform["attempt_count"]).isEqualTo(1)

        @Suppress("UNCHECKED_CAST")
        val resources = body["fhir_resources_created"] as List<Map<String, Any?>>
        assertThat(resources).hasSize(2)
        assertThat(resources[0]["resource_type"]).isEqualTo("Patient")
        assertThat(resources[0]["id"]).isEqualTo("Patient/123")
        assertThat(resources[1]["resource_type"]).isEqualTo("Encounter")
        assertThat(resources[1]["id"]).isEqualTo("Encounter/456")

        // No subs configured on the stub → empty matches + notifications.
        assertThat(body["subscriptions_matched"] as List<*>).isEmpty()
        assertThat(body["notifications_fired"] as List<*>).isEmpty()
    }

    // -----------------------------------------------------------------------
    // 2. Unknown id → 404.
    // -----------------------------------------------------------------------
    @Test
    fun `unknown id returns 404`() {
        val resp = rest.getForEntity(url("/admin/messages/99999/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
        assertThat(resp.body!!["error"]).isEqualTo("not_found")
    }

    // -----------------------------------------------------------------------
    // 3. RECEIVED row → 200 with effects_status=pending, empty lists.
    //    The ticket spec called this "402" by mistake; the right shape
    //    is a 200 with pending semantics so an operator can poll until
    //    the worker drains it.
    // -----------------------------------------------------------------------
    @Test
    fun `RECEIVED row returns pending with empty resource list`() {
        val saved = repository.saveAndFlush(
            newMsg(
                sourceSystem = "EPIC",
                sourceId = "PENDING001",
                status = IngestedMessageStatus.RECEIVED,
            ),
        )
        val resp = rest.getForEntity(url("/admin/messages/${saved.id}/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["effects_status"]).isEqualTo("pending")
        assertThat(body["fhir_resources_created"] as List<*>).isEmpty()
        assertThat(body["subscriptions_matched"] as List<*>).isEmpty()
        assertThat(body["notifications_fired"] as List<*>).isEmpty()
    }

    // -----------------------------------------------------------------------
    // 4. DEAD_LETTER row → 200 with effects_status=failed, last_error surfaced.
    // -----------------------------------------------------------------------
    @Test
    fun `DEAD_LETTER row returns failed with last_error`() {
        val saved = repository.saveAndFlush(
            newMsg(
                sourceSystem = "EPIC",
                sourceId = "DEAD001",
                status = IngestedMessageStatus.DEAD_LETTER,
            ).also {
                it.lastError = "matchbox transform failed: out of memory"
                it.attemptCount = 5
            },
        )
        val resp = rest.getForEntity(url("/admin/messages/${saved.id}/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["effects_status"]).isEqualTo("failed")

        @Suppress("UNCHECKED_CAST")
        val msg = body["message"] as Map<String, Any?>
        assertThat(msg["status"]).isEqualTo("DEAD_LETTER")
        assertThat(msg["last_error"] as String).contains("matchbox")

        assertThat(body["fhir_resources_created"] as List<*>).isEmpty()
        assertThat(body["subscriptions_matched"] as List<*>).isEmpty()
    }

    // -----------------------------------------------------------------------
    // 5. DELIVERED but NULL refs (pre-V005 OR no-transform short-circuit)
    //    → effects_status=unknown.
    // -----------------------------------------------------------------------
    @Test
    fun `DELIVERED row with null refs returns unknown`() {
        val saved = repository.saveAndFlush(
            newMsg(
                sourceSystem = "EPIC",
                sourceId = "PREV005001",
                status = IngestedMessageStatus.DELIVERED,
                createdResourceRefs = null,
            ).also {
                it.deliveredAt = OffsetDateTime.now()
            },
        )
        val resp = rest.getForEntity(url("/admin/messages/${saved.id}/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["effects_status"]).isEqualTo("unknown")
        assertThat(body["fhir_resources_created"] as List<*>).isEmpty()
    }

    // -----------------------------------------------------------------------
    // 6. Subscription matching: a Subscription with criteria="Patient?gender=female"
    //    matches a Patient resource by type-prefix.
    // -----------------------------------------------------------------------
    @Test
    fun `subscriptions_matched contains type-prefix-matching subscription`() {
        val deliveredAt = OffsetDateTime.now()
        val saved = repository.saveAndFlush(
            newMsg(
                sourceSystem = "EPIC",
                sourceId = "SUBMATCH001",
                status = IngestedMessageStatus.DELIVERED,
                createdResourceRefs = arrayOf("Patient/123", "Encounter/456"),
            ).also {
                it.deliveredAt = deliveredAt
                it.attemptCount = 1
            },
        )

        val stub = statusClient as StubHapiSubscriptionStatusClient
        val patientSub = Subscription().apply {
            id = "abc"
            criteria = "Patient?gender=female"
            channel.type = Subscription.SubscriptionChannelType.RESTHOOK
            channel.endpoint = "https://subscriber.example.com/notify"
        }
        val unrelatedSub = Subscription().apply {
            id = "xyz"
            criteria = "Observation?code=loinc|123"
            channel.type = Subscription.SubscriptionChannelType.RESTHOOK
            channel.endpoint = "https://other.example.com/notify"
        }
        stub.subscriptions = listOf(patientSub, unrelatedSub)
        stub.statusViews = mapOf(
            "abc" to SubscriptionStatusView(
                subscriptionId = "Subscription/abc",
                active = true,
                // Ticket #404: status + criteria fields added to the view.
                status = "active",
                criteria = "Patient?",
                channelType = "rest-hook",
                endpoint = "https://subscriber.example.com/notify",
                deliverySuccessCount = 1,
                deliveryFailureCount = 0,
                lastAttemptAt = deliveredAt.plusSeconds(2),
                lastAttemptOutcome = "success",
                lastError = null,
                events = listOf(
                    DeliveryEvent(
                        attemptedAt = deliveredAt.plusSeconds(2),
                        outcome = "success",
                        httpStatus = 200,
                        error = null,
                        durationMs = 142,
                    ),
                ),
            ),
        )

        val resp = rest.getForEntity(url("/admin/messages/${saved.id}/effects"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!

        @Suppress("UNCHECKED_CAST")
        val subs = body["subscriptions_matched"] as List<Map<String, Any?>>
        assertThat(subs).hasSize(1)
        assertThat(subs[0]["id"]).isEqualTo("Subscription/abc")
        assertThat(subs[0]["channel_type"]).isEqualTo("rest-hook")
        assertThat(subs[0]["criteria"]).isEqualTo("Patient?gender=female")
        assertThat(subs[0]["matched_resource"]).isEqualTo("Patient/123")
        assertThat(subs[0]["tbd_match_precise"]).isEqualTo(true)

        @Suppress("UNCHECKED_CAST")
        val notifications = body["notifications_fired"] as List<Map<String, Any?>>
        assertThat(notifications).hasSize(1)
        assertThat(notifications[0]["subscription_id"]).isEqualTo("Subscription/abc")
        assertThat(notifications[0]["outcome"]).isEqualTo("success")
        assertThat(notifications[0]["http_status"]).isEqualTo(200)
        assertThat(notifications[0]["tbd_time_windowed"]).isEqualTo(true)
    }
}

/**
 * Auth gate. Bearer token configured → unauthenticated requests get 401.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=secret-effects-token"])
class MessageEffectsControllerAuthOnTest {

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
        private val mllpPort: Int = pickFreePort()

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registerDatasource(registry, postgres)
            registry.add("subscription-service.mllp.port") { mllpPort }
            registry.add("subscription-service.worker.enabled") { "false" }
        }
    }

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun stubStatusClient(): HapiSubscriptionStatusClient = StubHapiSubscriptionStatusClient()
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var repository: IngestedMessageRepository
    @Autowired private lateinit var jdbc: JdbcTemplate
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun reset() {
        jdbc.execute("TRUNCATE TABLE ingested_messages RESTART IDENTITY")
    }

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `auth ON without Authorization header returns 401`() {
        val resp = rest.exchange(
            url("/admin/messages/1/effects"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON with correct token returns 404 for unknown id - confirms auth pass-through`() {
        val headers = HttpHeaders().apply { set("Authorization", "Bearer secret-effects-token") }
        val resp = rest.exchange(
            url("/admin/messages/99999/effects"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        // Auth passed → controller ran → 404 because id doesn't exist.
        // The point of this test is the 401 vs 404 distinction.
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
    }
}

private fun newMsg(
    sourceSystem: String,
    sourceId: String,
    status: IngestedMessageStatus = IngestedMessageStatus.RECEIVED,
    createdResourceRefs: Array<String>? = null,
) = IngestedMessage(
    sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
    sourceSystem = sourceSystem,
    sourceId = sourceId,
    messageType = "ADT^A01",
    rawMessage = "MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626120000||ADT^A01|$sourceId|P|2.5",
    rawContentType = "application/hl7-v2",
    status = status,
    createdResourceRefs = createdResourceRefs,
)
