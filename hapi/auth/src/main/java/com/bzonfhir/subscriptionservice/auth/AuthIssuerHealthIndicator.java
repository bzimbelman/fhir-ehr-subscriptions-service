package com.bzonfhir.subscriptionservice.auth;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.boot.actuate.health.Health;
import org.springframework.boot.actuate.health.HealthIndicator;

/**
 * Health indicator for the OIDC issuer's discovery document (Epic #387, ticket #393).
 *
 * <p>Fetches {@code ${issuer}/.well-known/openid-configuration} with a short
 * timeout. UP on 2xx, DOWN otherwise with the error captured in
 * {@code details.reason}. The endpoint URL is included in {@code details.url}
 * so an operator curling {@code /actuator/health} can see exactly what was
 * probed without grepping configuration.
 *
 * <p>The bean is registered by {@link AuthAutoConfiguration}, which is gated
 * by {@code subscription-service.auth.enabled=true}. When auth is disabled
 * the indicator is never instantiated, so the readiness probe does not
 * include an `authIssuer` block at all — kubelet treats the missing
 * indicator as absent, not as DOWN, which is the right behaviour for the
 * "auth disabled for dev" path.
 */
public class AuthIssuerHealthIndicator implements HealthIndicator {

  private static final Logger log = LoggerFactory.getLogger(AuthIssuerHealthIndicator.class);

  /**
   * Discovery document path appended to the issuer URL. Standardized by
   * OIDC Discovery 1.0 §4 ("OpenID Provider Configuration Request") —
   * every spec-compliant IdP (Keycloak, Auth0, Okta, Azure AD, Cognito,
   * Authentik, …) serves it at this exact suffix.
   */
  static final String DISCOVERY_SUFFIX = "/.well-known/openid-configuration";

  /**
   * Default HTTP probe timeout. 3s gives us enough headroom for a single
   * TLS handshake + small response over a slow network while still
   * fitting comfortably inside the Kubernetes readiness probe budget
   * (5s wall clock).
   */
  static final Duration DEFAULT_TIMEOUT = Duration.ofSeconds(3);

  private final AuthProperties props;
  private final HttpClient httpClient;
  private final Duration timeout;

  public AuthIssuerHealthIndicator(AuthProperties props) {
    this(props, DEFAULT_TIMEOUT);
  }

  /**
   * Package-private constructor used by tests so they can shorten the
   * timeout further or inject an HttpClient bound to a test fixture.
   */
  AuthIssuerHealthIndicator(AuthProperties props, Duration timeout) {
    this.props = props;
    this.timeout = timeout;
    this.httpClient =
        HttpClient.newBuilder()
            .version(HttpClient.Version.HTTP_1_1)
            .connectTimeout(timeout)
            .build();
  }

  @Override
  public Health health() {
    String issuer = props.getIssuer();
    if (issuer == null || issuer.isBlank()) {
      // AuthAutoConfiguration#validateIssuer already fails the application
      // context on startup when auth is enabled + issuer is blank, so this
      // branch is defensive only. Returning UNKNOWN (rather than DOWN)
      // avoids surfacing a misleading reason in the rare case where
      // someone constructs this indicator directly.
      return Health.unknown().withDetail("reason", "issuer not configured").build();
    }
    String url = issuer.replaceAll("/+$", "") + DISCOVERY_SUFFIX;
    HttpRequest request =
        HttpRequest.newBuilder()
            .uri(URI.create(url))
            .timeout(timeout)
            .header("Accept", "application/json")
            .GET()
            .build();
    try {
      HttpResponse<Void> response = httpClient.send(request, HttpResponse.BodyHandlers.discarding());
      int status = response.statusCode();
      if (status >= 200 && status < 300) {
        return Health.up().withDetail("url", url).withDetail("status_code", status).build();
      }
      log.warn("Auth issuer health check returned status={} url={}", status, url);
      return Health.down()
          .withDetail("url", url)
          .withDetail("status_code", status)
          .withDetail("reason", "non-2xx response")
          .build();
    } catch (Exception e) {
      log.warn("Auth issuer health check failed url={} reason={}", url, e.toString());
      return Health.down()
          .withDetail("url", url)
          .withDetail("reason", e.getClass().getSimpleName() + ": " + (e.getMessage() == null ? "" : e.getMessage()))
          .build();
    }
  }
}
