package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import org.hl7.fhir.r4.model.AuditEvent

/**
 * Side-effecting persistence side of the audit-event-fhir plugin.
 *
 * Pulled out of [AuditEventFhirEnricher] because the SPI surface
 * ([com.bzonfhir.subscriptionservice.spi.AuditEventEnricher]) is
 * **pure** — it shapes the `AuditEvent` but does not persist it.
 * Persistence is the runtime adapter's responsibility.
 *
 * In the hapi-auth runtime (where today's `AuditEventInterceptor` lives),
 * the persister is HAPI's `DaoRegistry`. Outside the HAPI process (e.g.
 * if the interface-engine ever needs to emit AuditEvents directly), the
 * persister would be a `RestTemplate`-based push to a configured FHIR
 * server. The interface stays the same.
 *
 * ## Failure handling
 *
 * Per the SPI contract: "AuditEvent emission is best-effort — failing
 * the underlying request because we couldn't persist the audit row would
 * be worse than missing it." All implementations MUST catch + log
 * internally; never propagate an exception.
 */
fun interface AuditEventEmitter {

    /**
     * Persist a freshly-built [AuditEvent]. Implementations swallow
     * failures (logging them) — see interface kdoc.
     */
    fun persist(event: AuditEvent)
}
