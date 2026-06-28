package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir
import java.nio.file.Path
import java.time.Instant

/**
 * EntitlementHolder unit tests (ticket #460, Epic #428).
 *
 * The holder is the JVM-wide authoritative set. These tests exercise the
 * full failure-mode tree from the ticket spec:
 *
 *   - Empty key      -> FOSS, no network call.
 *   - Key + success  -> active set + cache write.
 *   - Key + failure + fresh cache -> cached set + WARN.
 *   - Key + failure + stale cache -> empty set + WARN.
 *   - Key + LicenseRevokedException -> empty set + ERROR.
 *
 * The LicenseClient is stubbed; no HTTP is made. This keeps the test
 * sub-second and free of network flake.
 */
class EntitlementHolderTest {

    class StubLicenseClient(
        var response: VerifiedLicenseResponse? = null,
        var throwOnFetch: RuntimeException? = null,
        var callCount: Int = 0,
    ) {
        fun asClient(): TestLicenseClientShim = TestLicenseClientShim(this)
    }

    /**
     * Tiny shim that exposes the `fetchEntitlements(licenseKey)` contract the
     * holder relies on. We don't subclass [LicenseClient] (it's a concrete
     * class with HTTP state); instead, the holder takes a functional
     * `(String) -> VerifiedLicenseResponse` so tests can swap it.
     */
    class TestLicenseClientShim(private val stub: StubLicenseClient) {
        fun fetch(licenseKey: String): VerifiedLicenseResponse {
            stub.callCount++
            stub.throwOnFetch?.let { throw it }
            return stub.response
                ?: throw IllegalStateException("test setup error: no stub response configured")
        }
    }

    private fun props(
        enabled: Boolean = true,
        key: String = "license-abc",
        cachePath: String,
    ) = LicenseProperties(
        enabled = enabled,
        key = key,
        cachePath = cachePath,
    )

    @Test
    fun `disabled holder serves empty set without calling the client`(@TempDir tmp: Path) {
        val stub = StubLicenseClient()
        val holder = EntitlementHolder(
            properties = props(enabled = false, cachePath = tmp.resolve("c.json").toString()),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()
        assertThat(holder.current().isEmpty()).isTrue()
        assertThat(stub.callCount).isEqualTo(0)
    }

    @Test
    fun `empty key serves empty set without calling the client`(@TempDir tmp: Path) {
        val stub = StubLicenseClient()
        val holder = EntitlementHolder(
            properties = props(key = "", cachePath = tmp.resolve("c.json").toString()),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()
        assertThat(holder.current().isEmpty()).isTrue()
        assertThat(stub.callCount).isEqualTo(0)
    }

    @Test
    fun `successful fetch publishes the active set and writes the cache`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("c.json").toString()
        val stub = StubLicenseClient(
            response = VerifiedLicenseResponse(
                entitlements = listOf("audit.export.iti20", "alerting.pagerduty"),
                tierName = "pro",
                expiresAt = "2027-06-28T00:00:00Z",
                signature = "compact.jws.here",
                signatureKid = "kid-1",
            )
        )
        val holder = EntitlementHolder(
            properties = props(cachePath = cachePath),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()

        assertThat(holder.current().has("audit.export.iti20")).isTrue()
        assertThat(holder.current().has("alerting.pagerduty")).isTrue()
        assertThat(holder.current().has("missing.feature")).isFalse()

        val cached = LicenseCache.read(cachePath)!!
        assertThat(cached.entitlements).containsExactlyInAnyOrder("audit.export.iti20", "alerting.pagerduty")
        assertThat(cached.signatureKid).isEqualTo("kid-1")
    }

    @Test
    fun `unreachable server with fresh cache falls back to cached entitlements`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("c.json").toString()
        val fetchedAt = Instant.parse("2026-06-27T00:00:00Z")
        // Seed a fresh cache (1 day old, well within the 7d TTL).
        LicenseCache.write(
            LicenseCacheEntry(
                fetchedAt = fetchedAt,
                licenseKeyFingerprint = "abcd1234",
                entitlements = listOf("audit.export.iti20"),
                tierName = "pro",
                expiresAt = "2027-06-28T00:00:00Z",
                signature = "compact.jws.here",
                signatureKid = "kid-1",
            ),
            cachePath,
        )
        val stub = StubLicenseClient(throwOnFetch = LicenseClientException("connection refused"))
        val holder = EntitlementHolder(
            properties = props(cachePath = cachePath),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()

        assertThat(holder.current().has("audit.export.iti20")).isTrue()
    }

    @Test
    fun `unreachable server with stale cache degrades to FOSS`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("c.json").toString()
        val fetchedAt = Instant.parse("2026-06-01T00:00:00Z")  // 27 days old
        LicenseCache.write(
            LicenseCacheEntry(
                fetchedAt = fetchedAt,
                licenseKeyFingerprint = "abcd1234",
                entitlements = listOf("audit.export.iti20"),
                tierName = "pro",
                expiresAt = "2027-06-28T00:00:00Z",
                signature = "compact.jws.here",
                signatureKid = "kid-1",
            ),
            cachePath,
        )
        val stub = StubLicenseClient(throwOnFetch = LicenseClientException("connection refused"))
        val holder = EntitlementHolder(
            properties = props(cachePath = cachePath),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()

        assertThat(holder.current().isEmpty()).isTrue()
    }

    @Test
    fun `revoked license degrades to FOSS instantly even with fresh cache`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("c.json").toString()
        // Seed a fresh cache — must be IGNORED on revocation.
        LicenseCache.write(
            LicenseCacheEntry(
                fetchedAt = Instant.parse("2026-06-27T00:00:00Z"),
                licenseKeyFingerprint = "abcd1234",
                entitlements = listOf("audit.export.iti20"),
                tierName = "pro",
                expiresAt = "2027-06-28T00:00:00Z",
                signature = "compact.jws.here",
                signatureKid = "kid-1",
            ),
            cachePath,
        )
        val stub = StubLicenseClient(throwOnFetch = LicenseRevokedException("revoked"))
        val holder = EntitlementHolder(
            properties = props(cachePath = cachePath),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()

        assertThat(holder.current().isEmpty()).isTrue()
    }

    @Test
    fun `refresh re-runs the fetch and overwrites the active set`(@TempDir tmp: Path) {
        val cachePath = tmp.resolve("c.json").toString()
        val stub = StubLicenseClient(
            response = VerifiedLicenseResponse(
                entitlements = listOf("audit.export.iti20"),
                tierName = "pro",
                expiresAt = "2027-06-28T00:00:00Z",
                signature = "sig1",
                signatureKid = "kid-1",
            )
        )
        val holder = EntitlementHolder(
            properties = props(cachePath = cachePath),
            fetcher = stub.asClient()::fetch,
            clock = { Instant.parse("2026-06-28T00:00:00Z") },
        )
        holder.initialFetch()
        assertThat(holder.current().toSortedList()).containsExactly("audit.export.iti20")

        // Server now grants a new entitlement. The next refresh sees it.
        stub.response = VerifiedLicenseResponse(
            entitlements = listOf("audit.export.iti20", "alerting.pagerduty"),
            tierName = "pro",
            expiresAt = "2027-06-28T00:00:00Z",
            signature = "sig2",
            signatureKid = "kid-1",
        )
        holder.refresh()
        assertThat(holder.current().toSortedList())
            .containsExactly("alerting.pagerduty", "audit.export.iti20")
        assertThat(stub.callCount).isEqualTo(2)
    }
}
