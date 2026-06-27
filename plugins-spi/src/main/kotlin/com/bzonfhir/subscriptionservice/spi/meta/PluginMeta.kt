package com.bzonfhir.subscriptionservice.spi.meta

/**
 * Identity + provenance for any plugin that implements an SPI surface in
 * this module.
 *
 * Every concrete SPI implementation MUST expose a [PluginMeta] via its
 * `meta` property. The runtime uses these fields for:
 *
 *  - **Discovery / listing.** The operator UI's "what's loaded" footer
 *    iterates registered plugins and shows the `id`, `version`, and
 *    `supplier`. See master plan §3.2.1.
 *  - **SPI-shape compatibility.** [schemaVersion] tracks the SPI module's
 *    SPI shape, NOT the plugin's own version. When the SPI changes in
 *    a breaking way we bump the schema version; the runtime refuses to
 *    load plugins whose `schemaVersion` no longer matches.
 *  - **Telemetry / support routing.** Bug reports include the plugin id +
 *    version so we know which third party shipped what.
 *
 * The values here are deliberately data-class fields, not annotations —
 * annotations are awkward to instantiate at runtime when a plugin is
 * loaded via `ServiceLoader`, and we want plugin authors to construct
 * meta in plain Kotlin/Java.
 *
 * Example:
 * ```kotlin
 * override val meta = PluginMeta(
 *     id = "hl7v2-mllp",
 *     version = "1.0.0",
 *     schemaVersion = 1,
 *     supplier = PluginSupplier.FIRST_PARTY,
 *     description = "Default HL7 v2 MLLP listener",
 * )
 * ```
 *
 * @property id Stable identifier the runtime uses to deduplicate plugins
 *   and surface them in UI. Lowercase, hyphen-separated by convention
 *   (e.g. `"hl7v2-mllp"`, `"audit-event-fhir"`, `"epic-vendor-profile"`).
 * @property version The plugin's OWN version, following semver. Bumped by
 *   the plugin author when they ship a new release.
 * @property schemaVersion Which version of the plugins-spi SPI shape the
 *   plugin was authored against. Starts at `1`; bumps when an existing
 *   SPI interface gets a binary-incompatible change. Plugins built
 *   against an older schemaVersion may still load if the runtime knows
 *   the surface is backward-compatible.
 * @property supplier Who shipped this plugin — affects support routing
 *   and the UI footer's "tier" string.
 * @property description Short, human-readable. Surfaces in the UI footer
 *   and in `bd`-style listings of installed plugins.
 */
data class PluginMeta(
    val id: String,
    val version: String,
    val schemaVersion: Int,
    val supplier: PluginSupplier,
    val description: String,
)

/**
 * Provenance of a plugin. Used by the operator UI and by support tooling
 * to color-code "this came from us vs the community vs a commercial
 * partner."
 *
 * - [FIRST_PARTY] — shipped in the canonical `subscription-service` JAR
 *   we publish (the built-in plugins under `plugins-builtin/` and the
 *   commercial extensions baked into the published image when a
 *   license is present).
 * - [COMMUNITY] — submitted to and accepted into the public community
 *   plugins repo. No warranty.
 * - [COMMERCIAL] — sold by us or a partner under a license agreement
 *   separate from the FOSS Apache 2.0 grant.
 */
enum class PluginSupplier {
    FIRST_PARTY,
    COMMUNITY,
    COMMERCIAL,
}
