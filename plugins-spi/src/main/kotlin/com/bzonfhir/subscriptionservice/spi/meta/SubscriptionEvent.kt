package com.bzonfhir.subscriptionservice.spi.meta

import java.time.Instant

/**
 * What a [com.bzonfhir.subscriptionservice.spi.MessageSink] or a
 * [com.bzonfhir.subscriptionservice.spi.SubscriptionFilter] sees when a
 * FHIR Subscription fires.
 *
 * After ingest → mapping → persistence has produced FHIR resources, the
 * subscription engine evaluates each active Subscription's criteria
 * against the changed resource and (if it matches) emits a
 * [SubscriptionEvent] into the delivery pipeline.
 *
 * The SPI carries:
 *
 *   - **The trigger.** Which resource changed, what operation caused
 *     the change ([SubscriptionTrigger.CREATE]/UPDATE/DELETE), the
 *     resource type + id.
 *   - **The subscription identity.** Just the subscription id and a
 *     handful of pre-extracted hints (criteria, channel type) so a
 *     sink or filter doesn't need to re-read the Subscription from
 *     HAPI to know what to do.
 *   - **The resource payload.** Serialized FHIR JSON. The SPI keeps
 *     this as a string rather than a parsed `IBaseResource` so the
 *     plugin author can choose their own JSON library — not every
 *     plugin needs HAPI on its compile classpath.
 *
 * @property eventId A monotonic UUID v4 the runtime generates per fired
 *   event. Used for idempotent delivery: a sink that's seen this
 *   eventId before can short-circuit. Distinct from
 *   [PipelineMessage.correlationId], which traces back to the inbound
 *   message that produced the resource; eventId is local to the
 *   outbound delivery.
 * @property occurredAt When the subscription was evaluated and matched.
 * @property correlationId The end-to-end correlation id originally
 *   stamped on the inbound message that caused this resource change.
 *   Lets a sink reach back to "which HL7 message did this come from."
 * @property subscriptionId The HAPI logical id of the Subscription
 *   that's firing (`Subscription/abc-123`).
 * @property subscriptionCriteria The Subscription's `criteria` field
 *   (e.g. `"Patient?identifier=mrn|123"`), provided for context.
 *   Sinks generally don't re-evaluate the criteria; this is for
 *   logging/debugging.
 * @property channelType The Subscription channel type
 *   (`"rest-hook"`, `"websocket"`, `"message"`, etc.). A custom
 *   sink may key its behaviour off the channel type without parsing
 *   the full Subscription resource.
 * @property trigger Which lifecycle event on the resource caused
 *   this Subscription to fire.
 * @property resourceType FHIR resource type that triggered the fire
 *   (`"Patient"`, `"Observation"`).
 * @property resourceId Logical id of the triggering resource.
 * @property resourceJson Full FHIR JSON of the resource. Plugin authors
 *   can parse it with whichever library they prefer; we keep it as
 *   a string at the SPI boundary.
 */
data class SubscriptionEvent(
    val eventId: String,
    val occurredAt: Instant,
    val correlationId: String,
    val subscriptionId: String,
    val subscriptionCriteria: String,
    val channelType: String,
    val trigger: SubscriptionTrigger,
    val resourceType: String,
    val resourceId: String,
    val resourceJson: String,
)

/**
 * The lifecycle event that caused a FHIR Subscription to fire.
 *
 * FHIR Subscriptions don't natively distinguish create / update / delete
 * in their criteria, but downstream sinks often care (e.g. an outbound
 * sink that posts to a CDC topic). The interface engine fills this in
 * based on which HAPI interceptor pointcut produced the change.
 */
enum class SubscriptionTrigger {
    CREATE,
    UPDATE,
    DELETE,
}

/**
 * Static context the runtime hands a [com.bzonfhir.subscriptionservice.spi.SubscriptionFilter]
 * about the Subscription being evaluated, so the filter can decide
 * whether to fire WITHOUT having to look the Subscription up in HAPI.
 *
 * Kept separate from [SubscriptionEvent] because some filters care only
 * about the subscription (e.g. "is this subscription in a tenant we're
 * allowed to deliver for"), not the firing resource.
 *
 * @property subscriptionId HAPI logical id.
 * @property tenantId The partition id when multi-tenancy is on; `null`
 *   on single-tenant deployments.
 * @property channelType Same as [SubscriptionEvent.channelType].
 * @property tags Optional Subscription meta tags. Used by filters to key
 *   on operator-supplied labels (e.g. `urgency=stat`).
 */
data class SubscriptionContext(
    val subscriptionId: String,
    val tenantId: String?,
    val channelType: String,
    val tags: Map<String, String> = emptyMap(),
)

/**
 * Return type of [com.bzonfhir.subscriptionservice.spi.MessageSink.handle].
 *
 * Sealed so consumers can `when`-exhaustively over success vs failure;
 * the runtime uses the failure reason to drive retry policy and DLQ
 * routing.
 */
sealed class SinkOutcome {
    /**
     * The sink delivered the event. The runtime will mark the
     * Subscription as having seen this event and will not redeliver.
     *
     * @property externalId Optional ack/correlation id the sink received
     *   from its downstream system (e.g. an HTTP `Location` header
     *   from a REST-hook target, a Kafka offset, an S3 object key).
     *   Persisted alongside the event for forensic queries.
     */
    data class Delivered(val externalId: String? = null) : SinkOutcome()

    /**
     * The sink could not deliver. The runtime applies the configured
     * retry policy and ultimately moves the event to the DLQ if all
     * retries exhaust.
     *
     * @property reason Human-readable description; surfaces in logs.
     * @property retryable Hint to the runtime: `true` for transient
     *   failures (5xx, timeout) where the runtime should retry; `false`
     *   for permanent failures (4xx, bad payload) where retry won't
     *   help and the runtime should DLQ immediately.
     */
    data class Failed(val reason: String, val retryable: Boolean = true) : SinkOutcome()
}
