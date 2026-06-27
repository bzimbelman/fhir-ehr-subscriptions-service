package com.bzonfhir.subscriptionservice.spi.meta

/**
 * The return type of
 * [com.bzonfhir.subscriptionservice.spi.AuthorizationDecider.decide].
 *
 * Three-valued logic on purpose:
 *
 *   - [Allow] — this decider says "yes, allow." Final answer; the runtime
 *     short-circuits and grants the request.
 *   - [Deny] — this decider says "no, deny." Final answer; the runtime
 *     short-circuits and rejects the request with the included reason.
 *   - [Abstain] — this decider has no opinion; the runtime moves on to
 *     the next decider in the chain (or falls back to the built-in
 *     OIDC role check).
 *
 * Three-valued instead of boolean: many real-world authz integrations
 * cover only a slice of the access surface (e.g. "I only know about
 * Epic users; for non-Epic users I have nothing to say"). Forcing those
 * deciders to return `false` for unknown subjects would lock everyone
 * else out. [Abstain] lets a decider opt out cleanly without affecting
 * the final answer.
 *
 * The chain ordering and the "any [Allow] wins" vs "any [Deny] wins"
 * semantics are runtime configuration, not part of the SPI. Both modes
 * are common (OAuth scopes use OR-of-allow; XACML defaults to
 * deny-overrides). The runtime resolves it at boot.
 */
sealed class AuthDecision {
    /**
     * Grant the request. The runtime stops evaluating further
     * deciders if it's in `allow-overrides` mode.
     *
     * @property reason Optional explanation, surfaces in audit logs.
     */
    data class Allow(val reason: String? = null) : AuthDecision()

    /**
     * Deny the request. The runtime returns a 403 to the caller and
     * stops evaluating further deciders if it's in `deny-overrides`
     * mode.
     *
     * @property reason Human-readable; surfaces both in the response
     *   body (typically a sanitized form) and in audit logs (full
     *   detail).
     */
    data class Deny(val reason: String) : AuthDecision()

    /**
     * This decider has no opinion. The runtime moves on; if every
     * decider abstains, the runtime falls back to its default
     * behaviour (which for the FOSS image is "deny — no decider
     * granted access").
     */
    data object Abstain : AuthDecision()
}

/**
 * A minimal authenticated-subject shape, mirroring `java.security.Principal`
 * but without dragging in the JAAS surface.
 *
 * The OIDC interceptor (see `hapi/auth/.../OidcJwtAuthenticationInterceptor.java`)
 * already parses bearer tokens; an [AuthorizationDecider] receives the
 * resulting principal rather than re-parsing the token. Custom deciders
 * are free to consult [claims] for tenant-specific entitlements that
 * aren't carried as scopes.
 *
 * @property name The subject's identifier — typically the OIDC `sub`
 *   claim or a Keycloak preferred-username.
 * @property scopes Scopes asserted by the bearer token, parsed from the
 *   space-separated `scope` claim.
 * @property roles Realm/client roles the OIDC provider attached.
 * @property claims Other JWT claims, surfaced as a string-keyed map so
 *   the SPI doesn't depend on a specific JWT library.
 */
data class Principal(
    val name: String,
    val scopes: Set<String> = emptySet(),
    val roles: Set<String> = emptySet(),
    val claims: Map<String, String> = emptyMap(),
)
