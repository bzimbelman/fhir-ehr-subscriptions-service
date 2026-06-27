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
import org.springframework.http.MediaType
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.springframework.test.context.TestPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket

/**
 * Web-layer tests for the `/admin/matchbox` admin endpoints (Epic #398, ticket #405).
 *
 * Same scaffolding pattern as [SubscriptionsAdminControllerTest]:
 *   - Real Postgres in Testcontainers so JPA/Flyway autoconfig is happy.
 *   - Random Spring web port via @SpringBootTest.WebEnvironment.RANDOM_PORT.
 *   - Two top-level classes — one auth-off, one auth-on — because
 *     @TestPropertySource binds at class-load time.
 *
 * The controller talks to Matchbox via [MatchboxAdminGateway]; we replace
 * the production implementation with a [FakeMatchboxAdminGateway] via
 * @TestConfiguration + @Primary so the tests don't need a live Matchbox
 * container.
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
class AdminMatchboxControllerAuthOffTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun fakeMatchboxAdminGateway(): MatchboxAdminGateway = FakeMatchboxAdminGateway()
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
                "Docker is not available; AdminMatchboxControllerTest requires Testcontainers."
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
    @Autowired private lateinit var gateway: MatchboxAdminGateway
    @Autowired private lateinit var rest: TestRestTemplate

    @BeforeEach
    fun reset() {
        (gateway as FakeMatchboxAdminGateway).reset()
    }

    private fun url(path: String) = "http://localhost:$port$path"
    private fun fake() = gateway as FakeMatchboxAdminGateway

    // ---------------------------------------------------------------------
    // /health endpoint
    // ---------------------------------------------------------------------

    @Test
    fun `health returns reachable=true when Matchbox metadata succeeds`() {
        fake().nextVersion = "v3.9.13"
        val resp = rest.getForEntity(url("/admin/matchbox/health"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["reachable"]).isEqualTo(true)
        assertThat(body["version"]).isEqualTo("v3.9.13")
        assertThat(body["base_url"] as String).contains("matchbox")
        assertThat(body["checked_at"] as String).isNotBlank()
        assertThat((body["response_time_ms"] as Number).toLong()).isGreaterThanOrEqualTo(0L)
        assertThat(body["error"]).isNull()
    }

    @Test
    fun `health returns reachable=false plus error when Matchbox throws`() {
        fake().nextVersionError = RuntimeException("502 Bad Gateway")
        val resp = rest.getForEntity(url("/admin/matchbox/health"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["reachable"]).isEqualTo(false)
        assertThat(body["version"]).isNull()
        assertThat(body["error"] as String).contains("502")
    }

    // ---------------------------------------------------------------------
    // /structuremaps endpoint
    // ---------------------------------------------------------------------

    @Test
    fun `structuremaps returns normalised list from Matchbox`() {
        fake().nextStructureMaps = listOf(
            StructureMapItem(
                id = "ADT_A01",
                url = "http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A01",
                name = "ADT_A01",
                title = "HL7 v2 ADT A01 -> FHIR R4 Bundle",
                status = "active",
                version = "1.0.0",
            ),
            StructureMapItem(
                id = "ADT_A04",
                url = "http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A04",
                name = "ADT_A04",
                title = null,
                status = "draft",
                version = null,
            ),
        )
        val resp = rest.getForEntity(url("/admin/matchbox/structuremaps"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["total"]).isEqualTo(2)
        assertThat(body["error"]).isNull()
        @Suppress("UNCHECKED_CAST")
        val items = body["items"] as List<Map<String, Any?>>
        assertThat(items).hasSize(2)
        assertThat(items[0]["id"]).isEqualTo("ADT_A01")
        assertThat(items[0]["status"]).isEqualTo("active")
        assertThat(items[1]["title"]).isNull()
    }

    @Test
    fun `structuremaps returns empty list with error when Matchbox unreachable`() {
        fake().nextStructureMapsError = RuntimeException("connection refused")
        val resp = rest.getForEntity(url("/admin/matchbox/structuremaps"), Map::class.java)
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val body = resp.body!!
        assertThat(body["total"]).isEqualTo(0)
        assertThat(body["error"] as String).contains("connection refused")
        @Suppress("UNCHECKED_CAST")
        assertThat(body["items"] as List<Map<String, Any?>>).isEmpty()
    }

    // ---------------------------------------------------------------------
    // /transform endpoint
    // ---------------------------------------------------------------------

    @Test
    fun `transform POSTs to Matchbox and returns success=true plus bundle`() {
        fake().nextTransformBundle = mapOf(
            "resourceType" to "Bundle",
            "type" to "transaction",
            "entry" to emptyList<Any>(),
        )
        val headers = HttpHeaders().apply { contentType = MediaType.APPLICATION_JSON }
        val body = """{"source_format":"hl7v2","raw_message":"MSH|^~\\&|TEST|FAC|RECV|CDS|20260626120000||ADT^A04|405-XFORM-1|P|2.5","map_url":"http://example/StructureMap/ADT_A04"}"""
        val resp = rest.exchange(
            url("/admin/matchbox/transform"),
            HttpMethod.POST,
            HttpEntity(body, headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val respBody = resp.body!!
        assertThat(respBody["success"]).isEqualTo(true)
        assertThat(respBody["error"]).isNull()
        @Suppress("UNCHECKED_CAST")
        val bundle = respBody["bundle"] as Map<String, Any?>
        assertThat(bundle["resourceType"]).isEqualTo("Bundle")
        // Server forwarded the map_url verbatim.
        assertThat(fake().lastTransformMapUrl).isEqualTo("http://example/StructureMap/ADT_A04")
        assertThat(fake().lastTransformRaw).contains("ADT^A04")
    }

    @Test
    fun `transform returns success=false plus error message on Matchbox failure`() {
        fake().nextTransformError = RuntimeException("matchbox: unknown source ADT_A99")
        val headers = HttpHeaders().apply { contentType = MediaType.APPLICATION_JSON }
        val body = """{"raw_message":"MSH|^~\\&|X|Y|Z|W|20260626120000||ADT^A99|405-XFORM-2|P|2.5","map_url":"http://example/StructureMap/ADT_A99"}"""
        val resp = rest.exchange(
            url("/admin/matchbox/transform"),
            HttpMethod.POST,
            HttpEntity(body, headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val respBody = resp.body!!
        assertThat(respBody["success"]).isEqualTo(false)
        assertThat(respBody["bundle"]).isNull()
        assertThat(respBody["error"] as String).contains("ADT_A99")
    }

    @Test
    fun `transform with empty raw_message returns 400`() {
        val headers = HttpHeaders().apply { contentType = MediaType.APPLICATION_JSON }
        val body = """{"raw_message":"","map_url":"http://example/StructureMap/ADT_A04"}"""
        val resp = rest.exchange(
            url("/admin/matchbox/transform"),
            HttpMethod.POST,
            HttpEntity(body, headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.BAD_REQUEST)
        assertThat(resp.body!!["error"] as String).contains("raw_message")
    }

    @Test
    fun `transform falls back to server-side default map_url when none supplied`() {
        fake().nextTransformBundle = mapOf("resourceType" to "Bundle", "type" to "transaction")
        val headers = HttpHeaders().apply { contentType = MediaType.APPLICATION_JSON }
        // No map_url field at all -> backend should fall back to the
        // configured `subscription-service.matchbox.structuremap.adt-a01`
        // default (set by application.yaml's hl7.org URL).
        val body = """{"raw_message":"MSH|^~\\&|EPIC|HOSP|RECV|CDS|20260626120000||ADT^A01|405-DEF|P|2.5"}"""
        val resp = rest.exchange(
            url("/admin/matchbox/transform"),
            HttpMethod.POST,
            HttpEntity(body, headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.body!!["success"]).isEqualTo(true)
        // The default StructureMap URL is the configured ADT_A01 default.
        assertThat(fake().lastTransformMapUrl).contains("ADT_A01")
    }

    @Test
    fun `auth OFF allows requests without Authorization header on health`() {
        val resp = rest.exchange(
            url("/admin/matchbox/health"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

/**
 * Token configured → admin endpoints under `/admin/matchbox/` require
 * Bearer auth. Same AdminAuthInterceptor handles it because that
 * interceptor is mounted on the `/admin/` glob, so no additional wiring
 * is needed when a new `/admin/...` controller is added.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
@TestPropertySource(properties = ["ipf.admin.auth-token=secret-test-token-405"])
class AdminMatchboxControllerAuthOnTest {

    @TestConfiguration
    class StubConfig {
        @Bean
        @Primary
        fun fakeMatchboxAdminGateway(): MatchboxAdminGateway = FakeMatchboxAdminGateway()
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
    fun `auth ON without token returns 401 on health`() {
        val resp = rest.exchange(
            url("/admin/matchbox/health"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON without token returns 401 on structuremaps`() {
        val resp = rest.exchange(
            url("/admin/matchbox/structuremaps"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON without token returns 401 on transform`() {
        val headers = HttpHeaders().apply { contentType = MediaType.APPLICATION_JSON }
        val body = """{"raw_message":"x","map_url":"y"}"""
        val resp = rest.exchange(
            url("/admin/matchbox/transform"),
            HttpMethod.POST,
            HttpEntity(body, headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.UNAUTHORIZED)
    }

    @Test
    fun `auth ON with correct token returns 200 on health`() {
        val headers = HttpHeaders().apply { set("Authorization", "Bearer secret-test-token-405") }
        val resp = rest.exchange(
            url("/admin/matchbox/health"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
    }
}

/**
 * In-memory stub for [MatchboxAdminGateway]. Each test seeds the
 * `nextX` fields before exercising the controller; the controller's
 * call lands here without ever touching the network.
 */
internal class FakeMatchboxAdminGateway : MatchboxAdminGateway {
    var nextVersion: String? = "stub-version"
    var nextVersionError: Exception? = null
    var nextStructureMaps: List<StructureMapItem> = emptyList()
    var nextStructureMapsError: Exception? = null
    var nextTransformBundle: Any? = null
    var nextTransformError: Exception? = null

    var lastTransformMapUrl: String? = null
    var lastTransformRaw: String? = null

    fun reset() {
        nextVersion = "stub-version"
        nextVersionError = null
        nextStructureMaps = emptyList()
        nextStructureMapsError = null
        nextTransformBundle = null
        nextTransformError = null
        lastTransformMapUrl = null
        lastTransformRaw = null
    }

    override fun fetchMetadataVersion(): String? {
        nextVersionError?.let { throw it }
        return nextVersion
    }

    override fun listStructureMaps(): List<StructureMapItem> {
        nextStructureMapsError?.let { throw it }
        return nextStructureMaps
    }

    override fun transform(structureMapCanonical: String, rawMessage: String): Any {
        lastTransformMapUrl = structureMapCanonical
        lastTransformRaw = rawMessage
        nextTransformError?.let { throw it }
        return nextTransformBundle ?: error("test did not seed nextTransformBundle")
    }
}
