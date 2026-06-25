package com.bzonfhir.subscription_service.auth;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.Set;

import org.junit.jupiter.api.Test;

class SmartScopeTest {

  @Test
  void parsesCrudsFlagsInAnyOrder() {
    SmartScope s = SmartScope.parse("system/Patient.cruds");
    assertThat(s).isNotNull();
    assertThat(s.getResourceType()).isEqualTo("Patient");
    assertThat(s.getPermissions())
        .containsExactlyInAnyOrder(
            SmartScope.Permission.CREATE,
            SmartScope.Permission.READ,
            SmartScope.Permission.UPDATE,
            SmartScope.Permission.DELETE,
            SmartScope.Permission.SEARCH);
  }

  @Test
  void parsesReadOnlyScope() {
    SmartScope s = SmartScope.parse("system/Patient.r");
    assertThat(s.getPermissions())
        .containsExactly(SmartScope.Permission.READ)
        .doesNotContain(
            SmartScope.Permission.CREATE,
            SmartScope.Permission.UPDATE,
            SmartScope.Permission.DELETE);
  }

  @Test
  void parsesSubscriptionCrusFromDocs() {
    // The docs/auth.md catalog lists this exact scope. Mapping is canonical:
    // c, r, u, s — no delete.
    SmartScope s = SmartScope.parse("system/Subscription.crus");
    assertThat(s).isNotNull();
    assertThat(s.allows(SmartScope.Permission.CREATE)).isTrue();
    assertThat(s.allows(SmartScope.Permission.READ)).isTrue();
    assertThat(s.allows(SmartScope.Permission.UPDATE)).isTrue();
    assertThat(s.allows(SmartScope.Permission.SEARCH)).isTrue();
    assertThat(s.allows(SmartScope.Permission.DELETE)).isFalse();
  }

  @Test
  void rejectsUserScopesAndPatientScopes() {
    // Only system/ is supported in v1. patient/ and user/ scopes return null so the caller
    // ignores them rather than 400'ing the request.
    assertThat(SmartScope.parse("user/Patient.read")).isNull();
    assertThat(SmartScope.parse("patient/Observation.read")).isNull();
  }

  @Test
  void rejectsMalformedScopes() {
    assertThat(SmartScope.parse(null)).isNull();
    assertThat(SmartScope.parse("")).isNull();
    assertThat(SmartScope.parse("   ")).isNull();
    assertThat(SmartScope.parse("system/Patient")).isNull(); // no flags
    assertThat(SmartScope.parse("system/Patient.")).isNull();
    assertThat(SmartScope.parse("system/Patient.x")).isNull(); // unknown flag
    assertThat(SmartScope.parse("system/.r")).isNull(); // no resource type
    assertThat(SmartScope.parse("system/patient.r")).isNull(); // lowercase resource type
  }

  @Test
  void parseAllHandlesSpaceDelimitedScopeClaim() {
    Set<SmartScope> scopes =
        SmartScope.parseAll("openid system/Patient.r system/Subscription.crus profile");
    // openid + profile are not SMART resource scopes — silently dropped.
    assertThat(scopes).hasSize(2);
    assertThat(scopes)
        .extracting(SmartScope::getResourceType)
        .containsExactlyInAnyOrder("Patient", "Subscription");
  }

  @Test
  void parseAllHandlesTabsAndNewlines() {
    Set<SmartScope> scopes =
        SmartScope.parseAll("system/Patient.r\tsystem/Observation.r\n  system/Patient.cruds");
    assertThat(scopes).hasSize(3);
  }

  @Test
  void parseAllReturnsEmptyForNullOrBlank() {
    assertThat(SmartScope.parseAll(null)).isEmpty();
    assertThat(SmartScope.parseAll("")).isEmpty();
    assertThat(SmartScope.parseAll("   \t\n  ")).isEmpty();
    assertThat(SmartScope.parseAll("nothing-recognized here either")).isEmpty();
  }

  @Test
  void canonicalToString() {
    // The toString output uses canonical CRUDS order regardless of input order.
    SmartScope s = SmartScope.parse("system/Patient.sruc");
    assertThat(s).isNotNull();
    assertThat(s.toString()).isEqualTo("system/Patient.crus");
  }

  @Test
  void equalsAndHashCode() {
    SmartScope a = SmartScope.parse("system/Patient.r");
    SmartScope b = SmartScope.parse("system/Patient.r");
    SmartScope c = SmartScope.parse("system/Patient.cr");
    assertThat(a).isEqualTo(b);
    assertThat(a.hashCode()).isEqualTo(b.hashCode());
    assertThat(a).isNotEqualTo(c);
  }
}
