package com.bzonfhir.subscriptionservice.interfaceengine.persistence

import org.springframework.data.domain.Pageable
import org.springframework.data.jpa.repository.JpaRepository
import org.springframework.stereotype.Repository

/**
 * Spring Data repository for [IngestedMessage] (Epic #378, ticket #380).
 *
 * The custom finders here are intentionally minimal — just the lookups
 * the downstream stories need:
 *
 *   #381 (receive route persists): [findFirstBySourceSystemAndSourceId]
 *         lets the inbound route do an idempotency check before insert.
 *   #382 (async worker):           [findTop10ByStatusOrderByReceivedAtAsc]
 *         is the worker's poll query — oldest RECEIVED rows first, bounded
 *         batch size so one poll can't starve other workers.
 *   monitoring (future):           [countByStatus] for cheap gauge metrics.
 *
 * Anything more sophisticated (the retry scheduler's "ready to retry"
 * query, dead-letter sweeps, etc.) lands in #382 where it has tests.
 */
@Repository
interface IngestedMessageRepository : JpaRepository<IngestedMessage, Long> {

    /**
     * Idempotency lookup for inbound: returns the existing row for a
     * (sourceSystem, sourceId) pair if we've already received it, null
     * otherwise. Matches the UNIQUE constraint on the table.
     */
    fun findFirstBySourceSystemAndSourceId(
        sourceSystem: String,
        sourceId: String,
    ): IngestedMessage?

    /**
     * Worker poll: oldest 10 rows in the given status, ordered by
     * [IngestedMessage.receivedAt] ascending (FIFO within the batch).
     * Spring Data's "Top" keyword caps the result size at the DB level.
     */
    fun findTop10ByStatusOrderByReceivedAtAsc(
        status: IngestedMessageStatus,
    ): List<IngestedMessage>

    /**
     * Cheap counter for monitoring / health endpoints.
     */
    fun countByStatus(status: IngestedMessageStatus): Long

    /**
     * Admin list (#384) — paged + status-filtered. Spring Data builds the
     * SQL from the method name. Sort comes from the Pageable so the
     * controller can ask for newest-first.
     */
    fun findByStatus(status: IngestedMessageStatus, pageable: Pageable): List<IngestedMessage>

    /**
     * Admin list (#384) — paged, no filter. Spring Data exposes findAll
     * but with a Pageable that returns a Page<T>; we only need a slice,
     * so this method-name finder gives us the list shape directly.
     */
    fun findAllBy(pageable: Pageable): List<IngestedMessage>
}
