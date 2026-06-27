package com.bzonfhir.subscriptionservice.audit;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;

import ca.uhn.fhir.jpa.api.dao.DaoRegistry;
import ca.uhn.fhir.rest.server.RestfulServer;

/**
 * Spring Boot auto-configuration for the FHIR {@code AuditEvent} interceptor
 * (ticket #391, Epic #387).
 *
 * <p>Gated by {@code subscription-service.audit.enabled} (default {@code true}). When
 * the {@link DaoRegistry} bean is present (i.e. we're running inside the real HAPI
 * server, not a unit test), the {@link DaoRegistryAuditEventPersister} is wired up
 * and the interceptor is registered on the {@link RestfulServer}. When no
 * {@link DaoRegistry} is on the context (e.g. unit tests of the autoconfig itself),
 * we fall back to a no-op persister so the autoconfig still produces beans the way
 * Spring expects.
 *
 * <p>Discovered via
 * {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}.
 */
@AutoConfiguration
@ConditionalOnProperty(
    prefix = "subscription-service.audit",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true)
@EnableConfigurationProperties(AuditProperties.class)
public class AuditAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(AuditAutoConfiguration.class);

  /**
   * Production persister: writes through HAPI's {@link DaoRegistry}. Only created
   * when DaoRegistry is on the context — outside the deployed image (e.g. autoconfig
   * unit tests) it's absent and we fall back to the no-op below.
   */
  @Bean
  @ConditionalOnMissingBean(AuditEventPersister.class)
  public AuditEventPersister auditEventPersister(
      @Autowired(required = false) DaoRegistry daoRegistry) {
    if (daoRegistry == null) {
      log.warn(
          "No DaoRegistry bean available — AuditEvent persistence disabled (no-op persister). "
              + "This is expected for autoconfig unit tests; in the HAPI image it indicates a "
              + "packaging problem.");
      return event -> {
        /* no-op */
      };
    }
    return new DaoRegistryAuditEventPersister(daoRegistry);
  }

  @Bean
  public AuditEventInterceptor auditEventInterceptor(
      AuditProperties props, AuditEventPersister persister) {
    return new AuditEventInterceptor(props, persister);
  }

  /**
   * Register the interceptor on the {@link RestfulServer} once it's fully constructed.
   * Same pattern as {@link com.bzonfhir.subscriptionservice.auth.AuthAutoConfiguration}.
   */
  @Bean
  public SmartInitializingSingleton subscriptionServiceAuditInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      AuditEventInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — AuditEventInterceptor NOT registered. This is fine "
                + "for unit tests, but indicates a packaging problem if it happens inside the "
                + "HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(interceptor);
      log.info("Registered AuditEventInterceptor on HAPI RestfulServer.");
    };
  }
}
