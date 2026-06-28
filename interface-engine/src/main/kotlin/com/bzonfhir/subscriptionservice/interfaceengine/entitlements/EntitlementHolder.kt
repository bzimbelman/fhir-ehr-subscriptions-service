package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties
import org.slf4j.LoggerFactory
import java.security.MessageDigest
import java.time.Duration
import java.time.Instant
import java.util.concurrent.atomic.AtomicReference

/**
 * The JVM-wide authoritative entitlement set (ticket #460, Epic #428).
 *
 * One bean per Spring context. Holds an immutable [EntitlementSet] that the
 * AOP guard (`EntitlementGuardAspect`) reads on every annotated call. The
 * holder:
 *
 *   1. On boot, calls [initialFetch]. If the license is enabled AND a key
 *      is set, hits the license server via [fetcher]; otherwise serves
 *      [EntitlementSet.EMPTY].
 *   2. On every refresh tick (every `subscription-service.license.refresh-interval-hours`
 *      hours), calls [refresh].
 *
 * Failure-mode tree from the ticket spec:
 *
 *   - `enabled=false` OR empty key       -> EMPTY, no network call.
 *   - server reachable, response valid   -> active set + cache write.
 *   - server unreachable, cache fresh    -> cached set + WARN.
 *   - server unreachable, cache stale    -> EMPTY + WARN.
 *   - server returned 401 (revoked)      -> EMPTY + ERROR, cache cleared.
 *
 * The holder is intentionally functional in its dep on the client: it
 * receives a `(licenseKey: String) -> VerifiedLicenseResponse` rather than
 * the concrete LicenseClient. This (a) keeps the unit tests pure JVM
 * (no HTTP) and (b) lets the Spring config build the LicenseClient with the
 * server URL and pass `client::fetchEntitlements` here.
 */
class EntitlementHolder(
    private val properties: LicenseProperties,
    private val fetcher: (String) -> VerifiedLicenseResponse,
    private val clock: () -> Instant = Instant::now,
) {
    private val log = LoggerFactory.getLogger(EntitlementHolder::class.java)

    private val active = AtomicReference(EntitlementSet.EMPTY)

    /** The current authoritative set. Reads are wait-free. */
    fun current(): EntitlementSet = active.get()

    /** Called on `@PostConstruct` (or by tests directly). Same logic as a refresh tick. */
    fun initialFetch() {
        runFetch(initial = true)
    }

    /** Called on every refresh tick. */
    fun refresh() {
        runFetch(initial = false)
    }

    private fun runFetch(initial: Boolean) {
        if (!properties.enabled) {
            if (initial) log.info("entitlement runtime disabled — running in FOSS mode (no commercial features)")
            active.set(EntitlementSet.EMPTY)
            return
        }
        val key = properties.key
        if (key.isBlank()) {
            if (initial) log.info("no LICENSE_KEY configured — running in FOSS mode (no commercial features)")
            active.set(EntitlementSet.EMPTY)
            return
        }
        val fingerprint = fingerprintLicenseKey(key)

        val verified: VerifiedLicenseResponse = try {
            fetcher(key)
        } catch (e: LicenseRevokedException) {
            // 401 from the server: the key has been revoked or is invalid.
            // Per the ticket spec: instant FOSS + ERROR. We do NOT consult
            // the cache — a revoked license must not be honoured even for
            // an entitlement window we previously cached.
            log.error(
                "license server rejected key ({}) — degrading to FOSS mode: {}",
                fingerprint,
                e.message,
            )
            active.set(EntitlementSet.EMPTY)
            return
        } catch (e: LicenseClientException) {
            // Anything else (network failure, malformed body, signature
            // mismatch) is treated as "unreachable". The UI follows the same
            // pattern (see `loadLicenseState` in licenseClient.ts).
            log.warn(
                "license server unreachable for key {} — checking cache: {}",
                fingerprint,
                e.message,
            )
            fallBackToCache(fingerprint)
            return
        } catch (e: Exception) {
            log.warn(
                "unexpected error fetching entitlements for key {} — checking cache: {}",
                fingerprint,
                e.message,
            )
            fallBackToCache(fingerprint)
            return
        }

        // Success path: publish + write the cache.
        val set = EntitlementSet.of(verified.entitlements)
        active.set(set)
        try {
            LicenseCache.write(
                LicenseCacheEntry(
                    fetchedAt = clock(),
                    licenseKeyFingerprint = fingerprint,
                    entitlements = verified.entitlements,
                    tierName = verified.tierName,
                    expiresAt = verified.expiresAt,
                    signature = verified.signature,
                    signatureKid = verified.signatureKid,
                ),
                properties.cachePath,
            )
        } catch (e: Exception) {
            // Failure to persist the cache must NOT take down the active set.
            log.warn("failed to persist license cache: {}", e.message)
        }
        log.info(
            "license entitlement set refreshed: {} entitlements (tier={}, kid={}, key={})",
            set.size, verified.tierName, verified.signatureKid, fingerprint,
        )
    }

    private fun fallBackToCache(fingerprint: String) {
        val cached = LicenseCache.read(properties.cachePath)
        if (cached == null) {
            log.warn("no on-disk license cache — running in FOSS mode (key={})", fingerprint)
            active.set(EntitlementSet.EMPTY)
            return
        }
        val ttl = Duration.ofDays(properties.cacheTtlDays)
        val now = clock()
        val age = Duration.between(cached.fetchedAt, now)
        if (age >= ttl) {
            log.warn(
                "license cache is stale ({} >= {}) — running in FOSS mode (key={})",
                age, ttl, fingerprint,
            )
            active.set(EntitlementSet.EMPTY)
            return
        }
        log.warn(
            "license server unreachable but cache is fresh (age={}); using {} cached entitlements (key={})",
            age, cached.entitlements.size, fingerprint,
        )
        active.set(EntitlementSet.of(cached.entitlements))
    }

    private fun fingerprintLicenseKey(licenseKey: String): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(licenseKey.toByteArray())
        return digest.joinToString("") { "%02x".format(it) }.substring(0, 8)
    }
}
