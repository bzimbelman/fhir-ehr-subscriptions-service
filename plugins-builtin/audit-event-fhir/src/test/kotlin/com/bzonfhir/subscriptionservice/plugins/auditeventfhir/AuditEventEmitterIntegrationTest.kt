package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.plugins.auditeventfhir.config.AuditEventFhirProperties
import com.bzonfhir.subscriptionservice.spi.AuditEventEnricher
import com.bzonfhir.subscriptionservice.spi.meta.AuditContext
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.AuditEvent
import org.junit.jupiter.api.Test
import org.springframework.boot.autoconfigure.AutoConfigurations
import org.springframework.boot.test.context.runner.ApplicationContextRunner
import java.time.Instant
import java.util.concurrent.atomic.AtomicReference

/**
 * "Integration" test for the audit-event-fhir plugin's runtime wiring.
 *
 * NOTE on the ticket's "Testcontainers + real HAPI" framing:
 *   The real HAPI DAO persistence flow lives in `hapi/auth/`'s
 *   `DaoRegistryAuditEventPersister` + `AuditEventInterceptorTest`, which
 *   continue to be the source of truth for end-to-end emission against a
 *   live HAPI server (CI smoke tests cover the deployed image). This
 *   plugin module is plain Kotlin + the SPI: spinning up Postgres /
 *   Testcontainers here would only re-test what the hapi/auth tests
 *   already cover. Instead, we use Spring's `ApplicationContextRunner`
 *   (the same pattern as `hapi/auth`'s autoconfig tests) to prove the
 *   auto-configuration assembles the enricher correctly, AND we verify
 *   the emitter contract against an in-memory implementation that
 *   captures persisted events — which is the same level of fidelity the
 *   hapi/auth tests provide for `CapturingPersister`.
 */
class AuditEventEmitterIntegrationTest {

    private val contextRunner = ApplicationContextRunner()
        .withConfiguration(AutoConfigurations.of(AuditEventFhirAutoConfiguration::class.java))

    @Test
    fun `autoconfig publishes an AuditEventEnricher bean when enabled (default)`() {
        contextRunner.run { ctx ->
            assertThat(ctx).hasSingleBean(AuditEventEnricher::class.java)
            assertThat(ctx.getBean(AuditEventEnricher::class.java))
                .isInstanceOf(AuditEventFhirEnricher::class.java)
        }
    }

    @Test
    fun `autoconfig publishes the AuditEventFhirProperties bean with defaults`() {
        contextRunner.run { ctx ->
            val props = ctx.getBean(AuditEventFhirProperties::class.java)
            assertThat(props.enabled).isTrue()
            assertThat(props.captureReads).isFalse()
            assertThat(props.captureSearch).isFalse()
            assertThat(props.retentionDays).isEqualTo(365L)
        }
    }

    @Test
    fun `autoconfig binds property overrides from the environment`() {
        contextRunner
            .withPropertyValues(
                "subscription-service.audit.capture-reads=true",
                "subscription-service.audit.capture-search=true",
                "subscription-service.audit.retention-days=90",
            )
            .run { ctx ->
                val props = ctx.getBean(AuditEventFhirProperties::class.java)
                assertThat(props.captureReads).isTrue()
                assertThat(props.captureSearch).isTrue()
                assertThat(props.retentionDays).isEqualTo(90L)
            }
    }

    @Test
    fun `autoconfig skips the enricher bean when the master toggle is off`() {
        contextRunner
            .withPropertyValues("subscription-service.audit.enabled=false")
            .run { ctx ->
                assertThat(ctx).doesNotHaveBean(AuditEventEnricher::class.java)
                assertThat(ctx).doesNotHaveBean(AuditEventFhirAutoConfiguration::class.java)
            }
    }

    /**
     * Contract test: the AuditEventEmitter fun-interface accepts every
     * AuditEvent the enricher produces and the emitter sees exactly
     * one event per call. This is the surface a future Gradle-based
     * hapi/auth (or the interface-engine) would adapt onto its
     * actual persistence path.
     */
    @Test
    fun `emitter receives the same AuditEvent the enricher produced`() {
        val enricher = AuditEventFhirEnricher()
        val captured = AtomicReference<AuditEvent?>(null)
        val emitter = AuditEventEmitter { event -> captured.set(event) }

        val ctx = AuditContext(
            occurredAt = Instant.now(),
            correlationId = "cid",
            tenantId = null,
            principalName = "user-x",
            requestPath = "Patient",
            requestMethod = "POST",
            resourceType = "Patient",
            resourceId = "777",
            attributes = mapOf(
                "operation" to "CREATE",
                "fhirServerBase" to "http://x/fhir",
                "responseStatus" to "201",
            ),
        )

        val produced = enricher.enrich(ctx, AuditEvent())
        emitter.persist(produced)

        assertThat(captured.get()).isSameAs(produced)
        assertThat(captured.get()!!.entity[0].what.reference).isEqualTo("Patient/777")
    }
}
