package com.bzonfhir.subscriptionservice.plugins.profileloader

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Unit-level tests for [ProfileRegistry]. The full happy-path is also
 * covered indirectly by [ProfileLoaderTest], but these isolate the
 * registry's own contract: register, findById, findAll, count, and
 * the "newer registration replaces older" behavior on duplicate ids.
 */
class ProfileRegistryTest {

    @Test
    fun `empty registry has count zero and findAll returns empty`() {
        val registry = ProfileRegistry()
        assertThat(registry.count()).isEqualTo(0)
        assertThat(registry.findAll()).isEmpty()
        assertThat(registry.findById("anything")).isNull()
    }

    @Test
    fun `register adds the manifest and findById returns it`() {
        val registry = ProfileRegistry()
        val manifest = sampleManifest(id = "epic")

        val previous = registry.register(manifest)
        assertThat(previous).isNull()

        assertThat(registry.count()).isEqualTo(1)
        assertThat(registry.findById("epic")).isEqualTo(manifest)
        assertThat(registry.findAll()).containsExactly(manifest)
    }

    @Test
    fun `multiple profiles with distinct ids coexist`() {
        val registry = ProfileRegistry()
        val epic = sampleManifest(id = "epic")
        val athena = sampleManifest(id = "athena")
        val meditech = sampleManifest(id = "meditech")

        registry.register(epic)
        registry.register(athena)
        registry.register(meditech)

        assertThat(registry.count()).isEqualTo(3)
        assertThat(registry.findAll()).containsExactlyInAnyOrder(epic, athena, meditech)
        assertThat(registry.findById("epic")).isEqualTo(epic)
        assertThat(registry.findById("athena")).isEqualTo(athena)
        assertThat(registry.findById("meditech")).isEqualTo(meditech)
    }

    @Test
    fun `re-registering an id returns the previous value and replaces it`() {
        val registry = ProfileRegistry()
        val v1 = sampleManifest(id = "epic", version = "2024.1")
        val v2 = sampleManifest(id = "epic", version = "2025.1")

        registry.register(v1)
        val previous = registry.register(v2)

        assertThat(previous).isEqualTo(v1)
        assertThat(registry.count()).isEqualTo(1)
        assertThat(registry.findById("epic")).isEqualTo(v2)
    }

    @Test
    fun `findById returns null for an unregistered id`() {
        val registry = ProfileRegistry()
        registry.register(sampleManifest(id = "epic"))
        assertThat(registry.findById("not-loaded")).isNull()
    }

    private fun sampleManifest(id: String, version: String = "1.0"): ProfileManifest = ProfileManifest(
        profile = ProfileMeta(
            id = id,
            version = version,
            schemaVersion = 1,
            vendor = VendorInfo(
                name = "Test Vendor",
                productLine = "Test",
                productVersion = "1.x",
            ),
            fhirVersions = listOf("R4"),
        ),
        ingest = listOf(IngestEntry(id = "default", type = "hl7v2-mllp")),
        mappings = listOf(MappingEntry(messageType = "ADT^A04", map = "maps/x.fml")),
    )
}
