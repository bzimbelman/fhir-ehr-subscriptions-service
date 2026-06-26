package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessage
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageRepository
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageStatus
import com.fasterxml.jackson.annotation.JsonInclude
import com.fasterxml.jackson.annotation.JsonProperty
import org.hl7.fhir.r4.model.Subscription
import org.slf4j.LoggerFactory
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RestController
import java.time.OffsetDateTime

/**
 * Operator admin REST API for the per-message downstream-effects view
 * (Epic #387, ticket #392).
 *
 * Today an operator triaging "did my subscriber actually get notified for
 * this admit?" has to bounce across three surfaces: the [AdminMessagesController]
 * row for the inbound HL7v2 message (#384), the [AdminSubscriptionsController]
 * health summary for delivery counts (#390), and Prometheus aggregates for
 * trend data (#389). None of those join "this specific inbound message →
 * these specific Subscription matches → these specific notifications" in
 * one place. This controller does.
 *
 * Endpoint:
 *
 *   GET /admin/messages/{id}/effects
 *
 * Mounted under the same `/admin/` prefix the existing two controllers use,
 * so the [AdminAuthInterceptor] bearer-token gate (configured on the
 * `/admin/` glob) covers this endpoint without any per-class wiring.
 *
 * ## Data assembly
 *
 * The response joins data from three sources, in order of completeness:
 *
 *   1. The `ingested_messages` row — fully owned by the interface engine.
 *      We always have this; the basic message/transform timing fields are
 *      always populated.
 *   2. `created_resource_refs` (V005, ticket #392) — populated by the
 *      worker after a successful HAPI transaction POST. We have this for
 *      every row that went DELIVERED via the HAPI path; rows in other
 *      states (RECEIVED, FAILED, DEAD_LETTER) have it as NULL.
 *   3. Subscription matches and delivery attempts — these live in HAPI's
 *      datastore. We fetch them best-effort via [HapiSubscriptionStatusClient];
 *      gaps are documented in the response as `tbd` markers rather than
 *      silently presented as "no matches".
 *
 * ## `effects_status` field
 *
 * Top-level status field for quick triage:
 *
 *   - `delivered` — message reached HAPI; resource refs present, subs
 *     populated best-effort.
 *   - `pending`   — message is still in RECEIVED or TRANSFORMING; no
 *     effects to show yet. Resource list is empty; subs are empty.
 *   - `failed`    — message is FAILED with retries remaining or
 *     DEAD_LETTER. Resource list is empty; surface `last_error`.
 *   - `unknown`   — message is DELIVERED but `created_resource_refs` is
 *     NULL (pre-V005 row, OR the no-transform short-circuit path which
 *     never called HAPI). Resource list and subs both empty.
 *
 * The operator UI shows the appropriate widget for each status; alerts
 * key on the `failed` state.
 */
@RestController
@RequestMapping("/admin/messages")
class AdminMessageEffectsController(
    private val repository: IngestedMessageRepository,
    private val statusClient: HapiSubscriptionStatusClient,
) {

    private val log = LoggerFactory.getLogger(AdminMessageEffectsController::class.java)

    @GetMapping("/{id}/effects")
    fun effects(@PathVariable id: Long): ResponseEntity<Any> {
        val row = repository.findById(id).orElse(null)
            ?: return ResponseEntity.status(404).body(
                mapOf(
                    "error" to "not_found",
                    "message" to "no ingested_message with id=$id",
                ),
            )

        val effectsStatus = computeEffectsStatus(row)

        // Resource list comes straight off the row. NULL means "pre-V005
        // row OR no-transform short-circuit"; we surface that as an empty
        // list with effects_status="unknown" rather than synthesizing
        // anything misleading.
        val resources: List<CreatedResource> = row.createdResourceRefs
            ?.map { ref -> CreatedResource.fromReference(ref) }
            .orEmpty()

        // Subscription matching + notification listing only makes sense
        // when the row went through HAPI. For pending/failed/unknown we
        // short-circuit to empty lists; the operator looks at the
        // message + last_error and stops there.
        val (subs, notifications) = if (effectsStatus == EFFECTS_STATUS_DELIVERED && resources.isNotEmpty()) {
            try {
                resolveSubscriptionsAndNotifications(resources, row.deliveredAt)
            } catch (ex: Exception) {
                // Don't fail the whole response on a HAPI hiccup; the message
                // + resource list are the operator's primary triage data.
                // Log + return empty for the HAPI-derived sections.
                log.warn(
                    "effects: HAPI side data fetch failed for id={}: {}",
                    id,
                    ex.message,
                )
                Pair(emptyList(), emptyList())
            }
        } else {
            Pair(emptyList(), emptyList())
        }

        return ResponseEntity.ok(
            MessageEffectsResponse(
                effectsStatus = effectsStatus,
                message = MessageSummaryView.fromRow(row),
                transform = TransformView.fromRow(row),
                fhirResourcesCreated = resources,
                subscriptionsMatched = subs,
                notificationsFired = notifications,
            ),
        )
    }

    /**
     * Decide the top-level `effects_status` for the response. See class
     * doc for the four states.
     *
     * Internal so the unit test can poke at the boundary cases (e.g.
     * DELIVERED-but-NULL-refs, a freshly-RECEIVED row) without hitting
     * the full HTTP surface.
     */
    internal fun computeEffectsStatus(row: IngestedMessage): String = when (row.status) {
        IngestedMessageStatus.RECEIVED,
        IngestedMessageStatus.TRANSFORMING,
        -> EFFECTS_STATUS_PENDING

        IngestedMessageStatus.FAILED,
        IngestedMessageStatus.DEAD_LETTER,
        -> EFFECTS_STATUS_FAILED

        IngestedMessageStatus.DELIVERED ->
            if (row.createdResourceRefs == null) {
                // DELIVERED with NULL refs = either a pre-V005 row or the
                // no-transform short-circuit. Either way the operator
                // can't see effects; flag it as unknown rather than
                // silently presenting an empty effects view.
                EFFECTS_STATUS_UNKNOWN
            } else {
                EFFECTS_STATUS_DELIVERED
            }
    }

    /**
     * For each created resource, ask HAPI which active Subscriptions
     * would match it AND list any delivery attempts whose timestamp
     * overlaps the message's delivered-at window.
     *
     * This is the "approximation" path documented in the ticket: we don't
     * have a per-notification join key that says "this delivery attempt
     * was triggered by this resource", so we use the cheapest correct-
     * enough heuristic — for each subscription that *could* match by
     * criteria.startsWith(resourceType), include it; for each subscription's
     * recent delivery history, include events within +/- 60s of the
     * delivered_at timestamp.
     *
     * The `tbd` flag on the response items documents which fields are
     * approximations vs. exact joins.
     */
    private fun resolveSubscriptionsAndNotifications(
        resources: List<CreatedResource>,
        deliveredAt: OffsetDateTime?,
    ): Pair<List<MatchedSubscription>, List<NotificationFired>> {
        if (resources.isEmpty()) return Pair(emptyList(), emptyList())

        val subscriptions: List<Subscription> = statusClient.listSubscriptions()
        if (subscriptions.isEmpty()) return Pair(emptyList(), emptyList())

        // For each (resource × subscription) pair, check if the
        // subscription's `criteria` field starts with the resource type
        // (e.g. "Patient?gender=female" matches a Patient resource). This
        // is an approximation; a precise match would require running the
        // criteria as a FHIR search against HAPI — which the existing
        // SubscriptionMatcherInterceptor inside HAPI does, but we don't
        // have a clean read-only handle for it on the client side. The
        // type-prefix check is correct in the common case (criteria scoped
        // to one resource type) and operator-friendly when wrong (clearly
        // labelled `tbd_match_precise=true`).
        val matched = mutableListOf<MatchedSubscription>()
        for (resource in resources) {
            for (sub in subscriptions) {
                val criteria = sub.criteria ?: continue
                if (!criteria.startsWith(resource.resourceType + "?") &&
                    !criteria.equals(resource.resourceType, ignoreCase = true)
                ) {
                    continue
                }
                matched += MatchedSubscription(
                    id = "Subscription/${sub.idElement?.idPart ?: continue}",
                    channelType = sub.channel?.type?.toCode() ?: "unknown",
                    endpoint = sub.channel?.endpoint?.takeUnless { it.isNullOrBlank() },
                    criteria = criteria,
                    matchedResource = resource.id,
                    tbdMatchPrecise = true,
                )
            }
        }

        // Walk per-subscription delivery history and include events that
        // overlap the message's delivered-at window. The 60-second window
        // is symmetric and intentionally generous — HAPI's clock and the
        // interface engine's clock may differ; we'd rather over-include
        // and let the operator filter than miss the relevant attempt.
        val notifications = mutableListOf<NotificationFired>()
        if (deliveredAt != null) {
            val windowSecs = MATCH_WINDOW_SECONDS
            val lower = deliveredAt.minusSeconds(windowSecs)
            val upper = deliveredAt.plusSeconds(windowSecs)
            val uniqueSubs = matched.map { it.id }.toSet()
            for (subId in uniqueSubs) {
                val rawId = subId.removePrefix("Subscription/")
                val view = try {
                    statusClient.statusFor(rawId) ?: continue
                } catch (ex: Exception) {
                    log.debug("effects: statusFor({}) failed: {}", rawId, ex.message)
                    continue
                }
                for (event in view.events) {
                    val ts = event.attemptedAt ?: continue
                    if (ts.isBefore(lower) || ts.isAfter(upper)) continue
                    notifications += NotificationFired(
                        subscriptionId = subId,
                        channelType = view.channelType,
                        endpoint = view.endpoint,
                        attemptedAt = ts,
                        outcome = event.outcome,
                        httpStatus = event.httpStatus,
                        durationMs = event.durationMs,
                        error = event.error,
                        tbdTimeWindowed = true,
                    )
                }
            }
        }

        return Pair(matched, notifications)
    }

    companion object {
        /** Top-level effects_status values; see class doc. */
        const val EFFECTS_STATUS_DELIVERED = "delivered"
        const val EFFECTS_STATUS_PENDING = "pending"
        const val EFFECTS_STATUS_FAILED = "failed"
        const val EFFECTS_STATUS_UNKNOWN = "unknown"

        /**
         * Symmetric window around the message's `delivered_at` used to
         * include HAPI delivery attempts in the effects view. Kept on the
         * generous side so a small clock skew between the interface
         * engine and HAPI doesn't drop a relevant attempt — the operator
         * can filter further; we'd rather over-include.
         */
        const val MATCH_WINDOW_SECONDS = 60L
    }
}

// -- JSON shapes -------------------------------------------------------------

/**
 * Top-level response body. `JsonInclude.Include.NON_NULL` keeps `null`
 * fields out of the wire shape so the response is compact when, e.g., a
 * pending message has no delivered_at.
 */
@JsonInclude(JsonInclude.Include.NON_NULL)
data class MessageEffectsResponse(
    @JsonProperty("effects_status") val effectsStatus: String,
    val message: MessageSummaryView,
    val transform: TransformView,
    @JsonProperty("fhir_resources_created") val fhirResourcesCreated: List<CreatedResource>,
    @JsonProperty("subscriptions_matched") val subscriptionsMatched: List<MatchedSubscription>,
    @JsonProperty("notifications_fired") val notificationsFired: List<NotificationFired>,
)

@JsonInclude(JsonInclude.Include.NON_NULL)
data class MessageSummaryView(
    val id: Long?,
    @JsonProperty("correlation_id") val correlationId: String?,
    @JsonProperty("received_at") val receivedAt: OffsetDateTime?,
    val status: String,
    @JsonProperty("source_system") val sourceSystem: String,
    @JsonProperty("source_id") val sourceId: String,
    @JsonProperty("message_type") val messageType: String,
    @JsonProperty("last_error") val lastError: String?,
) {
    companion object {
        fun fromRow(row: IngestedMessage) = MessageSummaryView(
            id = row.id,
            correlationId = row.correlationId,
            receivedAt = row.receivedAt,
            status = row.status.name,
            sourceSystem = row.sourceSystem,
            sourceId = row.sourceId,
            messageType = row.messageType,
            lastError = row.lastError,
        )
    }
}

@JsonInclude(JsonInclude.Include.NON_NULL)
data class TransformView(
    @JsonProperty("delivered_at") val deliveredAt: OffsetDateTime?,
    @JsonProperty("attempt_count") val attemptCount: Int,
    @JsonProperty("last_attempt_at") val lastAttemptAt: OffsetDateTime?,
) {
    companion object {
        fun fromRow(row: IngestedMessage) = TransformView(
            deliveredAt = row.deliveredAt,
            attemptCount = row.attemptCount,
            lastAttemptAt = row.lastAttemptAt,
        )
    }
}

@JsonInclude(JsonInclude.Include.NON_NULL)
data class CreatedResource(
    @JsonProperty("resource_type") val resourceType: String,
    val id: String,
) {
    companion object {
        /**
         * Split a canonical `ResourceType/id` reference into its two
         * pieces. Tolerates leading slashes and version-history suffixes
         * (defensively — the worker already normalizes, but a manually
         * UPDATEd row could carry a less-clean value).
         */
        fun fromReference(ref: String): CreatedResource {
            val cleaned = ref.trim().trimStart('/').substringBefore("/_history/")
            val parts = cleaned.split('/')
            if (parts.size < 2) {
                // Can't parse — return the reference as-is in the `id`
                // field with an "unknown" resource type so operators can
                // still see what was stored.
                return CreatedResource(resourceType = "unknown", id = ref)
            }
            return CreatedResource(
                resourceType = parts[parts.size - 2],
                id = "${parts[parts.size - 2]}/${parts.last()}",
            )
        }
    }
}

@JsonInclude(JsonInclude.Include.NON_NULL)
data class MatchedSubscription(
    val id: String,
    @JsonProperty("channel_type") val channelType: String,
    val endpoint: String?,
    val criteria: String,
    @JsonProperty("matched_resource") val matchedResource: String,
    /**
     * `true` when the match was inferred by criteria-type prefix rather
     * than a true FHIR-server-side evaluation of the subscription
     * criteria against the resource. Operators are expected to verify
     * the match by re-running the criteria as a FHIR search if they
     * need a definitive answer. See [AdminMessageEffectsController]
     * resolveSubscriptionsAndNotifications doc for the reasoning.
     */
    @JsonProperty("tbd_match_precise") val tbdMatchPrecise: Boolean = false,
)

@JsonInclude(JsonInclude.Include.NON_NULL)
data class NotificationFired(
    @JsonProperty("subscription_id") val subscriptionId: String,
    @JsonProperty("channel_type") val channelType: String,
    val endpoint: String?,
    @JsonProperty("attempted_at") val attemptedAt: OffsetDateTime?,
    val outcome: String,
    @JsonProperty("http_status") val httpStatus: Int?,
    @JsonProperty("duration_ms") val durationMs: Long?,
    val error: String?,
    /**
     * `true` when this notification was associated with the message by
     * timestamp proximity (within [AdminMessageEffectsController.MATCH_WINDOW_SECONDS]
     * of the row's `delivered_at`) rather than a hard join key. Same
     * "approximation" caveat as [MatchedSubscription.tbdMatchPrecise].
     */
    @JsonProperty("tbd_time_windowed") val tbdTimeWindowed: Boolean = false,
)
