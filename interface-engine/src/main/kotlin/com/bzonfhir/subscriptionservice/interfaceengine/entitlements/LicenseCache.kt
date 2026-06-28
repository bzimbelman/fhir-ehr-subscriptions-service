package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.fasterxml.jackson.annotation.JsonCreator
import com.fasterxml.jackson.annotation.JsonInclude
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.DeserializationFeature
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.databind.SerializationFeature
import com.fasterxml.jackson.datatype.jsr310.JavaTimeModule
import org.slf4j.LoggerFactory
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.time.Duration
import java.time.Instant

/**
 * On-disk license cache with a 7-day TTL (ticket #460, Epic #428).
 *
 * JVM mirror of `ui/src/lib/license/licenseCache.ts`. Why disk? A
 * license-server outage MUST NOT degrade the engine all the way to FOSS the
 * moment it starts up if it has a fresh cached set on disk. The engine reads
 * the cache at startup AND on every refresh tick (every 12h by default); a
 * transient outage degrades to "stale-active" rather than "FOSS". After 7
 * days without a successful fetch we give up and fall back to FOSS so a
 * permanently-misconfigured server doesn't pretend forever.
 *
 * Path: `~/.subscription-service/license-cache.json` by default; the
 * EntitlementHolder reads `subscription-service.license.cache-path` from
 * config and passes it down.
 *
 * Atomicity: writes go to a `<path>.tmp-<pid>-<ts>` sibling first, then
 * `Files.move(REPLACE_EXISTING, ATOMIC_MOVE)`. A partial write can never
 * leave the cache corrupt — a reader either sees the old file or the new
 * file, never an in-flight truncation.
 *
 * Stateless: this object is a pair of pure functions over a path. All
 * Spring-managed lifecycle lives in EntitlementHolder.
 */
object LicenseCache {

    /** Cache freshness window. Matches the UI's `CACHE_TTL_MS = 7 * 24 * 60 * 60 * 1000`. */
    val TTL: Duration = Duration.ofDays(7)

    private val log = LoggerFactory.getLogger(LicenseCache::class.java)

    private val mapper: ObjectMapper = ObjectMapper()
        .registerModule(JavaTimeModule())
        // Instant -> "2026-06-28T00:00:00Z" rather than 1_751_068_800 — match the UI's JSON shape.
        .disable(SerializationFeature.WRITE_DATES_AS_TIMESTAMPS)
        // An older cache from a prior epic might have fewer fields; ignore them rather than crash boot.
        .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)
        .setSerializationInclusion(JsonInclude.Include.NON_NULL)

    /** Read the cache. Returns `null` on missing file, IO error, or malformed JSON. */
    fun read(cachePath: String): LicenseCacheEntry? {
        val path = Path.of(cachePath)
        if (!Files.exists(path)) return null
        return try {
            val bytes = Files.readAllBytes(path)
            mapper.readValue(bytes, LicenseCacheEntry::class.java)
        } catch (e: Exception) {
            // A corrupt cache shouldn't crash boot. Log + treat as missing.
            log.warn("failed to read license cache at {} — treating as absent: {}", cachePath, e.message)
            null
        }
    }

    /** Write the cache atomically. Creates the parent directory if necessary. */
    fun write(entry: LicenseCacheEntry, cachePath: String) {
        val path = Path.of(cachePath)
        val parent = path.parent
        if (parent != null) Files.createDirectories(parent)
        val tmp = path.resolveSibling("${path.fileName}.tmp-${ProcessHandle.current().pid()}-${System.currentTimeMillis()}")
        Files.write(tmp, mapper.writerWithDefaultPrettyPrinter().writeValueAsBytes(entry))
        // ATOMIC_MOVE so a reader can't observe a half-written file.
        Files.move(tmp, path, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
    }

    /** True iff `fetchedAt` is within [TTL] of [now]. */
    fun isFresh(entry: LicenseCacheEntry, now: Instant = Instant.now()): Boolean {
        return Duration.between(entry.fetchedAt, now) < TTL
    }
}

/**
 * On-disk cache entry. Mirrors the UI's `LicenseCacheEntry` shape so that
 * future tooling (e.g. a "view cache" admin endpoint) can read the same
 * JSON on both sides.
 */
data class LicenseCacheEntry @JsonCreator constructor(
    /** When the response was fetched. ISO-8601 in the on-disk JSON. */
    @param:JsonProperty("fetchedAt") val fetchedAt: Instant,
    /** First 8 chars of sha256(licenseKey). Safe to log. */
    @param:JsonProperty("licenseKeyFingerprint") val licenseKeyFingerprint: String,
    /** The verified entitlement strings. */
    @param:JsonProperty("entitlements") val entitlements: List<String>,
    /** Tier name from the server. */
    @param:JsonProperty("tierName") val tierName: String,
    /** License-expiry timestamp (ISO-8601 string as the server sent it). */
    @param:JsonProperty("expiresAt") val expiresAt: String,
    /** Compact JWS of the entitlement body. */
    @param:JsonProperty("signature") val signature: String,
    /** Kid of the JWK that verified this entry. */
    @param:JsonProperty("signatureKid") val signatureKid: String? = null,
)
