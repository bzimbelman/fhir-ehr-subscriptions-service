package com.bzonfhir.subscriptionservice.multitenancy;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;

import java.util.HashMap;
import java.util.Map;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.interceptor.model.RequestPartitionId;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.ForbiddenOperationException;
import com.bzonfhir.subscriptionservice.auth.KeycloakJwtAuthenticationInterceptor;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * Behavioral tests for {@link TenantPartitionInterceptor}.
 *
 * <p>The interceptor is the bridge between authenticated JWT claims and HAPI's partition
 * model. Tests cover both modes (disabled, enabled) and the failure shapes when the JWT is
 * present but the tenant claim is missing/blank.
 */
class TenantPartitionInterceptorTest {

  private MultitenancyProperties props;
  private TenantPartitionInterceptor interceptor;

  @BeforeEach
  void setUp() {
    props = new MultitenancyProperties();
    interceptor = new TenantPartitionInterceptor(props);
  }

  /** Minimal RequestDetails stub with a userData map populated from the supplied claims. */
  private RequestDetails mockRequestWithClaims(JWTClaimsSet claims) {
    RequestDetails rd = mock(RequestDetails.class);
    Map<Object, Object> userData = new HashMap<>();
    if (claims != null) {
      userData.put(KeycloakJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY, claims);
    }
    lenient().when(rd.getUserData()).thenReturn(userData);
    return rd;
  }

  // ---------------------------- DISABLED mode ----------------------------

  @Test
  void disabledMode_returnsDefaultPartitionEvenWithTenantClaim() {
    props.setMode(MultitenancyProperties.MultitenancyMode.DISABLED);
    JWTClaimsSet claims = new JWTClaimsSet.Builder().claim("tenant", "acme").build();
    RequestDetails rd = mockRequestWithClaims(claims);

    RequestPartitionId result = interceptor.identifyPartition(rd);

    assertThat(result.isDefaultPartition()).isTrue();
  }

  @Test
  void disabledMode_returnsDefaultPartitionEvenWithoutClaims() {
    props.setMode(MultitenancyProperties.MultitenancyMode.DISABLED);
    RequestDetails rd = mockRequestWithClaims(null);

    RequestPartitionId result = interceptor.identifyPartition(rd);

    assertThat(result.isDefaultPartition()).isTrue();
  }

  @Test
  void disabledIsDefaultMode() {
    // Confirms the default — a fresh MultitenancyProperties is DISABLED, so deployments
    // that don't set SUBSCRIPTION_SERVICE_MULTITENANCY behave as single-tenant.
    assertThat(new MultitenancyProperties().getMode())
        .isEqualTo(MultitenancyProperties.MultitenancyMode.DISABLED);
  }

  // ---------------------------- ENABLED mode ----------------------------

  @Test
  void enabledMode_jwtWithTenantAcme_returnsPartitionAcme() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    JWTClaimsSet claims = new JWTClaimsSet.Builder().claim("tenant", "acme").build();
    RequestDetails rd = mockRequestWithClaims(claims);

    RequestPartitionId result = interceptor.identifyPartition(rd);

    assertThat(result.getFirstPartitionNameOrNull()).isEqualTo("acme");
  }

  @Test
  void enabledMode_jwtMissingTenantClaim_throwsForbiddenOperationException() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    JWTClaimsSet claims = new JWTClaimsSet.Builder().subject("svc").build(); // no tenant
    RequestDetails rd = mockRequestWithClaims(claims);

    assertThatThrownBy(() -> interceptor.identifyPartition(rd))
        .isInstanceOf(ForbiddenOperationException.class)
        .hasMessageContaining("tenant claim required");
  }

  @Test
  void enabledMode_tenantClaimEmptyString_throwsForbiddenOperationException() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    JWTClaimsSet claims = new JWTClaimsSet.Builder().claim("tenant", "").build();
    RequestDetails rd = mockRequestWithClaims(claims);

    assertThatThrownBy(() -> interceptor.identifyPartition(rd))
        .isInstanceOf(ForbiddenOperationException.class)
        .hasMessageContaining("tenant claim required");
  }

  @Test
  void enabledMode_tenantClaimBlank_throwsForbiddenOperationException() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    JWTClaimsSet claims = new JWTClaimsSet.Builder().claim("tenant", "   ").build();
    RequestDetails rd = mockRequestWithClaims(claims);

    assertThatThrownBy(() -> interceptor.identifyPartition(rd))
        .isInstanceOf(ForbiddenOperationException.class)
        .hasMessageContaining("tenant claim required");
  }

  @Test
  void enabledMode_noClaimsAtAll_throwsForbiddenOperationException() {
    // If auth was disabled or skipped (e.g. anonymous allow-list path), there's no JWT.
    // In ENABLED multitenancy mode that's a 403 — we don't fall back to DEFAULT silently.
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    RequestDetails rd = mockRequestWithClaims(null);

    assertThatThrownBy(() -> interceptor.identifyPartition(rd))
        .isInstanceOf(ForbiddenOperationException.class)
        .hasMessageContaining("tenant claim required");
  }

  @Test
  void enabledMode_customTenantClaim_readsFromCustomKey() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    props.setTenantClaim("org_id");
    JWTClaimsSet claims = new JWTClaimsSet.Builder().claim("org_id", "globex").build();
    RequestDetails rd = mockRequestWithClaims(claims);

    RequestPartitionId result = interceptor.identifyPartition(rd);

    assertThat(result.getFirstPartitionNameOrNull()).isEqualTo("globex");
  }

  @Test
  void enabledMode_tenantClaimDefaultsToTenant() {
    assertThat(new MultitenancyProperties().getTenantClaim()).isEqualTo("tenant");
  }

  // ---------------------------- TEST MODE ----------------------------

  @Test
  void testMode_readsTenantFromHeader() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    props.setTestMode(true);
    RequestDetails rd = mock(RequestDetails.class);
    lenient().when(rd.getHeader("X-Test-Tenant")).thenReturn("acme");
    lenient().when(rd.getUserData()).thenReturn(new HashMap<>());

    RequestPartitionId result = interceptor.identifyPartition(rd);

    assertThat(result.getFirstPartitionNameOrNull()).isEqualTo("acme");
  }

  @Test
  void testMode_missingHeader_throwsForbiddenOperationException() {
    props.setMode(MultitenancyProperties.MultitenancyMode.ENABLED);
    props.setTestMode(true);
    RequestDetails rd = mock(RequestDetails.class);
    lenient().when(rd.getHeader("X-Test-Tenant")).thenReturn(null);
    lenient().when(rd.getUserData()).thenReturn(new HashMap<>());

    assertThatThrownBy(() -> interceptor.identifyPartition(rd))
        .isInstanceOf(ForbiddenOperationException.class)
        .hasMessageContaining("tenant");
  }
}
