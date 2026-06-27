package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.plugins.observabilityotel.OtelObservabilityEnricher
import com.bzonfhir.subscriptionservice.plugins.observabilityotel.StandardLogFields
import com.bzonfhir.subscriptionservice.plugins.observabilityotel.StandardMetricLabels
import com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher
import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Unit tests for [ObservabilityEnricherChain] (ticket #433).
 *
 * These tests don't boot a Spring context — they instantiate the chain
 * directly with a curated enricher list. The wiring confidence (Spring
 * discovers all `ObservabilityEnricher` beans on the classpath) is
 * covered by an integration-flavoured test elsewhere; this file focuses
 * on the merge semantics in isolation.
 */
class ObservabilityEnricherChainTest {

    @Test
    fun `single enricher contributions flow straight through`() {
        val chain = ObservabilityEnricherChain(listOf(OtelObservabilityEnricher()))

        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = null,
            pipelineStage = "mllp.receive",
            attributes = mapOf(
                StandardLogFields.KEY_SOURCE_PROTOCOL to "hl7v2-mllp",
                StandardLogFields.KEY_SOURCE_SYSTEM to "EPIC",
            ),
        )

        val fields = chain.enrichLogFields(ctx)
        assertThat(fields[StandardLogFields.KEY_SCHEMA_VERSION]).isEqualTo("1.0")
        assertThat(fields[StandardLogFields.KEY_CORRELATION_ID]).isEqualTo("cid-1")
        assertThat(fields[StandardLogFields.KEY_SOURCE_PROTOCOL]).isEqualTo("hl7v2-mllp")
    }

    @Test
    fun `multiple enrichers union their log-field contributions`() {
        val builtin = OtelObservabilityEnricher()
        val tenantEnricher = object : ObservabilityEnricher {
            override val meta = PluginMeta(
                id = "test-tenant",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMUNITY,
                description = "test tenant enricher",
            )

            override fun enrichLogFields(ctx: ObservabilityContext): Map<String, String> =
                mapOf("customer.id" to "acme")

            override fun enrichMetricLabels(
                metricName: String,
                ctx: ObservabilityContext,
            ): Map<String, String> = emptyMap()
        }

        val chain = ObservabilityEnricherChain(listOf(builtin, tenantEnricher))

        val fields = chain.enrichLogFields(
            ObservabilityContext(
                correlationId = "cid-1",
                tenantId = null,
                pipelineStage = "mllp.receive",
            ),
        )

        // Both contributions land.
        assertThat(fields[StandardLogFields.KEY_SCHEMA_VERSION]).isEqualTo("1.0")
        assertThat(fields["customer.id"]).isEqualTo("acme")
    }

    @Test
    fun `later enricher overwrites on key collision`() {
        // Two enrichers contribute the same key. The later one wins.
        // We don't throw on collisions — see the rationale in
        // ObservabilityEnricherChain.kt.
        val first = stubEnricher("first") { _ -> mapOf("k" to "v1") }
        val second = stubEnricher("second") { _ -> mapOf("k" to "v2") }
        val chain = ObservabilityEnricherChain(listOf(first, second))

        val fields = chain.enrichLogFields(
            ObservabilityContext("cid", null, "stage"),
        )

        assertThat(fields["k"]).isEqualTo("v2")
    }

    @Test
    fun `empty enricher list returns empty maps`() {
        val chain = ObservabilityEnricherChain(emptyList())

        assertThat(chain.enrichLogFields(ObservabilityContext("cid", null, "stage")))
            .isEmpty()
        assertThat(
            chain.enrichMetricLabels(
                StandardMetricLabels.METRIC_INGESTED_MESSAGES,
                ObservabilityContext("cid", null, "stage"),
            ),
        ).isEmpty()
    }

    @Test
    fun `metric labels for ingested_messages flow through the chain`() {
        val chain = ObservabilityEnricherChain(listOf(OtelObservabilityEnricher()))

        val labels = chain.enrichMetricLabels(
            StandardMetricLabels.METRIC_INGESTED_MESSAGES,
            ObservabilityContext(
                correlationId = "",
                tenantId = null,
                pipelineStage = "ingest.persist",
                attributes = mapOf(
                    StandardMetricLabels.LABEL_SOURCE_PROTOCOL to "hl7v2-mllp",
                    StandardMetricLabels.LABEL_SOURCE_SYSTEM to "EPIC",
                    StandardMetricLabels.LABEL_MESSAGE_TYPE to "ADT_A04",
                    StandardMetricLabels.LABEL_STATUS to "RECEIVED",
                ),
            ),
        )

        assertThat(labels)
            .containsEntry(StandardMetricLabels.LABEL_SOURCE_PROTOCOL, "hl7v2-mllp")
            .containsEntry(StandardMetricLabels.LABEL_SOURCE_SYSTEM, "EPIC")
            .containsEntry(StandardMetricLabels.LABEL_MESSAGE_TYPE, "ADT_A04")
            .containsEntry(StandardMetricLabels.LABEL_STATUS, "RECEIVED")
    }

    private fun stubEnricher(
        id: String,
        logFields: (ObservabilityContext) -> Map<String, String>,
    ): ObservabilityEnricher = object : ObservabilityEnricher {
        override val meta = PluginMeta(
            id = id,
            version = "0.0.1",
            schemaVersion = 1,
            supplier = PluginSupplier.COMMUNITY,
            description = "test stub",
        )

        override fun enrichLogFields(ctx: ObservabilityContext): Map<String, String> =
            logFields(ctx)

        override fun enrichMetricLabels(
            metricName: String,
            ctx: ObservabilityContext,
        ): Map<String, String> = emptyMap()
    }
}
