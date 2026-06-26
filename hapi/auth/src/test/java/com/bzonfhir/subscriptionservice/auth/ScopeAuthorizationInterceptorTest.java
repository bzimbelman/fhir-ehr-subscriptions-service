package com.bzonfhir.subscriptionservice.auth;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.HashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

import org.junit.jupiter.api.Test;
import org.mockito.Mockito;

import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.interceptor.auth.IAuthRule;

/**
 * Behavioral tests for the scope → rule mapping. We assert on the produced
 * {@link IAuthRule} list rather than on a fake HAPI request because HAPI's rule application
 * machinery is well-tested upstream and exercising it requires a full RestfulServer.
 *
 * <p>What we DO want to verify here:
 *
 * <ul>
 *   <li>Every documented scope produces at least the right verbs.
 *   <li>An empty scope set produces a deny-all-but-metadata rule list.
 *   <li>{@code subscription-service.auth.enabled=false} produces a single allow-all rule.
 * </ul>
 */
class ScopeAuthorizationInterceptorTest {

  private ScopeAuthorizationInterceptor newInterceptor(boolean enabled) {
    AuthProperties props = new AuthProperties();
    props.setEnabled(enabled);
    return new ScopeAuthorizationInterceptor(props);
  }

  @Test
  void mapsPatientReadScopeToSingleResourceRule() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Patient.r"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);

    assertRuleNames(rules)
        .contains("scope:Patient.r")
        .doesNotContain("scope:Patient.c", "scope:Patient.u", "scope:Patient.d");
  }

  @Test
  void mapsPatientCrudsToFullSet() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Patient.cruds"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    assertRuleNames(rules)
        .contains(
            "scope:Patient.c",
            "scope:Patient.r",
            "scope:Patient.u",
            "scope:Patient.d",
            "scope:Patient.s");
  }

  @Test
  void mapsSubscriptionCrusOmitsDelete() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Subscription.crus"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    assertRuleNames(rules)
        .contains(
            "scope:Subscription.c",
            "scope:Subscription.r",
            "scope:Subscription.u",
            "scope:Subscription.s")
        .doesNotContain("scope:Subscription.d");
  }

  @Test
  void mapsObservationReadOnlyScope() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Observation.r"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    assertRuleNames(rules)
        .contains("scope:Observation.r")
        .doesNotContain("scope:Observation.c", "scope:Observation.u");
  }

  @Test
  void multipleScopesProduceUnionOfRules() {
    Set<SmartScope> scopes =
        new LinkedHashSet<>(
            List.of(
                SmartScope.parse("system/Patient.r"),
                SmartScope.parse("system/Subscription.crus"),
                SmartScope.parse("system/Observation.r")));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    assertRuleNames(rules)
        .contains(
            "scope:Patient.r",
            "scope:Subscription.c",
            "scope:Subscription.r",
            "scope:Subscription.u",
            "scope:Subscription.s",
            "scope:Observation.r");
  }

  @Test
  void alwaysAllowsMetadata() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Patient.r"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    assertRuleNames(rules).contains("metadata");
  }

  @Test
  void terminatesWithDenyAll() {
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Patient.r"));
    List<IAuthRule> rules = newInterceptor(true).buildRules(scopes);
    // Last rule should be the catch-all deny.
    assertThat(rules.get(rules.size() - 1).getName())
        .isEqualTo("operation not permitted by SMART scopes");
  }

  @Test
  void emptyScopeSetProducesDenyAllExceptMetadata() {
    RequestDetails rd = Mockito.mock(RequestDetails.class);
    Map<Object, Object> userData = new HashMap<>();
    Mockito.when(rd.getUserData()).thenReturn(userData);
    Mockito.when(rd.getRequestPath()).thenReturn("Patient");

    List<IAuthRule> rules = newInterceptor(true).buildRuleList(rd);
    assertRuleNames(rules).contains("metadata", "no SMART scopes on this request");
  }

  @Test
  void disabledAuthAllowsAll() {
    RequestDetails rd = Mockito.mock(RequestDetails.class);
    Map<Object, Object> userData = new HashMap<>();
    userData.put(
        KeycloakJwtAuthenticationInterceptor.USER_DATA_SCOPES_KEY, Set.<SmartScope>of());
    Mockito.when(rd.getUserData()).thenReturn(userData);

    List<IAuthRule> rules = newInterceptor(false).buildRuleList(rd);
    // The exact rule shape isn't important — what matters is that at least one rule
    // exists, and there is no terminating deny.
    assertThat(rules).isNotEmpty();
    assertThat(rules.get(rules.size() - 1).getName())
        .isNotEqualTo("operation not permitted by SMART scopes");
  }

  @Test
  void buildRuleListReadsScopesFromRequestUserData() {
    RequestDetails rd = Mockito.mock(RequestDetails.class);
    Map<Object, Object> userData = new HashMap<>();
    Set<SmartScope> scopes = Set.of(SmartScope.parse("system/Patient.cruds"));
    userData.put(KeycloakJwtAuthenticationInterceptor.USER_DATA_SCOPES_KEY, scopes);
    Mockito.when(rd.getUserData()).thenReturn(userData);

    List<IAuthRule> rules = newInterceptor(true).buildRuleList(rd);
    assertRuleNames(rules).contains("scope:Patient.c", "scope:Patient.r", "scope:Patient.d");
  }

  // ----------------- helpers -----------------

  private static org.assertj.core.api.ListAssert<String> assertRuleNames(
      List<IAuthRule> rules) {
    return assertThat(rules.stream().map(IAuthRule::getName).toList());
  }
}
