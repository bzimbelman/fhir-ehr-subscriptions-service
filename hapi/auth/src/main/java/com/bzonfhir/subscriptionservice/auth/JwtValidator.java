package com.bzonfhir.subscriptionservice.auth;

import java.net.MalformedURLException;
import java.net.URL;
import java.text.ParseException;
import java.util.Date;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.source.JWKSource;
import com.nimbusds.jose.jwk.source.JWKSourceBuilder;
import com.nimbusds.jose.proc.BadJOSEException;
import com.nimbusds.jose.proc.JWSVerificationKeySelector;
import com.nimbusds.jose.proc.SecurityContext;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nimbusds.jwt.proc.ConfigurableJWTProcessor;
import com.nimbusds.jwt.proc.DefaultJWTClaimsVerifier;
import com.nimbusds.jwt.proc.DefaultJWTProcessor;

/**
 * Validates RS256 / RS384 / RS512 JWTs against a remote JWKS and an expected issuer.
 *
 * <p>Built on Nimbus JOSE+JWT (already bundled in the upstream HAPI image, so this class
 * adds no transitive dependency to the final container). Key rotation is handled by
 * Nimbus's {@code JWKSource}, which caches and refreshes the JWKS in the background.
 *
 * <p>Validation rules applied:
 *
 * <ol>
 *   <li>Token must be a syntactically valid JWS.
 *   <li>Signing algorithm must be one of RS256/RS384/RS512. HS* (symmetric) tokens are
 *       rejected to prevent an attacker from forging a token by guessing/finding the realm
 *       secret.
 *   <li>Signature must verify against a key advertised by the configured JWKS.
 *   <li>{@code iss} claim must equal the configured issuer.
 *   <li>{@code exp} must be in the future; {@code nbf} (if present) must be in the past.
 * </ol>
 *
 * <p>Anything else (audience, scope content) is left to the caller — scopes are inspected
 * separately by {@link ScopeAuthorizationInterceptor}.
 */
public class JwtValidator {

  private final String expectedIssuer;
  private final ConfigurableJWTProcessor<SecurityContext> jwtProcessor;

  /**
   * Builds a validator with default Nimbus key-source caching.
   *
   * @param props auth properties (issuer, JWKS URL, timeouts, cache TTL).
   * @throws IllegalArgumentException if the JWKS URL is malformed.
   */
  public JwtValidator(AuthProperties props) {
    this(props, defaultJwkSource(props));
  }

  /**
   * Test-friendly constructor that accepts an arbitrary {@link JWKSource}. Production code
   * goes through the single-arg constructor; tests inject a JWKS from an in-process mock
   * server.
   */
  public JwtValidator(AuthProperties props, JWKSource<SecurityContext> jwkSource) {
    if (props.getIssuer() == null || props.getIssuer().isBlank()) {
      throw new IllegalArgumentException("subscription-service.auth.issuer must be set");
    }
    this.expectedIssuer = props.getIssuer();

    ConfigurableJWTProcessor<SecurityContext> processor = new DefaultJWTProcessor<>();
    // Accept the three RS algorithms; reject HS* / none / EC unless explicitly asked.
    // RS256 is the OAuth2/OIDC default and is what every major IdP (Keycloak, Auth0,
    // Okta, Azure AD, Cognito, Authentik) signs access tokens with out of the box.
    processor.setJWSKeySelector(
        new JWSVerificationKeySelector<>(
            java.util.Set.of(JWSAlgorithm.RS256, JWSAlgorithm.RS384, JWSAlgorithm.RS512),
            jwkSource));
    // Verify iss + exp + nbf. Audience left unverified — many IdPs (Keycloak, Auth0,
    // Okta) put the client_id in `azp`, not `aud`, for client_credentials grants, so
    // requiring `aud` would break every M2M caller until audiences were configured
    // per-IdP. Operators that need audience enforcement can add their own verifier.
    processor.setJWTClaimsSetVerifier(
        new DefaultJWTClaimsVerifier<>(
            new JWTClaimsSet.Builder().issuer(expectedIssuer).build(),
            java.util.Set.of("exp")));

    this.jwtProcessor = processor;
  }

  private static JWKSource<SecurityContext> defaultJwkSource(AuthProperties props) {
    try {
      URL jwksUrl = new URL(props.resolveJwksUrl());
      return JWKSourceBuilder.create(jwksUrl)
          .retrying(true)
          .cache(props.getJwksCacheTtlMs(), props.getJwksConnectTimeoutMs())
          .build();
    } catch (MalformedURLException e) {
      throw new IllegalArgumentException(
          "Invalid JWKS URL: " + props.resolveJwksUrl(), e);
    }
  }

  /**
   * Validates a bearer token. Returns the verified claims on success.
   *
   * @throws InvalidTokenException with a human-readable reason for any validation failure.
   *     The reason intentionally avoids leaking secrets or token contents — the message is
   *     safe to surface in an OperationOutcome.
   */
  public JWTClaimsSet validate(String bearerToken) throws InvalidTokenException {
    if (bearerToken == null || bearerToken.isBlank()) {
      throw new InvalidTokenException("Authorization header missing or empty");
    }
    SignedJWT jwt;
    try {
      jwt = SignedJWT.parse(bearerToken);
    } catch (ParseException e) {
      throw new InvalidTokenException("Token is not a well-formed JWS");
    }
    try {
      JWTClaimsSet claims = jwtProcessor.process(jwt, null);
      // Belt-and-suspenders: DefaultJWTClaimsVerifier already checks exp; double-check
      // nbf since we didn't list it as required (tokens without nbf are valid).
      Date now = new Date();
      Date nbf = claims.getNotBeforeTime();
      if (nbf != null && nbf.after(now)) {
        throw new InvalidTokenException("Token not yet valid (nbf claim is in the future)");
      }
      return claims;
    } catch (BadJOSEException e) {
      // exp/nbf/iss/signature/key-resolution claims problems. BadJWTException
      // (claims) and BadJOSEException (signature/key) both extend this; we
      // treat them the same way — return a generic "rejected" with reason.
      throw new InvalidTokenException("Token rejected: " + e.getMessage());
    } catch (JOSEException e) {
      // Low-level JOSE failures (crypto provider missing, malformed key, ...).
      throw new InvalidTokenException("Token signature could not be verified");
    }
  }

  /** Signals to the interceptor that a 401 (not 403) should be returned. */
  public static final class InvalidTokenException extends Exception {
    public InvalidTokenException(String message) {
      super(message);
    }
  }
}
