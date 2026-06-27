package com.bzonfhir.subscriptionservice.plugins.profileloader

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Tests for the JSON-Schema-driven manifest validator. Each test
 * constructs a deliberately-broken manifest and asserts the validator
 * produces an actionable violation. The valid Epic + Athena fixtures
 * pass with zero violations (the "happy path" test) so we know the
 * schema isn't over-strict.
 */
class ProfileManifestValidatorTest {

    private val validator = ProfileManifestValidator()

    @Test
    fun `valid Epic manifest produces no violations`() {
        val yaml = javaClass.getResourceAsStream("/example-manifests/epic-2024-1.yaml")!!
            .bufferedReader().readText()

        val violations = validator.validate(yaml, "epic.yaml")
        assertThat(violations).isEmpty()
    }

    @Test
    fun `valid Athena manifest produces no violations`() {
        val yaml = javaClass.getResourceAsStream("/example-manifests/athena-2026-Q2.yaml")!!
            .bufferedReader().readText()

        val violations = validator.validate(yaml, "athena.yaml")
        assertThat(violations).isEmpty()
    }

    @Test
    fun `schemaVersion other than 1 is rejected`() {
        val yaml = baseValidManifest().replace("schemaVersion: 1", "schemaVersion: 99")

        val violations = validator.validate(yaml, "wrong-schema-version.yaml")
        assertThat(violations).isNotEmpty
        assertThat(violations.first().pointer).contains("schemaVersion")
    }

    @Test
    fun `unknown quirk key is rejected`() {
        val yaml = baseValidManifest() + """

            quirks:
              frobnication-strategy: yes
        """.trimIndent()

        val violations = validator.validate(yaml, "unknown-quirk.yaml")
        assertThat(violations).isNotEmpty
        assertThat(violations.any { it.pointer.contains("quirks") || it.message.contains("frobnication") })
            .isTrue
    }

    @Test
    fun `unknown audit enrichment rule key is rejected`() {
        val yaml = baseValidManifest() + """

            audit:
              agent-system: test
              enrichments:
                - frobnicate: pv1.7
        """.trimIndent()

        val violations = validator.validate(yaml, "unknown-enrichment.yaml")
        assertThat(violations).isNotEmpty
    }

    @Test
    fun `mapping entry with neither messageType nor sourceType is rejected`() {
        // We have to construct this from scratch — the helper has a valid
        // mapping. Build directly.
        val yaml = """
            profile:
              id: x
              version: "1"
              schemaVersion: 1
              vendor: { name: x, productLine: y, productVersion: z }
              fhirVersions: [R4]
            ingest:
              - id: in
                type: hl7v2-mllp
            mappings:
              - map: maps/x.fml
        """.trimIndent()

        val violations = validator.validate(yaml, "missing-discriminator.yaml")
        assertThat(violations).isNotEmpty
    }

    @Test
    fun `empty map path is rejected`() {
        val yaml = """
            profile:
              id: x
              version: "1"
              schemaVersion: 1
              vendor: { name: x, productLine: y, productVersion: z }
              fhirVersions: [R4]
            ingest:
              - id: in
                type: hl7v2-mllp
            mappings:
              - messageType: ADT^A04
                map: ""
        """.trimIndent()

        val violations = validator.validate(yaml, "empty-map.yaml")
        assertThat(violations).isNotEmpty
    }

    @Test
    fun `missing required profile fields are flagged`() {
        // vendor and fhirVersions missing.
        val yaml = """
            profile:
              id: x
              version: "1"
              schemaVersion: 1
            ingest:
              - id: in
                type: hl7v2-mllp
            mappings:
              - messageType: ADT^A04
                map: maps/x.fml
        """.trimIndent()

        val violations = validator.validate(yaml, "incomplete-profile.yaml")
        assertThat(violations).isNotEmpty
    }

    @Test
    fun `unknown top-level property is rejected (additionalProperties false)`() {
        val yaml = baseValidManifest() + """

            mysteryTopLevel: "not allowed"
        """.trimIndent()

        val violations = validator.validate(yaml, "extra-top.yaml")
        assertThat(violations).isNotEmpty
    }

    @Test
    fun `validation violations carry the source path verbatim`() {
        val yaml = baseValidManifest().replace("schemaVersion: 1", "schemaVersion: 99")

        val violations = validator.validate(yaml, "/path/to/bad-profile.yaml")
        assertThat(violations).isNotEmpty
        assertThat(violations.first().path).isEqualTo("/path/to/bad-profile.yaml")
    }

    /**
     * Minimal valid manifest used as the base for "now break exactly one
     * thing" tests. Doesn't include `quirks` / `audit` so each broken
     * test can append those without colliding.
     */
    private fun baseValidManifest(): String = """
        profile:
          id: testprofile
          version: "1.0"
          schemaVersion: 1
          vendor:
            name: Test
            productLine: TestLine
            productVersion: "1.x"
          fhirVersions: [R4]
        ingest:
          - id: default
            type: hl7v2-mllp
            config:
              port: 2575
        mappings:
          - messageType: ADT^A04
            map: maps/adt.fml
    """.trimIndent()
}
