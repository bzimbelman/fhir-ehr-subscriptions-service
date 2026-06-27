package com.bzonfhir.subscriptionservice.plugins.observabilityotel.config

import com.bzonfhir.subscriptionservice.plugins.observabilityotel.StandardLogFields

/**
 * Configuration knobs for the built-in observability plugin.
 *
 * Most operators won't touch these — the defaults are the standard
 * contract. The class exists so a future major-bump deprecation cycle
 * (a `schema_version` change per `docs/observability/log-schema.md`)
 * can flip the value via Spring property override rather than a code
 * change.
 *
 * # Why a separate package
 *
 * Kept under `config/` so the public top-level package contains only
 * the enricher + catalog types a third-party plugin author might
 * reference. Operators reading their `application.yaml` find the
 * `@ConfigurationProperties` class somewhere obvious.
 *
 * # Spring annotation handling
 *
 * The plugin module declares Spring `compileOnly`, so this class
 * doesn't carry a `@ConfigurationProperties` annotation directly — the
 * host (interface-engine) is responsible for binding properties onto
 * an instance of this class via its own `@EnableConfigurationProperties`
 * registration. Keeping the annotation off the class also means
 * third-party plugin authors who consume this module standalone don't
 * need Spring on their classpath at all.
 *
 * @property schemaVersion The log-schema version literal emitted on
 *   every record. Defaults to [StandardLogFields.SCHEMA_VERSION].
 *   Operators in the middle of a #397 deprecation cycle may override
 *   to a future `"2.0"` to test downstream consumers; production
 *   should leave this at the default.
 */
data class ObservabilityProperties(
    val schemaVersion: String = StandardLogFields.SCHEMA_VERSION,
)
