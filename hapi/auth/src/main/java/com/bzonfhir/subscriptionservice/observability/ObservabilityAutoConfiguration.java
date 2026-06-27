package com.bzonfhir.subscriptionservice.observability;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.context.annotation.Bean;

import ca.uhn.fhir.interceptor.api.IInterceptorService;
import ca.uhn.fhir.rest.server.RestfulServer;
import io.micrometer.core.instrument.MeterRegistry;

/**
 * Auto-configuration for the subscription-service observability layer
 * (Epic #387, ticket #388).
 *
 * <p>Registers {@link CorrelationIdInterceptor} on the HAPI {@link RestfulServer} so
 * every inbound FHIR request gets a {@code correlation_id} on its MDC and an
 * {@code X-Correlation-Id} echo header on its response. Companion to
 * {@link com.bzonfhir.subscriptionservice.auth.AuthAutoConfiguration} — both follow the
 * same {@link SmartInitializingSingleton} pattern to register their interceptors after
 * the HAPI starter's {@code restfulServer} bean is fully constructed.
 *
 * <p>Gated by {@code subscription-service.observability.enabled} (default {@code true})
 * so an operator can disable the interceptor in an environment where some other
 * trace-propagation mechanism is in play (e.g. an OTel agent that already manages MDC).
 *
 * <p>Discovered via
 * {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}.
 */
@AutoConfiguration
@ConditionalOnProperty(
    prefix = "subscription-service.observability",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true)
public class ObservabilityAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(ObservabilityAutoConfiguration.class);

  @Bean
  public CorrelationIdInterceptor correlationIdInterceptor() {
    return new CorrelationIdInterceptor();
  }

  /**
   * Register the interceptor on the {@link RestfulServer} once it's fully constructed.
   * Same pattern as {@link com.bzonfhir.subscriptionservice.auth.AuthAutoConfiguration};
   * see that class for why we use {@link SmartInitializingSingleton} rather than putting
   * the interceptor on {@code hapi.fhir.custom-interceptor-classes}.
   */
  @Bean
  public SmartInitializingSingleton correlationIdInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      CorrelationIdInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — CorrelationIdInterceptor NOT registered. "
                + "This is fine for unit tests, but indicates a packaging problem if it "
                + "happens inside the HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(interceptor);
      log.info("Registered CorrelationIdInterceptor on HAPI RestfulServer.");
    };
  }

  /**
   * Subscription-side metrics interceptor (Epic #387, ticket #389).
   *
   * <p>Gated by {@link ConditionalOnClass} on Micrometer — the HAPI image bundles
   * micrometer-core in its WEB-INF/lib, so this class is present at runtime; if a future
   * HAPI image strips it the bean would silently no-op rather than blowing up the
   * autoconfigure.
   *
   * <p>The {@link MeterRegistry} is injected via {@link ObjectProvider} so this autoconfig
   * is NOT order-dependent on {@code MetricsAutoConfiguration}. {@code @ConditionalOnBean}
   * has a known limitation in autoconfig classes — it can fire BEFORE the conditional
   * bean's autoconfig has run and report "no bean" even when one will exist by application
   * start. ObjectProvider sidesteps that race by deferring the lookup to the registrar's
   * runtime callback.
   */
  @Bean
  @ConditionalOnClass(MeterRegistry.class)
  public SubscriptionMetricsInterceptor subscriptionMetricsInterceptor(
      ObjectProvider<MeterRegistry> meterRegistryProvider) {
    MeterRegistry meterRegistry = meterRegistryProvider.getIfAvailable();
    if (meterRegistry == null) {
      log.warn(
          "No MeterRegistry bean found — SubscriptionMetricsInterceptor will be created "
              + "but won't publish anything until a registry appears. This is fine for "
              + "the in-process unit tests; in the HAPI image we expect a "
              + "PrometheusMeterRegistry from Spring Boot's metrics autoconfig.");
      // Construct against a no-op registry rather than returning null — the
      // @Hook methods still get called by HAPI, they just don't record anywhere.
      // This avoids forcing every caller of the bean to null-check.
      return new SubscriptionMetricsInterceptor(new io.micrometer.core.instrument.simple.SimpleMeterRegistry());
    }
    return new SubscriptionMetricsInterceptor(meterRegistry);
  }

  /**
   * Register the subscription metrics interceptor on HAPI's global {@link
   * IInterceptorService}. SUBSCRIPTION_* pointcuts are dispatched by HAPI's storage-layer
   * broadcaster, NOT the per-RestfulServer one, so {@code RestfulServer.registerInterceptor}
   * wouldn't see them — the @Hook annotations would attach to the wrong dispatcher and
   * silently never fire.
   *
   * <p>The {@code IInterceptorService} bean is autowired with {@code required=false} so the
   * autoconfigure still loads cleanly in unit-test contexts that don't bring up the full
   * HAPI storage stack (CorrelationIdInterceptorTest etc.).
   */
  @Bean
  @ConditionalOnClass(MeterRegistry.class)
  public SmartInitializingSingleton subscriptionMetricsInterceptorRegistrar(
      @Autowired(required = false) IInterceptorService interceptorService,
      SubscriptionMetricsInterceptor interceptor) {
    return () -> {
      if (interceptorService == null) {
        log.warn(
            "No IInterceptorService bean found — SubscriptionMetricsInterceptor NOT "
                + "registered. This is fine for unit tests, but indicates a packaging "
                + "problem if it happens inside the HAPI image.");
        return;
      }
      interceptorService.registerInterceptor(interceptor);
      log.info("Registered SubscriptionMetricsInterceptor on HAPI IInterceptorService.");
    };
  }
}
