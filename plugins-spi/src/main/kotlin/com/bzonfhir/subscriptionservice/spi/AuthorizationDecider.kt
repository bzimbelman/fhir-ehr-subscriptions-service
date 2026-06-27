package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.AuthDecision
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.Principal

/**
 * SPI #5 — Pluggable authorization.
 *
 * Today's authz (see `hapi/auth/.../ScopeAuthorizationInterceptor.java`)
 * is OIDC bearer-token + SMART-style scope checks. Sufficient for the
 * FOSS image; not sufficient for enterprises that need:
 *
 *  - LDAP / Active Directory group lookups
 *  - Azure AD app-role assignments
 *  - Okta SSO Service-Principal-Name checks
 *  - Per-tenant attribute-based access control (ABAC)
 *  - Customer-specific entitlement tables
 *
 * An [AuthorizationDecider] plugs in BEFORE the FHIR DAO sees the
 * request. The runtime chains every registered decider; each one
 * returns [AuthDecision.Allow], [AuthDecision.Deny], or
 * [AuthDecision.Abstain]. The chain composition policy (allow-overrides,
 * deny-overrides) is runtime configuration; the default is
 * deny-overrides — any explicit Deny blocks the request, all Abstains
 * fall through to the built-in OIDC role check.
 *
 * # Hot path
 *
 * [decide] is called on EVERY authenticated request. Implementations
 * must be fast: cache external lookups, short-circuit on the principal
 * the decider doesn't recognize (return [AuthDecision.Abstain]).
 *
 * # Audit
 *
 * Every Allow / Deny is logged in the request's AuditEvent with the
 * decider's [meta.id] and the [AuthDecision.Allow.reason] /
 * [AuthDecision.Deny.reason] so operators can trace which decider made
 * which call.
 *
 * # Stability: EXPERIMENTAL
 */
interface AuthorizationDecider {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Decide whether [principal] can perform [action] on [resource].
     *
     * @param principal The authenticated subject, as resolved from the
     *   OIDC bearer token by `OidcJwtAuthenticationInterceptor`.
     * @param action A coarse-grained verb (`"read"`, `"create"`,
     *   `"update"`, `"delete"`, `"search"`). The runtime maps FHIR
     *   REST operations into these verbs before invoking the decider.
     * @param resource A FHIR resource string (`"Patient/123"`,
     *   `"Subscription"`) or a non-FHIR resource path
     *   (`"/admin/messages"`, `"/admin/dlq"`).
     */
    fun decide(principal: Principal, action: String, resource: String): AuthDecision
}
