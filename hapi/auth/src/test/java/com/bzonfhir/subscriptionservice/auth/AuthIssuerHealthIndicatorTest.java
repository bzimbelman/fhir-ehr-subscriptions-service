package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Duration;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.actuate.health.Health;
import org.springframework.boot.actuate.health.Status;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.WireMock;

/**
 * Unit tests for {@link AuthIssuerHealthIndicator} (Epic #387, ticket #393).
 *
 * <p>Backed by Wiremock so we exercise the real {@link java.net.http.HttpClient}
 * pipeline rather than a Mockito-stubbed one. The two scenarios cover the
 * UP / DOWN branches the readiness probe relies on:
 *
 * <ol>
 *   <li>200 on the discovery document → UP with status_code=200.
 *   <li>500 on the discovery document → DOWN with reason="non-2xx response".
 * </ol>
 */
class AuthIssuerHealthIndicatorTest {

  private WireMockServer wireMock;
  private AuthProperties props;

  @BeforeEach
  void setUp() {
    wireMock = new WireMockServer(0);
    wireMock.start();
    props = new AuthProperties();
    // The indicator strips trailing slashes; passing the raw localhost url
    // exercises that path as a side benefit.
    props.setIssuer("http://localhost:" + wireMock.port());
  }

  @AfterEach
  void tearDown() {
    if (wireMock != null) {
      wireMock.stop();
    }
  }

  @Test
  void up_when_discoveryReturns200() {
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo(AuthIssuerHealthIndicator.DISCOVERY_SUFFIX))
            .willReturn(
                WireMock.aResponse()
                    .withStatus(200)
                    .withHeader("Content-Type", "application/json")
                    .withBody("{\"issuer\":\"" + props.getIssuer() + "\"}")));

    AuthIssuerHealthIndicator indicator =
        new AuthIssuerHealthIndicator(props, Duration.ofSeconds(2));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.UP);
    assertThat(health.getDetails())
        .containsEntry("status_code", 200)
        .containsEntry("url", props.getIssuer() + AuthIssuerHealthIndicator.DISCOVERY_SUFFIX);
  }

  @Test
  void down_when_discoveryReturns500() {
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo(AuthIssuerHealthIndicator.DISCOVERY_SUFFIX))
            .willReturn(WireMock.aResponse().withStatus(500)));

    AuthIssuerHealthIndicator indicator =
        new AuthIssuerHealthIndicator(props, Duration.ofSeconds(2));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.DOWN);
    assertThat(health.getDetails())
        .containsEntry("status_code", 500)
        .containsEntry("reason", "non-2xx response");
  }

  @Test
  void down_when_endpointUnreachable() {
    // Stop the mock so the connect fails fast.
    int port = wireMock.port();
    wireMock.stop();
    props.setIssuer("http://localhost:" + port);

    AuthIssuerHealthIndicator indicator =
        new AuthIssuerHealthIndicator(props, Duration.ofMillis(500));
    Health health = indicator.health();

    assertThat(health.getStatus()).isEqualTo(Status.DOWN);
    assertThat(health.getDetails()).containsKey("reason");
    assertThat(health.getDetails().get("reason").toString()).contains("Exception");
  }
}
