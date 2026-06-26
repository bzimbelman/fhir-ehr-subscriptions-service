package com.bzonfhir.subscriptionservice.interfaceengine.observability

import io.opentelemetry.api.OpenTelemetry
import io.opentelemetry.api.trace.Tracer
import io.opentelemetry.context.propagation.ContextPropagators
import io.opentelemetry.sdk.autoconfigure.AutoConfiguredOpenTelemetrySdk
import org.slf4j.LoggerFactory
import org.springframework.core.env.Environment
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * OpenTelemetry SDK bootstrap (Epic #387, ticket #394).
 *
 * Wires a single [OpenTelemetry] bean for the interface engine that:
 *
 *   - reads all of its configuration from `OTEL_*` environment variables
 *     (the standard envvar surface every OTel-aware tool already understands),
 *   - picks the W3C `traceparent` propagator by default (so we don't have
 *     to set OTEL_PROPAGATORS=tracecontext explicitly — it's the SDK
 *     default since 1.35),
 *   - ships spans via OTLP gRPC to OTEL_EXPORTER_OTLP_ENDPOINT when set,
 *   - is a NO-OP (zero overhead, no network connections) when
 *     OTEL_SDK_DISABLED=true. This is the default in our compose / Helm
 *     env files because most operators don't have a collector handy.
 *
 * ## Why a Spring @Bean instead of `GlobalOpenTelemetry`
 *
 * Spring beans are testable: `@TestConfiguration` can replace the
 * [OpenTelemetry] bean with one wired to an in-memory exporter for
 * `OtelTraceTest`. `GlobalOpenTelemetry.set(...)` is a JVM-global
 * singleton and trying to swap it between tests is brittle. The
 * downside — components that don't get the bean injected can't reach
 * the SDK — doesn't apply here: every code path that creates spans
 * goes through code we own, so we always inject.
 *
 * We DO NOT call `setResultAsGlobal()` on the autoconfigured SDK.
 * `GlobalOpenTelemetry.set(...)` is a JVM-singleton; in a test suite
 * where multiple Spring contexts boot in the same JVM the second
 * context's bean construction would throw "GlobalOpenTelemetry.set
 * has already been called". The bean lookup is the canonical path for
 * our own code, and the third-party-library scenario (where some
 * dependency calls `GlobalOpenTelemetry.get()`) doesn't apply to our
 * current deps. If a future library needs the global, we can flip the
 * production path to register globally — but only after the test
 * harness is wired with a per-context global-reset.
 *
 * ## What the SDK does when OTEL_SDK_DISABLED=true
 *
 * Per OTel's own docs (see `AutoConfiguredOpenTelemetrySdkBuilder` line
 * 482-489 in the 1.41 source): the propagators are still configured,
 * but the TracerProvider, MeterProvider, and LoggerProvider are
 * NOT — they return no-op implementations. Span recording becomes a
 * sequence of dead-code returns; there's no exporter, no batch
 * processor, no thread pool. This matches the ticket's "OFF by default,
 * no overhead" requirement exactly.
 *
 * ## OTEL_SERVICE_NAME default
 *
 * The autoconfigure module reads `OTEL_SERVICE_NAME` first, then falls
 * back to `OTEL_RESOURCE_ATTRIBUTES=service.name=...`, then to
 * `"unknown_service:java"`. We don't want the operator to have to set
 * `OTEL_SERVICE_NAME` for the trace to be searchable in Jaeger; ship a
 * sensible default via `addPropertiesSupplier` that fires only when
 * neither env var is set. The Helm values + compose env files set the
 * env vars explicitly anyway, but this protects the dev-laptop case
 * (gradle bootRun with OTEL_SDK_DISABLED=false and nothing else set).
 */
@Configuration
class OtelConfig {

    private val log = LoggerFactory.getLogger(OtelConfig::class.java)

    /**
     * Build the OpenTelemetry SDK from environment variables.
     *
     * `setResultAsGlobal()` registers the SDK with [io.opentelemetry.api.GlobalOpenTelemetry]
     * for any library that doesn't accept the SDK as an explicit
     * argument (we don't currently have any such library in our deps,
     * but doing it costs nothing and is the standard pattern).
     */
    @Bean
    fun openTelemetry(environment: Environment): OpenTelemetry {
        val sdk = AutoConfiguredOpenTelemetrySdk.builder()
            // Default service name picked when the operator hasn't set
            // OTEL_SERVICE_NAME. The Helm values set it explicitly to
            // `subscription-service-interface-engine`; this is the
            // fallback for `gradle bootRun` and local dev.
            .addPropertiesSupplier {
                mapOf("otel.service.name" to "subscription-service-interface-engine")
            }
            // Bridge Spring's Environment into the OTel SDK so that
            // properties set via @TestPropertySource (and via the
            // standard application.yaml override pipeline) flow into
            // the SDK's config without having to set them as JVM
            // system properties. This is the official OTel-SDK
            // pattern for embedding the autoconfigure module inside
            // a framework that has its own property source.
            //
            // The SDK reads `otel.*` property names; the OTEL_*
            // env-var form is converted by AutoConfiguredOpenTelemetrySdk
            // internally. Spring property keys are looked up with the
            // dotted form (e.g. `otel.sdk.disabled`); the OS env-var
            // path (OTEL_SDK_DISABLED=true) still works because
            // Spring's Environment exposes env vars with both forms.
            .addPropertiesCustomizer { _ ->
                // The SDK passes us a ConfigProperties view of what it
                // already resolved from env vars / system properties.
                // We return a Map<String, String> of OVERRIDES it should
                // merge on top. Spring's Environment wins — properties
                // set via @TestPropertySource or application.yaml take
                // precedence over what the SDK picked up on its own.
                val overrides = mutableMapOf<String, String>()
                for (key in OTEL_SPRING_KEYS) {
                    environment.getProperty(key)?.let { overrides[key] = it }
                }
                overrides
            }
            .build()
            .openTelemetrySdk
        log.info(
            "OpenTelemetry SDK initialized (no-op when OTEL_SDK_DISABLED=true): tracerProvider={}",
            sdk.sdkTracerProvider,
        )
        return sdk
    }

    companion object {
        /**
         * OTel autoconfigure property names we bridge from Spring's
         * environment. We don't blanket-copy every `otel.*` property
         * Spring might know about (the OTel SDK has hundreds), just
         * the handful operators commonly override. Add more here when
         * needed.
         */
        private val OTEL_SPRING_KEYS: List<String> = listOf(
            "otel.sdk.disabled",
            "otel.service.name",
            "otel.exporter.otlp.endpoint",
            "otel.resource.attributes",
            "otel.traces.exporter",
            "otel.metrics.exporter",
            "otel.logs.exporter",
            "otel.propagators",
        )
    }

    /**
     * Single shared [Tracer] used by every span-creating site in the app
     * (MLLP receive, worker.process, HTTP client interceptors). The
     * instrumentation-name `subscription-service-interface-engine` shows
     * up on every span as the `otel.scope.name` attribute, so an
     * operator can filter by scope in Jaeger to see only spans from this
     * service even when the same Jaeger receives spans from HAPI too.
     */
    @Bean
    fun interfaceEngineTracer(openTelemetry: OpenTelemetry): Tracer =
        openTelemetry.getTracer("subscription-service-interface-engine")

    /**
     * Expose the SDK's [ContextPropagators] as a bean so the HTTP client
     * interceptor doesn't have to call `openTelemetry.propagators` at
     * every request. Same propagator set the SDK uses internally — W3C
     * traceparent by default; `tracestate` is included if a caller sends
     * it.
     */
    @Bean
    fun otelPropagators(openTelemetry: OpenTelemetry): ContextPropagators =
        openTelemetry.propagators
}
