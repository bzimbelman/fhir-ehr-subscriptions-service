package com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config

import org.springframework.boot.context.properties.ConfigurationProperties

/**
 * Operator-facing configuration for the entitlement runtime
 * (ticket #460, Epic #428).
 *
 * Bound from `application.yaml` under `subscription-service.license`.
 * Example:
 *
 * ```yaml
 * subscription-service:
 *   license:
 *     enabled: true
 *     key: "${LICENSE_KEY:}"
 *     server-url: "${LICENSE_SERVER_URL:https://license.bzonfhir.com}"
 *     cache-path: "${user.home}/.subscription-service/license-cache.json"
 *     cache-ttl-days: 7
 *     refresh-interval-hours: 12
 *     fail-mode: deny
 * ```
 *
 * Default values are FOSS-safe: the `key` field defaults to empty, which
 * disables every commercial gate without making any network call. Operators
 * with a license set `LICENSE_KEY` and (optionally) override the server URL.
 */
@ConfigurationProperties(prefix = "subscription-service.license")
data class LicenseProperties(

    /**
     * Master toggle. When `false`, [enabled] is treated as if there were no
     * license key: the holder skips all network calls, the entitlement set
     * is empty, and the guard aspect lets every annotated method through.
     *
     * Used by tests + dev environments to short-circuit the gate without
     * unsetting the env var. Default `true` so production behaviour is the
     * documented behaviour.
     */
    val enabled: Boolean = true,

    /**
     * The license key. Empty string -> FOSS mode (no network calls, empty
     * entitlement set, all commercial features off).
     *
     * Operators inject this via `LICENSE_KEY` env var. Defaulting to empty
     * matches the UI's behaviour (`ui/src/lib/license/licenseClient.ts`
     * reads `process.env.LICENSE_KEY`).
     */
    val key: String = "",

    /**
     * License-server base URL. Defaults to the public hostname; CI overrides
     * to a MockWebServer instance.
     */
    val serverUrl: String = "https://license.bzonfhir.com",

    /**
     * On-disk cache path. The UI uses
     * `~/.subscription-service/license-cache.json`; we use the same path so
     * the two processes coexist on a single dev box without stomping on each
     * other (the contents are not shared but the file shape is compatible).
     */
    val cachePath: String = "${System.getProperty("user.home")}/.subscription-service/license-cache.json",

    /**
     * Cache TTL in days. Exposed as a knob even though [LicenseCache.TTL]
     * is the implementation default — tests use 0 to force "always stale".
     */
    val cacheTtlDays: Long = 7,

    /**
     * Refresh interval. Spring's @Scheduled honours the `fixedRateString`
     * property so we expose hours-as-Long.
     */
    val refreshIntervalHours: Long = 12,

    /**
     * Failure mode for [com.bzonfhir.subscriptionservice.interfaceengine.entitlements.RequiresEntitlement]
     * when the active set is missing the required entitlement.
     *
     * - `DENY` (default): throw `EntitlementMissingException`. Production.
     * - `LOG`: log a WARN and let the method body run. Dev-only escape
     *   hatch for "I forgot to set LICENSE_KEY but want to test the
     *   commercial path locally" — NEVER use in production.
     */
    val failMode: FailMode = FailMode.DENY,
) {
    enum class FailMode { DENY, LOG }
}
