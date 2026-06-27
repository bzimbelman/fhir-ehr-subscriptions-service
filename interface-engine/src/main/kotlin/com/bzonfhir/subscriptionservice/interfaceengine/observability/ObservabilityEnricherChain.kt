package com.bzonfhir.subscriptionservice.interfaceengine.observability

import com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher
import com.bzonfhir.subscriptionservice.spi.meta.ObservabilityContext
import org.slf4j.LoggerFactory
import org.springframework.stereotype.Component

/**
 * Composes every registered [ObservabilityEnricher] bean into one
 * call surface (ticket #433, Epic #425).
 *
 * # Why a chain
 *
 * Multiple enrichers may run side-by-side:
 *
 *   - The built-in [com.bzonfhir.subscriptionservice.plugins
 *     .observabilityotel.OtelObservabilityEnricher] (`FIRST_PARTY`) —
 *     contributes the standard fields/labels.
 *   - A commercial billing enricher (`COMMERCIAL`) — adds
 *     `customer.id` / `billing.plan`.
 *   - A community compliance enricher (`COMMUNITY`) — adds
 *     `data.classification`.
 *
 * Spring discovers every `ObservabilityEnricher` bean on the classpath
 * via constructor injection; this chain unions their contributions in
 * a single pass.
 *
 * # Last writer wins for key collisions
 *
 * When two enrichers return the same key from `enrichLogFields` or
 * `enrichMetricLabels`, the LATER one wins. We don't throw — that'd
 * leave the operator with a service that won't start if a single
 * third-party plugin conflicts with the built-in. Instead, we log a
 * DEBUG line per collision so the operator can see what happened in
 * the boot logs and decide which plugin to disable.
 *
 * # Hot path
 *
 * Both [enrichLogFields] and [enrichMetricLabels] fire on every log
 * line / every metric increment in the interface-engine. The chain
 * iterates an `Array<ObservabilityEnricher>` (sized at boot), invokes
 * each, and merges into a single LinkedHashMap. No reflection, no
 * locking — Spring's `List<ObservabilityEnricher>` injection gives us
 * an immutable bean order.
 *
 * # Empty-chain behaviour
 *
 * When no enrichers are registered (extremely unusual — the built-in
 * is always present in normal deployments), the chain returns empty
 * maps and the runtime falls back to whatever defaults the call site
 * provides. The plugin host (#431-#432) is responsible for surfacing
 * "no observability plugin loaded" as a startup warning if that
 * becomes a supported operating mode.
 */
@Component
class ObservabilityEnricherChain(
    private val enrichers: List<ObservabilityEnricher>,
) {

    private val log = LoggerFactory.getLogger(ObservabilityEnricherChain::class.java)

    init {
        // One INFO line at boot listing which enrichers are wired. Lets
        // an operator confirm the plugin actually loaded without grep-ing
        // through Spring's enormous bean-creation banner.
        log.info(
            "ObservabilityEnricherChain wired with {} enricher(s): {}",
            enrichers.size,
            enrichers.map { it.meta.id },
        )
    }

    /**
     * Aggregate every registered enricher's log-field contribution into
     * one map. Iteration order matches the Spring bean order, so a
     * higher-priority enricher (declared first via `@Order` or
     * `@Priority`) lands its fields in the head of the map; later
     * enrichers overwrite on key collisions.
     */
    fun enrichLogFields(ctx: ObservabilityContext): Map<String, String> {
        if (enrichers.isEmpty()) return emptyMap()
        val merged = LinkedHashMap<String, String>(8)
        for (enricher in enrichers) {
            val contribution = enricher.enrichLogFields(ctx)
            if (contribution.isEmpty()) continue
            for ((k, v) in contribution) {
                if (log.isDebugEnabled && merged.containsKey(k) && merged[k] != v) {
                    log.debug(
                        "ObservabilityEnricher key collision: '{}' was '{}', '{}' overwrote with '{}'",
                        k, merged[k], enricher.meta.id, v,
                    )
                }
                merged[k] = v
            }
        }
        return merged
    }

    /**
     * Aggregate every registered enricher's metric-label contribution
     * for `metricName`. Same merge semantics as [enrichLogFields].
     */
    fun enrichMetricLabels(
        metricName: String,
        ctx: ObservabilityContext,
    ): Map<String, String> {
        if (enrichers.isEmpty()) return emptyMap()
        val merged = LinkedHashMap<String, String>(8)
        for (enricher in enrichers) {
            val contribution = enricher.enrichMetricLabels(metricName, ctx)
            if (contribution.isEmpty()) continue
            for ((k, v) in contribution) {
                if (log.isDebugEnabled && merged.containsKey(k) && merged[k] != v) {
                    log.debug(
                        "ObservabilityEnricher label collision on metric '{}': '{}' was '{}', '{}' overwrote with '{}'",
                        metricName, k, merged[k], enricher.meta.id, v,
                    )
                }
                merged[k] = v
            }
        }
        return merged
    }

    /**
     * Test-friendly accessor for the wired enricher list. Used by
     * `EnricherChainWiringTest` to assert the built-in plugin landed
     * in the chain without having to inspect the Spring context
     * directly.
     */
    internal fun enrichers(): List<ObservabilityEnricher> = enrichers
}
