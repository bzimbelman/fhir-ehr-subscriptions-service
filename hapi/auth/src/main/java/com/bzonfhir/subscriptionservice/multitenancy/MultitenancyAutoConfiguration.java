package com.bzonfhir.subscriptionservice.multitenancy;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;

import ca.uhn.fhir.rest.server.RestfulServer;
import ca.uhn.fhir.rest.server.interceptor.partition.RequestTenantPartitionInterceptor;
import ca.uhn.fhir.rest.server.tenant.UrlBaseTenantIdentificationStrategy;

/**
 * Spring Boot auto-configuration for the subscription-service multi-tenancy layer.
 *
 * <p>Always loaded (no {@code @ConditionalOnProperty} gate) — the interceptor's behavior is
 * driven by {@link MultitenancyProperties#getMode()}. In {@code DISABLED} mode the
 * interceptor still hooks into the partition pointcuts and returns
 * {@link ca.uhn.fhir.interceptor.model.RequestPartitionId#defaultPartition()}; that costs
 * essentially nothing per request and means the JPA layer sees a deterministic partition
 * answer either way.
 *
 * <p>Two side effects on top of bean registration:
 *
 * <ol>
 *   <li>Unregister the upstream HAPI starter's {@link RequestTenantPartitionInterceptor}.
 *       The starter installs that interceptor unconditionally when {@code hapi.fhir.partitioning}
 *       is configured in {@code application.yaml}, but its job (read tenant from URL path)
 *       conflicts with our contract (URLs always look like {@code /fhir/Patient/123}).
 *   <li>Clear the {@link UrlBaseTenantIdentificationStrategy} on the {@code RestfulServer}.
 *       Same reason — keeps the URL shape stable regardless of multi-tenancy mode.
 * </ol>
 *
 * <p>The starter's {@code partitioning:} YAML block STILL gets us what we need from the
 * storage layer: {@code JpaStorageSettings.partitioningEnabled=true}, which makes the
 * Postgres schema include {@code partition_id} on every resource table. That column is
 * always there; we just don't let the URL strategy come along for the ride.
 */
@AutoConfiguration
@EnableConfigurationProperties(MultitenancyProperties.class)
public class MultitenancyAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(MultitenancyAutoConfiguration.class);

  @Bean
  public TenantPartitionInterceptor tenantPartitionInterceptor(MultitenancyProperties props) {
    log.info(
        "Subscription-service multi-tenancy: mode={} tenantClaim={} testMode={}",
        props.getMode(),
        props.getTenantClaim(),
        props.isTestMode());
    return new TenantPartitionInterceptor(props);
  }

  /**
   * Registers our partition interceptor on the {@link RestfulServer} once it's fully
   * constructed, and at the same time tears down the starter's URL-based tenant wiring.
   *
   * <p>Order vs the auth interceptor matters: this runs after the
   * {@code subscriptionServiceAuthInterceptorRegistrar} so the auth interceptor has
   * already stashed the validated JWT claims in {@code RequestDetails.userData} by the
   * time the partition pointcut fires.
   */
  @Bean
  public SmartInitializingSingleton subscriptionServiceMultitenancyInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      TenantPartitionInterceptor interceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — subscription-service multi-tenancy interceptor "
                + "NOT registered. This is fine for unit tests, but indicates a packaging "
                + "problem if it happens inside the HAPI image.");
        return;
      }

      // Drop the starter's URL-based partitioning machinery. We always want the FHIR base
      // URL to look the same; the tenant comes from the JWT (or is DEFAULT), never from
      // a path segment.
      var existing = restfulServer.getInterceptorService().getAllRegisteredInterceptors();
      for (Object i : existing) {
        if (i instanceof RequestTenantPartitionInterceptor) {
          restfulServer.unregisterInterceptor(i);
          log.info(
              "Unregistered upstream HAPI starter interceptor {} — replaced by "
                  + "TenantPartitionInterceptor",
              i.getClass().getSimpleName());
        }
      }
      restfulServer.setTenantIdentificationStrategy(null);

      restfulServer.registerInterceptor(interceptor);
      log.info(
          "Registered subscription-service multi-tenancy interceptor on HAPI RestfulServer: {}",
          interceptor.getClass().getSimpleName());
    };
  }
}
