package com.bzonfhir.subscriptionservice.plugins.observabilityotel

import com.bzonfhir.subscriptionservice.plugins.observabilityotel.config.ObservabilityProperties
import com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher
import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier

/**
 * Built-in [ObservabilityEnricher] (ticket #433, Epic #425).
 *
 * Owns the "what gets stamped" half of the existing OTel / Prometheus /
 * correlation-id wiring:
 *
 *   - The standard log-field set every JSON record carries
 *     (`schema_version`, `correlation_id`, `trace_id`, `span_id`,
 *     `source_protocol`, `source_system`, `message_type`) — see
 *     [StandardLogFields].
 *   - The bounded Prometheus label set per well-known metric series —
 *     see [StandardMetricLabels].
 *
 * The TRANSPORT (OTel SDK init, Logback JSON encoder, Prometheus
 * actuator endpoint, scheduled DLQ-size poller) stays where it lives
 * today — in `interface-engine/...observability/`. The plugin only
 * decides what fields/labels appear; the infrastructure handles the
 * mechanics of writing them.
 *
 * # Why this plugin is FIRST_PARTY, not COMMUNITY
 *
 * Every customer deployment gets the standard log/metric shape from
 * this plugin; it's not opt-in. The plugin surface still exists (rather
 * than baking the catalog directly into the runtime) because:
 *
 *   1. Third-party plugins can compose alongside this one to add
 *      tenant-specific log fields or metric labels (`customer.id`,
 *      `billing.plan`, compliance fields). The runtime iterates all
 *      registered `ObservabilityEnricher` beans and unions their
 *      contributions.
 *   2. Treating "the standard set" as a plugin is the same shape as
 *      the audit / vendor-profile SPIs (#430): the host runs N
 *      enrichers, one of which we ship and several of which the
 *      operator/customer can configure.
 *
 * # Hot path
 *
 * Both [enrichLogFields] and [enrichMetricLabels] fire on every log
 * line / every metric increment. Implementation delegates to the
 * stateless `StandardLogFields.build` and `StandardMetricLabels.build`
 * helpers — pure map construction, no I/O, no string parsing, no
 * regex.
 */
class OtelObservabilityEnricher(
    /**
     * Optional configuration shim. Operators can swap the schema
     * version literal via `subscription-service.observability.schema-version`
     * for the deprecation cycle of a major bump. Defaults align with
     * [StandardLogFields.SCHEMA_VERSION].
     */
    @Suppress("unused")
    private val properties: ObservabilityProperties = ObservabilityProperties(),
) : ObservabilityEnricher {

    override val meta: PluginMeta = PluginMeta(
        id = "observability-otel",
        version = "0.1.0",
        // schemaVersion = 1 — the plugins-spi shape this was authored
        // against. Matches plugins-spi 0.1.0-SNAPSHOT.
        schemaVersion = 1,
        supplier = PluginSupplier.FIRST_PARTY,
        description = "Built-in observability enricher: standard log fields + Prometheus labels.",
    )

    override fun enrichLogFields(ctx: ObservabilityContext): Map<String, String> =
        StandardLogFields.build(ctx)

    override fun enrichMetricLabels(
        metricName: String,
        ctx: ObservabilityContext,
    ): Map<String, String> =
        StandardMetricLabels.build(metricName, ctx)
}
