package com.bzonfhir.subscriptionservice.auth;

import java.util.List;
import java.util.Locale;
import java.util.Set;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.AuthenticationException;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.JWTClaimsSet.Builder;

/**
 * HAPI interceptor that requires a valid Keycloak-issued JWT on every FHIR request, with
 * a small allow-list of anonymous paths (CapabilityStatement, SMART config discovery).
 *
 * <p>Hooks into {@link Pointcut#SERVER_INCOMING_REQUEST_POST_PROCESSED} so HAPI has parsed
 * the request path but not yet dispatched to a provider. On success, the decoded
 * {@link JWTClaimsSet} is stashed in {@code RequestDetails.userData} under
 * {@link #USER_DATA_CLAIMS_KEY} and the {@code scope} claim under
 * {@link #USER_DATA_SCOPES_KEY} so {@link ScopeAuthorizationInterceptor} can read them
 * without re-parsing the token.
 *
 * <p>Failures throw {@link AuthenticationException}; HAPI translates that to an HTTP 401
 * with an {@code OperationOutcome} body. The exception message is the one returned by
 * {@link JwtValidator} — safe to surface, no secrets leaked.
 */
@Interceptor
public class KeycloakJwtAuthenticationInterceptor {

  /** Key under which the validated claims set is stashed in {@code RequestDetails.userData}. */
  public static final String USER_DATA_CLAIMS_KEY = "subscription-service.auth.claims";

  /**
   * Key under which the parsed scopes ({@link Set} of {@link SmartScope}) are stashed in
   * {@code RequestDetails.userData}. Pre-parsed so the authz interceptor doesn't repeat the
   * regex on every request.
   */
  public static final String USER_DATA_SCOPES_KEY = "subscription-service.auth.scopes";

  private static final Logger log =
      LoggerFactory.getLogger(KeycloakJwtAuthenticationInterceptor.class);
  private static final String BEARER_PREFIX = "bearer ";

  private final AuthProperties props;
  private final JwtValidator validator;

  public KeycloakJwtAuthenticationInterceptor(AuthProperties props, JwtValidator validator) {
    this.props = props;
    this.validator = validator;
  }

  /**
   * Pointcut chosen so that:
   *
   * <ul>
   *   <li>HAPI has computed the request path (we need it to apply the anonymous allow-list).
   *   <li>No resource provider has run yet (so an auth failure cleanly short-circuits the
   *       request).
   *   <li>{@link RequestDetails#getUserData()} is available for downstream interceptors.
   * </ul>
   */
  @Hook(Pointcut.SERVER_INCOMING_REQUEST_POST_PROCESSED)
  public boolean authenticate(RequestDetails requestDetails) {
    if (!props.isEnabled()) {
      return true;
    }

    String path = normalize(requestDetails.getRequestPath());
    if (isAnonymousAllowed(path)) {
      log.debug("Allowing anonymous request to {}", path);
      return true;
    }

    String header = requestDetails.getHeader("Authorization");
    if (header == null || header.isBlank()) {
      throw new AuthenticationException(
          "Authorization header required: provide 'Bearer <token>'");
    }
    if (header.length() <= BEARER_PREFIX.length()
        || !header.substring(0, BEARER_PREFIX.length())
            .toLowerCase(Locale.ROOT)
            .equals(BEARER_PREFIX)) {
      throw new AuthenticationException(
          "Authorization header must use the 'Bearer' scheme");
    }
    String token = header.substring(BEARER_PREFIX.length()).trim();

    JWTClaimsSet claims;
    try {
      claims = validator.validate(token);
    } catch (JwtValidator.InvalidTokenException e) {
      log.info("Rejected request to {}: {}", path, e.getMessage());
      throw new AuthenticationException(e.getMessage());
    }

    requestDetails.getUserData().put(USER_DATA_CLAIMS_KEY, claims);
    requestDetails
        .getUserData()
        .put(USER_DATA_SCOPES_KEY, SmartScope.parseAll(extractScopeClaim(claims)));

    if (log.isDebugEnabled()) {
      log.debug(
          "Authenticated request to {} (sub={}, azp={}, scopes={})",
          path,
          claims.getSubject(),
          claims.getClaim("azp"),
          claims.getClaim("scope"));
    }
    return true;
  }

  /**
   * Keycloak places the scope list in a string-valued {@code scope} claim by default; some
   * deployments configure it as a list. Handle both.
   */
  static String extractScopeClaim(JWTClaimsSet claims) {
    Object raw = claims.getClaim("scope");
    if (raw == null) {
      return "";
    }
    if (raw instanceof String s) {
      return s;
    }
    if (raw instanceof List<?> list) {
      StringBuilder sb = new StringBuilder();
      for (Object item : list) {
        if (item == null) continue;
        if (sb.length() > 0) sb.append(' ');
        sb.append(item.toString());
      }
      return sb.toString();
    }
    return raw.toString();
  }

  private boolean isAnonymousAllowed(String path) {
    if (props.getAllowAnonymousPaths() == null) {
      return false;
    }
    for (String allowed : props.getAllowAnonymousPaths()) {
      if (allowed == null || allowed.isBlank()) continue;
      String norm = normalize(allowed);
      // Exact or prefix match — covers e.g. "/metadata" against requestPath "metadata".
      if (path.equals(norm) || path.startsWith(norm + "/")) {
        return true;
      }
    }
    return false;
  }

  private static String normalize(String path) {
    if (path == null) return "";
    String p = path.trim();
    while (p.startsWith("/")) {
      p = p.substring(1);
    }
    while (p.endsWith("/") && p.length() > 1) {
      p = p.substring(0, p.length() - 1);
    }
    return p;
  }

  /** Test seam — wraps the Nimbus {@link JWTClaimsSet} builder to keep tests readable. */
  static Builder claimsBuilder() {
    return new Builder();
  }
}
