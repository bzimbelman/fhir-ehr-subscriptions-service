package com.bzonfhir.subscriptionservice.interfaceengine.worker

import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.stereotype.Service
import org.springframework.transaction.annotation.Propagation
import org.springframework.transaction.annotation.Transactional
import java.time.OffsetDateTime

/**
 * Transactional JDBC gateway for [IngestedMessageWorker].
 *
 * Lives as its own bean (not inlined into the worker) so the @Transactional
 * methods can be reached through a Spring proxy. A `this.markDelivered(...)`
 * call from inside the worker would bypass the proxy and silently run with
 * no transaction. Pulling the JDBC code out makes the proxy boundary
 * explicit — every transactional operation is a call to the injected
 * `gateway` bean. Same pattern as [IngestPersistService] → repository.
 *
 * `open` so Spring's CGLIB proxy can subclass; the `kotlin-spring` plugin
 * (configured in build.gradle.kts) makes @Service classes open
 * automatically, but the explicit modifier here is a documentation aid.
 *
 * The `@ConditionalOnProperty` mirrors the worker's so the bean lifecycle
 * stays simple: when the worker is disabled, the gateway is disabled too,
 * and nothing in the JPA/JDBC startup path drags either of them in.
 */
@Service
@ConditionalOnProperty(
    prefix = "subscription-service.worker",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
open class WorkerJdbcGateway(
    private val jdbc: JdbcTemplate,
) {

    /**
     * Atomic claim:
     *
     *   - SELECT id ... FOR UPDATE SKIP LOCKED LIMIT $batchSize
     *   - UPDATE status='TRANSFORMING', last_attempt_at=now() on the same ids
     *   - COMMIT (handled by @Transactional)
     *
     * The SELECT and UPDATE share one transaction so the rows stay locked
     * until commit. After commit, the WHERE filter (status='RECEIVED' OR ...)
     * excludes status='TRANSFORMING' rows naturally — no need for a
     * second-level lock during slow I/O.
     *
     * REQUIRES_NEW is defensive: this method is callable from any context
     * (the scheduled poll thread, future ad-hoc tools, tests with an
     * ambient tx). REQUIRES_NEW guarantees a short, dedicated transaction.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW)
    open fun claimBatch(batchSize: Int): List<Long> {
        val ids = jdbc.queryForList(
            """
            SELECT id FROM ingested_messages
             WHERE status = 'RECEIVED'
                OR (status = 'FAILED' AND (next_attempt_at IS NULL OR next_attempt_at <= now()))
             ORDER BY received_at
             LIMIT ?
             FOR UPDATE SKIP LOCKED
            """.trimIndent(),
            Long::class.java,
            batchSize,
        )
        if (ids.isEmpty()) return emptyList()
        val placeholders = ids.joinToString(",") { "?" }
        val updateSql =
            "UPDATE ingested_messages " +
                "SET status='TRANSFORMING', last_attempt_at = now() " +
                "WHERE id IN ($placeholders)"
        jdbc.update(updateSql, *ids.toTypedArray())
        return ids
    }

    /**
     * Read the few columns [IngestedMessageWorker.processOne] needs. Done
     * in its own read-only transaction so the connection is returned to
     * the pool as soon as the row is materialized — important because the
     * matchbox + HAPI calls that follow are slow.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW, readOnly = true)
    open fun loadRow(id: Long): ClaimedRow? {
        val rows = jdbc.queryForList(
            """
            SELECT id, message_type, raw_message, attempt_count
              FROM ingested_messages
             WHERE id = ?
            """.trimIndent(),
            id,
        )
        val row = rows.firstOrNull() ?: return null
        return ClaimedRow(
            id = (row["id"] as Number).toLong(),
            messageType = row["message_type"] as String,
            rawMessage = row["raw_message"] as String,
            attemptCount = (row["attempt_count"] as Number).toInt(),
        )
    }

    /**
     * Terminal update for the success path. Stamps `delivered_at = now()`,
     * sets `last_error` (usually null on success, but the "no transform
     * configured" short-circuit writes the sentinel here for visibility).
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW)
    open fun markDelivered(id: Long, lastError: String?) {
        jdbc.update(
            """
            UPDATE ingested_messages
               SET status = 'DELIVERED',
                   delivered_at = now(),
                   last_error = ?
             WHERE id = ?
            """.trimIndent(),
            lastError,
            id,
        )
    }

    /**
     * Terminal update for the failure path. Increments attempt_count,
     * stamps last_attempt_at + last_error. `next_attempt_at` is left NULL
     * for this story — ticket #383 will compute the exponential backoff
     * here.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW)
    open fun markFailed(id: Long, attemptCount: Int, lastError: String) {
        // Truncate last_error to a reasonable size — Postgres TEXT is
        // unbounded but very long error messages from a stack trace would
        // bloat the table. 4 KB is more than enough for any single
        // exception message we expect.
        val trimmed = if (lastError.length > 4096) lastError.take(4096) else lastError
        jdbc.update(
            """
            UPDATE ingested_messages
               SET status = 'FAILED',
                   attempt_count = ?,
                   last_attempt_at = now(),
                   last_error = ?,
                   next_attempt_at = NULL
             WHERE id = ?
            """.trimIndent(),
            attemptCount + 1,
            trimmed,
            id,
        )
    }

    /**
     * Crash-recovery sweep: any row stuck in TRANSFORMING whose
     * last_attempt_at is older than the cutoff is moved to FAILED with a
     * sentinel last_error. Called on ApplicationReadyEvent.
     *
     * Returns the number of rows updated, for logging.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW)
    open fun recoverStaleTransforming(staleSeconds: Long): Int {
        val cutoff = OffsetDateTime.now().minusSeconds(staleSeconds)
        return jdbc.update(
            """
            UPDATE ingested_messages
               SET status = 'FAILED',
                   last_error = 'worker died mid-process',
                   last_attempt_at = now()
             WHERE status = 'TRANSFORMING'
               AND (last_attempt_at IS NULL OR last_attempt_at <= ?)
            """.trimIndent(),
            cutoff,
        )
    }
}

/**
 * Row view used by [IngestedMessageWorker.processOne]. Just enough to
 * drive the matchbox call and the terminal update; the admin API reads
 * the full row through the JPA repository when it needs it.
 */
data class ClaimedRow(
    val id: Long,
    val messageType: String,
    val rawMessage: String,
    val attemptCount: Int,
)
