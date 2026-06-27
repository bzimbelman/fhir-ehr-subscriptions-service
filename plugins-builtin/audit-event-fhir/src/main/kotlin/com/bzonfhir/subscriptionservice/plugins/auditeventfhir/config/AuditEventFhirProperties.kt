package com.bzonfhir.subscriptionservice.plugins.auditeventfhir.config

import org.springframework.boot.context.properties.ConfigurationProperties

/**
 * Configuration knobs for the audit-event-fhir built-in plugin (ticket
 * #432, Epic #425). Identical key surface to the legacy
 * `com.bzonfhir.subscriptionservice.audit.AuditProperties` in `hapi/auth/`,
 * because the plugin re-expresses that interceptor.
 *
 * Bound from `application.yaml` under the prefix
 * `subscription-service.audit`. Example:
 *
 * ```yaml
 * subscription-service:
 *   audit:
 *     enabled: true
 *     capture-reads: false
 *     capture-search: false
 *     retention-days: 365
 * ```
 *
 * Two configuration-properties beans CAN exist on the same prefix when
 * the legacy Java [com.bzonfhir.subscriptionservice.audit.AuditProperties]
 * and this Kotlin variant run side-by-side — Spring binds the same YAML
 * values into both. That's intentional during the in-tree -> plugin
 * migration: the hapi/auth interceptor continues to read the Java bean,
 * while a future Gradle-based hapi/auth (or any other consumer of the
 * plugin) reads this Kotlin one. Once hapi/auth migrates to Gradle, the
 * Java variant is deleted and this becomes the sole owner.
 */
@ConfigurationProperties(prefix = "subscription-service.audit")
data class AuditEventFhirProperties(
    /**
     * Master toggle. `true` by default; flip off only for dev clusters
     * where audit volume would obscure log debugging.
     */
    var enabled: Boolean = true,

    /**
     * If `true`, also produce AuditEvents for read operations. Default
     * `false` because reads dwarf writes in typical FHIR workloads.
     */
    var captureReads: Boolean = false,

    /**
     * If `true`, also produce AuditEvents for type-level search
     * operations. Default `false` — same volume rationale.
     */
    var captureSearch: Boolean = false,

    /**
     * Informational retention horizon (days). Not enforced by this
     * plugin — purging old AuditEvents is a separate scheduled job that
     * has not yet been implemented. Documented here so the value lives
     * next to the other audit knobs.
     */
    var retentionDays: Long = 365L,
)
