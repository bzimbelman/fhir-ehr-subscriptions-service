package com.bzonfhir.subscriptionservice.plugins.auditeventfhir

import com.bzonfhir.subscriptionservice.plugins.auditeventfhir.config.AuditEventFhirProperties
import com.bzonfhir.subscriptionservice.spi.AuditEventEnricher
import org.springframework.boot.autoconfigure.AutoConfiguration
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean

/**
 * Spring Boot auto-configuration for the audit-event-fhir built-in plugin
 * (ticket #432, Epic #425).
 *
 * When this plugin JAR is on a Spring Boot application's classpath and
 * the gate `subscription-service.audit.enabled` (default `true`) is on,
 * an [AuditEventFhirEnricher] bean appears in the application context.
 *
 * The hapi-auth `AuditEventInterceptor` (refactored as part of this
 * ticket) discovers the enricher via Spring's `ObjectProvider<AuditEventEnricher>`
 * mechanism — every enricher bean gets a turn on every emitted event.
 *
 * ## Why ConditionalOnMissingBean
 *
 * Tests (and any consumer that wants to override the enricher) can
 * declare their own `AuditEventEnricher` bean to take precedence. The
 * production wiring stays default-on.
 *
 * ## Discovery
 *
 * Listed in
 * `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`.
 * Spring Boot 3.x picks autoconfigs up from that file at boot time.
 */
@AutoConfiguration
@ConditionalOnProperty(
    prefix = "subscription-service.audit",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
@EnableConfigurationProperties(AuditEventFhirProperties::class)
open class AuditEventFhirAutoConfiguration {

    @Bean
    @ConditionalOnMissingBean
    open fun auditEventFhirEnricher(): AuditEventEnricher = AuditEventFhirEnricher()
}
