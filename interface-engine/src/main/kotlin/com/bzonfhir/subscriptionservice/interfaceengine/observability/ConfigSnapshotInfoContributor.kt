package com.bzonfhir.subscriptionservice.interfaceengine.observability

import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.actuate.info.Info
import org.springframework.boot.actuate.info.InfoContributor
import org.springframework.stereotype.Component

/**
 * Contributes an `subscription_service` block to `/actuator/info` carrying
 * the *effective* runtime configuration (Epic #387, ticket #393).
 *
 * # Why
 *
 * Operators have repeatedly needed a one-stop URL that reports which mode
 * the running pod is in: is auth on or off, what's the validation mode,
 * which Matchbox / HAPI base URL is actually being used, etc. Tailing
 * startup logs works but assumes you can `kubectl logs --since=...` the
 * right pod, which isn't always possible in production (log retention,
 * pod was restarted before you noticed the problem, etc.). A live
 * `/actuator/info` snapshot is the simplest answer.
 *
 * # What's included
 *
 * Just the knobs that operators care about for triage. We do NOT dump the
 * entire Spring `Environment` — that would expose the whole servlet
 * container, JDBC pool, logging config, etc., and would also be a constant
 * audit-risk regression (every new internal property would silently leak
 * onto the wire).
 *
 * # Secret masking
 *
 * Anything whose key path ends in `password`, `secret`, `token`, or `audience`
 * is replaced with the literal string `********`. Audience is masked even
 * though it's not strictly secret because some IdPs key request authorization
 * on the audience value, and customers regard it as sensitive.
 *
 * The values come from `@Value` injections (NOT from `Environment.getProperty`
 * after construction). That way the bound values are exactly what every
 * other bean got — including the precedence among yaml, env vars, and
 * `--spring.config.location=...` overrides — and we don't have to
 * re-implement Spring's property-resolution logic to keep the snapshot
 * accurate.
 */
@Component
class ConfigSnapshotInfoContributor(
    @Value("\${subscription-service.auth.enabled:false}") private val authEnabled: Boolean,
    @Value("\${subscription-service.auth.issuer:}") private val authIssuer: String,
    @Value("\${subscription-service.auth.jwks-url:}") private val authJwksUrl: String,
    @Value("\${subscription-service.auth.audience:}") private val authAudience: String,
    @Value("\${subscription-service.validation.mode:off}") private val validationMode: String,
    @Value("\${subscription-service.channel-security:strict}") private val channelSecurity: String,
    @Value("\${subscription-service.multitenancy:disabled}") private val multitenancy: String,
    @Value("\${subscription-service.matchbox.base-url:}") private val matchboxBaseUrl: String,
    @Value("\${subscription-service.hapi.base-url:}") private val hapiBaseUrl: String,
    @Value("\${spring.datasource.url:}") private val ipfDbUrl: String,
    @Value("\${spring.datasource.username:}") private val ipfDbUser: String,
    @Value("\${spring.datasource.password:}") private val ipfDbPassword: String,
    @Value("\${ipf.admin.auth-token:}") private val adminAuthToken: String,
) : InfoContributor {

    override fun contribute(builder: Info.Builder) {
        val snapshot =
            linkedMapOf<String, Any>(
                "version" to "0.4.0",
                "feature_toggles" to
                    linkedMapOf(
                        "auth_enabled" to authEnabled,
                        "validation_mode" to validationMode,
                        "channel_security" to channelSecurity,
                        "multitenancy" to multitenancy,
                    ),
                "auth" to
                    linkedMapOf(
                        "issuer" to authIssuer,
                        "jwks_url" to authJwksUrl,
                        "audience" to maskIfPresent(authAudience),
                    ),
                "downstream" to
                    linkedMapOf(
                        "matchbox_base_url" to matchboxBaseUrl,
                        "hapi_base_url" to hapiBaseUrl,
                    ),
                "ipf_db" to
                    linkedMapOf(
                        "url" to ipfDbUrl,
                        "user" to ipfDbUser,
                        "password" to maskIfPresent(ipfDbPassword),
                    ),
                "admin" to
                    linkedMapOf(
                        "auth_token" to maskIfPresent(adminAuthToken),
                    ),
            )
        builder.withDetail("subscription_service", snapshot)
    }

    /**
     * Returns `********` if the value is non-blank, the empty string
     * otherwise. The empty case lets operators tell a "secret is set
     * but masked" apart from "secret is not configured", which is the
     * common operator triage question for an
     * `IPF_ADMIN_AUTH_TOKEN=""` deployment that was supposed to have
     * a token.
     */
    private fun maskIfPresent(value: String): String = if (value.isBlank()) "" else MASKED

    companion object {
        const val MASKED: String = "********"
    }
}
