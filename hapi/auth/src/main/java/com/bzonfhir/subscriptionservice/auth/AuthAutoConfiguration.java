package com.bzonfhir.subscriptionservice.auth;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.SmartInitializingSingleton;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;

import ca.uhn.fhir.rest.server.RestfulServer;

/**
 * Spring Boot auto-configuration entry point for the subscription-service auth layer.
 *
 * <p>Discovered via {@code META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports}
 * — when this JAR is on the HAPI classpath, Spring Boot's autoconfiguration machinery
 * picks it up and wires the beans below into the existing HAPI Spring context.
 *
 * <p>The whole configuration is gated by
 * {@code subscription-service.auth.enabled=true} (the default). Setting that property to
 * {@code false} (or env {@code SUBSCRIPTION_SERVICE_AUTH_ENABLED=false}) causes Spring to
 * skip this class entirely, leaving HAPI in its unauthenticated upstream state — useful
 * for local development against the docker-compose stack without a Keycloak running.
 */
@AutoConfiguration
@ConditionalOnProperty(
    prefix = "subscription-service.auth",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true)
@EnableConfigurationProperties(AuthProperties.class)
public class AuthAutoConfiguration {

  private static final Logger log = LoggerFactory.getLogger(AuthAutoConfiguration.class);

  /**
   * Validates incoming JWTs against the configured Keycloak realm's JWKS. Singleton: the
   * underlying Nimbus {@code JWKSource} caches the JWKS and refreshes it on its own
   * schedule, so reusing one instance across requests is correct and cheap.
   */
  @Bean
  public JwtValidator jwtValidator(AuthProperties props) {
    log.info(
        "Subscription-service auth enabled: issuer={} jwks={}",
        props.getIssuer(),
        props.resolveJwksUrl());
    return new JwtValidator(props);
  }

  @Bean
  public KeycloakJwtAuthenticationInterceptor keycloakJwtAuthenticationInterceptor(
      AuthProperties props, JwtValidator validator) {
    return new KeycloakJwtAuthenticationInterceptor(props, validator);
  }

  @Bean
  public ScopeAuthorizationInterceptor scopeAuthorizationInterceptor(AuthProperties props) {
    return new ScopeAuthorizationInterceptor(props);
  }

  /**
   * Registers the two interceptors on the HAPI {@link RestfulServer} once it's fully
   * constructed. The starter's {@code restfulServer} bean does NOT pick up arbitrary
   * {@code IServerInterceptor} beans from the context automatically — only ones listed
   * under {@code hapi.fhir.custom-interceptor-classes}. Using a
   * {@link SmartInitializingSingleton} sidesteps that config requirement and works on any
   * version of the starter that exposes a {@code RestfulServer} bean.
   */
  @Bean
  public SmartInitializingSingleton subscriptionServiceAuthInterceptorRegistrar(
      @Autowired(required = false) RestfulServer restfulServer,
      KeycloakJwtAuthenticationInterceptor authInterceptor,
      ScopeAuthorizationInterceptor authzInterceptor) {
    return () -> {
      if (restfulServer == null) {
        log.warn(
            "No RestfulServer bean found — subscription-service auth interceptors NOT "
                + "registered. This is fine for unit tests, but indicates a packaging "
                + "problem if it happens inside the HAPI image.");
        return;
      }
      restfulServer.registerInterceptor(authInterceptor);
      restfulServer.registerInterceptor(authzInterceptor);
      log.info(
          "Registered subscription-service auth interceptors on HAPI RestfulServer: {}, {}",
          authInterceptor.getClass().getSimpleName(),
          authzInterceptor.getClass().getSimpleName());
    };
  }
}
