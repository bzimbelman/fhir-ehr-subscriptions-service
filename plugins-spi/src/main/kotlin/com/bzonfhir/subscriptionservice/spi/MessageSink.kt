package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.SinkOutcome
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionEvent

/**
 * SPI #3 — Pluggable outbound delivery.
 *
 * By default the engine delivers fired Subscriptions over the channels
 * FHIR Subscription itself supports (`rest-hook`, `email`, `message`,
 * `websocket`). A [MessageSink] plugin can route the same event ELSEWHERE
 * in addition or instead — Kafka, S3, an HL7 outbound, a custom REST
 * endpoint, a CDC pipeline.
 *
 * Sinks compose: more than one sink can be registered for a given event,
 * and the runtime fans the event out to all of them. Per-sink delivery
 * outcomes are tracked independently — one sink succeeding and another
 * failing produces one of each persisted in the delivery log.
 *
 * # Registration
 *
 * Plugins register via `ServiceLoader` (Java's standard mechanism).
 * Profile-scoped sinks (e.g. "only fire this sink when the originating
 * profile is Epic") can implement a sibling interface in a later
 * minor version of the SPI; today the runtime applies any registered
 * sink to every event.
 *
 * # Idempotency
 *
 * The runtime guarantees AT LEAST ONCE delivery semantics:
 * [SubscriptionEvent.eventId] may arrive at a sink more than once if a
 * retry overlaps with the previous attempt's eventual success. Sinks
 * should de-duplicate on `eventId` for write-once downstream systems.
 *
 * # Failure handling
 *
 * Sinks return a [SinkOutcome] rather than throwing. A thrown exception
 * is treated as `Failed(retryable = true)` with the exception message as
 * the reason. Use [SinkOutcome.Failed] explicitly when the failure is
 * permanent (`retryable = false`) so the runtime DLQs immediately.
 *
 * # Stability: EXPERIMENTAL
 */
interface MessageSink {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Handle one fired Subscription event. The implementation may
     * block; the runtime invokes this from a delivery thread pool.
     *
     * Return [SinkOutcome.Delivered] for success (optionally including
     * a downstream ack id), [SinkOutcome.Failed] for failure. The
     * runtime persists the outcome alongside the event in the delivery
     * log for forensic queries.
     */
    fun handle(event: SubscriptionEvent): SinkOutcome
}
