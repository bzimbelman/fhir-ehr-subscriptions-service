package com.bzonfhir.subscriptionservice.interfaceengine.persistence

import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.context.annotation.Lazy
import org.springframework.dao.DataIntegrityViolationException
import org.springframework.stereotype.Service
import org.springframework.transaction.annotation.Propagation
import org.springframework.transaction.annotation.Transactional

/**
 * Transactional gateway for the inbound receive path (Epic #378, ticket #381).
 *
 * The interface engine's MLLP receive route MUST do exactly two things on the
 * synchronous, sender-visible code path:
 *
 *   1. Persist the raw message + metadata to `ingested_messages` with
 *      `status = RECEIVED`, durably (the DB transaction commits before the
 *      ACK is written to the wire).
 *   2. Reply with an HL7v2 ACK reflecting whether step 1 succeeded.
 *
 * The transform → POST-to-HAPI work that the route did in #361 has moved
 * out of the synchronous path entirely: the async worker (ticket #382)
 * polls `status = RECEIVED` rows and drives the transform/delivery on its
 * own schedule. That separation is the reason this service exists — keeping
 * the JPA write behind a Spring-managed `@Transactional` boundary lets the
 * route stay declarative (Camel `.bean(...)`) and gives us a clean unit of
 * work the test suite can validate with Testcontainers.
 *
 * Idempotency contract:
 *
 *   The sender (EHR) is allowed — encouraged, even — to retry on connection
 *   blips. A retry resends the same MSH-10 control id. Our table has a UNIQUE
 *   constraint on (`source_system`, `source_id`); the route MUST treat a
 *   duplicate replay as a successful receive (ACK AA), NOT a failure. The
 *   sender has done its job; we've already taken ownership.
 *
 *   This service implements the canonical pattern: optimistically INSERT,
 *   and if the unique-constraint trips, look up the existing row and return
 *   it. That is correct under concurrent ingest where two threads/pods race
 *   on the same control id — the loser of the race converges on the row the
 *   winner wrote, which is the right answer either way. Doing an `existsBy…`
 *   check first would race with another writer between the check and the
 *   insert.
 *
 * Failure model:
 *
 *   The only outcome the route is allowed to surface as AE is "we genuinely
 *   could not take ownership of this message" — the DB is unreachable, the
 *   insert fails with a non-unique-violation error, etc. Everything else,
 *   including the idempotent-duplicate path above, returns successfully and
 *   the route ACKs AA. AE causes EHR-side retry storms; we save it for cases
 *   where retrying is actually the right move.
 */
// `open` so Spring's CGLIB proxy can subclass it to intercept the
// @Transactional methods. Without this Kotlin would emit `final class` and
// proxy creation would silently fall back to a no-op JDK proxy (no
// transactions). The kotlin-spring plugin (configured in build.gradle.kts)
// makes @Component/@Service classes open automatically — keeping the
// explicit modifier here as a documentation aid.
@Service
open class IngestPersistService(
    private val repository: IngestedMessageRepository,
) {
    private val log = LoggerFactory.getLogger(IngestPersistService::class.java)

    // Self-injection (via @Lazy to break the construction-time cycle) so the
    // outer entry point [persistReceived] can invoke the inner
    // @Transactional methods THROUGH the Spring proxy. A plain
    // `this.insertReceived(...)` would bypass the proxy entirely and run as
    // a no-tx method call, which would defeat the whole REQUIRES_NEW design.
    // This is the same self-injection pattern Spring's own docs describe for
    // the "calling a transactional method from within the same class"
    // problem.
    @Autowired
    @Lazy
    private lateinit var self: IngestPersistService

    /**
     * Persist (or look up on duplicate) an inbound message and return the
     * resulting row. Result fields the caller cares about:
     *
     *   - [IngestedMessage.id] is server-assigned; safe to use after this call
     *     returns.
     *   - The returned entity reflects what's actually in the DB, including
     *     the server-assigned `receivedAt`.
     *
     * Throws [DataIntegrityViolationException] only for non-uniqueness
     * integrity failures (NOT NULL violations, type mismatches, etc.) and any
     * other [RuntimeException] for transport-level DB failures (connection
     * pool exhausted, network reset, etc.). The caller — Camel's onException
     * handler — is responsible for translating those into an AE ACK.
     *
     * Propagation = REQUIRES_NEW so this commits before control returns to
     * Camel, regardless of whether the route is running under `transacted()`
     * itself. The MLLP listener does NOT participate in JTA; we want the
     * INSERT durable before the ACK is computed.
     */
    open fun persistReceived(
        sourceProtocol: IngestedMessageSourceProtocol,
        sourceSystem: String,
        sourceId: String,
        messageType: String,
        rawMessage: String,
        rawContentType: String,
    ): IngestedMessage {
        return try {
            self.insertReceived(
                sourceProtocol = sourceProtocol,
                sourceSystem = sourceSystem,
                sourceId = sourceId,
                messageType = messageType,
                rawMessage = rawMessage,
                rawContentType = rawContentType,
            )
        } catch (ex: DataIntegrityViolationException) {
            // Distinguish "duplicate control id" (benign — sender retry) from
            // any other integrity issue (a real failure). Spring wraps the
            // Postgres SQLSTATE 23505 in DataIntegrityViolationException; we
            // resolve which one we're looking at by trying the lookup, which
            // MUST run in its own transaction. Postgres aborts the inserting
            // transaction on unique-violation; any further work in that
            // transaction fails with "current transaction is aborted,
            // commands ignored until end of transaction block". Forcing a
            // fresh transaction (lookupExisting is REQUIRES_NEW) sidesteps
            // that — Postgres has already rolled back the failed insert by
            // the time the exception surfaces here.
            val existing = self.lookupExisting(sourceSystem, sourceId)
            if (existing != null) {
                log.info(
                    "duplicate receive sourceSystem={} sourceId={} existingId={} — treating as idempotent",
                    sourceSystem,
                    sourceId,
                    existing.id,
                )
                existing
            } else {
                // Integrity violation that wasn't a duplicate row — surface it.
                throw ex
            }
        }
    }

    /**
     * Inner insert in its own transaction. REQUIRES_NEW + the catch-and-
     * recover pattern in [persistReceived] means a unique-violation here
     * rolls back ONLY this transaction — the caller's transaction (if any)
     * stays clean and can subsequently call [lookupExisting] without hitting
     * Postgres's "transaction aborted" error. saveAndFlush forces the
     * INSERT to be issued inside the transaction (not deferred to commit),
     * so the exception is throwable from this method rather than at commit
     * time where we can't recover from it cleanly.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW)
    open fun insertReceived(
        sourceProtocol: IngestedMessageSourceProtocol,
        sourceSystem: String,
        sourceId: String,
        messageType: String,
        rawMessage: String,
        rawContentType: String,
    ): IngestedMessage {
        val candidate = IngestedMessage(
            sourceProtocol = sourceProtocol,
            sourceSystem = sourceSystem,
            sourceId = sourceId,
            messageType = messageType,
            rawMessage = rawMessage,
            rawContentType = rawContentType,
            status = IngestedMessageStatus.RECEIVED,
        )
        return repository.saveAndFlush(candidate)
    }

    /**
     * Idempotency lookup, in its own transaction. Called only after the
     * insert path has hit a [DataIntegrityViolationException] — running it
     * in a fresh transaction is REQUIRED because the failed insert poisoned
     * its own transaction in Postgres.
     */
    @Transactional(propagation = Propagation.REQUIRES_NEW, readOnly = true)
    open fun lookupExisting(sourceSystem: String, sourceId: String): IngestedMessage? =
        repository.findFirstBySourceSystemAndSourceId(sourceSystem, sourceId)
}
