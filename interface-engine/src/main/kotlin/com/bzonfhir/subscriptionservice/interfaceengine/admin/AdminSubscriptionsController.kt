package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.context.FhirContext
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.ObjectMapper
import org.slf4j.LoggerFactory
import org.springframework.http.MediaType
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PatchMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.RequestBody
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
    private val fhirContext: FhirContext,
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

    /**
     * Full FHIR Subscription resource as JSON. The operator UI (ticket
     * #404) renders this on the detail page so an operator can inspect
     * the registered configuration without having a FHIR client open in
     * another tab. We return the resource serialized via HAPI's R4 JSON
     * parser (rather than `Subscription` itself, which Jackson would
     * mangle — HAPI's model classes are deliberately not Jackson-friendly).
     *
     * Sticking the response under `/admin/` keeps the bearer-token gate;
     * the UI never has to authenticate to `/fhir/Subscription/{id}`
     * directly.
     */
    @GetMapping("/{id}/resource", produces = [MediaType.APPLICATION_JSON_VALUE])
    fun resource(@PathVariable id: String): ResponseEntity<Any> {
        val sub = statusClient.readSubscription(id) ?: return ResponseEntity.status(404).body(
            mapOf(
                "error" to "not_found",
                "message" to "no Subscription with id=$id on HAPI",
            ),
        )
        // HAPI's JSON parser produces a canonical R4 representation;
        // re-parse as a Map so Spring/Jackson re-serializes inside the
        // standard response envelope (and we can still hit MediaType
        // negotiation through the existing Spring stack).
        val json = fhirContext.newJsonParser().setPrettyPrint(false).encodeResourceToString(sub)
        // Use a single shared ObjectMapper. Spring's auto-configured
        // ObjectMapper is the one we'd ideally inject, but for this
        // narrow use case a stock one is fine — there are no module
        // dependencies on the parsed structure.
        val tree: Any = ObjectMapper().readValue(json, Any::class.java)
        return ResponseEntity.ok(tree)
    }

    /**
     * Operator action: flip a Subscription's status between `active` and
     * `off` (or to any other FHIR R4 status code) without requiring the
     * operator to round-trip through a FHIR PUT themselves. The UI
     * (#404) lands a one-click toggle on the subscription detail page.
     *
     * Body shape:
     *   `{"status": "active"}`   — turn it on
     *   `{"status": "off"}`      — disable delivery
     *
     * Returns the updated [SubscriptionHealthItem] envelope on success,
     * `404` if the id is unknown, `400` on a bad status value.
     *
     * Audit: emits a single-line JSON log entry (`audit_event=...`) so
     * the action surfaces in the rolling Loki / journald pipeline used
     * by ticket #403. Ticket #407 will replace this with a structured
     * AuditEvent resource written to HAPI.
     */
    @PatchMapping("/{id}/status", consumes = [MediaType.APPLICATION_JSON_VALUE])
    fun patchStatus(
        @PathVariable id: String,
        @RequestBody body: PatchStatusRequest,
    ): ResponseEntity<Any> {
        val newStatus = body.status?.trim()?.lowercase()
            ?: return ResponseEntity.status(400).body(
                mapOf(
                    "error" to "bad_request",
                    "message" to "request body must include a non-empty 'status' field",
                ),
            )
        val updated = try {
            statusClient.setStatus(id, newStatus)
        } catch (ex: IllegalArgumentException) {
            return ResponseEntity.status(400).body(
                mapOf(
                    "error" to "invalid_status",
                    "message" to (ex.message ?: "invalid status value"),
                ),
            )
        } ?: return ResponseEntity.status(404).body(
            mapOf(
                "error" to "not_found",
                "message" to "no Subscription with id=$id on HAPI",
            ),
        )
        log.info(
            "audit_event=subscription_status_changed subscription_id={} new_status={}",
            updated.subscriptionId,
            newStatus,
        )
        return ResponseEntity.ok(SubscriptionHealthItem.fromView(updated))
    }

    companion object {
        const val MAX_LIMIT = 500
    }
}

/**
 * Request body for PATCH `/admin/subscriptions/{id}/status`. Kept as a
 * top-level data class (not nested in the controller) so Spring/Jackson
 * can construct it via the no-arg constructor synthesised by Kotlin
 * defaults.
 */
data class PatchStatusRequest(val status: String? = null)

// -- JSON shapes -----------------------------------------------------------

data class SubscriptionsHealthResponse(
    val total: Int,
    val items: List<SubscriptionHealthItem>,
)

data class SubscriptionHealthItem(
    val id: String,
    val active: Boolean,
    /**
     * Raw FHIR R4 Subscription.status code (`active` / `off` /
     * `requested` / `error`). Added in ticket #404 so the operator UI
     * can show the full pill vocabulary; `active` (boolean) stays for
     * backwards compatibility with #390's contract.
     */
    val status: String,
    /** The Subscription.criteria string (e.g. `Patient?`). Added in #404. */
    val criteria: String,
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
            status = view.status,
            criteria = view.criteria,
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
