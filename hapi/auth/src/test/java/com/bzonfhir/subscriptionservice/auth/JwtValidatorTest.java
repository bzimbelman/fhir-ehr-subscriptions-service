package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.security.KeyPair;
import java.util.Date;
import java.util.List;

import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.github.tomakehurst.wiremock.client.WireMock;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * Exercises the full Nimbus JWT pipeline against a Wiremock JWKS endpoint. This is the
 * only test class that goes through the real {@link JwtValidator#JwtValidator(AuthProperties)}
 * constructor — the rest use injected JWKSources.
 */
class JwtValidatorTest {

  private WireMockServer wireMock;
  private KeyPair keyPair;
  private String issuer;

  @BeforeEach
  void setUp() throws Exception {
    wireMock = new WireMockServer(0);
    wireMock.start();
    keyPair = JwtTestSupport.generateRsaKeyPair();
    String publicJwks = JwtTestSupport.publicJwksJson(keyPair);
    wireMock.stubFor(
        WireMock.get(WireMock.urlEqualTo("/protocol/openid-connect/certs"))
            .willReturn(
                WireMock.aResponse()
                    .withHeader("Content-Type", "application/json")
                    .withBody(publicJwks)));
    issuer = "http://localhost:" + wireMock.port();
  }

  @AfterEach
  void tearDown() {
    if (wireMock != null) wireMock.stop();
  }

  private JwtValidator validator() {
    AuthProperties props = new AuthProperties();
    props.setIssuer(issuer);
    props.setJwksUrl(issuer + "/protocol/openid-connect/certs");
    return new JwtValidator(props);
  }

  @Test
  void acceptsValidToken() throws Exception {
    JWTClaimsSet claims =
        JwtTestSupport.defaultClaims(issuer).claim("scope", "system/Patient.r").build();
    String token = JwtTestSupport.signJwt(keyPair, claims);

    JWTClaimsSet decoded = validator().validate(token);

    assertThat(decoded.getIssuer()).isEqualTo(issuer);
    assertThat(decoded.getStringClaim("scope")).isEqualTo("system/Patient.r");
  }

  @Test
  void rejectsExpiredToken() throws Exception {
    long now = System.currentTimeMillis();
    JWTClaimsSet claims =
        JwtTestSupport.defaultClaims(issuer)
            .expirationTime(new Date(now - 60_000))
            .build();
    String token = JwtTestSupport.signJwt(keyPair, claims);

    assertThatThrownBy(() -> validator().validate(token))
        .isInstanceOf(JwtValidator.InvalidTokenException.class)
        .hasMessageContaining("Expired");
  }

  @Test
  void rejectsTokenWithWrongIssuer() throws Exception {
    JWTClaimsSet claims =
        JwtTestSupport.defaultClaims("https://attacker.example.com").build();
    String token = JwtTestSupport.signJwt(keyPair, claims);

    assertThatThrownBy(() -> validator().validate(token))
        .isInstanceOf(JwtValidator.InvalidTokenException.class)
        .hasMessageContaining("iss");
  }

  @Test
  void rejectsTokenSignedByUnknownKey() throws Exception {
    // Token signed by a different RSA pair that the JWKS endpoint doesn't publish.
    KeyPair other = JwtTestSupport.generateRsaKeyPair();
    JWTClaimsSet claims = JwtTestSupport.defaultClaims(issuer).build();
    String token = JwtTestSupport.signJwt(other, claims);

    assertThatThrownBy(() -> validator().validate(token))
        .isInstanceOf(JwtValidator.InvalidTokenException.class);
  }

  @Test
  void rejectsMalformedToken() {
    assertThatThrownBy(() -> validator().validate("not.a.jwt.because.too.many.dots"))
        .isInstanceOf(JwtValidator.InvalidTokenException.class)
        .hasMessageContaining("well-formed");
  }

  @Test
  void rejectsNullOrEmptyToken() {
    assertThatThrownBy(() -> validator().validate(null))
        .isInstanceOf(JwtValidator.InvalidTokenException.class);
    assertThatThrownBy(() -> validator().validate(""))
        .isInstanceOf(JwtValidator.InvalidTokenException.class);
    assertThatThrownBy(() -> validator().validate("   "))
        .isInstanceOf(JwtValidator.InvalidTokenException.class);
  }

  @Test
  void rejectsTokenWithNbfInFuture() throws Exception {
    long now = System.currentTimeMillis();
    JWTClaimsSet claims =
        JwtTestSupport.defaultClaims(issuer)
            .notBeforeTime(new Date(now + 60_000))
            .build();
    String token = JwtTestSupport.signJwt(keyPair, claims);

    assertThatThrownBy(() -> validator().validate(token))
        .isInstanceOf(JwtValidator.InvalidTokenException.class);
  }

  @Test
  void extractScopeClaimHandlesListShape() {
    // Keycloak default is a space-delimited string, but some deployments use a JSON array.
    JWTClaimsSet listClaims =
        new JWTClaimsSet.Builder()
            .claim("scope", List.of("system/Patient.r", "system/Observation.r"))
            .build();
    String extracted = KeycloakJwtAuthenticationInterceptor.extractScopeClaim(listClaims);
    assertThat(extracted).contains("system/Patient.r", "system/Observation.r");
  }
}
