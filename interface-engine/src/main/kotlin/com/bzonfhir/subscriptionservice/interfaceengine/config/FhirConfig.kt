package com.bzonfhir.subscriptionservice.interfaceengine.config

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IClientInterceptor
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.client.api.IHttpRequest
import ca.uhn.fhir.rest.client.api.IHttpResponse
import ca.uhn.fhir.rest.client.api.ServerValidationModeEnum
import com.bzonfhir.subscriptionservice.interfaceengine.observability.CorrelationId
import io.opentelemetry.context.Context
import io.opentelemetry.context.propagation.ContextPropagators
import io.opentelemetry.context.propagation.TextMapSetter
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
        // OpenTelemetry propagators (Epic #387, ticket #394). Used by
        // the HAPI client interceptor to inject W3C `traceparent` (+
        // optional `tracestate`) onto every outbound request. When the
        // SDK is disabled the active context is the no-op root and
        // inject is a no-op — no header added, no overhead.
        propagators: ContextPropagators,
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
        // Register a second interceptor that injects the W3C traceparent
        // for the current OTel context onto every outbound request.
        // HAPI's auth JAR has a matching server-side extractor that
        // continues the trace into HAPI's request scope (ticket #394).
        client.registerInterceptor(OtelTraceparentClientInterceptor(propagators))
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

/**
 * HAPI client interceptor: injects the W3C `traceparent` header onto
 * every outbound request from the current OTel context (Epic #387,
 * ticket #394).
 *
 * Uses the SDK's [ContextPropagators] rather than encoding the header
 * by hand so an SDK upgrade that ships a new `traceparent` version
 * (currently v00) propagates the right thing automatically.
 *
 * When the SDK is disabled, `Context.current()` is the no-op root and
 * the propagator's inject(...) writes nothing onto the carrier — no
 * traceparent header appears on the wire, matching the "off by default
 * with zero overhead" requirement.
 */
private class OtelTraceparentClientInterceptor(
    private val propagators: ContextPropagators,
) : IClientInterceptor {

    override fun interceptRequest(request: IHttpRequest) {
        propagators.textMapPropagator.inject(Context.current(), request, IHttpRequestSetter)
    }

    override fun interceptResponse(response: IHttpResponse?) {
        // No-op — the OTel SDK ends the client span via the wrapping
        // try/finally in the caller (see MatchboxClientImpl / the
        // workerSpan in IngestedMessageWorker). HAPI's IGenericClient
        // doesn't expose a "request completed" hook with timing data
        // we'd attach to the span here.
    }

    private object IHttpRequestSetter : TextMapSetter<IHttpRequest> {
        override fun set(carrier: IHttpRequest?, key: String, value: String) {
            // HAPI's addHeader is additive; if someone (presumably
            // tests) stamped a traceparent on the request before this
            // interceptor ran, both would land. That's unlikely — only
            // this interceptor sets the OTel headers — so we accept the
            // simpler API rather than juggling removeHeaders + addHeader.
            carrier?.addHeader(key, value)
        }
    }
}
