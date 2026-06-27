package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.dataformat.yaml.YAMLFactory
import com.networknt.schema.JsonSchema
import com.networknt.schema.JsonSchemaFactory
import com.networknt.schema.SchemaValidatorsConfig
import com.networknt.schema.SpecVersion
import com.networknt.schema.ValidationMessage
import java.nio.file.Files
import java.nio.file.Path

/**
 * Semantic validation of a profile manifest. Operates on the raw YAML
 * (as a parsed JSON tree) rather than on the bound [ProfileManifest] so
 * the JSON-Schema-shaped error messages reference the YAML keys the
 * profile author wrote, not our Kotlin field names.
 *
 * The schema lives at `src/main/resources/schemas/profile-manifest-v1.json`.
 * It enforces:
 *
 *  - `profile.schemaVersion` MUST equal `1`. A profile with
 *    `schemaVersion: 99` (or any other value) is rejected with a clear
 *    "this engine doesn't understand this schema version" message.
 *  - `quirks` keys MUST be from the known-strategies set. The schema's
 *    `additionalProperties: false` block under `quirks` enumerates the
 *    valid keys; an unknown one (e.g. `frobnication-strategy: yes`)
 *    fails.
 *  - `audit.enrichments` rule keys MUST be from the known set
 *    (`addOriginatingUser`, `addPatientFacility`, `addPracticeId`,
 *    `addAthenaUser`).
 *  - `mappings[*]` items MUST have exactly one of `messageType`
 *    OR `sourceType` (enforced via `oneOf`).
 *  - The `map:` path MUST be non-empty. (Whether the file actually
 *    exists on disk is checked at load time, where the manifest
 *    directory is known — schema validation alone can't resolve
 *    relative paths.)
 *
 * Anything that requires filesystem context (missing FML file, profile
 * id collisions across files) is enforced by [ProfileLoader], not here.
 */
class ProfileManifestValidator {

    private val yamlMapper = ObjectMapper(YAMLFactory())

    private val schema: JsonSchema by lazy {
        val schemaStream = javaClass.getResourceAsStream(SCHEMA_RESOURCE_PATH)
            ?: error("Profile manifest schema missing at $SCHEMA_RESOURCE_PATH (build resource issue)")
        val schemaNode = ObjectMapper().readTree(schemaStream)
        val factory = JsonSchemaFactory.getInstance(SpecVersion.VersionFlag.V202012)
        // Default config — strict, no remote $ref resolution (we're
        // self-contained).
        factory.getSchema(schemaNode, SchemaValidatorsConfig())
    }

    /**
     * Validate a manifest's parsed YAML tree against the JSON Schema.
     * Returns the list of [ValidationViolation]s; an empty list means the
     * manifest is structurally valid.
     *
     * @param yamlPath The path the YAML was read from, surfaced in
     *   violation messages.
     */
    fun validate(yamlPath: Path): List<ValidationViolation> {
        if (!Files.isRegularFile(yamlPath)) {
            return listOf(
                ValidationViolation(
                    path = yamlPath.toString(),
                    pointer = "(file)",
                    message = "not a regular file",
                ),
            )
        }
        val tree = yamlMapper.readTree(yamlPath.toFile())
        return validateTree(tree, yamlPath.toString())
    }

    /**
     * Validate a manifest given as a YAML string. Convenience for unit
     * tests; production callers use the [Path] overload.
     */
    fun validate(yaml: String, source: String): List<ValidationViolation> {
        val tree = yamlMapper.readTree(yaml)
        return validateTree(tree, source)
    }

    private fun validateTree(tree: com.fasterxml.jackson.databind.JsonNode, source: String): List<ValidationViolation> {
        val messages: Set<ValidationMessage> = schema.validate(tree)
        return messages.map { msg ->
            ValidationViolation(
                path = source,
                pointer = msg.instanceLocation.toString().ifBlank { "(root)" },
                message = msg.message,
            )
        }
    }

    companion object {
        internal const val SCHEMA_RESOURCE_PATH = "/schemas/profile-manifest-v1.json"
    }
}

/**
 * One validation failure produced by [ProfileManifestValidator]. The
 * triple of (file path, JSON pointer to the bad node, human message) is
 * exactly what an operator needs to find and fix the bad line.
 *
 * @property path The file the violation was found in (or a test source
 *   identifier).
 * @property pointer A JSON-Pointer-style reference to the offending
 *   node inside the manifest (e.g. `/profile/schemaVersion`). The
 *   underlying validator emits these in `$.profile.schemaVersion`
 *   form; we surface whatever it gives us.
 * @property message Validator-supplied human description of the
 *   violation.
 */
data class ValidationViolation(
    val path: String,
    val pointer: String,
    val message: String,
)

/**
 * Raised by [ProfileLoader.load] when the loader is configured to
 * fail-fast on validation errors (see
 * `subscription-service.profiles.fail-on-validation-error`). The default
 * behaviour is to log + skip the bad profile, so this exception is the
 * opt-in fail-fast escape hatch — useful in CI gates that verify the
 * profiles bundled with a release are all structurally sound before
 * shipping.
 */
class ProfileManifestValidationException(
    val violations: List<ValidationViolation>,
) : RuntimeException(formatMessage(violations)) {

    companion object {
        private fun formatMessage(violations: List<ValidationViolation>): String {
            if (violations.isEmpty()) return "Profile manifest validation failed (no details)"
            val first = violations.first()
            val rest = if (violations.size > 1) " (+${violations.size - 1} more)" else ""
            return "Profile manifest '${first.path}' validation failed at ${first.pointer}: ${first.message}$rest"
        }
    }
}
