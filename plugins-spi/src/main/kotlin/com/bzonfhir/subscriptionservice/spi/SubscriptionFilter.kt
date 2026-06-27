package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionContext
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionEvent

/**
 * SPI #4 — Programmatic Subscription filter.
 *
 * FHIR Subscription `criteria` are an MDB-style matcher — they handle
 * "fire on Observation where patient = X" but can't easily express
 * "fire only on weekdays between 9 and 5" or "fire only when our ML
 * model scores this observation above threshold Y."
 *
 * A [SubscriptionFilter] plugin lets a deployment add programmatic
 * filtering on top of the criteria match. The runtime evaluates each
 * registered filter AFTER the criteria match succeeds and AFTER the
 * principal authorization passes; only if every filter returns `true`
 * does the event proceed to the sinks.
 *
 * # Filter ordering
 *
 * Multiple filters are AND-composed: every filter must return `true`
 * for the event to fire. If any filter returns `false`, the event is
 * dropped with the filter's [meta.id] recorded in the audit trail so
 * operators can see which filter blocked which fire.
 *
 * # Determinism
 *
 * Filters are expected to be stateless and deterministic — given the
 * same `event` and `subscription`, return the same boolean every call.
 * The runtime makes no caching guarantee, but a deterministic filter
 * makes replay-style debugging possible.
 *
 * # Stability: EXPERIMENTAL
 */
interface SubscriptionFilter {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Return `true` to allow the event to fire, `false` to suppress it.
     *
     * The filter must not throw under normal operation. A thrown
     * exception is treated as "filter errored, event suppressed" and
     * logged with the filter id so operators can investigate.
     *
     * @param event The fired Subscription event.
     * @param subscription Static context about the subscription
     *   (tenant id, channel type, operator-supplied tags) so the
     *   filter doesn't need to look up the subscription in HAPI.
     */
    fun shouldFire(event: SubscriptionEvent, subscription: SubscriptionContext): Boolean
}
