package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import org.hl7.fhir.r4.model.AuditEvent

/**
 * SPI #7b — AuditEvent enricher.
 *
 * The runtime builds a base `AuditEvent` for every interesting HAPI
 * REST operation (see `hapi/auth/.../audit/AuditEventInterceptor.java`).
 * That base event covers WHO (principal), WHAT (resource type + id),
 * WHEN (timestamp), and OUTCOME (success / failure code).
 *
 * Customers regularly need MORE on those events:
 *
 *  - Vendor-aware fields: when an Epic ADT triggered the write, stamp
 *    the originating Epic user (PV1-7) onto `AuditEvent.agent`. See
 *    master plan §4.3.
 *  - Compliance-aware fields: regulator-mandated `purposeOfUse`,
 *    `dataLifecycle`, or breach-notification context.
 *  - Operational fields: ticket id from the customer's change-management
 *    system, link to their incident-management tool.
 *
 * The base `AuditEventInterceptor` walks every registered
 * [AuditEventEnricher] before persisting the event. Enrichers mutate
 * the in-progress `AuditEvent` via the HAPI R4 model setters.
 *
 * # Composition
 *
 * Enrichers are NOT exclusive — every registered enricher gets a turn
 * on every event. Two enrichers writing to overlapping slots produces
 * a last-writer-wins outcome; the runtime does NOT mediate conflicts.
 * In practice this is rare: enrichers stamp orthogonal fields.
 *
 * # Error handling
 *
 * If an enricher throws, the runtime LOGS and SWALLOWS the exception,
 * persists the AuditEvent without that enricher's contribution, and
 * continues. AuditEvent emission is best-effort — failing the
 * underlying request because we couldn't enrich the audit row would
 * be worse than missing an enrichment.
 *
 * # Stability: EXPERIMENTAL
 */
interface AuditEventEnricher {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Enrich [baseEvent] in place using context from [ctx], and return
     * it (typically the same instance). Returning a different
     * `AuditEvent` instance is supported but unusual; the runtime
     * persists whichever instance the LAST enricher returns.
     *
     * Implementations should be defensive — [ctx] fields may be null
     * (anonymous request, no resource id when listing) and the
     * baseEvent may already have agents / entities / sources added by
     * prior enrichers.
     *
     * @param ctx Normalized request context for the audited operation.
     * @param baseEvent The in-progress AuditEvent. Mutate via HAPI R4
     *   setters (`addAgent`, `addEntity`, `getSource().setObserver(...)`).
     * @return The (possibly same, possibly new) AuditEvent to persist.
     */
    fun enrich(ctx: AuditContext, baseEvent: AuditEvent): AuditEvent
}
