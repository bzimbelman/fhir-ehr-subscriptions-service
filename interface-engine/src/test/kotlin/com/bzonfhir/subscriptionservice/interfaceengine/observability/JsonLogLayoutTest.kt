package com.bzonfhir.subscriptionservice.interfaceengine.observability

import ch.qos.logback.classic.Level
import ch.qos.logback.classic.LoggerContext
import ch.qos.logback.core.OutputStreamAppender
import com.fasterxml.jackson.databind.JsonNode
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

/**
 * Validates the JSON log layout emitted by `logback-spring.xml` (Epic #387,
 * ticket #388).
 *
 * We deliberately do NOT load `logback-spring.xml` directly — Spring's
 * `<springProfile>` extension needs the Spring Boot LoggingSystem bootstrap
 * to interpret. Instead, we construct a [LogstashEncoder] configured the
 * same way our XML configures it (same `customFields`, same MDC inclusion)
 * and assert the produced record meets the schema documented in
 * docs/observability/log-schema.md.
 *
 * If this test changes its assertions about field names, the doc must
 * change with it; #397's CI gate will eventually enforce that diff.
 */
class JsonLogLayoutTest {

    private lateinit var loggerContext: LoggerContext
    private lateinit var buffer: ByteArrayOutputStream
    private lateinit var appender: OutputStreamAppender<ch.qos.logback.classic.spi.ILoggingEvent>
    private val mapper = ObjectMapper()

    @BeforeEach
    fun setUp() {
        loggerContext = LoggerFactory.getILoggerFactory() as LoggerContext
        buffer = ByteArrayOutputStream()

        // Encoder configured to match logback-spring.xml. Keeping the
        // construction here (not pulling from the XML) so this test asserts
        // on the SCHEMA, not on whether the XML successfully loaded —
        // separate concerns, separate tests.
        val encoder = LogstashEncoder().apply {
            context = loggerContext
            customFields = """{"schema_version":"1.0"}"""
            // include correlation_id as a top-level field, as the prod
            // config does.
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

    private fun readRecord(): JsonNode {
        // Trim trailing newline + parse. LogstashEncoder writes one record
        // per line by default in prod mode.
        val line = buffer.toString(StandardCharsets.UTF_8).trim()
        return mapper.readTree(line)
    }

    @Test
    fun `simple INFO line includes schema_version, level, message, logger_name, thread_name, timestamp`() {
        val log = LoggerFactory.getLogger("test.simple")
        log.info("hello world")

        val node = readRecord()
        assertThat(node.get("@timestamp").asText()).isNotBlank()
        assertThat(node.get("level").asText()).isEqualTo("INFO")
        assertThat(node.get("logger_name").asText()).isEqualTo("test.simple")
        assertThat(node.get("message").asText()).isEqualTo("hello world")
        assertThat(node.get("thread_name").asText()).isNotBlank()
        assertThat(node.get("schema_version").asText()).isEqualTo("1.0")
    }

    @Test
    fun `MDC correlation_id is rendered as top-level field`() {
        val log = LoggerFactory.getLogger("test.mdc")
        val id = "test-corr-1234"
        MDC.put(CorrelationId.MDC_KEY, id)
        try {
            log.info("with mdc")
        } finally {
            MDC.remove(CorrelationId.MDC_KEY)
        }

        val node = readRecord()
        assertThat(node.get("correlation_id").asText()).isEqualTo(id)
    }

    @Test
    fun `exception adds stack_trace field`() {
        val log = LoggerFactory.getLogger("test.error")
        log.warn("with exception", IllegalStateException("boom"))

        val node = readRecord()
        assertThat(node.get("stack_trace").asText())
            .contains("IllegalStateException")
            .contains("boom")
    }

    @Test
    fun `parameter substitution is rendered into message field`() {
        val log = LoggerFactory.getLogger("test.params")
        log.info("id={} type={}", 42, "ADT_A04")

        val node = readRecord()
        assertThat(node.get("message").asText()).isEqualTo("id=42 type=ADT_A04")
    }
}
