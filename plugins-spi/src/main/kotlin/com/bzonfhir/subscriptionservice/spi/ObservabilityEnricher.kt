package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta

/**
 * SPI #7a — Observability enricher.
 *
 * The runtime already emits structured JSON logs (Logstash encoder, see
 * `interface-engine/build.gradle.kts`) and Prometheus metrics (Micrometer
 * registry). An [ObservabilityEnricher] plugin extends what gets
 * stamped on those signals — without forking the loggers/metrics
 * themselves.
 *
 * Two extension hooks:
 *
 *  - [enrichLogFields] — invoked once per log statement that crosses
 *    the runtime's enricher gate. Returned key/value pairs are added
 *    as MDC fields and the Logstash encoder serializes them as
 *    top-level JSON fields.
 *  - [enrichMetricLabels] — invoked when the runtime is about to emit
 *    a metric with the named series. Returned key/value pairs become
 *    metric labels.
 *
 * # Use cases
 *
 *  - Tie metrics to a customer's billing system (`customer.id`,
 *    `billing.plan`).
 *  - Add custom compliance fields (`compliance.region`,
 *    `data.classification`).
 *  - Cross-reference with a customer-side trace id
 *    (`customer.trace.id`).
 *
 * # Hot path warning
 *
 * Both methods fire on every log / every metric increment. Implementations
 * must be cheap — no I/O, no database lookups, no JSON parsing. Pre-compute
 * everything at plugin construction time.
 *
 * # Stability: EXPERIMENTAL
 */
interface ObservabilityEnricher {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Return key/value pairs the runtime should add to the MDC for
     * the log record about to be emitted. The keys land as top-level
     * fields in the Logstash JSON. Returning an empty map is fine
     * (the enricher had nothing to contribute for this context).
     */
    fun enrichLogFields(ctx: ObservabilityContext): Map<String, String>

    /**
     * Return key/value pairs the runtime should add as metric labels
     * to the named series. Keys must be valid Prometheus label names
     * (alphanumeric + underscore, no dots). Returning an empty map
     * is fine.
     *
     * Beware label cardinality: Prometheus performance degrades with
     * many unique label combinations. Avoid high-cardinality values
     * (user ids, message ids); prefer bounded enums (region, tier,
     * supplier).
     *
     * @param metricName The fully-qualified metric series name
     *   (`"mllp.received.count"`, `"worker.transform.duration"`).
     *   Enrichers can branch on this to emit labels selectively.
     */
    fun enrichMetricLabels(metricName: String, ctx: ObservabilityContext): Map<String, String>
}
