package com.bzonfhir.subscriptionservice.observability;

import java.util.Collections;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.context.annotation.Bean;

import ca.uhn.fhir.rest.server.RestfulServer;
import io.opentelemetry.api.OpenTelemetry;
import io.opentelemetry.sdk.autoconfigure.AutoConfiguredOpenTelemetrySdk;

/**
 * OpenTelemetry auto-configuration for the HAPI auth JAR (Epic #387, ticket #394).
 *
 * <p>Builds an {@link OpenTelemetry} bean from {@code OTEL_*} environment variables
 * (the SDK reads them on its own at build time) and registers {@link
 * OtelTraceInterceptor} on the HAPI {@link RestfulServer}.
 *
 * <p>Sibling of {@link ObservabilityAutoConfiguration}; kept as its own autoconfigure
 * class so it can be discovered / excluded independently (e.g. an operator who wants
 * correlation_id propagation but not OTel can omit just this one from
 * {@code spring.autoconfigure.exclude}).
 *
 * <p>Gated by {@code subscription-service.observability.otel.enabled} (default
 * {@code true}). When OTel is off, the bean is never built and the
 * {@link AutoConfiguredOpenTelemetrySdk} call is skipped entirely — no SDK threads,
 * no environment variable parsing, no warnings. When OTel is on but
 * {@code OTEL_SDK_DISABLED=true}, the SDK still builds (a no-op) and the trace
 * interceptor still runs, but emitted spans are silently dropped — zero wire-format
 * overhead.
 */
@AutoConfiguration
@ConditionalOnClass(OpenTelemetry.class)
@ConditionalOnProperty(
    prefix = "subscription-service.observability.otel",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true)
public class OtelAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(OtelAutoConfiguration.class);

  /**
   * Default service name when {@code OTEL_SERVICE_NAME} isn't set. The Helm chart
   * sets the env var explicitly; this is the fallback for the JAR-bundled tests.
   */
  static final String DEFAULT_SERVICE_NAME = "subscription-service-hapi";

  /**
   * Build the OpenTelemetry SDK. Reads OTEL_* env vars and returns the configured
   * SDK; when {@code OTEL_SDK_DISABLED=true} this is a no-op SDK that costs nothing
   * at runtime.
   *
   * <p>The bean is marked {@link ConditionalOnMissingBean} so {@link
   * com.bzonfhir.subscriptionservice.observability.OtelTraceInterceptor} tests can
   * supply their own in-memory exporter-backed SDK via {@code @TestConfiguration}.
   */
  @Bean
  @ConditionalOnMissingBean
  public OpenTelemetry openTelemetry() {
    OpenTelemetry sdk =
        AutoConfiguredOpenTelemetrySdk.builder()
            .addPropertiesSupplier(
                () -> Collections.singletonMap("otel.service.name", DEFAULT_SERVICE_NAME))
            .setResultAsGlobal()
            .build()
            .getOpenTelemetrySdk();
    log.info("OpenTelemetry SDK initialized (no-op when OTEL_SDK_DISABLED=true)");
    return sdk;
  }

  @Bean
  public OtelTraceInterceptor otelTraceInterceptor(OpenTelemetry openTelemetry) {
    return new OtelTraceInterceptor(openTelemetry);
  }

  /**
   * Register the trace interceptor on the {@link RestfulServer} once it's fully
   * constructed. Same {@link SmartInitializingSingleton} pattern as
   * {@link ObservabilityAutoConfiguration#correlationIdInterceptorRegistrar} — see
   * that javadoc for the why.
   */
  @Bean
  public SmartInitializingSingleton otelTraceInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      OtelTraceInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — OtelTraceInterceptor NOT registered. "
                + "This is fine for unit tests, but indicates a packaging problem if it "
                + "happens inside the HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(interceptor);
      log.info("Registered OtelTraceInterceptor on HAPI RestfulServer.");
    };
  }
}
