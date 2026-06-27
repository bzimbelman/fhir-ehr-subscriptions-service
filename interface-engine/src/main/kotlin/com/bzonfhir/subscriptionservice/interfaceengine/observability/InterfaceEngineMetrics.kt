package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.plugins.observabilityotel.StandardMetricLabels
import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import io.micrometer.core.instrument.Counter
import io.micrometer.core.instrument.MeterRegistry
import io.micrometer.core.instrument.Tags
import io.micrometer.core.instrument.Timer
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.ObjectProvider
import org.springframework.stereotype.Component
import java.time.Duration

/**
 * Custom Micrometer metrics for the interface engine (Epic #387, ticket #389).
 *
 * The Prometheus registry maps Micrometer's dotted naming to Prometheus's
 * snake_case convention automatically:
 *
 *   Micrometer name `interface_engine.ingested_messages`
 *     becomes Prometheus  `interface_engine_ingested_messages_total`
 *
 * A counter named with the `_total` suffix in Micrometer's eye would end up
 * `_total_total` in Prometheus, so we name without the suffix and let the
 * registry add it. Same for `_seconds` on timers.
 *
 * ## HIGH-CARDINALITY LABELS: do NOT add the wrong ones
 *
 * Prometheus labels are aggregable — one time-series per unique label-value
 * combination. A label whose values come from a high-cardinality stream
 * (e.g. UUIDs, IP addresses, MRN, error message text) creates one time-
 * series per unique value, which can run a Prometheus into the millions of
 * series and OOM it. Rules followed below:
 *
 *   1. NEVER use these as label values:
 *        - correlation ids / UUIDs
 *        - patient identifiers (MRN, FHIR id)
 *        - raw error messages
 *        - IP addresses
 *        - SQL or HTTP URLs
 *        - request ids / control ids (HL7 MSH-10)
 *   2. PII never appears in a label. Labels are queryable; data values are
 *      not. A label is the wrong place for protected information by both
 *      cardinality AND privacy axes.
 *   3. `reason` on the DLQ counter is the most likely footgun. The
 *      `last_error` string would shred us — every transient failure spins
 *      up a new error string. [normalizeDlqReason] truncates to 60 chars
 *      and substitutes URL/UUID/integer-like sequences with placeholders
 *      so the cardinality stays bounded to the dozen-or-so distinct
 *      failure SHAPES we actually have.
 *
 * If you're tempted to add a label not listed in the constructor of each
 * metric below: read those rules first. The PrometheusMetricsTest enforces
 * them with an assertion that no observed label value contains a UUID, IP,
 * or string over 80 chars.
 */
@Component
class InterfaceEngineMetrics(
    private val meterRegistry: MeterRegistry,
    // Ticket #433: consult the ObservabilityEnricher chain for the per-metric
    // label catalog instead of hard-coding tag names in this class. An
    // ObjectProvider keeps the wiring optional during tests that boot a
    // narrow context without the plugin loaded — the helper falls back to
    // an empty chain in that case, and the tag-name constants below still
    // ship the legacy hard-coded shape for backward compatibility.
    enricherChain: ObjectProvider<ObservabilityEnricherChain>,
) {

    private val log = LoggerFactory.getLogger(InterfaceEngineMetrics::class.java)

    /**
     * Chain reference, resolved at construction time so the per-metric
     * label catalog is captured once rather than re-resolved on every
     * hot-path call. Nullable when no chain bean is present (very
     * narrow test contexts only).
     */
    private val chain: ObservabilityEnricherChain? = enricherChain.ifAvailable

    // -------------------------------------------------------------------------
    // Counter: interface_engine_ingested_messages_total
    //
    // Increments on every status transition the row makes through the durable
    // inbound store. Labels are LOW-CARDINALITY by construction — they're all
    // categorical enums or short strings under our control.
    // -------------------------------------------------------------------------

    /**
     * Increment the ingested-messages counter for one (terminal) status
     * transition. Called from [com.bzonfhir.subscriptionservice
     * .interfaceengine.persistence.IngestPersistService.persistReceived] for
     * `status=RECEIVED`, and from the worker for each terminal transition
     * (DELIVERED, FAILED, DEAD_LETTER). The contract is: ONE increment per
     * row per status change — not one per poll, not one per retry attempt.
     *
     * @param sourceProtocol e.g. `HL7V2_MLLP` (enum-bounded set of values)
     * @param sourceSystem the sender — bounded to our customer list
     *   (typically 1-10 values per environment). NEVER a free-form id.
     * @param messageType e.g. `ADT_A01` (bounded to the v2 message types
     *   we support; ~20 values across all customers)
     * @param status the new status — five possible values
     */
    fun incrementIngestedMessages(
        sourceProtocol: String,
        sourceSystem: String,
        messageType: String,
        status: String,
    ) {
        // Build a context and consult the enricher chain for any
        // plugin-contributed labels on top of the standard set. The chain
        // honours the catalog: third-party plugins can stamp tenant /
        // billing labels iff they explicitly opt in for this metric series.
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "ingest.persist",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to sourceProtocol,
                StandardMetricLabels.LABEL_SOURCE_SYSTEM to sourceSystem,
                StandardMetricLabels.LABEL_MESSAGE_TYPE to messageType,
                StandardMetricLabels.LABEL_STATUS to status,
            ),
        )
        val tags = tagsFor(METRIC_INGESTED_MESSAGES, ctx)
        Counter.builder(METRIC_INGESTED_MESSAGES)
            .description("Count of inbound messages by status transition")
            .tags(tags)
            .register(meterRegistry)
            .increment()
    }

    // -------------------------------------------------------------------------
    // Timer: interface_engine_transform_duration_seconds
    //
    // Wraps one Matchbox $transform call. Outcome=success/failure.
    // -------------------------------------------------------------------------

    /**
     * Record one Matchbox `$transform` round-trip. Caller times the call
     * themselves and passes a [Duration]; the helper picks the right
     * outcome bucket. Two-value outcome label keeps cardinality at exactly
     * 2 — far below any concerning threshold.
     */
    fun recordTransformDuration(duration: Duration, success: Boolean) {
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "worker.transform",
            attributes = mapOf(
                StandardMetricLabels.LABEL_OUTCOME to if (success) "success" else "failure",
            ),
        )
        val tags = tagsFor(METRIC_TRANSFORM_DURATION, ctx)
        Timer.builder(METRIC_TRANSFORM_DURATION)
            .description("Latency of one Matchbox StructureMap/\$transform call")
            .tags(tags)
            .register(meterRegistry)
            .record(duration)
    }

    // -------------------------------------------------------------------------
    // Timer: interface_engine_hapi_post_duration_seconds
    // -------------------------------------------------------------------------

    /**
     * Record one HAPI transaction Bundle POST round-trip. Same shape as
     * the transform timer.
     */
    fun recordHapiPostDuration(duration: Duration, success: Boolean) {
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "worker.deliver",
            attributes = mapOf(
                StandardMetricLabels.LABEL_OUTCOME to if (success) "success" else "failure",
            ),
        )
        val tags = tagsFor(METRIC_HAPI_POST_DURATION, ctx)
        Timer.builder(METRIC_HAPI_POST_DURATION)
            .description("Latency of one HAPI Bundle transaction POST")
            .tags(tags)
            .register(meterRegistry)
            .record(duration)
    }

    // -------------------------------------------------------------------------
    // Counter: interface_engine_dlq_transitions_total
    //
    // One increment per DEAD_LETTER transition. The `reason` label is the
    // primary cardinality risk on this whole class; [normalizeDlqReason]
    // collapses any high-cardinality fragments out of it.
    // -------------------------------------------------------------------------

    /**
     * Increment the DLQ-transitions counter. `rawReason` is typically the
     * worker's `last_error` text; we normalize it to a low-cardinality
     * SHAPE label before stamping the time-series.
     */
    fun incrementDlqTransitions(sourceProtocol: String, rawReason: String) {
        val normalizedReason = normalizeDlqReason(rawReason)
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "worker.dlq",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to sourceProtocol,
                StandardMetricLabels.LABEL_REASON to normalizedReason,
            ),
        )
        val tags = tagsFor(METRIC_DLQ_TRANSITIONS, ctx)
        Counter.builder(METRIC_DLQ_TRANSITIONS)
            .description("Count of message rows transitioning to DEAD_LETTER")
            .tags(tags)
            .register(meterRegistry)
            .increment()
    }

    // -------------------------------------------------------------------------
    // Timer: interface_engine_received_to_delivered_seconds
    //
    // End-to-end latency for the durable inbound pipeline — receive (#381)
    // through delivered (#382). Recorded by the worker on the success
    // terminal update.
    // -------------------------------------------------------------------------

    /**
     * Record the wall-clock time from `received_at` → `delivered_at` for
     * a single successful row. Skipped on FAILED / DEAD_LETTER rows (no
     * delivered_at to measure against).
     */
    fun recordReceivedToDelivered(duration: Duration) {
        Timer.builder(METRIC_RECEIVED_TO_DELIVERED)
            .description("End-to-end latency: row received → delivered")
            .register(meterRegistry)
            .record(duration)
    }

    /**
     * Build the Micrometer [Tags] for a metric series by consulting the
     * [ObservabilityEnricherChain]. The chain returns the union of
     * plugin-contributed labels (the built-in `OtelObservabilityEnricher`
     * stamps the standard catalog; third-party plugins can add tenant /
     * billing labels on top).
     *
     * When no chain is wired (extremely narrow test contexts that boot
     * `InterfaceEngineMetrics` without the observability plugin), this
     * falls back to the legacy hard-coded label set — extracted from
     * the context's attributes via [StandardMetricLabels.build]. The
     * resulting tag set is identical to what `OtelObservabilityEnricher`
     * would have returned, so series shape is stable across deployments.
     */
    private fun tagsFor(metricName: String, ctx: ObservabilityContext): Tags {
        val labels = chain?.enrichMetricLabels(metricName, ctx)
            ?: StandardMetricLabels.build(metricName, ctx)
        if (labels.isEmpty()) return Tags.empty()
        var tags = Tags.empty()
        for ((k, v) in labels) {
            tags = tags.and(k, v)
        }
        return tags
    }

    companion object {
        // Metric name constants. Centralized so tests can reference the same
        // strings the producer uses, and so a typo in a producer surfaces
        // immediately as a missing-series rather than as a silently-named
        // new metric.
        //
        // Naming convention: dots in Micrometer become underscores in
        // Prometheus. The `_total` / `_seconds` suffix is added by the
        // Prometheus registry automatically — do NOT include it here or
        // you'll get `..._total_total`.
        const val METRIC_INGESTED_MESSAGES = "interface_engine.ingested_messages"
        const val METRIC_TRANSFORM_DURATION = "interface_engine.transform.duration"
        const val METRIC_HAPI_POST_DURATION = "interface_engine.hapi_post.duration"
        const val METRIC_DLQ_TRANSITIONS = "interface_engine.dlq_transitions"
        const val METRIC_DLQ_CURRENT_SIZE = "interface_engine.dlq_current_size"
        const val METRIC_RECEIVED_TO_DELIVERED = "interface_engine.received_to_delivered"

        /**
         * Normalize a free-form error string to a low-cardinality SHAPE so
         * it's safe to use as a Prometheus label value.
         *
         * Steps:
         *
         *   1. Substitute UUIDs with `<uuid>`.
         *   2. Substitute URLs with `<url>`.
         *   3. Substitute long integer sequences with `<id>`.
         *   4. Substitute IPv4 / IPv6 addresses with `<ip>`.
         *   5. Truncate to 60 chars.
         *
         * The goal: collapse "matchbox: 422 for control_id=ABC123 from
         * 10.x.y.z" and "matchbox: 422 for control_id=XYZ987 from
         * 192.168.1.1" both to "matchbox: 422 for control_id=<id> from <ip>".
         * Same SHAPE → same time-series → bounded cardinality.
         *
         * Public so [PrometheusMetricsTest] can verify the contract.
         */
        fun normalizeDlqReason(raw: String): String {
            if (raw.isBlank()) return "unknown"
            var normalized = raw
                // UUIDs first — they overlap with the integer pattern below
                // if we run that one first.
                .replace(UUID_REGEX, "<uuid>")
                // URLs (http://..., https://...). Eager match; URLs end at
                // whitespace or end-of-string.
                .replace(URL_REGEX, "<url>")
                // IPv4 dotted quads, IPv6 colon-hex. Crude but covers the
                // common cases that appear in error strings.
                .replace(IPV4_REGEX, "<ip>")
                .replace(IPV6_REGEX, "<ip>")
                // Long integer-like sequences (control ids, sequence
                // numbers, etc.). 4+ digits to avoid matching HTTP status
                // codes (422, 500). Note: this DELIBERATELY does not
                // collapse short numbers — keeping "422" as the visible
                // shape token is what makes DLQ alerts grouped by status.
                .replace(LONG_INT_REGEX, "<id>")

            // Truncate to a bounded length AFTER substitution so an inbound
            // string full of UUIDs doesn't get cut mid-UUID and leave a
            // fragment in the label.
            if (normalized.length > MAX_REASON_LENGTH) {
                normalized = normalized.take(MAX_REASON_LENGTH)
            }
            return normalized
        }

        /** Max characters allowed in a `reason` label value. */
        const val MAX_REASON_LENGTH: Int = 60

        // Regex constants. Compiled once at class-load time.
        // UUID v4-ish: 8-4-4-4-12 hex. Case-insensitive.
        private val UUID_REGEX = Regex(
            "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}",
        )
        // URLs: protocol prefix is the giveaway.
        private val URL_REGEX = Regex("https?://[^\\s]+")
        // IPv4 dotted quad.
        private val IPV4_REGEX = Regex("\\b\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\b")
        // IPv6: a colon-separated hex group, at least 4 segments to dodge
        // false positives on things like "HH:MM:SS:msec" timestamps.
        private val IPV6_REGEX = Regex("\\b[0-9a-fA-F]{1,4}(:[0-9a-fA-F]{1,4}){3,7}\\b")
        // 4+ digit numbers — bigger than HTTP status codes so we keep the
        // useful "500", "422", "404" shape tokens visible.
        private val LONG_INT_REGEX = Regex("\\b\\d{4,}\\b")
    }
}

/**
 * Tag helpers — present so the producer code can build a Tags object once
 * and pass it down, rather than building one Tag per metric call. Not used
 * heavily today; reserved for code paths that will emit multiple metrics
 * on the same row (e.g. a future "validation" stage).
 */
@Suppress("unused")
fun ingestedMessageTags(
    sourceProtocol: String,
    sourceSystem: String,
    messageType: String,
    status: String,
): Tags = Tags.of(
    "source_protocol", sourceProtocol,
    "source_system", sourceSystem,
    "message_type", messageType,
    "status", status,
)
