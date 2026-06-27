package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config.Hl7V2MllpProperties
import org.apache.camel.CamelContext
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * Spring Boot auto-configuration for the HL7 v2 MLLP ingest plugin
 * (ticket #431, Epic #425).
 *
 * Listed in `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`,
 * which is the Spring Boot 3 mechanism for "register this @Configuration
 * when my JAR is on the classpath." A `@SpringBootApplication` host
 * (interface-engine) picks the config up automatically; no
 * `@ComponentScan` change is required.
 *
 * ## Gating conditions
 *
 * 1. `@ConditionalOnClass(CamelContext::class)` — the auto-config only
 *    activates if the host actually has Camel on its classpath. A
 *    future host that doesn't use Camel (e.g. a pure REST gateway)
 *    wouldn't accidentally pull in the MLLP plugin's beans.
 * 2. `@ConditionalOnProperty(... matchIfMissing = true)` — the plugin
 *    enables itself BY DEFAULT (to preserve today's behaviour: every
 *    interface-engine deployment has MLLP listening on 2575) but
 *    operators can flip the switch off via env var
 *    `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_ENABLED=false`.
 *
 * ## Backward compatibility with the legacy port property
 *
 * The pre-#431 code read MLLP port from `subscription-service.mllp.port`.
 * The new code reads `subscription-service.ingest.hl7v2-mllp.port`.
 * Spring's @ConfigurationProperties doesn't natively chain property
 * names, so the [hl7v2MllpIngestSource] bean below picks the effective
 * port using this precedence:
 *
 *   1. New key `subscription-service.ingest.hl7v2-mllp.port` if
 *      explicitly set (i.e. not equal to the default 2575).
 *   2. Legacy key `subscription-service.mllp.port` if set.
 *   3. New key's default (2575).
 *
 * Why the fallback exists: many interface-engine tests register
 * `subscription-service.mllp.port` to a random free port via
 * @DynamicPropertySource (see AdminAuditControllerTest,
 * AdminMessagesControllerTest, etc. — over 20 callers). Rather than
 * rewrite every test, we honour BOTH names with the new one winning if
 * both are set. Operators migrating off the legacy key just need to
 * remove it from their config; no env-var coordination required.
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnClass(CamelContext::class)
@ConditionalOnProperty(
    prefix = "subscription-service.ingest.hl7v2-mllp",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
@EnableConfigurationProperties(Hl7V2MllpProperties::class)
open class Hl7V2MllpAutoConfiguration {

    /**
     * Construct the [Hl7V2MllpIngestSource] bean. The host's
     * `IngestSourceRegistry` will discover it (along with any sibling
     * `IngestSource` beans a different plugin contributes) and invoke
     * `.start(callback)` on each at boot.
     *
     * Note that we DO NOT call start() here. The SPI's lifecycle
     * explicitly separates construction from start so the host owns
     * the callback definition. If we started here, we'd have no
     * callback to register and the plugin would receive messages with
     * nowhere to send them.
     *
     * The `@Value(...:#{null})` SpEL trick yields a Kotlin nullable
     * Int? when the legacy property is absent. Without `#{null}` the
     * default would be the literal string "null" which fails Int
     * binding.
     */
    @Bean
    open fun hl7v2MllpIngestSource(
        properties: Hl7V2MllpProperties,
        camelContext: CamelContext,
        @Value("\${subscription-service.mllp.port:#{null}}") legacyPort: Int?,
    ): Hl7V2MllpIngestSource =
        Hl7V2MllpIngestSource(
            properties = mergeLegacyPort(properties, legacyPort),
            camelContext = camelContext,
        )

    /**
     * Pick the effective port per the precedence documented on the
     * class. Pure function — no Spring context required, easy to test.
     */
    private fun mergeLegacyPort(
        properties: Hl7V2MllpProperties,
        legacyPort: Int?,
    ): Hl7V2MllpProperties {
        val effectivePort = when {
            properties.port != DEFAULT_PORT -> properties.port
            legacyPort != null -> legacyPort
            else -> properties.port
        }
        return if (effectivePort == properties.port) {
            properties
        } else {
            properties.copy(port = effectivePort)
        }
    }

    companion object {
        /**
         * Default port — duplicated from Hl7V2MllpProperties so the
         * fallback detection in [mergeLegacyPort] can tell "operator
         * explicitly set port=2575" from "operator didn't set anything
         * so the default of 2575 kicked in." A future refactor could
         * use OptionalInt on the properties class to remove the
         * ambiguity entirely, but that would change the public config
         * surface.
         */
        const val DEFAULT_PORT: Int = 2575
    }
}
