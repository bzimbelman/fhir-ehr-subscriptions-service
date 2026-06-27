package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.plugins.observabilityotel.OtelObservabilityEnricher
import com.bzonfhir.subscriptionservice.plugins.observabilityotel.config.ObservabilityProperties
import com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher
import org.springframework.beans.factory.annotation.Value
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * Wires the built-in
 * [com.bzonfhir.subscriptionservice.plugins.observabilityotel.OtelObservabilityEnricher]
 * into the Spring context (ticket #433, Epic #425).
 *
 * # Why a host-side @Configuration rather than @Component in the plugin
 *
 * `plugins-builtin/observability-otel` declares Spring `compileOnly`
 * so that a third-party consumer can depend on the plugin module
 * without dragging Spring onto their classpath. That means the plugin
 * can't carry a `@Component` annotation itself. The host
 * (`interface-engine`) supplies the binding.
 *
 * This is the same pattern the audit / vendor-profile SPIs will follow
 * in #431-#432 once they land — the runtime owns the @Bean wiring; the
 * plugin module ships pure SPI implementations.
 *
 * # Properties
 *
 * Operators override `subscription-service.observability.schema-version`
 * to swap the value during a major-bump deprecation cycle. Defaults
 * align with [com.bzonfhir.subscriptionservice.plugins.observabilityotel
 * .StandardLogFields.SCHEMA_VERSION] (`"1.0"`).
 */
@Configuration
class BuiltinObservabilityPluginConfig {

    /**
     * Bind the `subscription-service.observability.*` namespace onto
     * the plugin's properties record. `@Value` rather than
     * `@ConfigurationProperties` because the plugin module declares
     * Spring `compileOnly` and the `@ConfigurationProperties` setup
     * is heavier than necessary for one field.
     */
    @Bean
    fun observabilityProperties(
        @Value("\${subscription-service.observability.schema-version:1.0}")
        schemaVersion: String,
    ): ObservabilityProperties = ObservabilityProperties(schemaVersion = schemaVersion)

    /**
     * Register the built-in plugin as an [ObservabilityEnricher] bean
     * so [ObservabilityEnricherChain] picks it up via
     * `List<ObservabilityEnricher>` constructor injection.
     */
    @Bean
    fun otelObservabilityEnricher(
        observabilityProperties: ObservabilityProperties,
    ): ObservabilityEnricher = OtelObservabilityEnricher(observabilityProperties)
}
