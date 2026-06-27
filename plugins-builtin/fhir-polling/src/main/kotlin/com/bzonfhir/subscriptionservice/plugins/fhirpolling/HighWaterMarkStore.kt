package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import java.time.Instant
import java.util.concurrent.ConcurrentHashMap

/**
 * Tracks the most recent `_lastUpdated` we've successfully fetched per
 * configured polling source (ticket #434).
 *
 * The FHIR polling plugin issues searches like
 * `Observation?_lastUpdated=gt{{lastRun}}` — the substitution value is
 * whatever this store says for the source's id. After each successful
 * poll, the plugin advances the mark to the newest
 * `Resource.meta.lastUpdated` in the returned Bundle. This is what
 * keeps successive polls from re-emitting the same resource forever.
 *
 * ## V1: in-memory only
 *
 * Marks are held in a `ConcurrentHashMap<String, Instant>`. That has
 * one important consequence: **the marks do not survive a JVM
 * restart**. After a restart, the first poll for each source returns
 * the sentinel (the UNIX epoch), so the FHIR server returns whatever
 * resources have been changed-since-1970 (in practice: everything in
 * its catalog newer than the last few hours' retention horizon).
 * Downstream, the engine's (sourceSystem, sourceId) idempotency key
 * absorbs the duplicates — every resource that was already persisted
 * during the previous lifetime gets a "duplicate receive" log line and
 * no new row.
 *
 * A future ticket will swap this in-memory store for a JPA-backed
 * version that persists marks in the engine's database. The interface
 * (the two methods on this class) stays the same so the plugin won't
 * change.
 *
 * ## Forward-only invariant
 *
 * [updateMark] takes the MAX of the current mark and the proposed
 * value. The plugin's per-bundle update path computes "the newest
 * lastUpdated across the bundle's entries" before calling
 * updateMark, so in practice the proposed value is always newer than
 * the current one — but the max-guard is cheap insurance against a
 * future caller that doesn't sort first. The invariant is critical:
 * if the mark could regress, the next poll would re-fetch resources
 * we already delivered.
 *
 * ## Thread safety
 *
 * Concurrent updates to the same source-id are serialized via
 * `ConcurrentHashMap.compute()` which holds a per-bin lock for the
 * duration of the remapping function. Readers via
 * `ConcurrentHashMap.getOrDefault()` are lock-free and see a
 * consistent snapshot — they might see a slightly stale value if a
 * writer is in mid-update, but never a torn value.
 */
class HighWaterMarkStore {

    /**
     * Per-source mark map. Key = [com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig.id].
     * Value = Instant of the newest `_lastUpdated` we've delivered for
     * that source. Absent key = "never seen before"; readers get
     * [DEFAULT_MARK] in that case.
     */
    private val marks: ConcurrentHashMap<String, Instant> = ConcurrentHashMap()

    /**
     * Read the current mark for [sourceId]. Returns [DEFAULT_MARK] (UNIX
     * epoch) if no mark has been stored yet — i.e. on the very first
     * poll for a freshly-started JVM. The plugin's search-template code
     * substitutes whatever this returns into `{{lastRun}}` placeholders.
     */
    fun getMark(sourceId: String): Instant =
        marks.getOrDefault(sourceId, DEFAULT_MARK)

    /**
     * Advance the mark for [sourceId] to [candidate], but only if
     * candidate is strictly newer than the current value. Idempotent
     * under repeated calls with the same value.
     *
     * Implementation note: the `compute` callback receives the
     * existing value (or null if absent) and returns the new value to
     * store. ConcurrentHashMap holds a per-bin lock for the duration
     * of the callback, so two threads racing to update the same key
     * are serialized — neither can read-then-overwrite the other's
     * write. The race test (`concurrent updateMark calls converge on
     * the latest value`) exercises this.
     */
    fun updateMark(sourceId: String, candidate: Instant) {
        marks.compute(sourceId) { _, existing ->
            if (existing == null || candidate.isAfter(existing)) candidate else existing
        }
    }

    companion object {
        /**
         * The sentinel "never polled" mark. UNIX epoch — earlier than
         * any plausible `Resource.meta.lastUpdated` value, so the first
         * search request gets back "every resource the server has."
         * Picked over `Instant.MIN` because FHIR servers reject
         * `_lastUpdated=gt-271821-04-20T...` shaped strings; the epoch
         * is the conventional "beginning of time" in clinical data.
         */
        val DEFAULT_MARK: Instant = Instant.parse("1970-01-01T00:00:00Z")
    }
}
