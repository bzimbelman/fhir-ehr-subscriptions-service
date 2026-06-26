package com.bzonfhir.subscriptionservice.multitenancy;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.interceptor.model.RequestPartitionId;
import ca.uhn.fhir.rest.api.server.RequestDetails;
import ca.uhn.fhir.rest.server.exceptions.ForbiddenOperationException;
import com.bzonfhir.subscriptionservice.auth.KeycloakJwtAuthenticationInterceptor;
import com.nimbusds.jwt.JWTClaimsSet;

/**
 * HAPI interceptor that maps each request to a {@link RequestPartitionId}.
 *
 * <p>Two modes, switched by {@link MultitenancyProperties#getMode()}:
 *
 * <ul>
 *   <li><b>DISABLED</b> — every request returns {@link RequestPartitionId#defaultPartition()}.
 *       Resources live in HAPI's {@code DEFAULT} partition; URLs look like
 *       {@code /fhir/Patient/123}; partitioning is invisible to subscribers.
 *   <li><b>ENABLED</b> — the JWT's tenant claim (default {@code "tenant"}) names the
 *       partition. Resources are partition-scoped automatically by HAPI's storage layer;
 *       tenants cannot see each other's data. Missing/blank tenant claim is rejected as
 *       HTTP 403.
 * </ul>
 *
 * <p>Hooks onto all three partition pointcuts available in HAPI 7.6.0
 * ({@link Pointcut#STORAGE_PARTITION_IDENTIFY_CREATE},
 * {@link Pointcut#STORAGE_PARTITION_IDENTIFY_READ},
 * {@link Pointcut#STORAGE_PARTITION_IDENTIFY_ANY}). All three delegate to the same logic
 * because the partition shouldn't depend on the operation — a tenant's writes and reads
 * land in the same partition.
 *
 * <p>The JWT is expected to have already been validated by
 * {@link KeycloakJwtAuthenticationInterceptor}, which stashes the decoded
 * {@link JWTClaimsSet} in {@link RequestDetails#getUserData()} under
 * {@link KeycloakJwtAuthenticationInterceptor#USER_DATA_CLAIMS_KEY}.
 */
@Interceptor
public class TenantPartitionInterceptor {

  private static final Logger log = LoggerFactory.getLogger(TenantPartitionInterceptor.class);

  /** Header read in {@link MultitenancyProperties#isTestMode()} mode. TEST-ONLY. */
  public static final String TEST_TENANT_HEADER = "X-Test-Tenant";

  private final MultitenancyProperties props;

  public TenantPartitionInterceptor(MultitenancyProperties props) {
    this.props = props;
    if (props.isTestMode()) {
      log.warn(
          "*** SUBSCRIPTION_SERVICE_MULTITENANCY_TEST_MODE IS ENABLED ***"
              + " Tenant will be read from the '{}' request header, BYPASSING JWT validation."
              + " This MUST NOT be enabled in production.",
          TEST_TENANT_HEADER);
    }
  }

  @Hook(Pointcut.STORAGE_PARTITION_IDENTIFY_CREATE)
  public RequestPartitionId partitionIdentifyCreate(RequestDetails requestDetails) {
    return identifyPartition(requestDetails);
  }

  @Hook(Pointcut.STORAGE_PARTITION_IDENTIFY_READ)
  public RequestPartitionId partitionIdentifyRead(RequestDetails requestDetails) {
    return identifyPartition(requestDetails);
  }

  @Hook(Pointcut.STORAGE_PARTITION_IDENTIFY_ANY)
  public RequestPartitionId partitionIdentifyAny(RequestDetails requestDetails) {
    return identifyPartition(requestDetails);
  }

  /**
   * Core resolution logic. Package-private so the unit test can drive it directly without
   * spinning up a HAPI server or a Spring context.
   */
  RequestPartitionId identifyPartition(RequestDetails requestDetails) {
    if (props.getMode() == MultitenancyProperties.MultitenancyMode.DISABLED) {
      return RequestPartitionId.defaultPartition();
    }

    // ENABLED. Resolve tenant from either the test-only header or the validated JWT.
    String tenant;
    if (props.isTestMode()) {
      tenant = requestDetails == null ? null : requestDetails.getHeader(TEST_TENANT_HEADER);
    } else {
      tenant = extractTenantFromJwt(requestDetails);
    }

    if (tenant == null || tenant.isBlank()) {
      throw new ForbiddenOperationException(
          "tenant claim required when multitenancy enabled");
    }

    if (log.isDebugEnabled()) {
      log.debug("Resolved tenant '{}' for request", tenant);
    }
    return RequestPartitionId.fromPartitionName(tenant.trim());
  }

  private String extractTenantFromJwt(RequestDetails requestDetails) {
    if (requestDetails == null || requestDetails.getUserData() == null) {
      return null;
    }
    Object raw =
        requestDetails
            .getUserData()
            .get(KeycloakJwtAuthenticationInterceptor.USER_DATA_CLAIMS_KEY);
    if (!(raw instanceof JWTClaimsSet claims)) {
      return null;
    }
    Object claim = claims.getClaim(props.getTenantClaim());
    return claim == null ? null : claim.toString();
  }
}
