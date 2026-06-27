package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.bzonfhir.subscriptionservice.plugins.profileloader.config.ProfileLoaderProperties
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.io.TempDir
import java.nio.file.Files
import java.nio.file.Path

/**
 * End-to-end-ish tests for [ProfileLoader]. Each test sets up a temp
 * directory with one or more manifests, runs `load()`, and asserts on
 * the resulting registry contents + load report.
 */
class ProfileLoaderTest {

    @Test
    fun `loads both Epic and Athena manifests from a directory`(@TempDir tempDir: Path) {
        copyResource("/example-manifests/epic-2024-1.yaml", tempDir.resolve("epic.yaml"))
        copyResource("/example-manifests/athena-2026-Q2.yaml", tempDir.resolve("athena.yaml"))

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )

        val report = loader.load()

        assertThat(report.scanned).isEqualTo(2)
        assertThat(report.loaded).isEqualTo(2)
        assertThat(report.skipped).isEqualTo(0)

        assertThat(registry.count()).isEqualTo(2)
        assertThat(registry.findById("epic")).isNotNull
        assertThat(registry.findById("athena")).isNotNull
        assertThat(registry.findById("epic")!!.profile.version).isEqualTo("2024.1")
        assertThat(registry.findById("athena")!!.profile.version).isEqualTo("2026-Q2")
    }

    @Test
    fun `silently skips when the configured directory does not exist`(@TempDir tempDir: Path) {
        val nonExistent = tempDir.resolve("nope")
        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = nonExistent.toString()),
            registry = registry,
        )

        val report = loader.load()

        assertThat(report.scanned).isEqualTo(0)
        assertThat(report.loaded).isEqualTo(0)
        assertThat(report.skipped).isEqualTo(0)
        assertThat(registry.count()).isEqualTo(0)
    }

    @Test
    fun `ignores non-yaml files in the directory`(@TempDir tempDir: Path) {
        copyResource("/example-manifests/epic-2024-1.yaml", tempDir.resolve("epic.yaml"))
        Files.writeString(tempDir.resolve("README.md"), "# don't load me")
        Files.writeString(tempDir.resolve("notes.txt"), "some scratch")

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )

        val report = loader.load()

        assertThat(report.scanned).isEqualTo(1)
        assertThat(report.loaded).isEqualTo(1)
        assertThat(registry.count()).isEqualTo(1)
    }

    @Test
    fun `also picks up yml extension files`(@TempDir tempDir: Path) {
        copyResource("/example-manifests/epic-2024-1.yaml", tempDir.resolve("epic.yml"))

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )

        val report = loader.load()
        assertThat(report.scanned).isEqualTo(1)
        assertThat(report.loaded).isEqualTo(1)
    }

    @Test
    fun `skips a parse-failing manifest but still loads the other valid one`(@TempDir tempDir: Path) {
        copyResource("/example-manifests/epic-2024-1.yaml", tempDir.resolve("epic.yaml"))
        Files.writeString(tempDir.resolve("broken.yaml"), "not: { valid: yaml")

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )

        val report = loader.load()
        assertThat(report.scanned).isEqualTo(2)
        assertThat(report.loaded).isEqualTo(1)
        assertThat(report.skipped).isEqualTo(1)
        assertThat(registry.findById("epic")).isNotNull
    }

    @Test
    fun `skips a validation-failing manifest by default and continues`(@TempDir tempDir: Path) {
        copyResource("/example-manifests/epic-2024-1.yaml", tempDir.resolve("epic.yaml"))
        // Same content with schemaVersion bumped past what we support.
        val epicYaml = javaClass.getResourceAsStream("/example-manifests/epic-2024-1.yaml")!!
            .bufferedReader().readText()
            .replace("schemaVersion: 1", "schemaVersion: 99")
        Files.writeString(tempDir.resolve("bad-schema-version.yaml"), epicYaml)

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )

        val report = loader.load()
        assertThat(report.scanned).isEqualTo(2)
        assertThat(report.loaded).isEqualTo(1)
        assertThat(report.skipped).isEqualTo(1)
        assertThat(registry.findById("epic")).isNotNull
    }

    @Test
    fun `strict mode throws on validation failure`(@TempDir tempDir: Path) {
        val epicYaml = javaClass.getResourceAsStream("/example-manifests/epic-2024-1.yaml")!!
            .bufferedReader().readText()
            .replace("schemaVersion: 1", "schemaVersion: 99")
        Files.writeString(tempDir.resolve("bad.yaml"), epicYaml)

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(
                directory = tempDir.toString(),
                failOnValidationError = true,
            ),
            registry = registry,
        )

        assertThatThrownBy { loader.load() }
            .isInstanceOf(ProfileManifestValidationException::class.java)
    }

    @Test
    fun `reloading replaces an existing profile with the same id`(@TempDir tempDir: Path) {
        val originalYaml = javaClass.getResourceAsStream("/example-manifests/epic-2024-1.yaml")!!
            .bufferedReader().readText()
        Files.writeString(tempDir.resolve("epic-v1.yaml"), originalYaml)
        Files.writeString(
            tempDir.resolve("epic-v2.yaml"),
            originalYaml.replace("version: \"2024.1\"", "version: \"2025.1\""),
        )

        val registry = ProfileRegistry()
        val loader = ProfileLoader(
            properties = ProfileLoaderProperties(directory = tempDir.toString()),
            registry = registry,
        )
        loader.load()

        // Both files have profile.id=epic; one wins. Either 2024.1 or
        // 2025.1 is acceptable — the loader doesn't guarantee ordering
        // beyond "alphabetical file name", so epic-v2.yaml wins after
        // epic-v1.yaml is overwritten.
        assertThat(registry.count()).isEqualTo(1)
        assertThat(registry.findById("epic")!!.profile.version).isEqualTo("2025.1")
    }

    private fun copyResource(resourcePath: String, target: Path) {
        val stream = javaClass.getResourceAsStream(resourcePath)
            ?: error("Test resource not found: $resourcePath")
        Files.writeString(target, stream.bufferedReader().readText())
    }
}
