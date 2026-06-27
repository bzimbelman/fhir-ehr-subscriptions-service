package com.bzonfhir.subscriptionservice.plugins.profileloader

import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir
import java.nio.file.Files
import java.nio.file.Path

/**
 * Verifies the parser produces correct [ProfileManifest] instances from
 * both the Epic-style (HL7 v2) and Athena-style (REST) example fixtures.
 * The same fixtures live under `src/test/resources/example-manifests/`
 * and become the seed Epic #426's profile authors copy from.
 */
class ProfileManifestParserTest {

    private val parser = ProfileManifestParser()

    @Test
    fun `parses the Epic example manifest with all expected fields`() {
        val manifest = parseResource("/example-manifests/epic-2024-1.yaml")

        assertThat(manifest.profile.id).isEqualTo("epic")
        assertThat(manifest.profile.version).isEqualTo("2024.1")
        assertThat(manifest.profile.schemaVersion).isEqualTo(1)
        assertThat(manifest.profile.vendor.name).isEqualTo("Epic Systems")
        assertThat(manifest.profile.vendor.productLine).isEqualTo("Epic")
        assertThat(manifest.profile.vendor.productVersion).isEqualTo("2024.x")
        assertThat(manifest.profile.fhirVersions).containsExactly("R4")
        assertThat(manifest.profile.hl7Versions).containsExactly("v2.5", "v2.5.1", "v2.7")

        assertThat(manifest.ingest).hasSize(1)
        assertThat(manifest.ingest[0].id).isEqualTo("mllp-default")
        assertThat(manifest.ingest[0].type).isEqualTo("hl7v2-mllp")
        assertThat(manifest.ingest[0].config["port"]).isEqualTo(2575)
        assertThat(manifest.ingest[0].config["facilityResolver"]).isEqualTo("epic-msh-3-facility")

        assertThat(manifest.mappings).hasSize(3)
        assertThat(manifest.mappings[0].messageType).isEqualTo("ADT^A04")
        assertThat(manifest.mappings[0].sourceType).isNull()
        assertThat(manifest.mappings[0].map).isEqualTo("maps/hl7v2-ADT-A04-Epic.fml")
        assertThat(manifest.mappings[0].tests).containsExactly("tests/adt-a04/")

        assertThat(manifest.quirks).containsEntry("msh3-format", "facility-shortcode-then-pipe")
        assertThat(manifest.quirks).containsEntry("empty-pid-strategy", "synthesize-from-mrn")
        assertThat(manifest.quirks).containsEntry("attachment-encoding", "base64-with-rtf-prefix-trim")

        assertThat(manifest.audit).isNotNull
        assertThat(manifest.audit!!.agentSystem).isEqualTo("epic")
        assertThat(manifest.audit!!.enrichments).hasSize(2)
        assertThat(manifest.audit!!.enrichments[0]).containsEntry("addOriginatingUser", "pv1.7")
        assertThat(manifest.audit!!.enrichments[1]).containsEntry("addPatientFacility", "msh.4")
    }

    @Test
    fun `parses the Athena example manifest (REST ingest, sourceType mappings, no hl7Versions)`() {
        val manifest = parseResource("/example-manifests/athena-2026-Q2.yaml")

        assertThat(manifest.profile.id).isEqualTo("athena")
        assertThat(manifest.profile.version).isEqualTo("2026-Q2")
        assertThat(manifest.profile.schemaVersion).isEqualTo(1)
        assertThat(manifest.profile.vendor.name).isEqualTo("athenahealth")
        assertThat(manifest.profile.fhirVersions).containsExactly("R4")
        assertThat(manifest.profile.hl7Versions).isEmpty()

        // Two REST-shaped ingest entries — neither is hl7v2-mllp.
        assertThat(manifest.ingest).hasSize(2)
        assertThat(manifest.ingest.map { it.type })
            .containsExactly("athena-native-rest", "fhir-r4-polling")
        assertThat(manifest.ingest[0].id).isEqualTo("athena-changed-resources")
        assertThat(manifest.ingest[0].config["pollIntervalSeconds"]).isEqualTo(60)
        // Nested config block (auth) is preserved as a Map<String, Any> — no
        // attempt at type-specific binding at the loader level.
        val authBlock = manifest.ingest[0].config["auth"]
        assertThat(authBlock).isInstanceOf(Map::class.java)
        @Suppress("UNCHECKED_CAST")
        val authMap = authBlock as Map<String, Any>
        assertThat(authMap["type"]).isEqualTo("oauth2-client-credentials")

        // Mappings use sourceType, not messageType.
        assertThat(manifest.mappings).hasSize(2)
        assertThat(manifest.mappings[0].messageType).isNull()
        assertThat(manifest.mappings[0].sourceType).isEqualTo("athena-changed-patients")
        assertThat(manifest.mappings[0].map).isEqualTo("maps/athena-patient-normalize.fml")

        // Quirks use the Athena-specific keys.
        assertThat(manifest.quirks).containsEntry("athena-patient-id-namespace", "practice-scoped")

        // Audit enrichments reference query params and response headers,
        // not HL7 fields.
        assertThat(manifest.audit!!.agentSystem).isEqualTo("athena")
        assertThat(manifest.audit!!.enrichments[0]).containsEntry("addPracticeId", "query.practiceid")
        assertThat(manifest.audit!!.enrichments[1])
            .containsEntry("addAthenaUser", "response-header.X-Audit-User")
    }

    @Test
    fun `parse from a Path round-trips through the file system`(@TempDir tempDir: Path) {
        val tmpFile = tempDir.resolve("epic.yaml")
        Files.writeString(
            tmpFile,
            javaClass.getResourceAsStream("/example-manifests/epic-2024-1.yaml")!!.bufferedReader().readText(),
        )

        val manifest = parser.parse(tmpFile)
        assertThat(manifest.profile.id).isEqualTo("epic")
    }

    @Test
    fun `unknown top-level property fails parsing with a helpful error mentioning the file`() {
        val yaml = """
            profile:
              id: bogus
              version: "1"
              schemaVersion: 1
              vendor: { name: x, productLine: y, productVersion: z }
              fhirVersions: [R4]
            ingest: []
            mappings: []
            mysteryField: "this isn't in the schema"
        """.trimIndent()

        assertThatThrownBy {
            parser.parse(yaml.byteInputStream(), "bogus-manifest.yaml")
        }
            .isInstanceOf(ProfileManifestParseException::class.java)
            .hasMessageContaining("bogus-manifest.yaml")
    }

    @Test
    fun `missing required top-level field fails parsing`() {
        // profile.id absent — Kotlin's null-safety should reject this since
        // ProfileMeta.id is non-nullable.
        val yaml = """
            profile:
              version: "1"
              schemaVersion: 1
              vendor: { name: x, productLine: y, productVersion: z }
              fhirVersions: [R4]
            ingest: []
            mappings: []
        """.trimIndent()

        assertThatThrownBy {
            parser.parse(yaml.byteInputStream(), "no-id.yaml")
        }
            .isInstanceOf(ProfileManifestParseException::class.java)
            .hasMessageContaining("no-id.yaml")
    }

    @Test
    fun `parse from non-existent Path throws ProfileManifestParseException`(@TempDir tempDir: Path) {
        val missing = tempDir.resolve("does-not-exist.yaml")
        assertThatThrownBy { parser.parse(missing) }
            .isInstanceOf(ProfileManifestParseException::class.java)
            .hasMessageContaining("does-not-exist.yaml")
    }

    private fun parseResource(resourcePath: String): ProfileManifest {
        val stream = javaClass.getResourceAsStream(resourcePath)
            ?: error("Test resource not found: $resourcePath")
        return parser.parse(stream, resourcePath)
    }
}
