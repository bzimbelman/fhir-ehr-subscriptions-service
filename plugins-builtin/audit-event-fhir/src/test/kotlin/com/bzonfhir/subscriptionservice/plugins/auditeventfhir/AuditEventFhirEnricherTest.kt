package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import com.bzonfhir.subscriptionservice.spi.meta.AuditEnrichmentRule
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome
import org.junit.jupiter.api.Test
import java.time.Instant

/**
 * SPI-contract tests for [AuditEventFhirEnricher] (ticket #432, Epic #425).
 *
 * The enricher is a pure function: take an [AuditContext] (and an in-progress
 * `AuditEvent` from the runtime, which here we hand in fresh) and return an
 * `AuditEvent` populated with the same shape the original
 * `AuditEventInterceptor.buildEvent()` produced.
 *
 * Side effects (HAPI persistence) live in `AuditEventEmitter`; those are
 * exercised by [AuditEventEmitterIntegrationTest], not here.
 */
class AuditEventFhirEnricherTest {

    private val enricher = AuditEventFhirEnricher()

    @Test
    fun `meta exposes plugin id, schema version, and first-party supplier`() {
        val meta = enricher.meta
        assertThat(meta.id).isEqualTo("audit-event-fhir")
        assertThat(meta.schemaVersion).isEqualTo(1)
        assertThat(meta.supplier).isEqualTo(PluginSupplier.FIRST_PARTY)
    }

    @Test
    fun `enrich on a CREATE Patient produces type=rest, action=C, agent=epic, entity=PatientResource`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-1",
            tenantId = null,
            principalName = "svc-acc",
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = "123",
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test-server/fhir",
                "agentSystem" to "epic",
                "azp" to "smart-app",
                "responseStatus" to "201",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        // DICOM "rest" subtype + Restful Operation type.
        assertThat(enriched.type.system).isEqualTo("http://dicom.nema.org/resources/ontology/dcm")
        assertThat(enriched.type.code).isEqualTo("rest")

        // action = C
        assertThat(enriched.action).isEqualTo(AuditEventAction.C)

        // outcome = 0 (success)
        assertThat(enriched.outcome).isEqualTo(AuditEventOutcome._0)

        // recorded populated
        assertThat(enriched.recorded).isNotNull()

        // agent: user (svc-acc) AND client app (smart-app) — two agents.
        assertThat(enriched.agent).hasSize(2)
        assertThat(enriched.agent[0].altId).isEqualTo("svc-acc")
        assertThat(enriched.agent[0].requestor).isTrue()
        assertThat(enriched.agent[1].altId).isEqualTo("smart-app")
        assertThat(enriched.agent[1].requestor).isFalse()

        // entity.what = Patient/123 (Reference target).
        assertThat(enriched.entity).hasSize(1)
        assertThat(enriched.entity[0].what.reference).isEqualTo("Patient/123")

        // source.site = the FHIR base URL.
        assertThat(enriched.source.site).isEqualTo("http://test-server/fhir")
    }

    @Test
    fun `enrich on a DELETE produces action=D and the entity reference`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-2",
            tenantId = null,
            principalName = "user-1",
            requestPath = "Patient/42",
            requestMethod = "DELETE",
            resourceType = "Patient",
            resourceId = "42",
            attributes = mapOf(
                "operation" to "DELETE",
                "fhirServerBase" to "http://test-server/fhir",
                "responseStatus" to "204",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.action).isEqualTo(AuditEventAction.D)
        assertThat(enriched.outcome).isEqualTo(AuditEventOutcome._0)
        assertThat(enriched.entity[0].what.reference).isEqualTo("Patient/42")
    }

    @Test
    fun `enrich on a 401 failure produces outcome=8 and an anonymous agent`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-3",
            tenantId = null,
            principalName = null, // unauthenticated
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = null,
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test-server/fhir",
                "exception.status" to "401",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.outcome).isEqualTo(AuditEventOutcome._8)
        assertThat(enriched.agent).hasSize(1)
        assertThat(enriched.agent[0].altId).isEqualTo("anonymous")
    }

    @Test
    fun `enrich on a 403 failure produces outcome=4 and preserves the identified agent`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-4",
            tenantId = null,
            principalName = "user-5",
            requestPath = "Patient/5",
            requestMethod = "GET",
            resourceType = "Patient",
            resourceId = "5",
            attributes = mapOf(
                "operation" to "READ",
                "fhirServerBase" to "http://test-server/fhir",
                "exception.status" to "403",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.outcome).isEqualTo(AuditEventOutcome._4)
        assertThat(enriched.agent).hasSize(1)
        assertThat(enriched.agent[0].altId).isEqualTo("user-5")
    }

    @Test
    fun `READ produces action=R`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-5",
            tenantId = null,
            principalName = "user-3",
            requestPath = "Patient/42",
            requestMethod = "GET",
            resourceType = "Patient",
            resourceId = "42",
            attributes = mapOf(
                "operation" to "READ",
                "fhirServerBase" to "http://test-server/fhir",
                "responseStatus" to "200",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.action).isEqualTo(AuditEventAction.R)
    }

    @Test
    fun `UPDATE produces action=U`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-6",
            tenantId = null,
            principalName = "user-2",
            requestPath = "Patient/42",
            requestMethod = "PUT",
            resourceType = "Patient",
            resourceId = "42",
            attributes = mapOf(
                "operation" to "UPDATE",
                "fhirServerBase" to "http://test-server/fhir",
                "responseStatus" to "200",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.action).isEqualTo(AuditEventAction.U)
        assertThat(enriched.entity[0].what.reference).isEqualTo("Patient/42")
    }

    @Test
    fun `period start and end are populated from occurredAt and request-end`() {
        val ctx = AuditContext(
            occurredAt = Instant.parse("2026-01-15T10:00:00Z"),
            correlationId = "cid-7",
            tenantId = null,
            principalName = "user-1",
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = null,
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test-server/fhir",
                "responseStatus" to "201",
            ),
        )

        val enriched = enricher.enrich(ctx, AuditEvent())

        assertThat(enriched.period.hasStart()).isTrue()
        assertThat(enriched.period.hasEnd()).isTrue()
    }

    @Test
    fun `vendor enrichment rule addOriginatingUser stamps an extra agent from attributes`() {
        // Master plan §4.3 — Epic profile pulls PV1-7 into an extra
        // agent.who slot. The runtime ships the resolved value in
        // ctx.attributes under the well-known key `enrichment.originatingUser`.
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-8",
            tenantId = null,
            principalName = "svc-acc",
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = "123",
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test-server/fhir",
                "agentSystem" to "epic",
                "responseStatus" to "201",
                "enrichment.originatingUser" to "epic-user-42",
            ),
        )

        val rules = listOf(AuditEnrichmentRule(field = "addOriginatingUser", source = "pv1.7"))
        val enriched = enricher.enrich(ctx, AuditEvent(), rules)

        // svc-acc + the extra originating-user agent.
        assertThat(enriched.agent.map { it.who?.reference ?: it.altId })
            .contains("Practitioner/epic-user-42")
    }

    @Test
    fun `unknown vendor enrichment rule keys are ignored (no throw)`() {
        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid-9",
            tenantId = null,
            principalName = "svc-acc",
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = "123",
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test-server/fhir",
                "responseStatus" to "201",
            ),
        )

        val rules = listOf(AuditEnrichmentRule(field = "addNonsense", source = "foo.bar"))
        // Must not throw.
        val enriched = enricher.enrich(ctx, AuditEvent(), rules)
        assertThat(enriched.agent).isNotEmpty
    }
}
