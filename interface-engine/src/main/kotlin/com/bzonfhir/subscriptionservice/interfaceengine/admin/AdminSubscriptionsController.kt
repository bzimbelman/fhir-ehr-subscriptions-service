package com.bzonfhir.subscriptionservice.interfaceengine.admin

import com.fasterxml.jackson.annotation.JsonProperty
import org.slf4j.LoggerFactory
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.RequestMapping
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import java.time.OffsetDateTime

/**
 * Operator admin REST API for HAPI Subscription delivery health
 * (Epic #387, ticket #390).
 *
 * Two endpoints, mounted under the same `/admin/` prefix the messages
 * controller (ticket #384) uses — same Spring Boot port (8090) and the
 * same [AdminAuthInterceptor] bearer-token gate, which is path-pattern
 * matched on the `/admin/` glob.
 *
 *   GET  /admin/subscriptions/health           — summary across all subscriptions
 *   GET  /admin/subscriptions/{id}/history     — recent delivery attempts for one
 *
 * Both endpoints are pure proxies in front of HAPI's Subscription store
 * — see [HapiSubscriptionStatusClient] for the architectural decision and
 * the HAPI 7.6 SubscriptionStatus shape we read from.
 *
 * Pagination on the history endpoint is in-process: we ask HAPI for the
 * full set of events (HAPI doesn't expose paging on `$status` in the way
 * the IG specifies for the resource), and slice the response. That's fine
 * because the per-subscription event window HAPI keeps is bounded (it's
 * a rolling buffer, not the full lifetime history).
 */
@RestController
@RequestMapping("/admin/subscriptions")
class AdminSubscriptionsController(
    private val statusClient: HapiSubscriptionStatusClient,
) {

    private val log = LoggerFactory.getLogger(AdminSubscriptionsController::class.java)

    @GetMapping("/health")
    fun health(): ResponseEntity<Any> {
        // One HAPI search + one $status op per subscription. The expected
        // scale is dozens — at hundreds the per-subscription op cost adds
        // up and we'll want to switch to a batched read, but at this point
        // we don't have a numbers-justified reason to optimize.
        val subscriptions = statusClient.listSubscriptions()
        val items: List<SubscriptionHealthItem> = subscriptions.mapNotNull { sub ->
            val id = sub.idElement?.idPart ?: return@mapNotNull null
            val view = try {
                statusClient.statusFor(id)
            } catch (ex: Exception) {
                // One subscription's failure shouldn't drop the whole list —
                // we log it and continue. The corresponding row will simply
                // be missing from the response; an operator notices via
                // count mismatch with HAPI directly.
                log.warn("statusFor Subscription/{} threw {} — skipping in summary", id, ex.message)
                null
            } ?: return@mapNotNull null

            SubscriptionHealthItem.fromView(view)
        }
        return ResponseEntity.ok(
            SubscriptionsHealthResponse(
                total = items.size,
                items = items,
            ),
        )
    }

    @GetMapping("/{id}/history")
    fun history(
        @PathVariable id: String,
        @RequestParam(required = false, defaultValue = "50") limit: Int,
        @RequestParam(required = false, defaultValue = "0") offset: Int,
    ): ResponseEntity<Any> {
        // Pagination clamps mirror /admin/messages — same defaults,
        // same cap, same offset rounding, so operators learn the rules once.
        val cappedLimit = limit.coerceIn(1, MAX_LIMIT)
        val cappedOffset = offset.coerceAtLeast(0)

        val view = statusClient.statusFor(id)
            ?: return ResponseEntity.status(404).body(
                mapOf(
                    "error" to "not_found",
                    "message" to "no Subscription with id=$id on HAPI",
                ),
            )

        val effectiveOffset = (cappedOffset / cappedLimit) * cappedLimit
        val window = view.events.drop(effectiveOffset).take(cappedLimit)

        return ResponseEntity.ok(
            SubscriptionHistoryResponse(
                subscriptionId = view.subscriptionId,
                total = view.events.size.toLong(),
                limit = cappedLimit,
                offset = effectiveOffset,
                items = window.map { DeliveryAttemptItem.fromEvent(it) },
            ),
        )
    }

    companion object {
        const val MAX_LIMIT = 500
    }
}

// -- JSON shapes -----------------------------------------------------------

data class SubscriptionsHealthResponse(
    val total: Int,
    val items: List<SubscriptionHealthItem>,
)

data class SubscriptionHealthItem(
    val id: String,
    val active: Boolean,
    @JsonProperty("channel_type") val channelType: String,
    val endpoint: String?,
    @JsonProperty("delivery_success_count") val deliverySuccessCount: Long,
    @JsonProperty("delivery_failure_count") val deliveryFailureCount: Long,
    @JsonProperty("last_attempt_at") val lastAttemptAt: OffsetDateTime?,
    @JsonProperty("last_attempt_outcome") val lastAttemptOutcome: String?,
    @JsonProperty("last_error") val lastError: String?,
) {
    companion object {
        fun fromView(view: SubscriptionStatusView) = SubscriptionHealthItem(
            id = view.subscriptionId,
            active = view.active,
            channelType = view.channelType,
            endpoint = view.endpoint,
            deliverySuccessCount = view.deliverySuccessCount,
            deliveryFailureCount = view.deliveryFailureCount,
            lastAttemptAt = view.lastAttemptAt,
            lastAttemptOutcome = view.lastAttemptOutcome,
            lastError = view.lastError,
        )
    }
}

data class SubscriptionHistoryResponse(
    @JsonProperty("subscription_id") val subscriptionId: String,
    val total: Long,
    val limit: Int,
    val offset: Int,
    val items: List<DeliveryAttemptItem>,
)

data class DeliveryAttemptItem(
    @JsonProperty("attempted_at") val attemptedAt: OffsetDateTime?,
    val outcome: String,
    @JsonProperty("http_status") val httpStatus: Int?,
    val error: String?,
    @JsonProperty("duration_ms") val durationMs: Long?,
) {
    companion object {
        fun fromEvent(event: DeliveryEvent) = DeliveryAttemptItem(
            attemptedAt = event.attemptedAt,
            outcome = event.outcome,
            httpStatus = event.httpStatus,
            error = event.error,
            durationMs = event.durationMs,
        )
    }
}
