package com.bzonfhir.subscriptionservice.interfaceengine.observability

import org.assertj.core.api.Assertions.assertThat
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
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import org.testcontainers.DockerClientFactory
import org.testcontainers.containers.PostgreSQLContainer
import org.testcontainers.containers.wait.strategy.Wait
import org.testcontainers.junit.jupiter.Container
import org.testcontainers.junit.jupiter.Testcontainers
import java.net.ServerSocket

/**
 * Web-layer tests for the [CorrelationIdFilter] (Epic #387, ticket #388).
 *
 * Boots a Spring context against the admin REST API and asserts:
 *
 *   1. A request with no `X-Correlation-Id` header gets a server-generated
 *      UUID back on the response.
 *   2. A request with a sender-supplied header gets the SAME value echoed
 *      back.
 *   3. A malformed header is sanitized — caller gets a fresh UUID.
 *
 * Reuses the Testcontainers Postgres pattern from AdminMessagesControllerTest
 * because the admin endpoints under test depend on the JPA repository, even
 * though the filter itself doesn't.
 */
@Testcontainers
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
@AutoConfigureTestDatabase(replace = AutoConfigureTestDatabase.Replace.NONE)
class AdminCorrelationIdHeaderTest {

    companion object {
        @Container
        @JvmStatic
        val postgres: PostgreSQLContainer<*> = PostgreSQLContainer("postgres:16-alpine")
            .withDatabaseName("ipf")
            .withUsername("ipf")
            .withPassword("ipf")
            .waitingFor(Wait.forListeningPort())
            .withStartupTimeout(java.time.Duration.ofSeconds(60))

        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @JvmStatic
        @DynamicPropertySource
        fun datasourceProperties(registry: DynamicPropertyRegistry) {
            registry.add("spring.datasource.url") { postgres.jdbcUrl }
            registry.add("spring.datasource.username") { postgres.username }
            registry.add("spring.datasource.password") { postgres.password }
            registry.add("subscription-service.mllp.port") { mllpPort }
            registry.add("subscription-service.worker.enabled") { "false" }
        }

        init {
            check(DockerClientFactory.instance().isDockerAvailable) {
                "Docker is not available; AdminCorrelationIdHeaderTest requires Testcontainers."
            }
        }
    }

    @LocalServerPort private var port: Int = 0
    @Autowired private lateinit var rest: TestRestTemplate

    private fun url(path: String) = "http://localhost:$port$path"

    @Test
    fun `request without header gets a server-generated UUID echoed back`() {
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(HttpHeaders()),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val echo = resp.headers.getFirst(CorrelationId.HEADER)
        assertThat(echo)
            .describedAs("server should generate and echo a correlation id when none was supplied")
            .isNotNull()
            .matches("[0-9a-fA-F-]{36}")
    }

    @Test
    fun `request with header gets the same value echoed back`() {
        val supplied = "my-test-id-1234"
        val headers = HttpHeaders().apply { set(CorrelationId.HEADER, supplied) }
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        assertThat(resp.headers.getFirst(CorrelationId.HEADER)).isEqualTo(supplied)
    }

    @Test
    fun `request with malformed header gets a sanitized fresh UUID back`() {
        val bad = "abc\ninjected-newline"
        val headers = HttpHeaders().apply { set(CorrelationId.HEADER, bad) }
        val resp = rest.exchange(
            url("/admin/messages"),
            HttpMethod.GET,
            HttpEntity<Void>(headers),
            Map::class.java,
        )
        assertThat(resp.statusCode).isEqualTo(HttpStatus.OK)
        val echo = resp.headers.getFirst(CorrelationId.HEADER)
        assertThat(echo)
            .isNotNull()
            .isNotEqualTo(bad)
            .matches("[0-9a-fA-F-]{36}")
    }
}
