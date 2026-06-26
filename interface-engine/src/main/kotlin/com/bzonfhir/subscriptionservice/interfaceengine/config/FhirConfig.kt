package com.bzonfhir.subscriptionservice.ipf.config

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.client.api.ServerValidationModeEnum
import org.springframework.beans.factory.annotation.Value
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * Beans for the HAPI FHIR R4 client used to POST transaction Bundles to HAPI.
 *
 * The `FhirContext` is intentionally a singleton: building one is expensive
 * (it scans the R4 model on first use), and HAPI's documentation explicitly
 * says one instance per JVM is the right shape.
 *
 * `ServerValidationModeEnum.NEVER` skips the client's "GET /metadata once on
 * first use" probe. Our HAPI dependency is already health-checked at compose
 * level, and skipping the probe makes the first message after a cold boot
 * faster + removes one place where a transient HAPI hiccup could ACK-AE.
 */
@Configuration
class FhirConfig {

    @Bean
    fun fhirContext(): FhirContext = FhirContext.forR4().apply {
        restfulClientFactory.serverValidationMode = ServerValidationModeEnum.NEVER
    }

    @Bean
    fun hapiFhirClient(
        fhirContext: FhirContext,
        @Value("\${subscription-service.hapi.base-url}") hapiBaseUrl: String,
        @Value("\${subscription-service.hapi.timeout-ms:30000}") timeoutMs: Int,
    ): IGenericClient {
        fhirContext.restfulClientFactory.socketTimeout = timeoutMs
        fhirContext.restfulClientFactory.connectTimeout = timeoutMs
        return fhirContext.newRestfulGenericClient(hapiBaseUrl)
    }
}
