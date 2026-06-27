package com.bzonfhir.subscriptionservice.plugins.profileloader.config

import org.springframework.boot.context.properties.ConfigurationProperties

/**
 * Configuration for the profile-loader plugin (ticket #435).
 *
 * Bound from `application.yaml` under
 * `subscription-service.profiles`. Example:
 *
 * ```yaml
 * subscription-service:
 *   profiles:
 *     enabled: true
 *     directory: "/app/profiles"
 *     fail-on-validation-error: false
 * ```
 *
 * Two operator-facing knobs, both with sensible defaults that preserve
 * "the host boots even if no profiles are present":
 *
 *  - [enabled] flips the loader off entirely. Useful when an operator
 *    is debugging an unrelated plugin's startup and wants to remove
 *    the profile-loader's side effects.
 *  - [failOnValidationError] is the strict-mode escape hatch. Default
 *    `false` keeps the host bootable in the face of one bad manifest
 *    among many (the bad one is logged + skipped); CI gates that
 *    verify "all profiles in this release are clean" flip this to
 *    `true` to fail the build.
 */
@ConfigurationProperties(prefix = "subscription-service.profiles")
data class ProfileLoaderProperties(

    /**
     * Master toggle. When `false`, the loader's auto-config skips bean
     * registration entirely; no directory is scanned and no
     * `ProfileRegistry` bean is created. Other plugins that depend on
     * the registry must declare `ObjectProvider<ProfileRegistry>` or
     * tolerate its absence.
     */
    val enabled: Boolean = true,

    /**
     * Filesystem path the loader scans at boot. Defaults to
     * `/app/profiles` — the conventional mount point inside the
     * production container image where operators bind-mount their
     * profile YAMLs (or where a Helm chart's ConfigMap projects them).
     *
     * Local dev / tests typically override this to a tmp dir.
     */
    val directory: String = "/app/profiles",

    /**
     * When `true`, the loader throws on the first validation
     * violation, aborting application startup. Default `false`: the
     * loader logs the violation, skips the bad profile, and keeps
     * loading the others. The strict mode is for CI gates that need a
     * non-zero exit code on a malformed profile.
     */
    val failOnValidationError: Boolean = false,
)
