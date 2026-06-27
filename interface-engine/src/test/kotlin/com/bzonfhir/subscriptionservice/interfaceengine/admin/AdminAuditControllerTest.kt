package com.bzonfhir.subscriptionservice.interfaceengine.admin

import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAgentComponent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventEntityComponent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Coding
import org.hl7.fhir.r4.model.Reference
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
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.springframework.test.context.TestPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket
import java.util.Date

/**
 * Web-layer tests for `/admin/audit` (Epic #398, ticket #407).
 *
 * Scaffolding mirrors [AdminMatchboxControllerTest]: real Postgres in
 * Testcontainers for the Spring autoconfig, random web port, and the
 * controller's HAPI client is replaced with a [FakeHapiAuditClient] so
 * the tests don't need a live HAPI.
 *
 * Two top-level classes: auth-off (covers the search + read happy paths
 * + filter passthrough) and auth-on (covers the 401 + bearer-auth
 * integration with the existing [AdminAuthInterceptor]).
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
class AdminAuditControllerAuthOffTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun fakeHapiAuditClient(): HapiAuditClient = FakeHapiAuditClient()
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
                "Docker is not available; AdminAuditControllerTest requires Testcontainers."
            }
        }

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
    @Autowired private lateinit var client: HapiAuditClient
    @Autowired private lateinit var rest: TestRestTemplate

    private fun url(path: String) = "http://localhost:$port$path"
    private fun fake() = client as FakeHapiAuditClient

    @BeforeEach
    fun reset() {
        fake().reset()
    }

    @Test
    fun `audit search normalises HAPI Bundle into row shape`() {
        fake().nextBundle = bundleOf(
            auditEvent(
                id = "abc",
                typeCode = "rest",
                subtypeCode = "create",
                outcome = AuditEventOutcome._0,
                action = AuditEventAction.C,
                agentRef = "Practitioner/123",
                agentName = "alice@example",
                entityRef = "Patient/456",
            ),
        )
        val resp = rest.getForEntity(url("/admin/audit"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat((body["total"] as Number).toInt()).isEqualTo(1)
        assertThat((body["limit"] as Number).toInt()).isEqualTo(50)
        assertThat((body["offset"] as Number).toInt()).isEqualTo(0)
        @Suppress("UNCHECKED_CAST")
        val items = body["items"] as List<Map<String, Any?>>
        assertThat(items).hasSize(1)
        val row = items[0]
        assertThat(row["id"]).isEqualTo("AuditEvent/abc")
        assertThat(row["type_code"]).isEqualTo("rest")
        assertThat(row["subtype_code"]).isEqualTo("create")
        assertThat(row["outcome"]).isEqualTo("0")
        assertThat(row["outcome_display"]).isEqualTo("Success")
        assertThat(row["action"]).isEqualTo("C")
        assertThat(row["agent_who"]).isEqualTo("Practitioner/123")
        assertThat(row["agent_name"]).isEqualTo("alice@example")
        assertThat(row["entity_what"]).isEqualTo("Patient/456")
        assertThat(row["entity_type"]).isEqualTo("Patient")
    }

    @Test
    fun `audit search passes through filters as criteria to the client`() {
        fake().nextBundle = bundleOf()
        val resp = rest.getForEntity(
            url(
                "/admin/audit?type=rest&subtype=create&outcome=0&agent=alice" +
                    "&date-from=2026-06-01&date-to=2026-06-30",
            ),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val captured = fake().lastCriteria!!
        assertThat(captured.type).isEqualTo("rest")
        assertThat(captured.subtype).isEqualTo("create")
        assertThat(captured.outcome).isEqualTo("0")
        assertThat(captured.agent).isEqualTo("alice")
        assertThat(captured.dateFrom).isEqualTo("2026-06-01")
        assertThat(captured.dateTo).isEqualTo("2026-06-30")
    }

    @Test
    fun `audit search caps limit at MAX_LIMIT (200)`() {
        fake().nextBundle = bundleOf()
        val resp = rest.getForEntity(
            url("/admin/audit?limit=9999&offset=10"),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat((resp.body!!["limit"] as Number).toInt()).isEqualTo(200)
        assertThat((resp.body!!["offset"] as Number).toInt()).isEqualTo(10)
        // And the cap also reached the client criteria.
        assertThat(fake().lastCriteria!!.limit).isEqualTo(200)
    }

    @Test
    fun `audit read returns the full FHIR resource as JSON`() {
        fake().nextRead = mapOf(
            "resourceType" to "AuditEvent",
            "id" to "abc",
            "recorded" to "2026-06-26T12:00:00Z",
            "type" to mapOf("system" to "http://example", "code" to "rest"),
        )
        val resp = rest.getForEntity(url("/admin/audit/abc"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["resourceType"]).isEqualTo("AuditEvent")
        assertThat(body["id"]).isEqualTo("abc")
        assertThat(fake().lastReadId).isEqualTo("abc")
    }

    @Test
    fun `audit read returns 404 when the client returns null`() {
        fake().nextRead = null
        val resp = rest.exchange(
            url("/admin/audit/missing"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.NOT_FOUND)
        assertThat(resp.body!!["error"]).isEqualTo("not_found")
    }
}

@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=secret-test-token-407"])
class AdminAuditControllerAuthOnTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun fakeHapiAuditClient(): HapiAuditClient = FakeHapiAuditClient()
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
    fun `auth ON without token returns 401, with correct token returns 200`() {
        val unauthorized = rest.exchange(
            url("/admin/audit"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(unauthorized.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)

        val headers = HttpHeaders().apply {
            set("Authorization", "Bearer secret-test-token-407")
        }
        val authorized = rest.exchange(
            url("/admin/audit"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(authorized.statusCode).isEqualTo(HttpStatus.OK)
    }
}

// -- helpers --------------------------------------------------------------

private fun bundleOf(vararg events: AuditEvent): Bundle {
    val b = Bundle()
    b.total = events.size
    for (ev in events) {
        b.addEntry().resource = ev
    }
    return b
}

private fun auditEvent(
    id: String,
    typeCode: String,
    subtypeCode: String,
    outcome: AuditEventOutcome,
    action: AuditEventAction,
    agentRef: String,
    agentName: String,
    entityRef: String,
): AuditEvent {
    val ev = AuditEvent()
    ev.id = id
    ev.type = Coding("http://example/dicom", typeCode, typeCode.uppercase())
    ev.addSubtype(Coding("http://example/restful-interaction", subtypeCode, subtypeCode))
    ev.outcome = outcome
    ev.action = action
    ev.recorded = Date()
    val agent = AuditEventAgentComponent()
    agent.requestor = true
    agent.who = Reference().setReference(agentRef)
    agent.name = agentName
    ev.addAgent(agent)
    val entity = AuditEventEntityComponent()
    entity.what = Reference().setReference(entityRef)
    ev.addEntity(entity)
    return ev
}

/**
 * In-memory stub for [HapiAuditClient]. Tests seed `nextBundle` /
 * `nextRead` before exercising the controller; the controller's call
 * lands here without ever touching the network.
 */
internal class FakeHapiAuditClient : HapiAuditClient {
    var nextBundle: Bundle = Bundle()
    var nextRead: Any? = null
    var lastCriteria: AuditSearchCriteria? = null
    var lastReadId: String? = null

    fun reset() {
        nextBundle = Bundle()
        nextRead = null
        lastCriteria = null
        lastReadId = null
    }

    override fun search(criteria: AuditSearchCriteria): Bundle {
        lastCriteria = criteria
        return nextBundle
    }

    override fun read(id: String): Any? {
        lastReadId = id
        return nextRead
    }
}
