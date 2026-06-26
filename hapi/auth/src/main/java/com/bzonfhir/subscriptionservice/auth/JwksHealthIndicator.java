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

import com.nimbusds.jose.jwk.JWKSet;

/**
 * Health indicator for the OIDC IdP's JWKS endpoint (Epic #387, ticket #393).
 *
 * <p>Fetches the JWKS, parses it, and asserts at least one signing key is
 * present. The "at least one key" check is the real signal — a 200
 * response that returns {@code {"keys":[]}} after a botched key rotation
 * leaves HAPI unable to validate ANY incoming JWT, so we need to fail
 * readiness in that case even though the HTTP status was fine.
 *
 * <p>Uses the JDK 11+ {@link HttpClient} + Nimbus {@link JWKSet#parse(String)}
 * (already on the HAPI classpath via the auth JAR's {@code nimbus-jose-jwt}
 * dep), so this indicator brings no new transitive runtime dependency.
 *
 * <p>Bean is gated by the same {@link AuthAutoConfiguration#ConditionalOnProperty}
 * as the rest of the auth layer — when {@code subscription-service.auth.enabled=false}
 * the indicator is never instantiated.
 */
public class JwksHealthIndicator implements HealthIndicator {

  private static final Logger log = LoggerFactory.getLogger(JwksHealthIndicator.class);

  /**
   * Default HTTP probe timeout (3s). Matches
   * {@link AuthIssuerHealthIndicator#DEFAULT_TIMEOUT}.
   */
  static final Duration DEFAULT_TIMEOUT = Duration.ofSeconds(3);

  private final AuthProperties props;
  private final HttpClient httpClient;
  private final Duration timeout;

  public JwksHealthIndicator(AuthProperties props) {
    this(props, DEFAULT_TIMEOUT);
  }

  /** Package-private test constructor — see {@link AuthIssuerHealthIndicator}. */
  JwksHealthIndicator(AuthProperties props, Duration timeout) {
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
    String url = props.resolveJwksUrl();
    if (url == null || url.isBlank()) {
      return Health.unknown().withDetail("reason", "jwks url not configured").build();
    }
    HttpRequest request =
        HttpRequest.newBuilder()
            .uri(URI.create(url))
            .timeout(timeout)
            .header("Accept", "application/json")
            .GET()
            .build();
    try {
      HttpResponse<String> response = httpClient.send(request, HttpResponse.BodyHandlers.ofString());
      int status = response.statusCode();
      if (status < 200 || status >= 300) {
        log.warn("JWKS health check returned status={} url={}", status, url);
        return Health.down()
            .withDetail("url", url)
            .withDetail("status_code", status)
            .withDetail("reason", "non-2xx response")
            .build();
      }
      JWKSet keyset;
      try {
        keyset = JWKSet.parse(response.body());
      } catch (Exception parseErr) {
        log.warn("JWKS health check failed to parse body url={} reason={}", url, parseErr.toString());
        return Health.down()
            .withDetail("url", url)
            .withDetail("reason", "malformed JWKS: " + parseErr.getClass().getSimpleName())
            .build();
      }
      int keyCount = keyset.getKeys().size();
      if (keyCount == 0) {
        log.warn("JWKS health check found empty keyset url={}", url);
        return Health.down()
            .withDetail("url", url)
            .withDetail("status_code", status)
            .withDetail("key_count", 0)
            .withDetail("reason", "JWKS contains no signing keys")
            .build();
      }
      return Health.up()
          .withDetail("url", url)
          .withDetail("status_code", status)
          .withDetail("key_count", keyCount)
          .build();
    } catch (Exception e) {
      log.warn("JWKS health check failed url={} reason={}", url, e.toString());
      return Health.down()
          .withDetail("url", url)
          .withDetail("reason", e.getClass().getSimpleName() + ": " + (e.getMessage() == null ? "" : e.getMessage()))
          .build();
    }
  }
}
