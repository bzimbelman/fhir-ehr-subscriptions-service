package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import com.fasterxml.jackson.annotation.JsonProperty
import org.slf4j.LoggerFactory
import org.springframework.data.domain.PageRequest
import org.springframework.data.domain.Sort
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.DeleteMapping
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.PostMapping
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import java.time.OffsetDateTime

/**
 * Operator admin REST API for the durable inbound message store
 * (Epic #378, ticket #384).
 *
 * Mounted on the same Spring Boot HTTP port as actuator (8090) under
 * `/admin/`. Intended for human-driven triage of stuck or failed
 * messages — not part of the message pipeline.
 *
 * The four endpoints:
 *
 *   GET    /admin/messages                — list, with optional status filter + pagination
 *   GET    /admin/messages/{id}           — full row including raw_message
 *   POST   /admin/messages/{id}/retry     — reset to RECEIVED so worker picks it up
 *   DELETE /admin/messages/{id}           — purge a DEAD_LETTER row
 *
 * State-transition rules:
 *   - retry is only allowed from FAILED or DEAD_LETTER
 *   - delete is only allowed from DEAD_LETTER
 *
 * Both rules return 409 Conflict with a human-readable JSON body when violated;
 * the operator's "force this through" path is to manually UPDATE the row in
 * the DB, which we deliberately don't expose over HTTP.
 *
 * Auth is handled by [AdminAuthInterceptor] — see that class for the model.
 */
@RestController
@RequestMapping("/admin/messages")
class AdminMessagesController(
    private val repository: IngestedMessageRepository,
) {

    private val log = LoggerFactory.getLogger(AdminMessagesController::class.java)

    @GetMapping
    fun list(
        @RequestParam(required = false) status: String?,
        @RequestParam(required = false, defaultValue = "50") limit: Int,
        @RequestParam(required = false, defaultValue = "0") offset: Int,
    ): ResponseEntity<Any> {
        // Clamp pagination. Negative values would blow up PageRequest; we
        // coerce to sane defaults rather than 400 because operators running
        // `curl` shouldn't have to memorize the bounds.
        val cappedLimit = limit.coerceIn(1, MAX_LIMIT)
        val cappedOffset = offset.coerceAtLeast(0)

        val statusFilter: IngestedMessageStatus? = when {
            status.isNullOrBlank() -> null
            else -> runCatching { IngestedMessageStatus.valueOf(status) }.getOrElse {
                return ResponseEntity.badRequest().body(
                    mapOf(
                        "error" to "invalid_status",
                        "message" to "status must be one of ${IngestedMessageStatus.values().toList()}",
                    ),
                )
            }
        }

        // Row-offset pagination via Spring Data's @Query method —
        // [IngestedMessageRepository.findAdminWindow] uses the standard
        // Pageable mechanism, and Hibernate translates the page's
        // first-result/max-results to the dialect's OFFSET/LIMIT clauses.
        // We build a Pageable with offset rounding to the page boundary
        // and size=limit; Spring Data uses pageNumber * pageSize for the
        // offset, so we feed it page=cappedOffset/cappedLimit when
        // page-aligned (the typical UI navigation pattern). Arbitrary
        // offsets are clamped to the nearest lower page boundary; we
        // document this on the docs/admin-api.md.
        val total: Long = statusFilter?.let { repository.countByStatus(it) }
            ?: repository.count()

        val items: List<IngestedMessage> = if (total == 0L) {
            emptyList()
        } else {
            val sort = Sort.by(Sort.Direction.DESC, "receivedAt").and(
                Sort.by(Sort.Direction.DESC, "id"),
            )
            val pageNumber = cappedOffset / cappedLimit
            val pageable = PageRequest.of(pageNumber, cappedLimit, sort)
            if (statusFilter != null) {
                repository.findByStatus(statusFilter, pageable)
            } else {
                repository.findAllBy(pageable)
            }
        }
        // Page-aligned offset actually used (see Pageable construction above).
        // Reflect this in the response so paging clients don't drift.
        val effectiveOffset = (cappedOffset / cappedLimit) * cappedLimit

        return ResponseEntity.ok(
            ListResponse(
                total = total,
                limit = cappedLimit,
                offset = effectiveOffset,
                items = items.map { it.toSummary() },
            ),
        )
    }

    @GetMapping("/{id}")
    fun get(@PathVariable id: Long): ResponseEntity<Any> {
        val row = repository.findById(id).orElse(null)
            ?: return notFound(id)
        return ResponseEntity.ok(row.toDetail())
    }

    @PostMapping("/{id}/retry")
    fun retry(@PathVariable id: Long): ResponseEntity<Any> {
        val row = repository.findById(id).orElse(null)
            ?: return notFound(id)

        if (row.status != IngestedMessageStatus.FAILED && row.status != IngestedMessageStatus.DEAD_LETTER) {
            return ResponseEntity.status(409).body(
                mapOf(
                    "error" to "invalid_state_for_retry",
                    "message" to "retry only allowed when status is FAILED or DEAD_LETTER (current: ${row.status})",
                    "id" to id,
                    "currentStatus" to row.status.name,
                ),
            )
        }

        row.status = IngestedMessageStatus.RECEIVED
        row.attemptCount = 0
        row.nextAttemptAt = null
        row.lastError = null
        val saved = repository.saveAndFlush(row)
        log.info("admin retry id={} previousStatus={}", id, row.status)
        return ResponseEntity.ok(saved.toDetail())
    }

    @DeleteMapping("/{id}")
    fun delete(@PathVariable id: Long): ResponseEntity<Any> {
        val row = repository.findById(id).orElse(null)
            ?: return notFound(id)

        if (row.status != IngestedMessageStatus.DEAD_LETTER) {
            return ResponseEntity.status(409).body(
                mapOf(
                    "error" to "invalid_state_for_delete",
                    "message" to "delete only allowed when status is DEAD_LETTER (current: ${row.status})",
                    "id" to id,
                    "currentStatus" to row.status.name,
                ),
            )
        }
        repository.delete(row)
        log.info("admin delete id={}", id)
        return ResponseEntity.noContent().build()
    }

    private fun notFound(id: Long): ResponseEntity<Any> =
        ResponseEntity.status(404).body(
            mapOf("error" to "not_found", "message" to "no ingested_message with id=$id"),
        )

    companion object {
        // Hard ceiling on page size. Operators paginating manually shouldn't
        // be able to ask for an unbounded dump that pins the server.
        const val MAX_LIMIT = 500
    }
}

/**
 * Summary projection for list responses — deliberately omits `raw_message`
 * so a page of 500 rows doesn't carry hundreds of KB of HL7 payload.
 */
data class IngestedMessageSummary(
    val id: Long?,
    @JsonProperty("received_at") val receivedAt: OffsetDateTime?,
    @JsonProperty("source_protocol") val sourceProtocol: String,
    @JsonProperty("source_system") val sourceSystem: String,
    @JsonProperty("source_id") val sourceId: String,
    @JsonProperty("message_type") val messageType: String,
    val status: String,
    @JsonProperty("attempt_count") val attemptCount: Int,
    @JsonProperty("last_attempt_at") val lastAttemptAt: OffsetDateTime?,
    @JsonProperty("last_error") val lastError: String?,
    @JsonProperty("delivered_at") val deliveredAt: OffsetDateTime?,
)

/**
 * Full projection for /admin/messages/{id} — includes raw_message and
 * raw_content_type so an operator can inspect the on-the-wire payload.
 */
data class IngestedMessageDetail(
    val id: Long?,
    @JsonProperty("received_at") val receivedAt: OffsetDateTime?,
    @JsonProperty("source_protocol") val sourceProtocol: String,
    @JsonProperty("source_system") val sourceSystem: String,
    @JsonProperty("source_id") val sourceId: String,
    @JsonProperty("message_type") val messageType: String,
    @JsonProperty("raw_message") val rawMessage: String,
    @JsonProperty("raw_content_type") val rawContentType: String,
    val status: String,
    @JsonProperty("attempt_count") val attemptCount: Int,
    @JsonProperty("last_attempt_at") val lastAttemptAt: OffsetDateTime?,
    @JsonProperty("next_attempt_at") val nextAttemptAt: OffsetDateTime?,
    @JsonProperty("last_error") val lastError: String?,
    @JsonProperty("delivered_at") val deliveredAt: OffsetDateTime?,
)

data class ListResponse(
    val total: Long,
    val limit: Int,
    val offset: Int,
    val items: List<IngestedMessageSummary>,
)

internal fun IngestedMessage.toSummary() = IngestedMessageSummary(
    id = id,
    receivedAt = receivedAt,
    sourceProtocol = sourceProtocol.name,
    sourceSystem = sourceSystem,
    sourceId = sourceId,
    messageType = messageType,
    status = status.name,
    attemptCount = attemptCount,
    lastAttemptAt = lastAttemptAt,
    lastError = lastError,
    deliveredAt = deliveredAt,
)

internal fun IngestedMessage.toDetail() = IngestedMessageDetail(
    id = id,
    receivedAt = receivedAt,
    sourceProtocol = sourceProtocol.name,
    sourceSystem = sourceSystem,
    sourceId = sourceId,
    messageType = messageType,
    rawMessage = rawMessage,
    rawContentType = rawContentType,
    status = status.name,
    attemptCount = attemptCount,
    lastAttemptAt = lastAttemptAt,
    nextAttemptAt = nextAttemptAt,
    lastError = lastError,
    deliveredAt = deliveredAt,
)
