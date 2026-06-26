package com.bzonfhir.subscriptionservice.interfaceengine.worker

import ca.uhn.fhir.rest.client.api.IGenericClient
import com.bzonfhir.subscriptionservice.interfaceengine.observability.CorrelationId
import jakarta.annotation.PostConstruct
import org.slf4j.LoggerFactory
import org.slf4j.event.Level
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.boot.context.event.ApplicationReadyEvent
import org.springframework.context.event.EventListener
import org.springframework.scheduling.annotation.Scheduled
import org.springframework.stereotype.Component
import kotlin.math.min
import kotlin.math.pow

/**
 * Asynchronous worker for `ingested_messages` (Epic #378, ticket #382).
 *
 * # Why this exists
 *
 * After ticket #381 the MLLP receive route persists raw HL7 v2 messages and
 * ACKs the sender as fast as possible. The transform-and-deliver work (call
 * matchbox `$transform`, POST the resulting Bundle to HAPI) moved off the
 * sender-visible path entirely. THIS class is the new home for that work.
 *
 * # Implementation choice: Spring @Scheduled (NOT a Camel timer route)
 *
 * Two options on the table:
 *
 *   A) Camel `from("timer:worker?period=${...}")` → bean call. Plays well
 *      with the existing IPF/Camel codebase, and an AdviceWith mock fits
 *      cleanly into the IngestRoutesTest pattern.
 *
 *   B) Spring `@Scheduled(fixedDelayString = "…")` on a `@Service`. Simpler
 *      — no Camel route, no AdviceWith — and the test harness becomes plain
 *      `@SpringBootTest` + `Awaitility` (which is already on the classpath).
 *
 * We picked B. Reasons:
 *
 *   - This worker has no Camel-specific behaviour (no error-handler chain,
 *     no exchange properties to thread through, no in-process MLLP ACK to
 *     coordinate with). The Camel infrastructure would be ceremony.
 *   - SELECT … FOR UPDATE SKIP LOCKED has to be raw JDBC for now anyway
 *     (Spring Data's `@Lock(LockModeType.PESSIMISTIC_WRITE)` doesn't easily
 *     expose `SKIP LOCKED`), so JdbcTemplate is in the picture either way.
 *   - Plain `@Scheduled` is one annotation on a method. Camel would require
 *     a RouteBuilder + a separate bean for the work + careful test setup to
 *     stop the route between tests. Less code, fewer moving parts.
 *   - Future scale-out: multiple replicas of this pod can run side-by-side
 *     and SKIP LOCKED gives each one a disjoint batch. No Camel cluster
 *     coordination required.
 *
 * # Why the JDBC work lives in [WorkerJdbcGateway] and not here
 *
 * Spring's @Transactional only takes effect when methods are called THROUGH
 * a proxy. A `this.markDelivered(...)` call from inside this class would
 * bypass the proxy entirely and run as a no-tx method. Pulling the JDBC
 * methods onto a separate @Service makes the proxy boundary explicit:
 * every transaction-requiring method is reached via the injected
 * [WorkerJdbcGateway] bean, which IS the proxy. Same pattern as
 * IngestPersistService → repository.
 *
 * # Lifecycle per row
 *
 * Each poll cycle:
 *
 *   1. `gateway.claimBatch()` — open ONE transaction, `SELECT … FOR UPDATE
 *      SKIP LOCKED` to claim a batch of ids, `UPDATE SET status='TRANSFORMING'`
 *      on those ids, COMMIT. The lock is released by the commit; other
 *      replicas can now ignore these rows because they're no longer in
 *      the worker's `WHERE` clause.
 *   2. For each claimed id, in a SEPARATE call:
 *        a. Look up the row.
 *        b. If `message_type` has a configured StructureMap → matchbox
 *           transform + HAPI POST.
 *           If not → short-circuit to DELIVERED with last_error set to a
 *           sentinel so the admin UI surfaces it.
 *        c. UPDATE the row with the final status. Each row's terminal
 *           UPDATE is its own short transaction so a failure on row N
 *           doesn't roll back rows 1..N-1.
 *
 * The TWO-PHASE claim is deliberate. If we held the SELECT FOR UPDATE lock
 * across the matchbox + HAPI calls, a 30-second matchbox response would
 * hold a Postgres row lock for 30 seconds and starve every other replica.
 * Marking TRANSFORMING + committing makes the row INELIGIBLE for re-claim
 * (the WHERE clause excludes TRANSFORMING), giving us the same exclusion
 * semantics without holding a DB-level lock during slow I/O.
 *
 * # Crash recovery
 *
 * If the JVM dies while a row is TRANSFORMING, on restart we have orphans:
 * status=TRANSFORMING with last_attempt_at older than now() - stale-seconds.
 * The [recoverStaleTransforming] event listener resets those to FAILED so
 * the next poll picks them up via the FAILED-with-retry-ready branch. This
 * runs on ApplicationReadyEvent so we get the recovery sweep before the
 * first poll fires.
 */
@Component
// ConditionalOnProperty: the bean only exists if worker.enabled=true. This
// is stronger than just gating the @Scheduled method body — the @Scheduled
// machinery itself is never registered for this bean, so disabling the
// worker means a truly inert app (useful for the existing IngestRoutesTest
// / AdminMessagesControllerTest, which set the property to false).
// matchIfMissing=true keeps the production default ON without requiring a
// property to be explicitly set somewhere.
@ConditionalOnProperty(
    prefix = "subscription-service.worker",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
class IngestedMessageWorker(
    private val gateway: WorkerJdbcGateway,
    private val matchboxClient: MatchboxClient,
    private val hapiClient: IGenericClient,
    @Value("\${subscription-service.worker.batch-size:10}") private val batchSize: Int,
    @Value("\${subscription-service.worker.transforming-stale-seconds:60}")
    private val staleSeconds: Long,
    @Value("\${subscription-service.matchbox.structuremap.adt-a01}")
    private val adtA01StructureMap: String,
    // ---- Retry policy (#383) ----------------------------------------------
    // maxAttempts: when (current.attemptCount + 1) >= maxAttempts the row
    //   goes to DEAD_LETTER instead of being scheduled for another retry.
    // backoffBaseMs / backoffFactor / backoffMaxMs: exponential backoff
    //   capped at backoffMaxMs. See [computeBackoffMillis] for the formula.
    // dlqLogLevel: severity of the one log line emitted per DLQ transition.
    @Value("\${subscription-service.worker.retry.max-attempts:5}")
    private val maxAttempts: Int,
    @Value("\${subscription-service.worker.retry.backoff-base-ms:1000}")
    private val backoffBaseMs: Long,
    @Value("\${subscription-service.worker.retry.backoff-max-ms:300000}")
    private val backoffMaxMs: Long,
    @Value("\${subscription-service.worker.retry.backoff-factor:2.0}")
    private val backoffFactor: Double,
    @Value("\${subscription-service.worker.retry.dlq-log-level:WARN}")
    private val dlqLogLevel: String,
) {
    private val log = LoggerFactory.getLogger(IngestedMessageWorker::class.java)

    private val dlqLevel: Level by lazy {
        runCatching { Level.valueOf(dlqLogLevel.uppercase()) }.getOrElse {
            log.warn("invalid dlq-log-level '{}', defaulting to WARN", dlqLogLevel)
            Level.WARN
        }
    }

    /**
     * Per-message-type → StructureMap canonical URL.
     *
     * v1 ships exactly one mapping (ADT_A01); everything else short-circuits
     * to DELIVERED with the `last_error='no transform configured'` sentinel.
     * When a new mapping lands, add an entry here and a corresponding
     * `subscription-service.matchbox.structuremap.<type>` config key.
     */
    private val transformByType: Map<String, String> by lazy {
        mapOf("ADT_A01" to adtA01StructureMap)
    }

    @PostConstruct
    fun logStartup() {
        log.info(
            "IngestedMessageWorker enabled batchSize={} staleSeconds={} transforms={} " +
                "retry.maxAttempts={} retry.backoffBaseMs={} retry.backoffMaxMs={} " +
                "retry.backoffFactor={} retry.dlqLogLevel={}",
            batchSize,
            staleSeconds,
            transformByType.keys,
            maxAttempts,
            backoffBaseMs,
            backoffMaxMs,
            backoffFactor,
            dlqLevel,
        )
    }

    /**
     * Sweep stale TRANSFORMING rows back to FAILED so the worker retries
     * them. Runs on application ready (i.e. once per JVM start, before
     * the first scheduled poll fires).
     *
     * We use the timestamp-based check rather than a "kill all TRANSFORMING"
     * rule so a healthy in-flight row owned by a sibling replica isn't
     * disturbed — only rows whose last_attempt_at is older than
     * staleSeconds are reset. (last_attempt_at is set when the row was
     * claimed, see WorkerJdbcGateway.claimBatch.)
     */
    @EventListener(ApplicationReadyEvent::class)
    fun recoverStaleTransforming() {
        val updated = gateway.recoverStaleTransforming(staleSeconds)
        if (updated > 0) {
            log.warn("recovered {} stale TRANSFORMING rows", updated)
        }
    }

    /**
     * Main poll. Runs at fixed delay (so we don't pile up overlapping runs
     * if a batch is slow). The work splits into:
     *
     *   1. gateway.claimBatch() — single tx, returns the claimed ids.
     *   2. for each id → processOne(id) — its own tx, can fail without
     *      affecting siblings.
     *
     * Errors thrown out of processOne() are caught here and logged so a
     * single bad row doesn't kill the polling thread.
     */
    @Scheduled(
        fixedDelayString = "\${subscription-service.worker.poll-interval-ms:1000}",
        initialDelayString = "\${subscription-service.worker.poll-interval-ms:1000}",
    )
    fun poll() {
        val claimed = try {
            gateway.claimBatch(batchSize)
        } catch (ex: Exception) {
            log.warn("worker poll: claim failed: {}", ex.message)
            return
        }
        if (claimed.isEmpty()) return
        log.debug("worker poll: claimed {} rows", claimed.size)
        for (id in claimed) {
            // Each row processes under its own MDC scope so its
            // correlation_id ends up on every log line emitted while
            // the row is in flight — and is cleared on exit so the
            // next iteration of this for-loop doesn't inherit it.
            // We don't yet know the correlation_id; processOne resolves
            // it from the row and re-installs the MDC there. Clear up
            // front so a leaked value from a prior poll thread is
            // discarded.
            org.slf4j.MDC.remove(CorrelationId.MDC_KEY)
            try {
                processOne(id)
            } catch (ex: Exception) {
                // processOne() is supposed to translate failures into FAILED
                // status updates itself; an exception out here means even
                // *that* path failed (DB unreachable, etc.). Log + continue.
                log.error("worker poll: row {} processing crashed: {}", id, ex.message)
            } finally {
                // Defensive cleanup — processOne sets MDC; clear on the
                // way out regardless of success or throw.
                org.slf4j.MDC.remove(CorrelationId.MDC_KEY)
            }
        }
    }

    /**
     * Process a single row that has already been claimed (status =
     * TRANSFORMING). Does the matchbox + HAPI work, then UPDATEs the row's
     * final state. Each terminal UPDATE is its own transaction so failures
     * don't cascade.
     *
     * Not @Transactional itself — we deliberately do the slow I/O OUTSIDE
     * a transaction. The terminal UPDATE in [WorkerJdbcGateway.markDelivered]
     * / [WorkerJdbcGateway.markFailedWithBackoff] / [WorkerJdbcGateway.markDeadLetter]
     * runs in its own short transaction.
     * This is important: holding a transaction open across a 30-second
     * matchbox call would tie up a Hikari connection that other request
     * paths (admin API, MLLP route) also need.
     */
    internal fun processOne(id: Long) {
        val row = gateway.loadRow(id) ?: run {
            log.warn("worker: row {} vanished after claim?", id)
            return
        }

        // Establish the correlation id BEFORE any other log line. Three
        // cases:
        //   (a) row has a stored correlation_id (post-#388 receive path) →
        //       adopt it as-is.
        //   (b) row has none (pre-#388 row, or future protocol that didn't
        //       carry one in) → mint a fresh UUID and back-fill it so the
        //       value persists across retries and is grep-able later.
        // We set MDC + send the header to matchbox + HAPI from the same
        // value either way.
        val correlationId = row.correlationId ?: CorrelationId.generate().also {
            // Best-effort back-fill — if the UPDATE races with another
            // replica (unlikely under SKIP LOCKED but theoretically
            // possible if a recovery sweep ran), the WHERE clause keeps
            // it idempotent. We don't read back; the value we just
            // generated is what THIS processOne uses.
            runCatching { gateway.backfillCorrelationId(id, it) }.onFailure { ex ->
                log.debug("worker: backfill correlation_id failed for id={} ({})", id, ex.message)
            }
        }
        org.slf4j.MDC.put(CorrelationId.MDC_KEY, correlationId)

        val messageType = row.messageType
        val rawMessage = row.rawMessage

        val structureMap = transformByType[messageType]
        if (structureMap == null) {
            // Unsupported message type — the row was already idempotently
            // persisted; there's nothing to transform. Mark DELIVERED with a
            // sentinel last_error so operators can see at a glance which
            // rows fell through. (Using DELIVERED rather than DEAD_LETTER
            // because the message was successfully *received* and we don't
            // intend to retry it — retry won't help if no map exists.)
            gateway.markDelivered(id, lastError = NO_TRANSFORM_SENTINEL)
            log.info(
                "worker: row {} type={} short-circuit DELIVERED ({})",
                id,
                messageType,
                NO_TRANSFORM_SENTINEL,
            )
            return
        }

        // -- Matchbox transform --
        // The MatchboxClient implementation reads the current MDC
        // correlation_id and sends it as `X-Correlation-Id` on the
        // outbound POST. Same value goes on the HAPI Bundle post below.
        val bundle = try {
            matchboxClient.transformToBundle(structureMap, rawMessage)
        } catch (ex: Exception) {
            val msg = "matchbox transform failed: ${ex.message ?: ex.javaClass.simpleName}"
            handleFailure(row, msg)
            return
        }

        // -- HAPI transaction POST --
        // HAPI client sees the correlation id via an additional request
        // header configured by [HapiClientConfig]'s ClientInterceptor that
        // copies MDC.get(correlation_id) onto every outbound request.
        try {
            hapiClient.transaction().withBundle(bundle).execute()
        } catch (ex: Exception) {
            val msg = "HAPI transaction failed: ${ex.message ?: ex.javaClass.simpleName}"
            handleFailure(row, msg)
            return
        }

        gateway.markDelivered(id, lastError = null)
        log.info("worker: row {} DELIVERED type={}", id, messageType)
    }

    /**
     * Apply the retry policy on failure (#383).
     *
     * Decision tree:
     *
     *   - new_attempt = current.attemptCount + 1
     *   - if new_attempt >= maxAttempts → DEAD_LETTER, one log line at
     *     `dlqLogLevel` (default WARN) with structured fields so operators
     *     can grep / alert on `event=dlq`.
     *   - otherwise → FAILED with `next_attempt_at = now() + backoff(new_attempt)`
     *     and one INFO log line tagged `event=retry_scheduled`.
     *
     * Each branch is exactly one DB UPDATE (via the gateway) and exactly
     * one log line — the rest of the policy is data on the row.
     */
    internal fun handleFailure(row: ClaimedRow, lastError: String) {
        val newAttemptCount = row.attemptCount + 1
        if (newAttemptCount >= maxAttempts) {
            gateway.markDeadLetter(
                id = row.id,
                newAttemptCount = newAttemptCount,
                lastError = lastError,
            )
            // Structured key=value pairs so a single grep can pull DLQ
            // events out of pod logs (`grep event=dlq`). Keep the field
            // names stable; runbooks may reference them.
            logAt(
                dlqLevel,
                "event=dlq message_id={} source_system={} source_id={} message_type={} " +
                    "attempt_count={} last_error={}",
                row.id,
                row.sourceSystem,
                row.sourceId,
                row.messageType,
                newAttemptCount,
                shortenError(lastError),
            )
            return
        }
        val backoffMillis = computeBackoffMillis(newAttemptCount)
        gateway.markFailedWithBackoff(
            id = row.id,
            newAttemptCount = newAttemptCount,
            lastError = lastError,
            backoffMillis = backoffMillis,
        )
        log.info(
            "event=retry_scheduled message_id={} source_system={} source_id={} " +
                "message_type={} attempt_count={} backoff_ms={} last_error={}",
            row.id,
            row.sourceSystem,
            row.sourceId,
            row.messageType,
            newAttemptCount,
            backoffMillis,
            shortenError(lastError),
        )
    }

    /**
     * Backoff formula (#383):
     *
     *   delay = min(base * factor^(attempt-1), max)
     *
     * `attempt` is the **post-increment** attempt count — i.e. when the
     * caller has just failed for the N-th time and is asking "how long
     * should I wait before the (N+1)-th try", they pass N.
     *
     * Defaults (1000ms / 2.0 / 300000ms) produce: 1s, 2s, 4s, 8s, 16s …
     * capped at 5 min. With max-attempts=5 the row hits DLQ after the
     * 5th failure with a total wall-clock of ~1+2+4+8 = 15s spent
     * waiting between retries.
     */
    internal fun computeBackoffMillis(newAttemptCount: Int): Long {
        // pow(double) is fine here — the values are tiny and the result
        // is converted to a Long after clamping. No overflow concerns.
        val raw = backoffBaseMs.toDouble() * backoffFactor.pow((newAttemptCount - 1).coerceAtLeast(0))
        return min(raw, backoffMaxMs.toDouble()).toLong().coerceAtLeast(0L)
    }

    private fun logAt(level: Level, format: String, vararg args: Any?) {
        when (level) {
            Level.ERROR -> log.error(format, *args)
            Level.WARN -> log.warn(format, *args)
            Level.INFO -> log.info(format, *args)
            Level.DEBUG -> log.debug(format, *args)
            Level.TRACE -> log.trace(format, *args)
        }
    }

    private fun shortenError(msg: String): String =
        if (msg.length > 200) msg.take(200) + "…" else msg

    companion object {
        /**
         * Stored in `last_error` for rows whose `message_type` has no
         * StructureMap configured. Public + named so admin tooling and
         * tests can match on it exactly. Do NOT change without updating
         * the admin UI's filter.
         */
        const val NO_TRANSFORM_SENTINEL = "no transform configured"
    }
}
