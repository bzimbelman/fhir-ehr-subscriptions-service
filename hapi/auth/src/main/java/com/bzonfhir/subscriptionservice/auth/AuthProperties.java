package com.bzonfhir.subscriptionservice.auth;

import java.util.Arrays;
import java.util.List;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration knobs for the subscription-service auth layer.
 *
 * <p>Bound from {@code application.yaml} under the prefix {@code subscription-service.auth}.
 * Example:
 *
 * <pre>
 * subscription-service:
 *   auth:
 *     enabled: true
 *     issuer: https://keycloak.bzonfhir.com/auth/realms/subscription-service
 *     jwks-url: ${subscription-service.auth.issuer}/protocol/openid-connect/certs
 *     allow-anonymous-paths:
 *       - /metadata
 *       - /.well-known/smart-configuration
 * </pre>
 *
 * <p>Designed so that {@code enabled=false} causes the auto-configuration to register
 * NO interceptors — useful for local dev without a Keycloak instance, where the
 * upstream HAPI behavior (anonymous access) is fine.
 */
@ConfigurationProperties(prefix = "subscription-service.auth")
public class AuthProperties {

  /**
   * Master toggle. When {@code false} the interceptors are never registered and HAPI behaves
   * as if this JAR were not on the classpath. Default {@code true} — every deployment that
   * pulls the derived image is expected to be running behind Keycloak.
   */
  private boolean enabled = true;

  /**
   * Expected {@code iss} claim on incoming JWTs. The token's {@code iss} MUST exactly match
   * this value or the request is rejected.
   *
   * <p>Default matches the Keycloak realm provisioned by ticket #358 on the WildFly-path
   * Keycloak instance at zdock. Override via env
   * {@code SUBSCRIPTION_SERVICE_AUTH_ISSUER} for other deployments.
   */
  private String issuer = "https://keycloak.bzonfhir.com/auth/realms/subscription-service";

  /**
   * JWKS endpoint used to fetch the realm's signing keys. Defaults to the standard Keycloak
   * path under {@link #issuer}. Override if Keycloak is exposed on an unusual path.
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
   * Cap on JWKS HTTP fetch latency (ms). Keycloak rotates keys infrequently and Nimbus
   * caches them, so this only matters for the very first request and during a key rollover.
   */
  private int jwksConnectTimeoutMs = 2000;

  /**
   * Cap on JWKS HTTP read latency (ms). See {@link #jwksConnectTimeoutMs}.
   */
  private int jwksReadTimeoutMs = 2000;

  /** Lifetime (ms) of the in-memory JWKS cache. 10 minutes mirrors Keycloak's default. */
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
   * Resolves the JWKS URL. Returns the explicitly-configured value if set, otherwise derives
   * it from the issuer in the standard Keycloak shape ({@code <issuer>/protocol/openid-connect/certs}).
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
