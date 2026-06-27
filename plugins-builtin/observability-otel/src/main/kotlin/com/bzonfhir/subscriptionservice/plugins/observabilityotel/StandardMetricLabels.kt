package com.bzonfhir.subscriptionservice.plugins.observabilityotel

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext

/**
 * Bounded-cardinality Prometheus label catalog the built-in
 * [OtelObservabilityEnricher] emits per metric series.
 *
 * # Why catalog-driven, not free-form
 *
 * Prometheus performance degrades with unbounded label cardinality
 * (`docs/observability/metric-catalog.md` "PII and cardinality rules").
 * The catalog below is the runtime's defence: for each well-known
 * metric, the plugin returns ONLY the labels documented in the catalog
 * — even when the [ObservabilityContext.attributes] map carries more.
 * That keeps the cardinality bound consistent across call sites: the
 * caller can't accidentally add a high-cardinality label by stashing it
 * on the context.
 *
 * # Metric ↔ label set
 *
 * Mirrors the table in `docs/observability/metric-catalog.md` §Interface
 * engine — ingestion. When that catalog changes, the [LABEL_SETS] map
 * below must change too; the [OtelObservabilityEnricherTest] covers
 * every documented series so a doc/code drift fails the build.
 *
 * | Metric (series name)                                | Labels emitted                                                  |
 * |-----------------------------------------------------|------------------------------------------------------------------|
 * | `[METRIC_INGESTED_MESSAGES]`                        | source_protocol, source_system, message_type, status            |
 * | `[METRIC_TRANSFORM_DURATION]`                       | outcome                                                          |
 * | `[METRIC_HAPI_POST_DURATION]`                       | outcome                                                          |
 * | `[METRIC_DLQ_TRANSITIONS]`                          | source_protocol, reason                                          |
 * | `[METRIC_DLQ_CURRENT_SIZE]`                         | (none — single time-series)                                      |
 * | `[METRIC_RECEIVED_TO_DELIVERED]`                    | (none — single time-series)                                      |
 *
 * Unknown metric names return an empty label set. That's a deliberate
 * choice over throwing: a future code change adds a metric without
 * updating the catalog first, the metric just emits without
 * plugin-added labels (the same shape it had before the plugin
 * existed), and the CI gate from #397 surfaces the gap.
 *
 * # Hot path warning
 *
 * [build] fires on every metric increment. No I/O, no string mangling
 * — pure map lookups against the precomputed [LABEL_SETS].
 */
object StandardMetricLabels {

    // --- Metric series names. Mirror InterfaceEngineMetrics constants. ---

    /** Counter for `interface_engine_ingested_messages_total`. */
    const val METRIC_INGESTED_MESSAGES: String = "interface_engine.ingested_messages"

    /** Histogram for `interface_engine_transform_duration_seconds`. */
    const val METRIC_TRANSFORM_DURATION: String = "interface_engine.transform.duration"

    /** Histogram for `interface_engine_hapi_post_duration_seconds`. */
    const val METRIC_HAPI_POST_DURATION: String = "interface_engine.hapi_post.duration"

    /** Counter for `interface_engine_dlq_transitions_total`. */
    const val METRIC_DLQ_TRANSITIONS: String = "interface_engine.dlq_transitions"

    /** Gauge for `interface_engine_dlq_current_size`. */
    const val METRIC_DLQ_CURRENT_SIZE: String = "interface_engine.dlq_current_size"

    /** Histogram for `interface_engine_received_to_delivered_seconds`. */
    const val METRIC_RECEIVED_TO_DELIVERED: String = "interface_engine.received_to_delivered"

    // --- Label-name constants. snake_case per the Prometheus convention. ---

    /** `source_protocol` — closed enum: `hl7v2-mllp`, `http`, `rest-hook`, `websocket`. */
    const val LABEL_SOURCE_PROTOCOL: String = "source_protocol"

    /** `source_system` — bounded enum (EHR/lab name). Capped at 100/deployment per catalog. */
    const val LABEL_SOURCE_SYSTEM: String = "source_system"

    /** `message_type` — bounded enum (`ADT_A01`, `ORU_R01`, ~20 values across customers). */
    const val LABEL_MESSAGE_TYPE: String = "message_type"

    /** `status` — five-value enum: `RECEIVED`, `TRANSFORMING`, `DELIVERED`, `RETRYING`, `DEAD_LETTER`. */
    const val LABEL_STATUS: String = "status"

    /** `outcome` — two/three-value enum: `success`, `failure`, `timeout`. */
    const val LABEL_OUTCOME: String = "outcome"

    /**
     * `reason` — closed enum for DLQ failure shape (normalized via
     * `InterfaceEngineMetrics.normalizeDlqReason` to keep cardinality
     * bounded; the doc declares the high-level values
     * `MAX_ATTEMPTS_EXCEEDED`, `UNRECOVERABLE_ERROR`, `MANUAL_PURGE`).
     */
    const val LABEL_REASON: String = "reason"

    /**
     * Pre-computed per-metric label sets. Lookup is O(1) on the metric
     * name, and the inner set is iterated in declaration order so
     * test assertions on `keys` stay deterministic.
     */
    private val LABEL_SETS: Map<String, Set<String>> = mapOf(
        METRIC_INGESTED_MESSAGES to linkedSetOf(
            LABEL_SOURCE_PROTOCOL,
            LABEL_SOURCE_SYSTEM,
            LABEL_MESSAGE_TYPE,
            LABEL_STATUS,
        ),
        METRIC_TRANSFORM_DURATION to linkedSetOf(LABEL_OUTCOME),
        METRIC_HAPI_POST_DURATION to linkedSetOf(LABEL_OUTCOME),
        METRIC_DLQ_TRANSITIONS to linkedSetOf(LABEL_SOURCE_PROTOCOL, LABEL_REASON),
        METRIC_DLQ_CURRENT_SIZE to emptySet(),
        METRIC_RECEIVED_TO_DELIVERED to emptySet(),
    )

    /**
     * Resolve the bounded label set for `metricName` from [ObservabilityContext.attributes].
     *
     *  - Unknown metric → returns empty map. The metric is emitted
     *    without plugin-added labels (same shape as before the plugin
     *    existed).
     *  - Known metric → emits the documented labels iff a non-blank
     *    value is present on the context's attributes under that key.
     *    Missing values are omitted (rather than emitting `""`), so a
     *    distinct "unknown" time-series doesn't appear by accident.
     *
     * Returns a [LinkedHashMap] for deterministic iteration order in
     * tests and in any downstream that round-trips through ordered JSON.
     */
    fun build(metricName: String, ctx: ObservabilityContext): Map<String, String> {
        val labelNames = LABEL_SETS[metricName] ?: return emptyMap()
        if (labelNames.isEmpty()) return emptyMap()

        val labels = LinkedHashMap<String, String>(labelNames.size)
        for (labelName in labelNames) {
            val value = ctx.attributes[labelName]
            if (!value.isNullOrBlank()) {
                labels[labelName] = value
            }
        }
        return labels
    }

    /**
     * Return the set of documented label names for [metricName], or an
     * empty set when the metric isn't in the catalog. Exposed so the
     * runtime can answer "what labels does this metric carry?" without
     * having to construct a fake context — useful for the CI gate the
     * doc-stability ticket (#397) will land.
     */
    fun knownLabelNames(metricName: String): Set<String> =
        LABEL_SETS[metricName] ?: emptySet()
}
