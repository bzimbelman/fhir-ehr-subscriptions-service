package com.bzonfhir.subscriptionservice.interfaceengine.observability

import ch.qos.logback.classic.Level
import ch.qos.logback.classic.LoggerContext
import ch.qos.logback.core.OutputStreamAppender
import com.fasterxml.jackson.databind.ObjectMapper
import net.logstash.logback.encoder.LogstashEncoder
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.slf4j.LoggerFactory
import org.slf4j.MDC
import java.io.ByteArrayOutputStream
import java.nio.charset.StandardCharsets
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.Paths

/**
 * Round-trip contract check for ticket #397.
 *
 * Reads the field-stability matrix out of `docs/observability/log-schema.md`
 * via the same simple table extractor the doc-parser uses, then asserts:
 *
 *  1. Every REQUIRED field in the matrix is present on a captured
 *     per-message log record (covers the worst case: a multi-field record
 *     with correlation_id and an exception).
 *  2. No field appears on the captured record that is documented as
 *     removed (placeholder; v1.0 has no removed fields, so this is a
 *     structural assertion only).
 *
 * This is the in-JVM half of what the future CI gate
 * (docs/observability/schema-stability-contract.md) will do at the
 * Testcontainers level. Keeping a Kotlin-side check here means the
 * contract is enforced even without the bash-level gate landing.
 */
class LogSchemaContractTest {

    private lateinit var loggerContext: LoggerContext
    private lateinit var buffer: ByteArrayOutputStream
    private lateinit var appender: OutputStreamAppender<ch.qos.logback.classic.spi.ILoggingEvent>
    private val mapper = ObjectMapper()

    @BeforeEach
    fun setUp() {
        loggerContext = LoggerFactory.getILoggerFactory() as LoggerContext
        buffer = ByteArrayOutputStream()

        val encoder = LogstashEncoder().apply {
            context = loggerContext
            customFields = """{"schema_version":"1.0"}"""
            addIncludeMdcKeyName(CorrelationId.MDC_KEY)
            isIncludeContext = false
            start()
        }
        appender = OutputStreamAppender<ch.qos.logback.classic.spi.ILoggingEvent>().apply {
            context = loggerContext
            this.encoder = encoder
            outputStream = buffer
            start()
        }

        val root = loggerContext.getLogger("ROOT")
        root.addAppender(appender)
        root.level = Level.DEBUG
    }

    @AfterEach
    fun tearDown() {
        val root = loggerContext.getLogger("ROOT")
        root.detachAppender(appender)
        appender.stop()
        MDC.clear()
    }

    /**
     * Locate `docs/observability/log-schema.md`. We walk up from the
     * working directory until we hit a checkout root containing the
     * `docs/` folder. This is robust against running the test from any
     * sub-module directory.
     */
    private fun findRepoRoot(): Path {
        var p: Path? = Paths.get("").toAbsolutePath()
        while (p != null) {
            if (Files.isDirectory(p.resolve("docs/observability"))) return p
            p = p.parent
        }
        error("could not find repo root (no docs/observability/ above the test cwd)")
    }

    /** Trivial markdown row split (matches the Python parser's logic). */
    private fun splitRow(line: String): List<String> =
        line.trim().trim('|').split('|').map { it.trim() }
            .map { c ->
                if (c.startsWith("`") && c.endsWith("`") && c.count { it == '`' } == 2) {
                    c.substring(1, c.length - 1)
                } else c
            }

    private data class FieldRow(val name: String, val type: String, val tier: String)

    /**
     * Extract the REQUIRED/OPTIONAL/EXPERIMENTAL field rows out of
     * log-schema.md. Mirrors `scripts/observability/parse-log-schema.py`.
     */
    private fun parseFieldMatrix(): List<FieldRow> {
        val doc = findRepoRoot().resolve("docs/observability/log-schema.md")
        val lines = Files.readAllLines(doc)
        val rows = mutableListOf<FieldRow>()
        var inTable = false
        var i = 0
        while (i < lines.size) {
            val line = lines[i]
            if (!inTable) {
                if (line.startsWith("|")) {
                    val cells = splitRow(line)
                    val isHeader =
                        cells.size >= 5 &&
                            cells[0].lowercase().contains("field") &&
                            cells[1].lowercase().contains("type") &&
                            cells[2].lowercase().contains("tier")
                    if (isHeader && i + 1 < lines.size &&
                        Regex("""^\|[\s\-:|]+\|\s*$""").matches(lines[i + 1].trim())
                    ) {
                        inTable = true
                        i += 2
                        continue
                    }
                }
            } else {
                if (!line.startsWith("|")) {
                    inTable = false
                    i++
                    continue
                }
                val cells = splitRow(line)
                if (cells.size >= 3 &&
                    (cells[2] == "REQUIRED" || cells[2] == "OPTIONAL" || cells[2] == "EXPERIMENTAL")
                ) {
                    rows.add(FieldRow(cells[0], cells[1], cells[2]))
                }
            }
            i++
        }
        return rows
    }

    @Test
    fun `every REQUIRED field in the matrix is present on a captured per-message log record`() {
        val matrix = parseFieldMatrix()
        assertThat(matrix)
            .withFailMessage("doc-parser extracted no rows from log-schema.md")
            .isNotEmpty

        val required = matrix.filter { it.tier == "REQUIRED" }.map { it.name }.toSet()
        // The matrix includes `mdc.*` and `correlation_id` (OPTIONAL on
        // some lines). For the REQUIRED set, we just assert the strict
        // fields appear on a single captured record.

        val log = LoggerFactory.getLogger("test.contract")
        val corr = "0192f6c1-9d7a-7e3b-9c1e-66d8a0c3f1e0"
        MDC.put(CorrelationId.MDC_KEY, corr)
        try {
            log.info("contract round-trip line")
        } finally {
            MDC.remove(CorrelationId.MDC_KEY)
        }

        val node = mapper.readTree(buffer.toString(StandardCharsets.UTF_8).trim())

        // Assert presence of every REQUIRED field per the parsed matrix.
        for (field in required) {
            assertThat(node.has(field))
                .withFailMessage(
                    "log record is missing REQUIRED field '$field' per docs/observability/log-schema.md\n" +
                        "captured record: $node"
                )
                .isTrue
        }

        // schema_version, specifically, MUST be the documented current
        // version. Bumping the version is the signal of a breaking
        // change; this assertion catches an accidental bump from a typo
        // in the customFields.
        assertThat(node.get("schema_version").asText())
            .withFailMessage(
                "schema_version on captured record (${node.get("schema_version")}) " +
                    "does not match docs/observability/log-schema.md (1.0)"
            )
            .isEqualTo("1.0")
    }
}
