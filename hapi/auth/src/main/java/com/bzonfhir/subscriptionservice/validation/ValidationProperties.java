package com.bzonfhir.subscriptionservice.validation;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Configuration knobs for the subscription-service US Core profile validation layer.
 *
 * <p>Bound from {@code application.yaml} (or the equivalent env var
 * {@code SUBSCRIPTION_SERVICE_VALIDATION_MODE}) under the prefix
 * {@code subscription-service.validation}. Example:
 *
 * <pre>
 * subscription-service:
 *   validation:
 *     mode: warn
 * </pre>
 *
 * <p>The three modes:
 *
 * <ul>
 *   <li>{@code OFF} (default) — no profile validation is run on inbound resources. HAPI behaves
 *       like the upstream image.
 *   <li>{@code WARN} — every inbound write is validated against the loaded IG profiles. The
 *       request succeeds either way; validation findings are surfaced in the response's
 *       OperationOutcome so subscribers can see what was non-conformant.
 *   <li>{@code ENFORCE} — every inbound write is validated. Any finding at severity
 *       {@code error} or higher rejects the request with HTTP 422 + an OperationOutcome
 *       listing the issues. (HAPI's {@code BaseValidatingInterceptor} hard-codes 422 here,
 *       not 412 — 422 is the FHIR-standard "resource failed validation" response code.)
 * </ul>
 *
 * <p>Default is {@code OFF} because the v2-to-FHIR StructureMaps don't always produce strictly
 * US Core–conformant output for every field; a brand-new deployment shouldn't reject real-world
 * traffic. Operators dial up to {@code WARN} once they see what their feeds actually produce,
 * then to {@code ENFORCE} once their custom maps fill the gaps. See {@code docs/architecture.md}
 * "Profile validation (US Core)" for the design rationale.
 */
@ConfigurationProperties(prefix = "subscription-service.validation")
public class ValidationProperties {

  /**
   * Validation strictness. See class javadoc for the meaning of each value. Default
   * {@link ValidationMode#OFF}.
   *
   * <p>Spring binds env vars case-insensitively, so {@code SUBSCRIPTION_SERVICE_VALIDATION_MODE=warn},
   * {@code =WARN}, or {@code =Warn} all map to {@link ValidationMode#WARN}.
   */
  private ValidationMode mode = ValidationMode.OFF;

  public ValidationMode getMode() {
    return mode;
  }

  public void setMode(ValidationMode mode) {
    this.mode = mode;
  }

  /** See {@link ValidationProperties} javadoc. */
  public enum ValidationMode {
    OFF,
    WARN,
    ENFORCE
  }
}
