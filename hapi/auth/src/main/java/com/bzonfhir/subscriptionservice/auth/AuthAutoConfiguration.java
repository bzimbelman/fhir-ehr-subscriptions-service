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
 * for local development against the docker-compose stack without any OIDC IdP running.
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
   * Error message thrown when auth is enabled but no issuer is configured. Documented as
   * a constant so tests can pin the wording (ticket #370). Mention BOTH the env-var fix
   * (production) and the disable-for-dev escape hatch (laptops, CI) so an operator can
   * resolve it without reading source.
   */
  static final String ISSUER_REQUIRED_MESSAGE =
      "subscription-service.auth.issuer is required when auth is enabled. "
          + "Set SUBSCRIPTION_SERVICE_AUTH_ISSUER to your OIDC provider's issuer URL "
          + "(e.g., https://your-idp.example.com/realms/<realm> for Keycloak, "
          + "https://<tenant>.us.auth0.com/ for Auth0, "
          + "https://<org>.okta.com/oauth2/default for Okta) "
          + "or set SUBSCRIPTION_SERVICE_AUTH_ENABLED=false for local dev.";

  /**
   * Validates incoming JWTs against the configured OIDC provider's JWKS. Singleton: the
   * underlying Nimbus {@code JWKSource} caches the JWKS and refreshes it on its own
   * schedule, so reusing one instance across requests is correct and cheap.
   *
   * <p>This bean's factory method is also where we fail-fast on a missing issuer
   * (ticket #370). Throwing here aborts Spring's context refresh, which in turn causes
   * the Spring Boot launcher to exit non-zero — so the HAPI container restarts in a loop
   * with the documented error in its logs, instead of starting up against the wrong
   * issuer (which would silently 401 every request from the configured IdP).
   */
  @Bean
  public JwtValidator jwtValidator(AuthProperties props) {
    validateIssuer(props);
    log.info(
        "Subscription-service auth enabled: issuer={} jwks={}",
        props.getIssuer(),
        props.resolveJwksUrl());
    return new JwtValidator(props);
  }

  /**
   * Package-private so the unit test can call it directly, but the production path is
   * always through {@link #jwtValidator(AuthProperties)}.
   */
  static void validateIssuer(AuthProperties props) {
    String issuer = props.getIssuer();
    if (issuer == null || issuer.isBlank()) {
      throw new IllegalStateException(ISSUER_REQUIRED_MESSAGE);
    }
  }

  @Bean
  public OidcJwtAuthenticationInterceptor oidcJwtAuthenticationInterceptor(
      AuthProperties props, JwtValidator validator) {
    return new OidcJwtAuthenticationInterceptor(props, validator);
  }

  @Bean
  public ScopeAuthorizationInterceptor scopeAuthorizationInterceptor(AuthProperties props) {
    return new ScopeAuthorizationInterceptor(props);
  }

  /**
   * Health indicator that probes the OIDC issuer's discovery document
   * (Epic #387, ticket #393). Wired into the HAPI {@code readiness} probe
   * group via the {@code management.endpoint.health.group.readiness}
   * include list — see hapi/application.yaml.
   *
   * <p>Bean name {@code authIssuer} is derived from the method name with
   * the {@code healthIndicator} suffix stripped (Spring Boot's
   * {@code HealthEndpoint} contribution-key convention). Don't rename the
   * method without also updating the include list in hapi/application.yaml
   * and the configmap-hapi.yaml override.
   */
  @Bean
  public AuthIssuerHealthIndicator authIssuerHealthIndicator(AuthProperties props) {
    return new AuthIssuerHealthIndicator(props);
  }

  /**
   * Health indicator that fetches the JWKS endpoint, parses it, and
   * asserts at least one signing key is present. Same bean-naming
   * caveats as {@link #authIssuerHealthIndicator(AuthProperties)} —
   * bean name {@code jwks} must stay in sync with the readiness
   * include list.
   */
  @Bean
  public JwksHealthIndicator jwksHealthIndicator(AuthProperties props) {
    return new JwksHealthIndicator(props);
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
      OidcJwtAuthenticationInterceptor authInterceptor,
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
