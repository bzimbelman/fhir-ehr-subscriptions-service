package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;

import java.security.KeyPair;
import java.time.Duration;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.actuate.health.Health;
import org.springframework.boot.actuate.health.Status;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.WireMock;

/**
 * Unit tests for {@link JwksHealthIndicator} (Epic #387, ticket #393).
 *
 * <p>Three scenarios:
 *
 * <ol>
 *   <li>JWKS returns a valid keyset → UP with key_count=1.
 *   <li>JWKS returns 500 → DOWN with reason="non-2xx response".
 *   <li>JWKS returns 200 with an empty keys array → DOWN with reason about
 *       "no signing keys".
 * </ol>
 */
class JwksHealthIndicatorTest {

  private static final String JWKS_PATH = "/protocol/openid-connect/certs";

  private WireMockServer wireMock;
  private AuthProperties props;

  @BeforeEach
  void setUp() {
    wireMock = new WireMockServer(0);
    wireMock.start();
    props = new AuthProperties();
    props.setIssuer("http://localhost:" + wireMock.port());
    props.setJwksUrl("http://localhost:" + wireMock.port() + JWKS_PATH);
  }

  @AfterEach
  void tearDown() {
    if (wireMock != null) {
      wireMock.stop();
    }
  }

  @Test
  void up_when_jwksReturnsValidKeyset() throws Exception {
    KeyPair kp = JwtTestSupport.generateRsaKeyPair();
    String publicJwks = JwtTestSupport.publicJwksJson(kp);
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo(JWKS_PATH))
            .willReturn(
                WireMock.aResponse()
                    .withStatus(200)
                    .withHeader("Content-Type", "application/json")
                    .withBody(publicJwks)));

    JwksHealthIndicator indicator = new JwksHealthIndicator(props, Duration.ofSeconds(2));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.UP);
    assertThat(health.getDetails()).containsEntry("status_code", 200);
    assertThat(health.getDetails().get("key_count").toString()).isEqualTo("1");
  }

  @Test
  void down_when_jwksReturns500() {
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo(JWKS_PATH))
            .willReturn(WireMock.aResponse().withStatus(500)));

    JwksHealthIndicator indicator = new JwksHealthIndicator(props, Duration.ofSeconds(2));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.DOWN);
    assertThat(health.getDetails())
        .containsEntry("status_code", 500)
        .containsEntry("reason", "non-2xx response");
  }

  @Test
  void down_when_jwksReturnsEmptyKeyset() {
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo(JWKS_PATH))
            .willReturn(
                WireMock.aResponse()
                    .withStatus(200)
                    .withHeader("Content-Type", "application/json")
                    .withBody("{\"keys\":[]}")));

    JwksHealthIndicator indicator = new JwksHealthIndicator(props, Duration.ofSeconds(2));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.DOWN);
    assertThat(health.getDetails())
        .containsEntry("status_code", 200)
        .containsEntry("key_count", 0)
        .containsEntry("reason", "JWKS contains no signing keys");
  }
}
