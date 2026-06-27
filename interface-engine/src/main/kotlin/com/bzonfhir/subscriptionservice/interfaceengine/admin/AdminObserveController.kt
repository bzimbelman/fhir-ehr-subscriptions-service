package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import org.springframework.beans.factory.annotation.Value
import org.springframework.core.io.ClassPathResource
import org.springframework.core.io.Resource
import org.springframework.http.MediaType
import org.springframework.http.ResponseEntity
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Agent-queryable observability surface (Epic #387, ticket #396).
 *
 * Distinct from the human-operator UI: this surface is JSON-only,
 * OpenAPI-described, versioned, and shaped for programmatic consumption.
 * An AI agent or on-call automation hits these endpoints to ask "what's
 * the state of the system?" without parsing logs or metrics.
 *
 * All endpoints share the existing `/admin/` glob bearer-token gate from
 * #384. A future story may want to require a dedicated scope like
 * `system/observability.r` instead of (or in addition to) the bearer
 * token; for v1, the same gate is fine.
 *
 * Schema stability: every response includes `schema_version` matching
 * the OpenAPI spec at /admin/observe/openapi.json. Same versioning rules
 * as the log schema (#397): MAJOR removes/renames, MINOR adds.
 */
@RestController
@RequestMapping("/admin/observe")
class AdminObserveController(
    private val repository: IngestedMessageRepository,
    private val jdbcTemplate: JdbcTemplate,
    @Value("\${spring.application.name:subscription-service-interface-engine}")
    private val serviceName: String,
    @Value("\${subscription-service.matchbox.base-url:}") private val matchboxUrl: String,
    @Value("\${subscription-service.hapi.base-url:}") private val hapiUrl: String,
    @Value("\${subscription-service.auth.issuer:}") private val authIssuer: String,
    @Value("\${subscription-service.auth.enabled:false}") private val authEnabled: Boolean,
    @Value("\${subscription-service.validation.mode:off}") private val validationMode: String,
    @Value("\${subscription-service.channel-security.mode:strict}") private val channelSecurityMode: String,
    @Value("\${subscription-service.multitenancy.mode:disabled}") private val multitenancyMode: String,
) {

    private val startedAt: Instant = Instant.now()

    /**
     * System-wide snapshot: are all components alive? Which feature
     * toggles are active? What's the headline state of the pipeline?
     */
    @GetMapping("/system", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun system(): Map<String, Any?> = mapOf(
        "schema_version" to SCHEMA_VERSION,
        "service" to serviceName,
        "uptime_seconds" to Duration.between(startedAt, Instant.now()).toSeconds(),
        "feature_toggles" to mapOf(
            "auth_enabled" to authEnabled,
            "validation_mode" to validationMode,
            "channel_security_mode" to channelSecurityMode,
            "multitenancy_mode" to multitenancyMode,
        ),
        "downstream" to mapOf(
            "matchbox_base_url" to matchboxUrl,
            "hapi_base_url" to hapiUrl,
            "auth_issuer" to authIssuer,
        ),
        "queue" to mapOf(
            "received" to repository.countByStatus(IngestedMessageStatus.RECEIVED),
            "transforming" to repository.countByStatus(IngestedMessageStatus.TRANSFORMING),
            "failed" to repository.countByStatus(IngestedMessageStatus.FAILED),
            "delivered" to repository.countByStatus(IngestedMessageStatus.DELIVERED),
            "dead_letter" to repository.countByStatus(IngestedMessageStatus.DEAD_LETTER),
        ),
    )

    /**
     * Throughput counts over a sliding window. Bucketed by hour for
     * windows ≤ 24h, by day otherwise.
     */
    @GetMapping("/throughput", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun throughput(
        @RequestParam(defaultValue = "24h") window: String,
    ): Map<String, Any?> {
        val windowDuration = parseWindow(window)
        val cutoff = OffsetDateTime.now(ZoneOffset.UTC).minus(windowDuration)
        val bucketWidth = if (windowDuration.toHours() <= 24) "hour" else "day"

        // SQL aggregation rather than fetching every row. ts_bucket is a
        // postgres function only in TimescaleDB; we use date_trunc which is
        // standard postgres and fine for our scale.
        val sql = """
            SELECT
              date_trunc(?, received_at) AS bucket,
              status,
              count(*) AS n
            FROM ingested_messages
            WHERE received_at >= ?
            GROUP BY bucket, status
            ORDER BY bucket
        """.trimIndent()

        data class Row(val bucket: OffsetDateTime, val status: String, val count: Long)
        val rows = jdbcTemplate.query(sql, { rs, _ ->
            Row(
                rs.getObject(1, OffsetDateTime::class.java),
                rs.getString(2),
                rs.getLong(3),
            )
        }, bucketWidth, cutoff)

        val byBucket = rows.groupBy { it.bucket }
            .toSortedMap()
            .map { (bucket, statusRows) ->
                mapOf(
                    "bucket_start" to bucket.toString(),
                    "counts" to statusRows.associate { it.status to it.count },
                )
            }

        return mapOf(
            "schema_version" to SCHEMA_VERSION,
            "window" to window,
            "bucket_width" to bucketWidth,
            "buckets" to byBucket,
        )
    }

    /**
     * Recent DEAD_LETTER messages. Mirrors `/admin/messages?status=DEAD_LETTER`
     * but with a stable shape and explicit limit semantics for agents.
     */
    @GetMapping("/dlq", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun dlq(@RequestParam(defaultValue = "20") limit: Int): Map<String, Any?> {
        val cappedLimit = limit.coerceIn(1, 100)
        val items = repository
            .findByStatus(
                IngestedMessageStatus.DEAD_LETTER,
                org.springframework.data.domain.PageRequest.of(0, cappedLimit),
            )
            .map { summarize(it) }
        val total = repository.countByStatus(IngestedMessageStatus.DEAD_LETTER)
        return mapOf(
            "schema_version" to SCHEMA_VERSION,
            "total" to total,
            "limit" to cappedLimit,
            "items" to items,
        )
    }

    /**
     * Trace one message by correlation_id across the pipeline. Returns
     * the message row plus a pointer to its trace (when OTel is enabled
     * and a backend like Jaeger is wired) and the in-DB effects.
     */
    @GetMapping("/trace/{correlationId}", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun trace(
        @org.springframework.web.bind.annotation.PathVariable correlationId: String,
    ): ResponseEntity<Map<String, Any?>> {
        val rows = jdbcTemplate.query(
            """
            SELECT id, received_at, source_protocol, source_system, source_id,
                   message_type, status, attempt_count, last_error,
                   delivered_at, correlation_id
              FROM ingested_messages
             WHERE correlation_id = ?
             ORDER BY received_at
            """.trimIndent(),
            { rs, _ ->
                mapOf<String, Any?>(
                    "id" to rs.getLong("id"),
                    "received_at" to rs.getObject("received_at", OffsetDateTime::class.java).toString(),
                    "source_protocol" to rs.getString("source_protocol"),
                    "source_system" to rs.getString("source_system"),
                    "source_id" to rs.getString("source_id"),
                    "message_type" to rs.getString("message_type"),
                    "status" to rs.getString("status"),
                    "attempt_count" to rs.getInt("attempt_count"),
                    "last_error" to rs.getString("last_error"),
                    "delivered_at" to rs.getObject("delivered_at", OffsetDateTime::class.java)?.toString(),
                    "correlation_id" to rs.getString("correlation_id"),
                )
            },
            correlationId,
        )
        if (rows.isEmpty()) {
            return ResponseEntity.status(404).body(
                mapOf(
                    "error" to "not_found",
                    "message" to "no messages found with correlation_id=$correlationId",
                ),
            )
        }
        return ResponseEntity.ok(
            mapOf(
                "schema_version" to SCHEMA_VERSION,
                "correlation_id" to correlationId,
                "messages" to rows,
                "trace_hint" to "search the OTel backend (Jaeger, Tempo, etc.) for trace_id derived from the correlation_id; see /admin/observe/openapi.json for the mapping",
            ),
        )
    }

    /**
     * OpenAPI 3.1 spec describing every endpoint on this surface. Static
     * JSON shipped on the classpath under
     * src/main/resources/admin/observe/openapi.json. Agents fetch this to
     * generate clients or validate responses.
     */
    @GetMapping("/openapi.json", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun openapi(): ResponseEntity<Resource> {
        val resource = ClassPathResource("admin/observe/openapi.json")
        return ResponseEntity.ok()
            .contentType(MediaType.APPLICATION_JSON)
            .body(resource)
    }

    private fun summarize(msg: IngestedMessage): Map<String, Any?> = mapOf(
        "id" to msg.id,
        "received_at" to msg.receivedAt?.toString(),
        "source_protocol" to msg.sourceProtocol?.name,
        "source_system" to msg.sourceSystem,
        "source_id" to msg.sourceId,
        "message_type" to msg.messageType,
        "status" to msg.status?.name,
        "attempt_count" to msg.attemptCount,
        "last_error" to msg.lastError,
        "correlation_id" to msg.correlationId,
    )

    private fun parseWindow(w: String): Duration {
        val match = Regex("(\\d+)([hd])").matchEntire(w.trim().lowercase())
            ?: throw IllegalArgumentException("window must look like '24h' or '7d', got '$w'")
        val n = match.groupValues[1].toLong()
        val unit = match.groupValues[2]
        return when (unit) {
            "h" -> Duration.ofHours(n)
            "d" -> Duration.ofDays(n)
            else -> error("unreachable")
        }
    }

    companion object {
        // Bump MAJOR when removing/renaming a REQUIRED field, MINOR when
        // adding any field. See docs/observability/log-schema.md for the
        // policy and #397 for the contract.
        const val SCHEMA_VERSION = "1.0"
    }
}
