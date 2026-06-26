package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.actuate.health.Health
import org.springframework.boot.actuate.health.HealthIndicator
import org.springframework.boot.actuate.health.Status
import org.springframework.stereotype.Component

/**
 * DLQ backlog health indicator (Epic #387, ticket #393).
 *
 * Counts `ingested_messages` rows in DEAD_LETTER status and classifies the
 * result:
 *
 *   - 0 rows               → UP (no operator attention needed)
 *   - 1..warn-threshold-1  → UP with `details.dlq_count=N` (informational)
 *   - warn-threshold+      → DEGRADED with `details.dlq_count=N`
 *
 * DEGRADED is deliberately NOT one of Spring Boot's built-in statuses
 * (`UP`, `DOWN`, `OUT_OF_SERVICE`, `UNKNOWN`). Spring's
 * `HealthAggregator` would default-map any unknown status to `UNKNOWN` for
 * the aggregated overall status, which would itself default to a 200 HTTP
 * response (`UNKNOWN` is not in `management.endpoint.health.status.http-mapping`
 * by default). That's the behaviour we want: kubelet should NOT pull the
 * pod from the Service endpoints because there are 10 DLQ rows — the rows
 * need operator attention, not a pod restart. Monitoring scrapes
 * `/actuator/health` and alerts on the `DEGRADED` status string showing up
 * for this indicator.
 *
 * Configurable thresholds (defaults match the ticket spec):
 *   `subscription-service.health.dlq.warn-threshold` (default 10)
 *
 * The repository's [IngestedMessageRepository.countByStatus] is a JPA-managed
 * count that compiles to a single `SELECT count(*)` and uses the existing
 * connection pool; no new resource use here. We catch exceptions broadly:
 * if the database is unreachable the dedicated `db` indicator (Spring
 * Boot's built-in `DataSourceHealthIndicator`) already reports DOWN. This
 * indicator falling back to UNKNOWN is the right behaviour — a DB outage
 * shouldn't be reported twice with different semantics.
 */
@Component
class DlqBacklogHealthIndicator(
    private val repository: IngestedMessageRepository,
    @Value("\${subscription-service.health.dlq.warn-threshold:10}") private val warnThreshold: Long,
) : HealthIndicator {

    private val log = LoggerFactory.getLogger(DlqBacklogHealthIndicator::class.java)

    override fun health(): Health {
        val count =
            try {
                repository.countByStatus(IngestedMessageStatus.DEAD_LETTER)
            } catch (e: Exception) {
                log.warn("DLQ backlog count query failed: {}", e.toString())
                return Health.unknown()
                    .withDetail("reason", e.javaClass.simpleName + ": " + (e.message ?: ""))
                    .build()
            }

        val builder =
            when {
                count == 0L -> Health.up()
                count < warnThreshold -> Health.up()
                else -> Health.status(DEGRADED)
            }
        return builder
            .withDetail("dlq_count", count)
            .withDetail("warn_threshold", warnThreshold)
            .build()
    }

    companion object {
        /**
         * Custom status returned when the DLQ backlog crosses the warn
         * threshold. Operators / dashboards key off the exact string
         * `"DEGRADED"`; do not rename without coordinating with the
         * subscription-service monitoring alerts.
         */
        val DEGRADED: Status = Status("DEGRADED")
    }
}
