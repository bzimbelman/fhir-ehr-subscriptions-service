package com.bzonfhir.subscriptionservice.plugins.profileloader

import com.fasterxml.jackson.databind.DeserializationFeature
import com.fasterxml.jackson.databind.ObjectMapper
import com.fasterxml.jackson.dataformat.yaml.YAMLFactory
import com.fasterxml.jackson.module.kotlin.KotlinModule
import com.fasterxml.jackson.module.kotlin.readValue
import java.io.InputStream
import java.nio.file.Files
import java.nio.file.Path

/**
 * Parse vendor-profile manifest YAML files into typed [ProfileManifest]
 * instances.
 *
 * The parser is deliberately format-only: it does NOT validate semantics
 * (unknown quirk keys, missing FML files, mutually-exclusive fields).
 * That work lives in [ProfileManifestValidator]. The split keeps each
 * piece independently testable and lets the loader emit
 * "this YAML doesn't parse" vs "this YAML parses but violates the schema"
 * as separate, actionable error categories.
 *
 * Jackson configuration choices, with rationale:
 *
 *   - `KotlinModule` registered — gives us data-class binding with
 *     constructor injection and `null`-safety honored by Kotlin's type
 *     system. Without this, Jackson would try to call no-arg
 *     constructors that don't exist on our data classes.
 *   - `FAIL_ON_UNKNOWN_PROPERTIES = true` — surfaces typos in manifests
 *     early. A profile author who writes `vendar:` instead of `vendor:`
 *     gets a parse error pointing at the bad key, not a silently
 *     missing field downstream. The JSON Schema also rejects unknown
 *     properties (`additionalProperties: false`); both layers catch it.
 *
 * The Jackson `ObjectMapper` instance is stateless and thread-safe
 * after construction — we hold a single shared instance per parser
 * instance, fine for the loader's single-threaded boot-time use.
 */
class ProfileManifestParser {

    private val mapper: ObjectMapper = ObjectMapper(YAMLFactory())
        .registerModule(KotlinModule.Builder().build())
        .configure(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES, true)

    /**
     * Parse a YAML manifest from a file system path.
     *
     * @throws ProfileManifestParseException when the file is unreadable
     *   or the YAML can't be bound to the [ProfileManifest] shape.
     */
    fun parse(path: Path): ProfileManifest {
        if (!Files.isRegularFile(path)) {
            throw ProfileManifestParseException(
                path = path.toString(),
                message = "not a regular file (does it exist?)",
            )
        }
        try {
            Files.newInputStream(path).use { stream ->
                return parseInternal(stream, path.toString())
            }
        } catch (e: ProfileManifestParseException) {
            throw e
        } catch (e: Exception) {
            throw ProfileManifestParseException(
                path = path.toString(),
                message = e.message ?: e.javaClass.simpleName,
                cause = e,
            )
        }
    }

    /**
     * Parse a YAML manifest from an `InputStream`. Useful for tests that
     * package fixtures as classpath resources and for any caller that
     * has already opened the stream itself.
     *
     * @param source A human-readable description of where the stream
     *   came from, used in error messages. Typically a file path or a
     *   classpath URI.
     */
    fun parse(stream: InputStream, source: String): ProfileManifest =
        try {
            parseInternal(stream, source)
        } catch (e: ProfileManifestParseException) {
            throw e
        } catch (e: Exception) {
            throw ProfileManifestParseException(
                path = source,
                message = e.message ?: e.javaClass.simpleName,
                cause = e,
            )
        }

    private fun parseInternal(stream: InputStream, source: String): ProfileManifest {
        val manifest: ProfileManifest = try {
            mapper.readValue(stream)
        } catch (e: Exception) {
            throw ProfileManifestParseException(
                path = source,
                message = "YAML did not bind to ProfileManifest: ${e.message ?: e.javaClass.simpleName}",
                cause = e,
            )
        }
        return manifest
    }
}

/**
 * Raised when a manifest YAML can't be parsed at all (file missing,
 * malformed YAML, missing required fields). Semantic-level violations
 * (unknown quirk key, missing FML file, schemaVersion mismatch) come
 * from [ProfileManifestValidator] and surface as
 * [ProfileManifestValidationException].
 *
 * @property path The file or classpath path of the offending manifest,
 *   surfaced verbatim in the message so operator logs identify the
 *   bad file immediately.
 */
class ProfileManifestParseException(
    val path: String,
    message: String,
    cause: Throwable? = null,
) : RuntimeException("Profile manifest '$path' could not be parsed: $message", cause)
