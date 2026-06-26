package com.bzonfhir.subscriptionservice.audit;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration knobs for the FHIR {@code AuditEvent} generator (ticket #391, Epic #387).
 *
 * <p>Bound from {@code application.yaml} under the prefix
 * {@code subscription-service.audit}. Example:
 *
 * <pre>
 * subscription-service:
 *   audit:
 *     enabled: true
 *     capture-reads: false
 *     capture-search: false
 *     retention-days: 365
 * </pre>
 *
 * <p>Defaults are tuned for production paranoia: writes are always audited; reads/searches
 * are opt-in because they're dominant in volume and only some deployments need them
 * for compliance.
 */
@ConfigurationProperties(prefix = "subscription-service.audit")
public class AuditProperties {

  /**
   * Master toggle. {@code true} by default — every deployment that pulls the derived
   * image is expected to write AuditEvents so the FHIR-standard audit trail is always
   * present. Set to {@code false} only for ephemeral dev clusters where the volume of
   * audit rows would obscure log debugging.
   */
  private boolean enabled = true;

  /**
   * If {@code true}, also produce AuditEvents for read operations (GET on a single
   * resource by id, GET history). Default {@code false} because read traffic dwarfs
   * write traffic in typical FHIR workloads — turn this on only when a compliance
   * framework explicitly requires it. Writes are always audited regardless of this
   * flag.
   */
  private boolean captureReads = false;

  /**
   * If {@code true}, also produce AuditEvents for type-level search operations
   * (e.g. {@code GET /fhir/Patient?name=Smith}). Default {@code false} — same
   * volume argument as {@link #captureReads}.
   */
  private boolean captureSearch = false;

  /**
   * Informational retention horizon (days). The interceptor itself does NOT delete
   * old AuditEvents — purging is a separate scheduled job that we have not yet
   * implemented. Recorded here so the value lives in one place and an operator
   * configuring retention can find it next to the other audit knobs.
   */
  private long retentionDays = 365L;

  // ------------- accessors -------------

  public boolean isEnabled() {
    return enabled;
  }

  public void setEnabled(boolean enabled) {
    this.enabled = enabled;
  }

  public boolean isCaptureReads() {
    return captureReads;
  }

  public void setCaptureReads(boolean captureReads) {
    this.captureReads = captureReads;
  }

  public boolean isCaptureSearch() {
    return captureSearch;
  }

  public void setCaptureSearch(boolean captureSearch) {
    this.captureSearch = captureSearch;
  }

  public long getRetentionDays() {
    return retentionDays;
  }

  public void setRetentionDays(long retentionDays) {
    this.retentionDays = retentionDays;
  }
}
