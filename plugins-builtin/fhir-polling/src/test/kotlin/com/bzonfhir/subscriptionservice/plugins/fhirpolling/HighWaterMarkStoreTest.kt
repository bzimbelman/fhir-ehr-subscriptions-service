package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Instant
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * Tests for [HighWaterMarkStore].
 *
 * The store tracks "the most recent `_lastUpdated` we've successfully
 * fetched, per configured source." It's the thing that keeps successive
 * polls from re-emitting the same Observation forever — each poll asks
 * the FHIR server for "things newer than the last mark," then advances
 * the mark to the newest resource returned.
 *
 * V1 is in-memory: a `ConcurrentHashMap<String, Instant>`. That has two
 * consequences captured in the tests:
 *
 *   1. Multiple sources keyed by id never collide (separate keys).
 *   2. Restarts forget the mark and replay the recent window.
 *      Idempotency downstream (the engine's (sourceSystem, sourceId)
 *      idempotency key) absorbs the duplicates — this is documented in
 *      the plugin's README.
 *
 * A future ticket will swap in a JPA-backed store that persists marks
 * across restarts; the interface stays the same so the plugin won't
 * change.
 */
class HighWaterMarkStoreTest {

    @Test
    fun `getMark returns default sentinel for unseen source-id`() {
        val store = HighWaterMarkStore()

        // The sentinel must be a finite Instant in the FAR past — when
        // the plugin substitutes it into a FHIR search like
        // `_lastUpdated=gt{{lastRun}}` we want the server to return
        // "everything since the dawn of time" on first poll. UNIX
        // epoch is the canonical FHIR-friendly choice.
        assertThat(store.getMark("never-seen-before"))
            .isEqualTo(Instant.parse("1970-01-01T00:00:00Z"))
    }

    @Test
    fun `updateMark then getMark round-trips the value`() {
        val store = HighWaterMarkStore()
        val now = Instant.parse("2026-06-25T14:30:01Z")

        store.updateMark("athena-observations", now)

        assertThat(store.getMark("athena-observations")).isEqualTo(now)
    }

    @Test
    fun `multiple source-ids don't share a mark`() {
        val store = HighWaterMarkStore()
        val obsMark = Instant.parse("2026-06-25T14:30:01Z")
        val encMark = Instant.parse("2026-06-25T15:45:22Z")

        store.updateMark("athena-observations", obsMark)
        store.updateMark("lab-encounters", encMark)

        assertThat(store.getMark("athena-observations")).isEqualTo(obsMark)
        assertThat(store.getMark("lab-encounters")).isEqualTo(encMark)
        // Sanity: an unrelated key still returns the sentinel.
        assertThat(store.getMark("documentreference"))
            .isEqualTo(Instant.parse("1970-01-01T00:00:00Z"))
    }

    @Test
    fun `updateMark advances forward only — older values are ignored`() {
        // This invariant matters when a poll's Bundle is unordered and
        // we accidentally call updateMark with an entry that's OLDER
        // than one we already saw. The mark must never regress; if it
        // did, the next poll would re-fetch resources we already
        // delivered. Documented behaviour: takes the max of current
        // and proposed.
        val store = HighWaterMarkStore()
        val newer = Instant.parse("2026-06-25T14:30:01Z")
        val older = Instant.parse("2026-06-25T14:00:00Z")

        store.updateMark("obs", newer)
        store.updateMark("obs", older)

        assertThat(store.getMark("obs")).isEqualTo(newer)
    }

    @Test
    fun `updateMark with identical value is a no-op`() {
        val store = HighWaterMarkStore()
        val ts = Instant.parse("2026-06-25T14:30:01Z")

        store.updateMark("obs", ts)
        store.updateMark("obs", ts)

        assertThat(store.getMark("obs")).isEqualTo(ts)
    }

    @Test
    fun `concurrent updateMark calls converge on the latest value`() {
        // Two threads racing to advance the same mark. After both
        // finish, getMark must return the LATER of the two timestamps.
        // The store uses ConcurrentHashMap.compute() to serialize the
        // compare-and-update; this test catches a regression where
        // someone refactors to a plain `if (newer) put(...)` which has
        // a TOCTOU race.
        val store = HighWaterMarkStore()
        val ts1 = Instant.parse("2026-06-25T14:30:01Z")
        val ts2 = Instant.parse("2026-06-25T15:30:01Z")
        val ready = CountDownLatch(1)
        val pool = Executors.newFixedThreadPool(2)
        try {
            val f1 = pool.submit {
                ready.await()
                repeat(5_000) { store.updateMark("race", ts1) }
            }
            val f2 = pool.submit {
                ready.await()
                repeat(5_000) { store.updateMark("race", ts2) }
            }
            ready.countDown()
            f1.get(10, TimeUnit.SECONDS)
            f2.get(10, TimeUnit.SECONDS)
        } finally {
            pool.shutdown()
        }

        assertThat(store.getMark("race")).isEqualTo(ts2)
    }
}
