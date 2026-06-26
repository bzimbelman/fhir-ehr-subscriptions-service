package com.bzonfhir.subscriptionservice.interfaceengine.observability

import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.actuate.health.Health
import org.springframework.boot.actuate.health.HealthIndicator
import org.springframework.stereotype.Component
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.time.Duration

/**
 * Health indicator for the upstream Matchbox FHIR server (Epic #387, ticket #393).
 *
 * Calls `GET ${MATCHBOX_BASE}/metadata` with a short, separate timeout. The
 * full Matchbox HTTP client (used for `$transform`) sits behind Camel's
 * shared `camel-http` component with a 30s timeout that's appropriate for
 * an IG-cold-load matchbox call — but wrong for a health check, which has
 * to return inside the Kubernetes readiness probe budget (5s wall clock).
 * We therefore use a dedicated [HttpClient] with a 3s timeout here so a
 * slow upstream can't wedge the readiness probe.
 *
 * Bean name `matchbox` matches the actuator health group include name in
 * `application.yaml` (`readiness.include: ping,db,matchbox,hapi,dlqBacklog`).
 * Spring derives the indicator's contribution key from the bean name with
 * the `HealthIndicator` suffix stripped, so the bean must be named
 * `matchboxHealthIndicator` — we accomplish that by declaring the class
 * `MatchboxHealthIndicator` and letting Spring's default
 * BeanNameGenerator pluralize it.
 */
@Component
class MatchboxHealthIndicator(
    @Value("\${subscription-service.matchbox.base-url}") private val baseUrl: String,
    @Value("\${subscription-service.health.downstream-timeout-ms:3000}") timeoutMs: Long,
) : HealthIndicator {

    private val log = LoggerFactory.getLogger(MatchboxHealthIndicator::class.java)

    private val timeout: Duration = Duration.ofMillis(timeoutMs)

    /**
     * Shared singleton client. HTTP/1.1 (matchbox doesn't speak HTTP/2 over
     * its bundled Jetty), connect timeout matched to the readiness budget.
     */
    private val httpClient: HttpClient =
        HttpClient.newBuilder()
            .version(HttpClient.Version.HTTP_1_1)
            .connectTimeout(timeout)
            .build()

    override fun health(): Health {
        val target = "${baseUrl.trimEnd('/')}/metadata"
        val request =
            HttpRequest.newBuilder()
                .uri(URI.create(target))
                .timeout(timeout)
                .header("Accept", "application/fhir+json")
                .GET()
                .build()

        return try {
            val response = httpClient.send(request, HttpResponse.BodyHandlers.discarding())
            if (response.statusCode() in 200..299) {
                Health.up()
                    .withDetail("url", target)
                    .withDetail("status_code", response.statusCode())
                    .build()
            } else {
                log.warn("Matchbox health check returned status={} url={}", response.statusCode(), target)
                Health.down()
                    .withDetail("url", target)
                    .withDetail("status_code", response.statusCode())
                    .withDetail("reason", "non-2xx response")
                    .build()
            }
        } catch (e: Exception) {
            log.warn("Matchbox health check failed url={} reason={}", target, e.toString())
            Health.down()
                .withDetail("url", target)
                .withDetail("reason", e.javaClass.simpleName + ": " + (e.message ?: ""))
                .build()
        }
    }
}
