package com.bzonfhir.subscriptionservice.auth;

import java.util.Arrays;
import java.util.List;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration knobs for the subscription-service auth layer.
 *
 * <p>Bound from {@code application.yaml} under the prefix {@code subscription-service.auth}.
 * Example (Keycloak shown; any OIDC IdP works — Auth0, Okta, Azure AD, Cognito,
 * Authentik, etc. — substitute the issuer URL):
 *
 * <pre>
 * subscription-service:
 *   auth:
 *     enabled: true
 *     issuer: https://your-idp.example.com/realms/subscription-service
 *     # jwks-url defaults to ${issuer}/protocol/openid-connect/certs (Keycloak shape);
 *     # set it explicitly for IdPs with a different JWKS path (e.g. Auth0's
 *     # /.well-known/jwks.json or Okta's /oauth2/default/v1/keys).
 *     jwks-url: ${subscription-service.auth.issuer}/protocol/openid-connect/certs
 *     allow-anonymous-paths:
 *       - /metadata
 *       - /.well-known/smart-configuration
 * </pre>
 *
 * <p>Designed so that {@code enabled=false} causes the auto-configuration to register
 * NO interceptors — useful for local dev without any OIDC IdP instance, where the
 * upstream HAPI behavior (anonymous access) is fine.
 */
@ConfigurationProperties(prefix = "subscription-service.auth")
public class AuthProperties {

  /**
   * Master toggle. When {@code false} the interceptors are never registered and HAPI behaves
   * as if this JAR were not on the classpath. Default {@code true} — every deployment that
   * pulls the derived image is expected to be running behind an OIDC IdP.
   */
  private boolean enabled = true;

  /**
   * Expected {@code iss} claim on incoming JWTs. The token's {@code iss} MUST exactly match
   * this value or the request is rejected.
   *
   * <p>No default — every deployment MUST supply its own issuer via
   * {@code SUBSCRIPTION_SERVICE_AUTH_ISSUER} (or the equivalent yaml property). When
   * {@link #enabled} is {@code true} and this is null/blank, the auto-configuration
   * fails fast at startup with a clear error (ticket #370). Previously this defaulted to
   * the maintainer's IdP instance, which silently pointed strangers' clones at the
   * wrong issuer.
   */
  private String issuer = null;

  /**
   * JWKS endpoint used to fetch the IdP's signing keys. When not set, derived from the
   * issuer using the Keycloak path ({@code <issuer>/protocol/openid-connect/certs}); set
   * this explicitly for IdPs with a different JWKS path — Auth0 uses
   * {@code <issuer>.well-known/jwks.json}, Okta uses {@code <issuer>/v1/keys}, etc.
   * Operators can always look up the exact value in the IdP's
   * {@code .well-known/openid-configuration} document under the {@code jwks_uri} key.
   */
  private String jwksUrl;

  /**
   * Servlet paths (relative to the FHIR base) that bypass authentication entirely. The CDS
   * Hooks discovery doc and the FHIR {@code CapabilityStatement} are anonymous-by-design
   * per the SMART on FHIR / HL7 conventions; everything else requires a token.
   *
   * <p>Match is a prefix-or-equal check against {@code RequestDetails.getRequestPath()}, NOT
   * against the full URL — HAPI's request-path is already relative to the FHIR base.
   */
  private List<String> allowAnonymousPaths =
      Arrays.asList("metadata", ".well-known/smart-configuration");

  /**
   * Cap on JWKS HTTP fetch latency (ms). IdPs typically rotate signing keys infrequently
   * and Nimbus caches them in-process, so this only matters for the very first request
   * and during a key rollover.
   */
  private int jwksConnectTimeoutMs = 2000;

  /**
   * Cap on JWKS HTTP read latency (ms). See {@link #jwksConnectTimeoutMs}.
   */
  private int jwksReadTimeoutMs = 2000;

  /**
   * Lifetime (ms) of the in-memory JWKS cache. 10 minutes is a reasonable middle ground
   * between freshness and load on the IdP — matches Keycloak's default JWKS cache TTL
   * and is well within the typical signing-key rotation window of every major IdP.
   */
  private long jwksCacheTtlMs = 600_000L;

  // ------------- accessors -------------

  public boolean isEnabled() {
    return enabled;
  }

  public void setEnabled(boolean enabled) {
    this.enabled = enabled;
  }

  public String getIssuer() {
    return issuer;
  }

  public void setIssuer(String issuer) {
    this.issuer = issuer;
  }

  /**
   * Resolves the JWKS URL. Returns the explicitly-configured value if set, otherwise
   * derives it from the issuer using the Keycloak path
   * ({@code <issuer>/protocol/openid-connect/certs}) — fine for Keycloak deployments and
   * for any IdP whose JWKS URL is derivable from the issuer. For everything else (Auth0,
   * Okta, etc.) the operator should set {@code SUBSCRIPTION_SERVICE_AUTH_JWKS_URL}
   * explicitly to the value advertised in the IdP's discovery document.
   */
  public String resolveJwksUrl() {
    if (jwksUrl != null && !jwksUrl.isBlank()) {
      return jwksUrl;
    }
    String base = issuer == null ? "" : issuer.replaceAll("/+$", "");
    return base + "/protocol/openid-connect/certs";
  }

  public String getJwksUrl() {
    return jwksUrl;
  }

  public void setJwksUrl(String jwksUrl) {
    this.jwksUrl = jwksUrl;
  }

  public List<String> getAllowAnonymousPaths() {
    return allowAnonymousPaths;
  }

  public void setAllowAnonymousPaths(List<String> allowAnonymousPaths) {
    this.allowAnonymousPaths = allowAnonymousPaths;
  }

  public int getJwksConnectTimeoutMs() {
    return jwksConnectTimeoutMs;
  }

  public void setJwksConnectTimeoutMs(int jwksConnectTimeoutMs) {
    this.jwksConnectTimeoutMs = jwksConnectTimeoutMs;
  }

  public int getJwksReadTimeoutMs() {
    return jwksReadTimeoutMs;
  }

  public void setJwksReadTimeoutMs(int jwksReadTimeoutMs) {
    this.jwksReadTimeoutMs = jwksReadTimeoutMs;
  }

  public long getJwksCacheTtlMs() {
    return jwksCacheTtlMs;
  }

  public void setJwksCacheTtlMs(long jwksCacheTtlMs) {
    this.jwksCacheTtlMs = jwksCacheTtlMs;
  }
}
