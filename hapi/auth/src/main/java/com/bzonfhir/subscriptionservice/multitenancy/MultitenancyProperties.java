package com.bzonfhir.subscriptionservice.multitenancy;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration knobs for the subscription-service multi-tenancy layer.
 *
 * <p>Bound from {@code application.yaml} under the prefix
 * {@code subscription-service.multitenancy}. Example:
 *
 * <pre>
 * subscription-service:
 *   multitenancy:
 *     mode: enabled                # disabled | enabled. Default: disabled.
 *     tenant-claim: tenant         # JWT claim name to read in ENABLED mode.
 *     test-mode: false             # TEST-ONLY. See {@link #testMode}.
 * </pre>
 *
 * <p>Driven by the env var {@code SUBSCRIPTION_SERVICE_MULTITENANCY} (mapped by Spring's
 * relaxed binding rules onto the {@code mode} field). The default is {@code DISABLED} —
 * deployments that don't set the var see HAPI's {@code DEFAULT} partition for every request
 * and the URLs and behaviour are indistinguishable from a single-tenant deploy.
 *
 * <p>Architectural note: the Postgres schema gets HAPI's {@code partition_id} column either
 * way — the {@code partitioning:} block in {@code application.yaml} is independent of this
 * mode. That's deliberate: it makes a future "convert single-tenant to multi-tenant"
 * migration a no-op (existing rows stay in DEFAULT, new tenants get their own partitions).
 *
 * <p>See {@code docs/multi-tenancy.md} for the operator workflow.
 */
@ConfigurationProperties(prefix = "subscription-service.multitenancy")
public class MultitenancyProperties {

  /**
   * How requests get mapped to HAPI partitions.
   *
   * <ul>
   *   <li>{@link #DISABLED} — every request returns
   *       {@link ca.uhn.fhir.interceptor.model.RequestPartitionId#defaultPartition()}. The
   *       JWT's tenant claim, if any, is ignored. URLs look like {@code /fhir/Patient/123}.
   *   <li>{@link #ENABLED} — read the {@code tenant} claim (or the value of
   *       {@link MultitenancyProperties#getTenantClaim()}) from the validated JWT and map it
   *       to a partition named after the claim. Missing/blank tenant claim => HTTP 403.
   * </ul>
   */
  public enum MultitenancyMode {
    DISABLED,
    ENABLED
  }

  /** Master toggle. See {@link MultitenancyMode}. Default {@link MultitenancyMode#DISABLED}. */
  private MultitenancyMode mode = MultitenancyMode.DISABLED;

  /**
   * JWT claim name to read for the tenant identifier in {@link MultitenancyMode#ENABLED}.
   * Default {@code "tenant"}. Operators can change this to match an existing Keycloak claim
   * mapper (e.g. {@code "org_id"}, {@code "tenant_id"}, {@code "https://my-app/tenant"}).
   */
  private String tenantClaim = "tenant";

  /**
   * TEST-ONLY backdoor. When {@code true}, the interceptor reads the tenant from the request
   * header {@code X-Test-Tenant} INSTEAD of the JWT, bypassing the full JWT validation chain.
   *
   * <p><strong>NEVER enable this in production.</strong> It exists so the e2e suite can
   * demonstrate tenant isolation without standing up a full Keycloak. Setting
   * {@code SUBSCRIPTION_SERVICE_MULTITENANCY_TEST_MODE=true} is enough to make any client
   * choose its own tenant by sending a header. The interceptor logs a loud warning on every
   * startup when it sees this flag set.
   */
  private boolean testMode = false;

  public MultitenancyMode getMode() {
    return mode;
  }

  public void setMode(MultitenancyMode mode) {
    this.mode = mode;
  }

  public String getTenantClaim() {
    return tenantClaim;
  }

  public void setTenantClaim(String tenantClaim) {
    this.tenantClaim = tenantClaim;
  }

  public boolean isTestMode() {
    return testMode;
  }

  public void setTestMode(boolean testMode) {
    this.testMode = testMode;
  }
}
