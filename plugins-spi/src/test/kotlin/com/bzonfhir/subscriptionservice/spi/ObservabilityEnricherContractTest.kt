package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

class ObservabilityEnricherContractTest {

    @Test
    fun `ObservabilityEnricher shape compiles and enrichers can branch on metric name`() {
        val enricher: ObservabilityEnricher = object : ObservabilityEnricher {
            override val meta = PluginMeta(
                id = "test-billing-labels",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMERCIAL,
                description = "Test stub billing-label enricher",
            )

            override fun enrichLogFields(ctx: ObservabilityContext): Map<String, String> =
                mapOf("billing.tenant" to (ctx.tenantId ?: "unknown"))

            override fun enrichMetricLabels(metricName: String, ctx: ObservabilityContext): Map<String, String> =
                // Only stamp billing labels on the metrics we care about — keeps
                // Prometheus cardinality bounded.
                if (metricName.startsWith("mllp.")) mapOf("tier" to "pro") else emptyMap()
        }

        val ctx = ObservabilityContext(
            correlationId = "cid-1",
            tenantId = "tenantA",
            pipelineStage = "mllp.receive",
        )

        val logFields = enricher.enrichLogFields(ctx)
        assertThat(logFields["billing.tenant"]).isEqualTo("tenantA")

        val mllpLabels = enricher.enrichMetricLabels("mllp.received.count", ctx)
        val hapiLabels = enricher.enrichMetricLabels("hapi.write.duration", ctx)
        assertThat(mllpLabels["tier"]).isEqualTo("pro")
        assertThat(hapiLabels).isEmpty()
    }
}
