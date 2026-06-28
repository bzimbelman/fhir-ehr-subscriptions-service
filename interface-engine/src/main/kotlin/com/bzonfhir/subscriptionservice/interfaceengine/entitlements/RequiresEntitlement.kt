package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

/**
 * Gate a method body on the customer's license entitlements
 * (ticket #460, Epic #428).
 *
 * Usage:
 *
 * ```kotlin
 * @RequiresEntitlement("audit.export.iti20")
 * fun exportAuditBundle(...): AuditBundle { ... }
 * ```
 *
 * Semantics (enforced by [EntitlementGuardAspect]):
 *
 *   1. If `subscription-service.license.enabled=false`, the method runs.
 *      (Dev mode: every commercial gate is permissive.)
 *   2. If the [EntitlementHolder]'s current set contains [value], the method runs.
 *   3. Otherwise, the aspect's behaviour depends on
 *      [com.bzonfhir.subscriptionservice.interfaceengine.entitlements.config.LicenseProperties.failMode]:
 *      - `DENY` (default): throw [EntitlementMissingException].
 *      - `LOG`: log a WARN and let the method body run.
 *
 * The annotation is RUNTIME-retained so Spring AOP can read it from the
 * proxy. Annotating only methods (not types) because the gate is per-method
 * by design — a class shouldn't be "all-or-nothing" in its entitlement story.
 *
 * @property value the dotted entitlement string the license must grant.
 *   This MUST match what the license server publishes verbatim — case-sensitive.
 */
@Target(AnnotationTarget.FUNCTION)
@Retention(AnnotationRetention.RUNTIME)
@MustBeDocumented
annotation class RequiresEntitlement(val value: String)

/**
 * Thrown by [EntitlementGuardAspect] when the active entitlement set does
 * not contain the required value AND the configured fail-mode is `DENY`.
 *
 * A `@ControllerAdvice` handler converts this to HTTP 402 Payment Required
 * for web requests; non-HTTP callers see the exception directly.
 */
class EntitlementMissingException(
    val entitlement: String,
    message: String = "missing required entitlement \"$entitlement\"",
) : RuntimeException(message)
