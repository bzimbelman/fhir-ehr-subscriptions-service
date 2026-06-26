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
 * Health indicator for the upstream HAPI FHIR server (Epic #387, ticket #393).
 *
 * Mirrors [MatchboxHealthIndicator] structurally — separate JDK [HttpClient]
 * with a 3-second timeout (independent of the much longer HAPI transaction
 * timeout configured for the worker's POST path) so a slow HAPI can't
 * wedge the Kubernetes readiness probe.
 *
 * The interface-engine cannot do useful work without HAPI: the async worker
 * (#382) posts every transformed Bundle to HAPI, and a HAPI outage means
 * those POSTs would fail and rows would pile up in FAILED / DEAD_LETTER.
 * So we want the interface-engine pod taken out of the Service endpoints
 * when HAPI is unreachable — which is exactly what including this
 * indicator in the `readiness` group accomplishes.
 */
@Component
class HapiHealthIndicator(
    @Value("\${subscription-service.hapi.base-url}") private val baseUrl: String,
    @Value("\${subscription-service.health.downstream-timeout-ms:3000}") timeoutMs: Long,
) : HealthIndicator {

    private val log = LoggerFactory.getLogger(HapiHealthIndicator::class.java)

    private val timeout: Duration = Duration.ofMillis(timeoutMs)

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
                log.warn("HAPI health check returned status={} url={}", response.statusCode(), target)
                Health.down()
                    .withDetail("url", target)
                    .withDetail("status_code", response.statusCode())
                    .withDetail("reason", "non-2xx response")
                    .build()
            }
        } catch (e: Exception) {
            log.warn("HAPI health check failed url={} reason={}", target, e.toString())
            Health.down()
                .withDetail("url", target)
                .withDetail("reason", e.javaClass.simpleName + ": " + (e.message ?: ""))
                .build()
        }
    }
}
