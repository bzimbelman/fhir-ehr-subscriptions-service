package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.bzonfhir.subscriptionservice.plugins.profileloader.config.ProfileLoaderProperties
import org.slf4j.LoggerFactory
import java.io.IOException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.Paths
import kotlin.io.path.extension
import kotlin.io.path.isRegularFile
import kotlin.io.path.name

/**
 * Scans the configured directory for vendor-profile manifests, parses
 * + validates each one, and registers the surviving manifests in the
 * shared [ProfileRegistry].
 *
 * Lifecycle:
 *
 *  1. Spring Boot constructs this bean via [ProfileLoaderAutoConfiguration].
 *  2. The auto-config's `ApplicationReadyEvent` listener calls [load].
 *  3. [load] walks the configured directory, processes each `*.yaml` /
 *     `*.yml` file, and populates the registry.
 *
 * Errors:
 *
 *  - Parse failures and validation failures are logged and the bad
 *    file is skipped — by default. Other valid profiles continue to
 *    load.
 *  - When `fail-on-validation-error: true` is set, the first
 *    validation violation is rethrown as
 *    [ProfileManifestValidationException], aborting application
 *    startup. Operators flip this on in CI gates.
 *  - A missing directory is NOT a failure — the loader logs an INFO
 *    line ("no profiles directory found, skipping") and exits. This
 *    preserves "the host boots cleanly even when no profiles are
 *    configured" which Epic #426 needs.
 *
 * # What this loader DOES NOT do (yet)
 *
 * v1 scope is parse + validate + register-in-catalog. The runtime
 * binding of ingest sources, mappings, and audit enrichments from the
 * parsed manifest happens in follow-on tickets under Epic #426:
 *
 *  - Wiring `ingest:` entries to IngestSource plugin instances —
 *    Epic #426 (vendor profile catalog, runtime activation).
 *  - Copying `.fml` files into Matchbox's StructureMap mount path or
 *    posting them via the Matchbox API — Epic #426.
 *  - Passing `audit:` enrichments through the audit-event-fhir
 *    plugin's enricher chain — Epic #426 (audit-aware profiles).
 *
 * The `ProfileRegistry` populated here is the seam those follow-on
 * tickets read from.
 */
class ProfileLoader(
    private val properties: ProfileLoaderProperties,
    private val registry: ProfileRegistry,
    private val parser: ProfileManifestParser = ProfileManifestParser(),
    private val validator: ProfileManifestValidator = ProfileManifestValidator(),
) {

    private val log = LoggerFactory.getLogger(ProfileLoader::class.java)

    /**
     * Scan the configured directory and load every manifest it finds.
     * Idempotent — repeated calls reload the registry (useful for the
     * future "hot reload on directory change" enhancement; not yet
     * wired up to a file watcher).
     *
     * @return [LoadReport] summarizing how many manifests were
     *   processed, loaded, and skipped. Mostly useful in tests; the
     *   production wiring also logs the same data at INFO level.
     */
    fun load(): LoadReport {
        val dir = Paths.get(properties.directory)
        if (!Files.isDirectory(dir)) {
            log.info(
                "Profile directory '{}' not present; no profiles will be loaded.",
                properties.directory,
            )
            return LoadReport(directory = properties.directory, scanned = 0, loaded = 0, skipped = 0)
        }

        val manifestFiles = listManifestFiles(dir)
        log.info(
            "Profile directory '{}' contains {} manifest file(s).",
            properties.directory,
            manifestFiles.size,
        )

        var loaded = 0
        var skipped = 0
        for (file in manifestFiles) {
            if (processFile(file)) {
                loaded++
            } else {
                skipped++
            }
        }

        log.info(
            "Profile loading complete: {} loaded, {} skipped (directory='{}').",
            loaded,
            skipped,
            properties.directory,
        )

        return LoadReport(
            directory = properties.directory,
            scanned = manifestFiles.size,
            loaded = loaded,
            skipped = skipped,
        )
    }

    private fun processFile(file: Path): Boolean {
        // First, parse. A parse failure is non-recoverable for this file
        // regardless of the strict-mode flag: there's nothing to validate.
        val manifest: ProfileManifest = try {
            parser.parse(file)
        } catch (e: ProfileManifestParseException) {
            log.warn("Skipping profile manifest '{}': parse error: {}", file, e.message)
            return false
        }

        // Validate the raw YAML against the schema. Note: we validate the
        // file, not the bound object — this lets the validator emit
        // YAML-key-shaped pointer paths in violations.
        val violations: List<ValidationViolation> = try {
            validator.validate(file)
        } catch (e: IOException) {
            log.warn("Skipping profile manifest '{}': could not re-read for validation: {}", file, e.message)
            return false
        }

        if (violations.isNotEmpty()) {
            val summary = violations.joinToString("; ") { "${it.pointer}: ${it.message}" }
            if (properties.failOnValidationError) {
                log.error(
                    "Profile manifest '{}' failed validation in strict mode: {}",
                    file,
                    summary,
                )
                throw ProfileManifestValidationException(violations)
            }
            log.warn(
                "Skipping profile manifest '{}': validation failed: {}",
                file,
                summary,
            )
            return false
        }

        // Register. A duplicate id is logged but not rejected; the newer
        // file wins.
        val previous = registry.register(manifest)
        if (previous != null) {
            log.warn(
                "Profile id '{}' loaded from '{}' replaced an existing entry (previous version: {}, new version: {}).",
                manifest.profile.id,
                file,
                previous.profile.version,
                manifest.profile.version,
            )
        } else {
            log.info(
                "Loaded profile '{}' version '{}' from '{}'.",
                manifest.profile.id,
                manifest.profile.version,
                file,
            )
        }
        return true
    }

    private fun listManifestFiles(dir: Path): List<Path> {
        return Files.walk(dir, 1).use { stream ->
            stream
                .filter { it.isRegularFile() }
                .filter { it.extension.equals("yaml", ignoreCase = true) || it.extension.equals("yml", ignoreCase = true) }
                .sorted(compareBy { it.name })
                .toList()
        }
    }
}

/**
 * Outcome of a [ProfileLoader.load] run.
 *
 * @property directory The directory that was scanned (mirrors the
 *   configured value so tests can assert without re-reading config).
 * @property scanned Number of `*.yaml` / `*.yml` files found.
 * @property loaded Number that parsed + validated cleanly and were
 *   registered.
 * @property skipped `scanned - loaded` — the count that failed parse
 *   or validation (and didn't trigger strict-mode abort).
 */
data class LoadReport(
    val directory: String,
    val scanned: Int,
    val loaded: Int,
    val skipped: Int,
)
