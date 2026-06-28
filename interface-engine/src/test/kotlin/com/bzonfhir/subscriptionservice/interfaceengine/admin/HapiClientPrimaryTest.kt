package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.rest.client.api.IGenericClient
import com.bzonfhir.subscriptionservice.interfaceengine.config.FhirConfig
import io.opentelemetry.context.propagation.ContextPropagators
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.springframework.boot.test.context.runner.ApplicationContextRunner
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * Regression test for ticket #520 (Epic #428, commercial plugin pack).
 *
 * ## The defect
 *
 * The `audit-export` commercial plugin contributes a second
 * `IGenericClient` bean (named `auditExportHapiClient`) for HAPI
 * AuditEvent queries. The engine's existing consumers
 * ([HapiSubscriptionStatusClientImpl], `AdminAuditController`,
 * `IngestedMessageWorker`) autowire `IGenericClient` WITHOUT a
 * `@Qualifier`. When the plugin is loaded, two beans of the same type
 * exist on the classpath with no `@Primary` marker. Spring fails
 * startup with `NoUniqueBeanDefinitionException` before the first MLLP
 * message is even ingested.
 *
 * The fix is small: mark the engine's primary HAPI client
 * ([FhirConfig.hapiFhirClient]) as `@Primary` so the unqualified
 * consumers resolve to it deterministically. The plugin's own bean
 * stays addressable via `@Qualifier("auditExportHapiClient")` for
 * callers that explicitly want it.
 *
 * ## Why test this here
 *
 * The commercial audit-export plugin lives in a separate (commercial)
 * source tree and isn't on this repo's build classpath. Without an
 * explicit regression test, a future contributor could remove the
 * `@Primary` annotation thinking it's redundant — and the failure
 * would only surface when the commercial pack is loaded in a customer
 * deployment. This test simulates the plugin's behaviour by
 * registering a second [IGenericClient] inside a `@TestConfiguration`
 * and asserts:
 *
 *   1. The Spring context loads without `NoUniqueBeanDefinitionException`.
 *   2. An unqualified `IGenericClient` consumer receives the
 *      engine's (`@Primary`) bean — NOT the simulated plugin bean.
 *
 * ## Test shape
 *
 * Uses `ApplicationContextRunner` (the same pattern as
 * `AuditEventEmitterIntegrationTest` in the audit-event-fhir plugin)
 * to boot a minimal context with just [FhirConfig] + the stubs it
 * needs. We avoid `@SpringBootTest` because we don't need the full
 * application context — just the bean-wiring contract.
 */
class HapiClientPrimaryTest {

    /**
     * Minimal context that boots [FhirConfig] plus the stubs FhirConfig
     * depends on: a fake [ContextPropagators] bean (provided by
     * `OtelConfig` in production, but we don't want to pull the full
     * OTel SDK autoconfig into this test), and the
     * `subscription-service.hapi.base-url` property the bean reads.
     *
     * Also registers a SECOND [IGenericClient] bean named
     * `auditExportHapiClient` to simulate the commercial audit-export
     * plugin. With no `@Primary` on FhirConfig's bean, Spring would
     * fail to start; with `@Primary`, the unqualified consumer below
     * resolves cleanly to the engine's bean.
     */
    private val contextRunner = ApplicationContextRunner()
        .withUserConfiguration(
            FhirConfig::class.java,
            StubPropagatorsConfig::class.java,
            SimulatedAuditExportPluginConfig::class.java,
            UnqualifiedConsumerConfig::class.java,
        )
        .withPropertyValues(
            "subscription-service.hapi.base-url=http://stub.hapi.test/fhir",
            "subscription-service.hapi.timeout-ms=1000",
        )

    @Test
    fun `context loads when a plugin contributes a second IGenericClient bean`() {
        contextRunner.run { ctx ->
            // If FhirConfig.hapiFhirClient is NOT @Primary, the startup
            // throws NoUniqueBeanDefinitionException because two beans
            // of type IGenericClient exist and the UnqualifiedConsumer
            // can't disambiguate. AssertJ's hasNotFailed() unwraps the
            // startup failure for a readable diff in that case.
            assertThat(ctx).hasNotFailed()
            assertThat(ctx).hasBean("hapiFhirClient")
            assertThat(ctx).hasBean("auditExportHapiClient")
        }
    }

    @Test
    fun `unqualified IGenericClient consumer resolves to the engines primary bean`() {
        contextRunner.run { ctx ->
            assertThat(ctx).hasNotFailed()

            // The unqualified consumer should have received the
            // engine's @Primary bean (`hapiFhirClient`), NOT the
            // simulated plugin bean (`auditExportHapiClient`).
            val consumer = ctx.getBean(UnqualifiedConsumer::class.java)
            val primaryBean = ctx.getBean("hapiFhirClient", IGenericClient::class.java)
            assertThat(consumer.client).isSameAs(primaryBean)

            // Sanity: getBean(IGenericClient::class.java) without a
            // qualifier should also pick the @Primary bean rather than
            // throw NoUniqueBeanDefinitionException.
            val resolved = ctx.getBean(IGenericClient::class.java)
            assertThat(resolved).isSameAs(primaryBean)
        }
    }

    @Test
    fun `plugin bean is still addressable by qualifier`() {
        contextRunner.run { ctx ->
            assertThat(ctx).hasNotFailed()

            // Plugins (and any future engine code that explicitly wants
            // the audit-export client) can still get the secondary bean
            // by name. @Primary doesn't hide it, just resolves the
            // ambiguity for unqualified injection points.
            val pluginBean = ctx.getBean("auditExportHapiClient", IGenericClient::class.java)
            val primaryBean = ctx.getBean("hapiFhirClient", IGenericClient::class.java)
            assertThat(pluginBean).isNotSameAs(primaryBean)
        }
    }

    /**
     * Stub `ContextPropagators` bean. The production wiring (in
     * `OtelConfig.otelPropagators`) builds this from the OTel SDK; we
     * just need any non-null instance so [FhirConfig.hapiFhirClient]
     * can construct. The propagator's behaviour is exercised by
     * `OtelTraceTest` / `OtelDisabledTest`; this test only cares about
     * the bean graph.
     */
    @Configuration
    class StubPropagatorsConfig {
        @Bean
        fun stubPropagators(): ContextPropagators = ContextPropagators.noop()
    }

    /**
     * Simulates the `audit-export` commercial plugin (Epic #428). The
     * real plugin's auto-configuration registers an `IGenericClient`
     * pointed at the AuditEvent FHIR endpoint, named
     * `auditExportHapiClient` so other plugin beans can `@Qualifier`
     * it. We use a Mockito mock here because the actual client
     * configuration is the plugin's concern; what matters for THIS
     * test is that a second bean of the same type exists.
     */
    @Configuration
    class SimulatedAuditExportPluginConfig {
        @Bean
        fun auditExportHapiClient(): IGenericClient =
            org.mockito.Mockito.mock(IGenericClient::class.java)
    }

    /**
     * Mimics the autowiring pattern of every existing engine consumer
     * of `IGenericClient` — no `@Qualifier`. If `FhirConfig.hapiFhirClient`
     * loses its `@Primary` annotation, the context will fail to
     * construct this bean with `NoUniqueBeanDefinitionException`.
     */
    @Configuration
    class UnqualifiedConsumerConfig {
        @Bean
        fun unqualifiedConsumer(hapiClient: IGenericClient): UnqualifiedConsumer =
            UnqualifiedConsumer(hapiClient)
    }

    class UnqualifiedConsumer(val client: IGenericClient)
}
