package com.bzonfhir.subscriptionservice.plugins.observabilityotel

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Behaviour tests for the built-in [OtelObservabilityEnricher].
 *
 * Two surfaces:
 *
 *  - [OtelObservabilityEnricher.enrichLogFields] — what the runtime
 *    stamps on every JSON log record.
 *  - [OtelObservabilityEnricher.enrichMetricLabels] — what Prometheus
 *    labels the runtime stamps on the named metric series.
 *
 * The metric-label half is the harder of the two because the catalog is
 * SERIES-SPECIFIC (per `docs/observability/metric-catalog.md`). One
 * metric takes `outcome` and nothing else; another takes
 * `source_protocol` + `reason`; the DLQ-size gauge takes no labels at
 * all. The tests below cover each documented series so a future PR
 * adjusting the catalog must update the corresponding test.
 */
class OtelObservabilityEnricherTest {

    private val enricher = OtelObservabilityEnricher()

    // ------------------------------------------------------------------------
    // meta
    // ------------------------------------------------------------------------

    @Test
    fun `meta declares stable plugin identity`() {
        assertThat(enricher.meta.id).isEqualTo("observability-otel")
        // schemaVersion = 1 — the version of the plugins-spi shape this
        // plugin was authored against (matches plugins-spi 0.1.0-SNAPSHOT).
        assertThat(enricher.meta.schemaVersion).isEqualTo(1)
        assertThat(enricher.meta.supplier).isEqualTo(PluginSupplier.FIRST_PARTY)
        assertThat(enricher.meta.version).isNotBlank()
        assertThat(enricher.meta.description).isNotBlank()
    }

    // ------------------------------------------------------------------------
    // enrichLogFields — the per-log enrichment surface
    // ------------------------------------------------------------------------

    @Test
    fun `enrichLogFields returns schema_version on every call`() {
        // Even a context with nothing in it must produce schema_version —
        // it's REQUIRED on every record per docs/observability/log-schema.md.
        val ctx = ObservabilityContext(
            correlationId = "",
            tenantId = null,
            pipelineStage = "startup",
        )

        val fields = enricher.enrichLogFields(ctx)

        assertThat(fields[StandardLogFields.KEY_SCHEMA_VERSION]).isEqualTo("1.0")
    }

    @Test
    fun `enrichLogFields lifts the full standard field set`() {
        val ctx = ObservabilityContext(
            correlationId = "abc-1234",
            tenantId = null,
            pipelineStage = "mllp.receive",
            attributes = mapOf(
                StandardLogFields.KEY_TRACE_ID to "0123456789abcdef0123456789abcdef",
                StandardLogFields.KEY_SPAN_ID to "0123456789abcdef",
                StandardLogFields.KEY_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardLogFields.KEY_SOURCE_SYSTEM to "EPIC",
                StandardLogFields.KEY_MESSAGE_TYPE to "ADT^A04",
            ),
        )

        val fields = enricher.enrichLogFields(ctx)

        assertThat(fields).containsEntry(StandardLogFields.KEY_SCHEMA_VERSION, "1.0")
        assertThat(fields).containsEntry(StandardLogFields.KEY_CORRELATION_ID, "abc-1234")
        assertThat(fields).containsEntry(
            StandardLogFields.KEY_TRACE_ID,
            "0123456789abcdef0123456789abcdef",
        )
        assertThat(fields).containsEntry(StandardLogFields.KEY_SPAN_ID, "0123456789abcdef")
        assertThat(fields).containsEntry(StandardLogFields.KEY_SOURCE_PROTOCOL, "hl7v2-mllp")
        assertThat(fields).containsEntry(StandardLogFields.KEY_SOURCE_SYSTEM, "EPIC")
        assertThat(fields).containsEntry(StandardLogFields.KEY_MESSAGE_TYPE, "ADT^A04")
    }

    // ------------------------------------------------------------------------
    // enrichMetricLabels — per-series catalog
    // ------------------------------------------------------------------------

    @Test
    fun `enrichMetricLabels for ingested_messages emits the documented label set`() {
        // Per docs/observability/metric-catalog.md the
        // `interface_engine_ingested_messages_total` series carries
        // `status`, `source_protocol`, `source_system`. We additionally
        // accept `message_type` from operators today (it lives on the
        // counter in InterfaceEngineMetrics.kt) — the catalog should
        // grow to document it. For now the plugin returns exactly the
        // bounded-cardinality set the runtime puts on the metric.
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "ingest.persist",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardMetricLabels.LABEL_SOURCE_SYSTEM to "EPIC",
                StandardMetricLabels.LABEL_MESSAGE_TYPE to "ADT_A04",
                StandardMetricLabels.LABEL_STATUS to "RECEIVED",
            ),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_INGESTED_MESSAGES,
            ctx,
        )

        assertThat(labels)
            .containsEntry(StandardMetricLabels.LABEL_SOURCE_PROTOCOL, "hl7v2-mllp")
            .containsEntry(StandardMetricLabels.LABEL_SOURCE_SYSTEM, "EPIC")
            .containsEntry(StandardMetricLabels.LABEL_MESSAGE_TYPE, "ADT_A04")
            .containsEntry(StandardMetricLabels.LABEL_STATUS, "RECEIVED")
    }

    @Test
    fun `enrichMetricLabels for transform_duration emits only outcome`() {
        // Per the catalog, the `_duration_seconds` histograms take a
        // two-value `outcome` label — and nothing else. Bounded
        // cardinality by construction. Even when source_protocol /
        // source_system are present on the context, they MUST NOT land
        // on this series — that would blow up the time-series count
        // for no analytical value.
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "worker.transform",
            attributes = mapOf(
                StandardMetricLabels.LABEL_OUTCOME to "success",
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardMetricLabels.LABEL_SOURCE_SYSTEM to "EPIC",
            ),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_TRANSFORM_DURATION,
            ctx,
        )

        assertThat(labels).containsEntry(StandardMetricLabels.LABEL_OUTCOME, "success")
        assertThat(labels.keys)
            .containsExactly(StandardMetricLabels.LABEL_OUTCOME)
    }

    @Test
    fun `enrichMetricLabels for hapi_post_duration emits only outcome`() {
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "worker.deliver",
            attributes = mapOf(StandardMetricLabels.LABEL_OUTCOME to "failure"),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_HAPI_POST_DURATION,
            ctx,
        )

        assertThat(labels.keys).containsExactly(StandardMetricLabels.LABEL_OUTCOME)
        assertThat(labels[StandardMetricLabels.LABEL_OUTCOME]).isEqualTo("failure")
    }

    @Test
    fun `enrichMetricLabels for dlq_transitions emits source_protocol and reason`() {
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "worker.dlq",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardMetricLabels.LABEL_REASON to "MAX_ATTEMPTS_EXCEEDED",
            ),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_DLQ_TRANSITIONS,
            ctx,
        )

        assertThat(labels.keys).containsExactlyInAnyOrder(
            StandardMetricLabels.LABEL_SOURCE_PROTOCOL,
            StandardMetricLabels.LABEL_REASON,
        )
    }

    @Test
    fun `enrichMetricLabels for dlq_current_size emits NO labels`() {
        // The gauge carries no labels (catalog cap = 1 series).
        // Even when the context has every label populated, we MUST
        // return an empty map.
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "dlq.poll",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardMetricLabels.LABEL_REASON to "X",
                StandardMetricLabels.LABEL_STATUS to "DEAD_LETTER",
            ),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_DLQ_CURRENT_SIZE,
            ctx,
        )

        assertThat(labels).isEmpty()
    }

    @Test
    fun `enrichMetricLabels for received_to_delivered emits NO labels`() {
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "worker.deliver",
            attributes = mapOf(StandardMetricLabels.LABEL_OUTCOME to "success"),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_RECEIVED_TO_DELIVERED,
            ctx,
        )

        assertThat(labels).isEmpty()
    }

    @Test
    fun `enrichMetricLabels for an unknown metric name returns empty`() {
        // Unknown series — neither in our catalog nor in any other
        // plugin's. Returning empty is the safe default; the runtime
        // emits the metric without any plugin-added labels, which
        // matches today's behaviour for any metric a future code change
        // adds without first updating the catalog.
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "anywhere",
            attributes = mapOf(StandardMetricLabels.LABEL_OUTCOME to "success"),
        )

        val labels = enricher.enrichMetricLabels("interface_engine.future.unknown", ctx)

        assertThat(labels).isEmpty()
    }

    @Test
    fun `enrichMetricLabels omits labels whose value is blank in the context`() {
        // Defensive: a context with KEY_SOURCE_SYSTEM = "" should NOT
        // produce `source_system=""` on the metric — that'd create a
        // distinct time-series for "unknown system" and confuse cardinality
        // budgets. Blank values are treated as absent.
        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "ingest.persist",
            attributes = mapOf(
                StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardMetricLabels.LABEL_SOURCE_SYSTEM to "",
                StandardMetricLabels.LABEL_MESSAGE_TYPE to "ADT_A04",
                StandardMetricLabels.LABEL_STATUS to "RECEIVED",
            ),
        )

        val labels = enricher.enrichMetricLabels(
            StandardMetricLabels.METRIC_INGESTED_MESSAGES,
            ctx,
        )

        assertThat(labels).doesNotContainKey(StandardMetricLabels.LABEL_SOURCE_SYSTEM)
        assertThat(labels).containsKey(StandardMetricLabels.LABEL_SOURCE_PROTOCOL)
    }
}
