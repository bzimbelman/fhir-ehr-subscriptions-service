package com.bzonfhir.subscriptionservice.plugins.observabilityotel

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Contract tests for [StandardLogFields].
 *
 * The catalog is the single source of truth for the well-known log
 * fields the plugin emits on every JSON log record. Treating it as a
 * separate object (rather than inlining the key strings inside the
 * enricher) lets tests assert on the catalog directly without booting
 * a Spring context.
 *
 * `schema_version` is the most load-bearing entry here: ticket #397
 * pins it to `"1.0"`, and the value SHALL NOT change without a major
 * deprecation cycle. The test holds the constant in-line so a future
 * accidental bump (e.g. someone changing `"1.0"` → `"1.1"` without
 * updating the doc) fails the test.
 */
class StandardLogFieldsTest {

    @Test
    fun `schema version is 1_0 — bumping this without a deprecation cycle is a contract break`() {
        assertThat(StandardLogFields.SCHEMA_VERSION).isEqualTo("1.0")
    }

    @Test
    fun `well-known log field keys match the canonical catalog`() {
        // The exact set of keys is part of the contract — adding a new
        // key is a MINOR bump (and must come with a doc update), removing
        // one is a MAJOR bump. This assertion is the canary that catches
        // accidental drift in source code without a corresponding doc
        // change.
        assertThat(StandardLogFields.KEY_SCHEMA_VERSION).isEqualTo("schema_version")
        assertThat(StandardLogFields.KEY_CORRELATION_ID).isEqualTo("correlation_id")
        assertThat(StandardLogFields.KEY_TRACE_ID).isEqualTo("trace_id")
        assertThat(StandardLogFields.KEY_SPAN_ID).isEqualTo("span_id")
        assertThat(StandardLogFields.KEY_SOURCE_PROTOCOL).isEqualTo("source_protocol")
        assertThat(StandardLogFields.KEY_SOURCE_SYSTEM).isEqualTo("source_system")
        assertThat(StandardLogFields.KEY_MESSAGE_TYPE).isEqualTo("message_type")
    }

    @Test
    fun `build returns schema_version on every call even when context is sparse`() {
        // The `schema_version` field is REQUIRED on every record per
        // docs/observability/log-schema.md. Even a context with nothing
        // but a stage name must produce a map containing schema_version.
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "startup",
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields[StandardLogFields.KEY_SCHEMA_VERSION])
            .isEqualTo(StandardLogFields.SCHEMA_VERSION)
    }

    @Test
    fun `build emits correlation_id when present on the context`() {
        val ctx = ObservabilityContext(
            correlationId = "abc-1234",
            tenantId = null,
            pipelineStage = "mllp.receive",
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields[StandardLogFields.KEY_CORRELATION_ID]).isEqualTo("abc-1234")
    }

    @Test
    fun `build omits correlation_id when the context value is blank`() {
        // The runtime's CorrelationIdFilter guarantees the MDC value is
        // non-blank when a request scope is active. A blank value here
        // means "no scope" — startup banner, scheduled-task boilerplate.
        // We omit the field rather than emit an empty string so the
        // log-schema doc's "REQUIRED on per-message log lines only"
        // contract is preserved literally.
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "startup",
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_CORRELATION_ID)
    }

    @Test
    fun `build emits source_protocol when carried on context attributes`() {
        // The runtime stashes source_protocol on ObservabilityContext.attributes
        // because it's a per-stage value the pipeline stage knows, not
        // something the bare context type can model. The plugin reads it
        // out and lifts it to a top-level field on every log line.
        val ctx = ObservabilityContext(
            correlationId = "abc",
            tenantId = null,
            pipelineStage = "mllp.receive",
            attributes = mapOf(
                StandardLogFields.KEY_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardLogFields.KEY_SOURCE_SYSTEM to "EPIC",
                StandardLogFields.KEY_MESSAGE_TYPE to "ADT_A04",
            ),
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields[StandardLogFields.KEY_SOURCE_PROTOCOL]).isEqualTo("hl7v2-mllp")
        assertThat(fields[StandardLogFields.KEY_SOURCE_SYSTEM]).isEqualTo("EPIC")
        assertThat(fields[StandardLogFields.KEY_MESSAGE_TYPE]).isEqualTo("ADT_A04")
    }

    @Test
    fun `build emits trace_id and span_id when carried on context attributes`() {
        // Same shape as source_protocol — the runtime extracts the active
        // OTel span (when the SDK is enabled) and stashes the IDs on the
        // context attributes before invoking enrichers. The plugin lifts
        // them onto every log record so a `grep <trace_id>` and a Jaeger
        // search by the same id return matching sets of rows.
        val ctx = ObservabilityContext(
            correlationId = "abc",
            tenantId = null,
            pipelineStage = "worker.process",
            attributes = mapOf(
                StandardLogFields.KEY_TRACE_ID to "0123456789abcdef0123456789abcdef",
                StandardLogFields.KEY_SPAN_ID to "0123456789abcdef",
            ),
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields[StandardLogFields.KEY_TRACE_ID])
            .isEqualTo("0123456789abcdef0123456789abcdef")
        assertThat(fields[StandardLogFields.KEY_SPAN_ID]).isEqualTo("0123456789abcdef")
    }

    @Test
    fun `build omits absent attribute fields rather than emitting empty strings`() {
        // OPTIONAL fields are absent when not applicable (per the
        // log-schema tier definition). Emitting `""` would force every
        // log aggregator to defensively treat empty as missing — we
        // avoid that by simply not including the key.
        val ctx = ObservabilityContext(
            correlationId = "abc",
            tenantId = null,
            pipelineStage = "worker.process",
        )

        val fields = StandardLogFields.build(ctx)

        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_SOURCE_PROTOCOL)
        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_SOURCE_SYSTEM)
        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_MESSAGE_TYPE)
        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_TRACE_ID)
        assertThat(fields).doesNotContainKey(StandardLogFields.KEY_SPAN_ID)
    }
}
