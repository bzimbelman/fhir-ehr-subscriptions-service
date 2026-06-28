package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir
import java.nio.file.Path
import java.time.Duration
import java.time.Instant

/**
 * On-disk license cache tests (ticket #460, Epic #428).
 *
 * Mirrors the UI's `licenseCache.ts` contract:
 *
 *   - Round-trip a cache entry through disk.
 *   - readCache() returns null when the file is missing or corrupt.
 *   - isFresh() honours the 7-day TTL.
 *   - writeCache() does NOT leak a .tmp file on success.
 */
class LicenseCacheTest {

    @Test
    fun `round-trips a cache entry through disk`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("license-cache.json").toString()
        val entry = LicenseCacheEntry(
            fetchedAt = Instant.parse("2026-06-28T00:00:00Z"),
            licenseKeyFingerprint = "abcd1234",
            entitlements = listOf("audit.export.iti20", "alerting.pagerduty"),
            tierName = "pro",
            expiresAt = "2027-06-28T00:00:00Z",
            signature = "compact.jws.here",
            signatureKid = "license-key-dev",
        )

        LicenseCache.write(entry, cachePath)
        val read = LicenseCache.read(cachePath)

        assertThat(read).isEqualTo(entry)
    }

    @Test
    fun `read returns null when the file does not exist`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("missing.json").toString()
        assertThat(LicenseCache.read(cachePath)).isNull()
    }

    @Test
    fun `read returns null when the file is malformed`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("bad.json").toString()
        java.nio.file.Files.writeString(java.nio.file.Path.of(cachePath), "not json at all")
        assertThat(LicenseCache.read(cachePath)).isNull()
    }

    @Test
    fun `isFresh returns true within the 7-day TTL`() {
        val fetchedAt = Instant.parse("2026-06-28T00:00:00Z")
        val entry = entryAt(fetchedAt)
        // 1 day later — fresh.
        assertThat(LicenseCache.isFresh(entry, fetchedAt + Duration.ofDays(1))).isTrue()
        // 6 days 23h later — fresh.
        assertThat(LicenseCache.isFresh(entry, fetchedAt + Duration.ofDays(6).plusHours(23))).isTrue()
    }

    @Test
    fun `isFresh returns false past the 7-day TTL`() {
        val fetchedAt = Instant.parse("2026-06-28T00:00:00Z")
        val entry = entryAt(fetchedAt)
        // 7 days + 1 sec — stale.
        assertThat(LicenseCache.isFresh(entry, fetchedAt + Duration.ofDays(7).plusSeconds(1))).isFalse()
        // 30 days later — stale.
        assertThat(LicenseCache.isFresh(entry, fetchedAt + Duration.ofDays(30))).isFalse()
    }

    @Test
    fun `write creates the parent directory if missing`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("nested/dir/cache.json").toString()
        val entry = entryAt(Instant.parse("2026-06-28T00:00:00Z"))
        LicenseCache.write(entry, cachePath)
        assertThat(LicenseCache.read(cachePath)).isEqualTo(entry)
    }

    @Test
    fun `write leaves no tmp files behind on success`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("license-cache.json").toString()
        val entry = entryAt(Instant.parse("2026-06-28T00:00:00Z"))
        LicenseCache.write(entry, cachePath)
        val siblings = tmp.toFile().listFiles()?.map { it.name } ?: emptyList()
        assertThat(siblings).containsExactly("license-cache.json")
    }

    private fun entryAt(fetchedAt: Instant) = LicenseCacheEntry(
        fetchedAt = fetchedAt,
        licenseKeyFingerprint = "abcd1234",
        entitlements = listOf("audit.export.iti20"),
        tierName = "pro",
        expiresAt = "2027-06-28T00:00:00Z",
        signature = "compact.jws.here",
        signatureKid = "license-key-dev",
    )
}
