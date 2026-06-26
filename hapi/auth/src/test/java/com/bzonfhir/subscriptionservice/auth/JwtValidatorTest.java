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
    // OAuth2/OIDC spec is a space-delimited string, but some IdPs and custom mappers
    // emit a JSON array instead. The interceptor accepts either.
    JWTClaimsSet listClaims =
        new JWTClaimsSet.Builder()
            .claim("scope", List.of("system/Patient.r", "system/Observation.r"))
            .build();
    String extracted = OidcJwtAuthenticationInterceptor.extractScopeClaim(listClaims);
    assertThat(extracted).contains("system/Patient.r", "system/Observation.r");
  }

  /**
   * Documentation-as-test (ticket #372): the validator accepts any well-formed issuer
   * URL, including the Auth0 shape (trailing slash, no realm path segment). This is a
   * pure config-parse check — no Keycloak-specific assumptions about the issuer string.
   */
  @Test
  void acceptsAuth0StyleIssuerUrl() throws Exception {
    String auth0Issuer = "https://my-tenant.us.auth0.com/";

    // Build a validator with an explicitly-set JWKS URL pointed at our local Wiremock,
    // so we can drive a real JWT verify against an Auth0-shaped issuer string.
    AuthProperties props = new AuthProperties();
    props.setIssuer(auth0Issuer);
    props.setJwksUrl(issuer + "/protocol/openid-connect/certs"); // local mock JWKS

    JWTClaimsSet claims = JwtTestSupport.defaultClaims(auth0Issuer).build();
    String token = JwtTestSupport.signJwt(keyPair, claims);

    JWTClaimsSet decoded = new JwtValidator(props).validate(token);
    assertThat(decoded.getIssuer()).isEqualTo(auth0Issuer);
  }
}
