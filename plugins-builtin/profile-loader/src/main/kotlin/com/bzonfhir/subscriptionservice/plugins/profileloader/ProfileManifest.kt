package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.fasterxml.jackson.annotation.JsonProperty

/**
 * The parsed shape of a vendor-profile manifest YAML file. Mirrors the
 * schema documented in `subscription-service-master-plan.md` §4.3 and
 * formalized as JSON Schema at `src/main/resources/schemas/profile-manifest-v1.json`.
 *
 * Two example shapes the parser MUST handle:
 *
 *  1. HL7 v2-style profile (Epic): one `hl7v2-mllp` ingest entry, several
 *     `messageType` mappings, audit enrichments that reference v2 fields.
 *  2. Non-v2 profile (Athena): one or more REST-shaped ingest entries,
 *     `sourceType`-keyed mappings (no v2 message type), audit
 *     enrichments that reference query params / response headers.
 *
 * The data class deliberately accepts both via [IngestEntry] (where
 * `config` is a free-form map the ingest plugin validates) and via
 * [MappingEntry] (where `messageType` and `sourceType` are alternatives
 * — one MUST be set, never both).
 *
 * @property profile Identity + vendor metadata.
 * @property ingest One or more ingest definitions. Each entry references
 *   an `IngestSource` plugin by its `type` discriminator and supplies
 *   that plugin's type-specific configuration.
 * @property mappings StructureMap (or normalization map) entries. Each
 *   references a `.fml` file relative to the manifest's directory.
 * @property quirks Vendor-specific deviation strategies. Each key MUST
 *   correspond to a quirk strategy the engine knows; unknown keys fail
 *   profile validation. Empty map allowed.
 * @property audit Audit-enrichment rules — copied into AuditEvent
 *   resources by the audit-event-fhir plugin when this profile is the
 *   originator of an audited operation. `null` when the profile doesn't
 *   contribute audit metadata.
 */
data class ProfileManifest(
    val profile: ProfileMeta,
    val ingest: List<IngestEntry>,
    val mappings: List<MappingEntry>,
    val quirks: Map<String, String> = emptyMap(),
    val audit: AuditConfig? = null,
)

/**
 * Identity + vendor metadata block of a profile manifest. Mirrors
 * `profile:` in the YAML.
 *
 * @property id Stable profile identifier (`epic`, `athena`,
 *   `meditech-expanse`). Must be unique across all loaded profiles —
 *   the [ProfileRegistry] uses this as its primary key.
 * @property version Profile-author-controlled semver / release tag. Two
 *   different versions of the same `id` are allowed at runtime; the
 *   most recent one wins for the moment (a future story may add
 *   tenant-scoped selection).
 * @property schemaVersion Which version of the manifest schema this
 *   file was authored against. Validator rejects any value other than
 *   `1` in this engine release.
 * @property vendor Human-readable vendor identity for the operator UI.
 * @property fhirVersions Which FHIR releases the profile claims to
 *   produce. The runtime cross-checks against the active HAPI server's
 *   FHIR version on bind; mismatches surface as warnings.
 * @property hl7Versions Which HL7 v2 versions the profile claims to
 *   ingest. Empty list when the profile has no v2 ingest — e.g. the
 *   Athena profile that's pure REST.
 */
data class ProfileMeta(
    val id: String,
    val version: String,
    val schemaVersion: Int,
    val vendor: VendorInfo,
    val fhirVersions: List<String>,
    val hl7Versions: List<String> = emptyList(),
)

/**
 * Vendor-identity sub-block — what shows up in the operator UI when a
 * profile is loaded.
 *
 * @property name Display name (`Epic Systems`, `athenahealth`).
 * @property productLine Vendor product family (`Epic`, `athenaOne`).
 * @property productVersion Vendor product version the profile targets
 *   (`2024.x`, `21.x`). Free-form; the operator UI shows this verbatim.
 */
data class VendorInfo(
    val name: String,
    val productLine: String,
    val productVersion: String,
)

/**
 * One inbound-data source the profile expects. Each entry maps to one
 * IngestSource plugin instance at runtime (or zero, if the host doesn't
 * have a plugin that handles [type] — that's a warning, not a load
 * failure, since profiles can declare optional ingest channels).
 *
 * @property id Unique identifier for this ingest channel WITHIN the
 *   profile. Surfaces in metrics labels (`ingest_id=mllp-default`) so
 *   the same profile can run multiple distinct MLLP listeners.
 * @property type The IngestSource plugin discriminator —
 *   `hl7v2-mllp`, `fhir-r4-polling`, `athena-native-rest`, etc. The
 *   ProfileLoader looks this up in the registry of available
 *   IngestSource plugins; an unknown type yields a warning (the
 *   profile is still loaded; that ingest channel is just inactive).
 * @property config Free-form key/value map. The interpretation is
 *   delegated to the corresponding IngestSource plugin's
 *   configuration-properties class. The profile-loader doesn't try
 *   to validate this — that's the ingest plugin's job.
 */
data class IngestEntry(
    val id: String,
    val type: String,
    val config: Map<String, Any> = emptyMap(),
)

/**
 * One mapping declaration — a StructureMap (.fml) file that transforms
 * an inbound message type or source-type into FHIR.
 *
 * Exactly one of [messageType] / [sourceType] MUST be set.
 *
 *  * [messageType] applies to HL7 v2 profiles: the value is the canonical
 *    `EVENT^TRIGGER` string (`"ADT^A04"`, `"ORM^O01"`).
 *  * [sourceType] applies to non-v2 profiles: the value is a profile-
 *    specific source identifier (`"athena-changed-patients"`,
 *    `"fhir-r4-observation"`).
 *
 * @property map Relative path (relative to the manifest's directory) to
 *   a `.fml` StructureMap file.
 * @property tests Optional list of paths to test-fixture directories
 *   used by the certification suite (Epic #426). Ignored by the
 *   runtime; the path strings are surfaced in `bd`-style listings.
 */
data class MappingEntry(
    val messageType: String? = null,
    val sourceType: String? = null,
    val map: String,
    val tests: List<String> = emptyList(),
)

/**
 * Audit configuration for a profile. Maps to the `audit:` block in the
 * manifest YAML. Consumed by the audit-event-fhir plugin's enricher:
 * for each `AuditEvent` emitted while this profile is the originator,
 * the enricher reads [agentSystem] and runs each enrichment rule.
 *
 * @property agentSystem Identifier that lands in `AuditEvent.agent.type`
 *   so downstream compliance tooling can filter "events caused by Epic"
 *   vs "events caused by Athena". Matches `profile.id` by convention
 *   but can be overridden (e.g. an Epic profile customized for a
 *   specific facility might set `agent-system: epic-hospitalA`).
 * @property enrichments Each map carries exactly ONE entry whose key is
 *   the well-known enrichment rule name (`addOriginatingUser`,
 *   `addPatientFacility`, `addPracticeId`, `addAthenaUser`) and whose
 *   value is the source expression (`pv1.7`, `msh.4`, `query.practiceid`,
 *   `response-header.X-Audit-User`). The list-of-singleton-maps shape
 *   mirrors the YAML's `- addOriginatingUser: pv1.7` form exactly,
 *   which keeps the manifest human-friendly.
 */
data class AuditConfig(
    @JsonProperty("agent-system")
    val agentSystem: String,
    val enrichments: List<Map<String, String>> = emptyList(),
)
