package com.bzonfhir.subscriptionservice.auth;

import java.util.ArrayList;
import java.util.Collection;
import java.util.Collections;
import java.util.List;
import java.util.Set;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.interceptor.auth.AuthorizationInterceptor;
import ca.uhn.fhir.rest.server.interceptor.auth.IAuthRule;
import ca.uhn.fhir.rest.server.interceptor.auth.PolicyEnum;
import ca.uhn.fhir.rest.server.interceptor.auth.RuleBuilder;

/**
 * Translates SMART scopes (already parsed and stashed by
 * {@link KeycloakJwtAuthenticationInterceptor}) into HAPI {@link IAuthRule}s for the current
 * request.
 *
 * <p>The default policy is {@link PolicyEnum#DENY}: any operation NOT explicitly granted by
 * an allow-rule is refused with HTTP 403 + {@code AuthenticationException} (HAPI's
 * authorization interceptor converts unmatched-deny verdicts to that exception class).
 *
 * <p>Anonymous paths (CapabilityStatement / SMART config) never reach this interceptor
 * because {@link KeycloakJwtAuthenticationInterceptor} short-circuits them earlier — but if
 * the upstream interceptor is disabled and a request lands here with no scopes, we ALLOW
 * read of the {@code /metadata} endpoint so HAPI's tester UI stays usable.
 */
public class ScopeAuthorizationInterceptor extends AuthorizationInterceptor {

  private static final Logger log = LoggerFactory.getLogger(ScopeAuthorizationInterceptor.class);

  private final AuthProperties props;

  public ScopeAuthorizationInterceptor(AuthProperties props) {
    super(PolicyEnum.DENY);
    this.props = props;
  }

  @Override
  public List<IAuthRule> buildRuleList(RequestDetails requestDetails) {
    if (!props.isEnabled()) {
      // Auth disabled globally — allow everything. The companion auth interceptor isn't
      // even registered in that case, so this branch only matters if someone wires this
      // bean directly without the auth interceptor.
      return new RuleBuilder().allowAll().build();
    }

    Set<SmartScope> scopes = extractScopes(requestDetails);
    if (scopes.isEmpty()) {
      // Defense in depth: if for any reason a request reaches the authz interceptor
      // without a validated token (e.g. someone removed the auth interceptor), deny it,
      // except for /metadata which is always safe.
      log.warn(
          "ScopeAuthorizationInterceptor invoked with no scopes for path={}, denying",
          requestDetails.getRequestPath());
      return new RuleBuilder()
          .allow("metadata")
          .metadata()
          .andThen()
          .denyAll("no SMART scopes on this request")
          .build();
    }

    return buildRules(scopes);
  }

  /**
   * Pure function: maps a set of scopes to a HAPI rule list. Split out for direct unit
   * testing without needing a {@link RequestDetails}.
   */
  public List<IAuthRule> buildRules(Set<SmartScope> scopes) {
    RuleBuilder rb = new RuleBuilder();
    // Always allow /metadata read; the CapabilityStatement is non-sensitive and HAPI's
    // tester UI fetches it on every page load.
    rb.allow("metadata").metadata();

    for (SmartScope scope : scopes) {
      String type = scope.getResourceType();
      if (scope.allows(SmartScope.Permission.CREATE)) {
        rb.allow("scope:" + type + ".c").create().resourcesOfType(type).withAnyId();
      }
      if (scope.allows(SmartScope.Permission.READ)) {
        rb.allow("scope:" + type + ".r").read().resourcesOfType(type).withAnyId();
      }
      if (scope.allows(SmartScope.Permission.UPDATE)) {
        rb.allow("scope:" + type + ".u").write().resourcesOfType(type).withAnyId();
      }
      if (scope.allows(SmartScope.Permission.DELETE)) {
        rb.allow("scope:" + type + ".d").delete().resourcesOfType(type).withAnyId();
      }
      if (scope.allows(SmartScope.Permission.SEARCH)) {
        // search() in HAPI's rule builder is `allowAll().forResources(type)`, but the
        // canonical pattern in HAPI examples is to allow `read()` which covers GET-by-id
        // AND search/type-level GET. We've already added the read rule above; add an
        // explicit search rule too so a `.s`-only scope (e.g. future `.s` analytics
        // scope) still allows search.
        rb.allow("scope:" + type + ".s").read().resourcesOfType(type).withAnyId();
      }
    }

    rb.denyAll("operation not permitted by SMART scopes");
    return rb.build();
  }

  @SuppressWarnings("unchecked")
  static Set<SmartScope> extractScopes(RequestDetails requestDetails) {
    Object stashed =
        requestDetails.getUserData().get(KeycloakJwtAuthenticationInterceptor.USER_DATA_SCOPES_KEY);
    if (stashed instanceof Set<?> set) {
      // Defensive copy as an immutable collection of SmartScope.
      List<SmartScope> out = new ArrayList<>();
      for (Object item : set) {
        if (item instanceof SmartScope s) {
          out.add(s);
        }
      }
      return Collections.unmodifiableSet(new java.util.LinkedHashSet<>(out));
    }
    if (stashed instanceof Collection<?> coll) {
      List<SmartScope> out = new ArrayList<>();
      for (Object item : coll) {
        if (item instanceof SmartScope s) {
          out.add(s);
        }
      }
      return Collections.unmodifiableSet(new java.util.LinkedHashSet<>(out));
    }
    return Collections.emptySet();
  }
}
