package com.bzonfhir.subscriptionservice.interfaceengine.config

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IClientInterceptor
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.client.api.IHttpRequest
import ca.uhn.fhir.rest.client.api.IHttpResponse
import ca.uhn.fhir.rest.client.api.ServerValidationModeEnum
import com.bzonfhir.subscriptionservice.interfaceengine.observability.CorrelationId
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
        val client = fhirContext.newRestfulGenericClient(hapiBaseUrl)
        // Register a client interceptor that copies the current MDC
        // correlation_id (set by IngestedMessageWorker.processOne) onto
        // every outbound HAPI request as `X-Correlation-Id`. The HAPI
        // server-side OidcJwtAuthenticationInterceptor (in the hapi-auth
        // JAR) reads the same header into its own MDC, so both services'
        // log lines for the same message share an id.
        client.registerInterceptor(CorrelationIdClientInterceptor())
        return client
    }
}

/**
 * HAPI client interceptor: stamps `X-Correlation-Id` on every outbound
 * request from the current MDC.
 *
 * Lives at file-package scope rather than nested in [FhirConfig] so it
 * can be unit-tested without the surrounding configuration class.
 */
private class CorrelationIdClientInterceptor : IClientInterceptor {
    override fun interceptRequest(request: IHttpRequest) {
        val id = CorrelationId.current() ?: return
        // addHeader rather than removeHeaders+addHeader: HAPI's generic
        // client doesn't expose a "set or replace" method on IHttpRequest
        // and we want this header to be additive, not destructive of
        // anything the caller layered on top.
        request.addHeader(CorrelationId.HEADER, id)
    }

    override fun interceptResponse(response: IHttpResponse?) {
        // No-op on the response. We could log the round-trip with the
        // correlation_id here, but every request already produces a log
        // line in IngestedMessageWorker, so this would be noise.
    }
}
