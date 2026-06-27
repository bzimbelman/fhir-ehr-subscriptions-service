package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.Reference
import org.junit.jupiter.api.Test
import java.time.Instant

class AuditEventEnricherContractTest {

    @Test
    fun `AuditEventEnricher shape compiles and mutates the in-progress AuditEvent`() {
        val enricher: AuditEventEnricher = object : AuditEventEnricher {
            override val meta = PluginMeta(
                id = "test-epic-audit",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.FIRST_PARTY,
                description = "Test stub Epic audit enricher",
            )

            override fun enrich(ctx: AuditContext, baseEvent: AuditEvent): AuditEvent {
                // Stamp originating user as a new agent. A real Epic enricher
                // would walk ctx.sourceMessage.attributes for the PV1-7 value.
                baseEvent.addAgent(
                    AuditEvent.AuditEventAgentComponent().apply {
                        who = Reference("Practitioner/${ctx.attributes["originatingUser"] ?: "unknown"}")
                        requestor = false
                    },
                )
                return baseEvent
            }
        }

        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-1",
            tenantId = "tenantA",
            principalName = "alice",
            requestPath = "/fhir/Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = "123",
            attributes = mapOf("originatingUser" to "epic-user-42"),
        )

        val baseEvent = AuditEvent()
        val enriched = enricher.enrich(ctx, baseEvent)

        assertThat(enriched.agent).hasSize(1)
        assertThat(enriched.agent[0].who.reference).isEqualTo("Practitioner/epic-user-42")
    }
}
