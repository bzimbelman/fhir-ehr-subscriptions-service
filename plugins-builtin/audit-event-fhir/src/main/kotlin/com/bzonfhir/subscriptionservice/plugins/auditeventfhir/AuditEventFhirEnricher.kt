package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.spi.AuditEventEnricher
import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import com.bzonfhir.subscriptionservice.spi.meta.AuditEnrichmentRule
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAgentComponent
import org.hl7.fhir.r4.model.Reference

/**
 * First-party built-in plugin (ticket #432, Epic #425).
 *
 * This is the SPI-shaped re-expression of the audit-emission logic that
 * historically lived inside `AuditEventInterceptor.buildEvent()` in the
 * `hapi/auth/` module. The hapi-auth interceptor still owns the HAPI
 * pointcut wiring (`@Hook(Pointcut.SERVER_OUTGOING_RESPONSE)`), but the
 * AuditEvent **shape** — `type`, `subtype`, `action`, `agent`, `entity`,
 * `source`, `period`, `outcome` — is produced here and here only.
 *
 * ## SPI contract
 *
 * `enrich(ctx, baseEvent)` is a pure function: read [AuditContext] +
 * mutate `baseEvent` via HAPI R4 setters + return it. No I/O, no
 * persistence. The persistence side lives in [AuditEventEmitter].
 *
 * Two overloads exist:
 *
 *  - `enrich(ctx, baseEvent)` — the SPI surface. Applies the standard
 *    AuditEvent shape (no vendor rules).
 *  - `enrich(ctx, baseEvent, rules)` — additionally applies vendor-supplied
 *    [AuditEnrichmentRule]s, which a future Epic-profile plugin will
 *    surface via [com.bzonfhir.subscriptionservice.spi.HL7VendorProfile.auditEnrichments].
 *    Today the rules are interpreted in-process; once a real vendor plugin
 *    exists, the runtime will pass them through this method.
 *
 * Behavior is identical to the legacy `AuditEventInterceptor` (verified
 * by both this module's tests and the `hapi/auth/` Java tests).
 */
class AuditEventFhirEnricher : AuditEventEnricher {

    override val meta: PluginMeta = PluginMeta(
        id = "audit-event-fhir",
        version = "0.1.0",
        schemaVersion = 1,
        supplier = PluginSupplier.FIRST_PARTY,
        description = "Built-in FHIR AuditEvent enricher — emits the standard " +
            "DICOM/FHIR AuditEvent shape for every audited HAPI operation.",
    )

    override fun enrich(ctx: AuditContext, baseEvent: AuditEvent): AuditEvent =
        enrich(ctx, baseEvent, rules = emptyList())

    /**
     * Variant that additionally applies vendor-supplied enrichment rules.
     *
     * Each [AuditEnrichmentRule] is matched by `field`:
     *
     *  - `addOriginatingUser` — read
     *    `ctx.attributes["enrichment.originatingUser"]` and add it as
     *    an extra `agent.who = Practitioner/<value>` slot. The
     *    HL7VendorProfile is responsible for resolving the rule's
     *    `source` (e.g. `"pv1.7"`) into that attribute upstream.
     *
     * Unknown rule keys are silently ignored — the SPI contract says
     * enrichers must be defensive: a profile that declares a rule the
     * built-in enricher doesn't know shouldn't fail; the rule simply
     * has no effect.
     */
    fun enrich(
        ctx: AuditContext,
        baseEvent: AuditEvent,
        rules: List<AuditEnrichmentRule>,
    ): AuditEvent {
        applyBaseShape(baseEvent, ctx)
        applyVendorRules(baseEvent, ctx, rules)
        return baseEvent
    }

    private fun applyVendorRules(
        event: AuditEvent,
        ctx: AuditContext,
        rules: List<AuditEnrichmentRule>,
    ) {
        for (rule in rules) {
            when (rule.field) {
                "addOriginatingUser" -> {
                    val value = ctx.attributes["enrichment.originatingUser"]
                    if (!value.isNullOrBlank()) {
                        event.addAgent(
                            AuditEventAgentComponent().apply {
                                requestor = false
                                who = Reference("Practitioner/$value")
                            },
                        )
                    }
                }
                // Other rule keys (addPatientFacility, addPracticeId, ...)
                // will be implemented when concrete vendor profiles land.
                // For now, unknown keys no-op — see method javadoc.
                else -> Unit
            }
        }
    }
}
