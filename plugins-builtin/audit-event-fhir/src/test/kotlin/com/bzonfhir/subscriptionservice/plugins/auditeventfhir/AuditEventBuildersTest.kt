package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.AuditEvent
import org.hl7.fhir.r4.model.AuditEvent.AuditEventAction
import org.hl7.fhir.r4.model.AuditEvent.AuditEventOutcome
import org.junit.jupiter.api.Test
import java.time.Instant

/**
 * Lower-level tests for the pure builder helpers in
 * `AuditEventBuilders.kt`. These exercise the cells the enricher
 * stitches together; the enricher's own contract test
 * ([AuditEventFhirEnricherTest]) covers the end-to-end shape.
 */
class AuditEventBuildersTest {

    @Test
    fun `subtypeFor maps the common operations to FHIR restful-interaction codes`() {
        assertThat(subtypeFor("CREATE").code).isEqualTo("create")
        assertThat(subtypeFor("READ").code).isEqualTo("read")
        assertThat(subtypeFor("VREAD").code).isEqualTo("read")
        assertThat(subtypeFor("UPDATE").code).isEqualTo("update")
        assertThat(subtypeFor("PATCH").code).isEqualTo("patch")
        assertThat(subtypeFor("DELETE").code).isEqualTo("delete")
        assertThat(subtypeFor("SEARCH_TYPE").code).isEqualTo("search")
        assertThat(subtypeFor("HISTORY_INSTANCE").code).isEqualTo("history")
        assertThat(subtypeFor("TRANSACTION").code).isEqualTo("transaction")
        assertThat(subtypeFor("BATCH").code).isEqualTo("batch")
    }

    @Test
    fun `actionFor maps every operation we care about`() {
        assertThat(actionFor("CREATE")).isEqualTo(AuditEventAction.C)
        assertThat(actionFor("READ")).isEqualTo(AuditEventAction.R)
        assertThat(actionFor("VREAD")).isEqualTo(AuditEventAction.R)
        assertThat(actionFor("SEARCH_TYPE")).isEqualTo(AuditEventAction.R)
        assertThat(actionFor("SEARCH_SYSTEM")).isEqualTo(AuditEventAction.R)
        assertThat(actionFor("HISTORY_INSTANCE")).isEqualTo(AuditEventAction.R)
        assertThat(actionFor("UPDATE")).isEqualTo(AuditEventAction.U)
        assertThat(actionFor("PATCH")).isEqualTo(AuditEventAction.U)
        assertThat(actionFor("DELETE")).isEqualTo(AuditEventAction.D)
        assertThat(actionFor("TRANSACTION")).isEqualTo(AuditEventAction.E)
    }

    @Test
    fun `outcomeFor returns 0 when neither responseStatus nor exception status is set`() {
        val ctx = baseCtx(emptyMap())
        assertThat(outcomeFor(ctx)).isEqualTo(AuditEventOutcome._0)
    }

    @Test
    fun `outcomeFor returns 4 for response code 400`() {
        val ctx = baseCtx(mapOf("responseStatus" to "400"))
        assertThat(outcomeFor(ctx)).isEqualTo(AuditEventOutcome._4)
    }

    @Test
    fun `outcomeFor returns 8 for response code 500`() {
        val ctx = baseCtx(mapOf("responseStatus" to "500"))
        assertThat(outcomeFor(ctx)).isEqualTo(AuditEventOutcome._8)
    }

    @Test
    fun `outcomeFor returns 8 when exception status is 401`() {
        val ctx = baseCtx(mapOf("exception.status" to "401"))
        assertThat(outcomeFor(ctx)).isEqualTo(AuditEventOutcome._8)
    }

    @Test
    fun `outcomeFor returns 4 when exception status is 403`() {
        val ctx = baseCtx(mapOf("exception.status" to "403"))
        assertThat(outcomeFor(ctx)).isEqualTo(AuditEventOutcome._4)
    }

    @Test
    fun `populateAgents adds an anonymous placeholder when no principal and no azp`() {
        val event = AuditEvent()
        val ctx = baseCtx(emptyMap()).copy(principalName = null)
        populateAgents(event, ctx)
        assertThat(event.agent).hasSize(1)
        assertThat(event.agent[0].altId).isEqualTo("anonymous")
        assertThat(event.agent[0].requestor).isTrue()
    }

    @Test
    fun `populateAgents adds two agents when principal AND azp are both set`() {
        val event = AuditEvent()
        val ctx = baseCtx(mapOf("azp" to "smart-app"))
        populateAgents(event, ctx)
        assertThat(event.agent).hasSize(2)
        assertThat(event.agent[0].altId).isEqualTo("svc-acc")
        assertThat(event.agent[0].requestor).isTrue()
        assertThat(event.agent[1].altId).isEqualTo("smart-app")
        assertThat(event.agent[1].requestor).isFalse()
    }

    @Test
    fun `populateEntities skips when neither resourceType nor resourceId is set`() {
        val event = AuditEvent()
        val ctx = baseCtx(emptyMap()).copy(resourceType = null, resourceId = null)
        populateEntities(event, ctx)
        assertThat(event.entity).isEmpty()
    }

    @Test
    fun `populateEntities emits a typed-display reference when only resourceType is known`() {
        val event = AuditEvent()
        val ctx = baseCtx(emptyMap()).copy(resourceType = "Patient", resourceId = null)
        populateEntities(event, ctx)
        assertThat(event.entity).hasSize(1)
        assertThat(event.entity[0].what.display).isEqualTo("Patient")
    }

    @Test
    fun `populateEntities emits a full ResourceType-slash-id Reference when both are known`() {
        val event = AuditEvent()
        val ctx = baseCtx(emptyMap()).copy(resourceType = "Patient", resourceId = "123")
        populateEntities(event, ctx)
        assertThat(event.entity).hasSize(1)
        assertThat(event.entity[0].what.reference).isEqualTo("Patient/123")
    }

    @Test
    fun `buildSource uses fhirServerBase when present, falls back to unknown`() {
        val withBase = buildSource(baseCtx(mapOf("fhirServerBase" to "http://x/fhir")))
        assertThat(withBase.site).isEqualTo("http://x/fhir")

        val withoutBase = buildSource(baseCtx(emptyMap()))
        assertThat(withoutBase.site).isEqualTo("unknown")
    }

    @Test
    fun `applyBaseShape wires every required slot for a successful CREATE`() {
        val event = AuditEvent()
        val ctx = baseCtx(
            mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://test/fhir",
                "responseStatus" to "201",
            ),
        )
        applyBaseShape(event, ctx)

        assertThat(event.type.code).isEqualTo("rest")
        assertThat(event.subtype).hasSize(1)
        assertThat(event.subtype[0].code).isEqualTo("create")
        assertThat(event.action).isEqualTo(AuditEventAction.C)
        assertThat(event.outcome).isEqualTo(AuditEventOutcome._0)
        assertThat(event.recorded).isNotNull()
        assertThat(event.source.site).isEqualTo("http://test/fhir")
        assertThat(event.agent).isNotEmpty
        assertThat(event.period.hasStart()).isTrue()
        assertThat(event.period.hasEnd()).isTrue()
    }

    private fun baseCtx(attrs: Map<String, String>): AuditContext = AuditContext(
        occurredAt = Instant.parse("2026-01-15T10:00:00Z"),
        correlationId = "cid",
        tenantId = null,
        principalName = "svc-acc",
        requestPath = "Patient",
        requestMethod = "POST",
        resourceType = "Patient",
        resourceId = "123",
        attributes = attrs,
    )
}
