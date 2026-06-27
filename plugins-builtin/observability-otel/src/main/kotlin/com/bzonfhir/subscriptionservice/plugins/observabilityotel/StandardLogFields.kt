package com.bzonfhir.subscriptionservice.plugins.observabilityotel

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext

/**
 * The canonical log-field catalog the built-in
 * [OtelObservabilityEnricher] emits on every JSON log record.
 *
 * # Why a separate object
 *
 * The catalog is the contract between the plugin and
 * `docs/observability/log-schema.md` (ticket #397). Keeping it in a
 * separate top-level object — rather than inlining the keys in the
 * enricher — lets:
 *
 *  - Tests assert on the catalog directly without booting Spring.
 *  - Other plugins reference [KEY_*] constants when they want to add a
 *    field NEXT TO our standard set without colliding on the same key.
 *  - The log-schema doc CI gate (#397) cross-checks every documented
 *    field against the constants here.
 *
 * # Schema version
 *
 * [SCHEMA_VERSION] is `"1.0"` and SHALL NOT change without a deprecation
 * cycle per the rules in `docs/observability/log-schema.md`. The
 * Logback config (`interface-engine/src/main/resources/logback-spring.xml`)
 * also pins `schema_version="1.0"` via `customFields`; both copies must
 * agree, and the [StandardLogFieldsTest] holds an explicit assertion on
 * the constant value as a typo canary.
 *
 * # Field selection
 *
 * The keys covered here are the "what every log line gets" set:
 *
 *  - [KEY_SCHEMA_VERSION] — REQUIRED, always emitted
 *  - [KEY_CORRELATION_ID] — OPTIONAL at the schema level, REQUIRED on
 *    per-message log lines
 *  - [KEY_TRACE_ID] / [KEY_SPAN_ID] — OPTIONAL, present when the OTel
 *    SDK is enabled and a span is active
 *  - [KEY_SOURCE_PROTOCOL] / [KEY_SOURCE_SYSTEM] / [KEY_MESSAGE_TYPE] —
 *    per-pipeline-stage fields the runtime stashes on the
 *    [ObservabilityContext.attributes] before invoking enrichers
 *
 * Anything not on this list is a candidate for a third-party
 * `ObservabilityEnricher` to add — that's the whole point of having the
 * plugin surface.
 */
object StandardLogFields {

    /**
     * The current log-schema version. Pinned to `"1.0"` per ticket #397.
     *
     * Changing this is a contract break (MAJOR bump): coordinate with
     * downstream log-aggregator dashboards and run the deprecation
     * cycle described in `docs/observability/log-schema.md`.
     */
    const val SCHEMA_VERSION: String = "1.0"

    /** REQUIRED on every record — the schema version pinned above. */
    const val KEY_SCHEMA_VERSION: String = "schema_version"

    /**
     * Server-assigned UUID per inbound message/request. REQUIRED on
     * per-message log lines (set by the servlet filter, Camel route
     * processor, or worker `processOne` entry).
     */
    const val KEY_CORRELATION_ID: String = "correlation_id"

    /** W3C trace id (32-char hex). OPTIONAL — present when OTel SDK is enabled. */
    const val KEY_TRACE_ID: String = "trace_id"

    /** W3C span id (16-char hex). OPTIONAL — present when OTel SDK is enabled. */
    const val KEY_SPAN_ID: String = "span_id"

    /** e.g. `hl7v2-mllp`, `http`. OPTIONAL — per-pipeline-stage value. */
    const val KEY_SOURCE_PROTOCOL: String = "source_protocol"

    /** e.g. `EPIC`, `CERNER`. OPTIONAL — bounded to the operator's customer set. */
    const val KEY_SOURCE_SYSTEM: String = "source_system"

    /** e.g. `ADT_A04`, `ORU_R01`. OPTIONAL — bounded to the v2 message types we support. */
    const val KEY_MESSAGE_TYPE: String = "message_type"

    /**
     * Build the standard log-field map from an [ObservabilityContext].
     *
     * Contract:
     *
     *  - The returned map ALWAYS contains [KEY_SCHEMA_VERSION].
     *  - It contains [KEY_CORRELATION_ID] iff the context's
     *    `correlationId` is non-blank. Blank values are treated as "no
     *    scope" (startup banners) and the key is omitted rather than
     *    emitted as empty string.
     *  - Optional keys ([KEY_TRACE_ID], [KEY_SPAN_ID],
     *    [KEY_SOURCE_PROTOCOL], [KEY_SOURCE_SYSTEM], [KEY_MESSAGE_TYPE])
     *    are emitted iff a non-blank value is present on
     *    [ObservabilityContext.attributes] under the same key.
     *
     * # Hot path warning
     *
     * This method fires on every log line that crosses the runtime's
     * enricher gate. It does no I/O, no string formatting, no regex —
     * just a fixed set of map lookups and conditional inserts. Keep it
     * that way.
     */
    fun build(ctx: ObservabilityContext): Map<String, String> {
        // LinkedHashMap so iteration order is deterministic for tests
        // and for any log-aggregator that round-trips through ordered
        // JSON. Initial capacity sized for the worst case (7 entries)
        // to avoid the rehash that a default capacity would force.
        val fields = LinkedHashMap<String, String>(8)
        fields[KEY_SCHEMA_VERSION] = SCHEMA_VERSION

        if (ctx.correlationId.isNotBlank()) {
            fields[KEY_CORRELATION_ID] = ctx.correlationId
        }

        putIfPresent(fields, KEY_TRACE_ID, ctx.attributes[KEY_TRACE_ID])
        putIfPresent(fields, KEY_SPAN_ID, ctx.attributes[KEY_SPAN_ID])
        putIfPresent(fields, KEY_SOURCE_PROTOCOL, ctx.attributes[KEY_SOURCE_PROTOCOL])
        putIfPresent(fields, KEY_SOURCE_SYSTEM, ctx.attributes[KEY_SOURCE_SYSTEM])
        putIfPresent(fields, KEY_MESSAGE_TYPE, ctx.attributes[KEY_MESSAGE_TYPE])

        return fields
    }

    /**
     * Insert `value` into `map[key]` only when the value is non-null
     * and non-blank. Sole purpose: keep [build] readable when there are
     * five optional inserts in a row.
     */
    private fun putIfPresent(
        map: MutableMap<String, String>,
        key: String,
        value: String?,
    ) {
        if (!value.isNullOrBlank()) {
            map[key] = value
        }
    }
}
