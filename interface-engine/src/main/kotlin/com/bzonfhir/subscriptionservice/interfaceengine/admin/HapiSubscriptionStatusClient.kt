package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.parser.DataFormatException
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.server.exceptions.BaseServerResponseException
import ca.uhn.fhir.rest.server.exceptions.ResourceNotFoundException
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Parameters
import org.hl7.fhir.r4.model.Subscription
import org.hl7.fhir.r4.model.UriType
import org.slf4j.LoggerFactory
import org.springframework.stereotype.Component
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset

/**
 * Read side of HAPI's Subscription store, used by the operator admin API
 * (Epic #387, ticket #390).
 *
 * Two questions the admin endpoints need answered, both of which require
 * round-trips to HAPI:
 *
 *   1. "List all Subscription resources currently registered, with their
 *      basic shape and current status." → [listSubscriptions].
 *   2. "For a single Subscription, fetch a `SubscriptionStatus`-shaped view
 *      of recent delivery attempts." → [statusFor].
 *
 * Pulled out of the controller class for the same reason MatchboxClient is
 * pulled out of the worker — so the admin controller test can swap in a
 * deterministic stub without having to mock HAPI's fluent client surface.
 *
 * ## Architectural decision: proxy vs. own table
 *
 * Per the #390 plan we considered two implementations:
 *
 *   - **Proxy** (this one): each call round-trips to HAPI via the HAPI FHIR
 *     client that's already wired by [FhirConfig]. No new schema, no new
 *     retention story, no new code in the hot ingest path.
 *   - **Own table**: hook the same `SUBSCRIPTION_AFTER_REST_HOOK_DELIVERY`
 *     pointcut as the metrics interceptor in #389, persist every attempt to
 *     a `subscription_delivery_log` table in the interface-engine database.
 *     Richer per-attempt detail, but adds a schema, a retention policy, a
 *     storage cost, and a write on the delivery path.
 *
 * We deferred the own-table approach. HAPI's SubscriptionStatus (R5 Backport
 * IG operation `$status`) is the standards-compliant answer to the same
 * question, and the Prometheus counters from #389 cover the aggregate
 * "are deliveries succeeding?" question for alerting. The own-table approach
 * is a fallback if we discover SubscriptionStatus doesn't carry enough
 * detail for legacy R4 criteria subscriptions — see [statusFor] for the
 * specific HAPI 7.6 behavior we observed.
 */
interface HapiSubscriptionStatusClient {

    /**
     * Page through all Subscription resources currently on HAPI. The
     * implementation may make multiple GETs against `/fhir/Subscription` to
     * cover all pages; the returned list is the full set.
     *
     * Caller is expected to apply its own paging via [list] / [offset] /
     * etc. — this layer just hands back the whole catalog because (a) the
     * admin UI is paging in process, (b) the subscription table is
     * operator-scale (dozens to low hundreds), not patient-scale.
     */
    fun listSubscriptions(): List<Subscription>

    /**
     * Read a single Subscription by id (the bare id, e.g. "123", NOT the
     * "Subscription/123" reference form). Returns null when HAPI returns 404.
     */
    fun readSubscription(id: String): Subscription?

    /**
     * Fetch SubscriptionStatus history for one Subscription. Returns a
     * structured list of recent delivery attempts.
     *
     * In HAPI 7.6 the R5 Backport IG ships StructureDefinitions and
     * OperationDefinitions in the package, but ticket #390 live-testing on
     * Rancher Desktop confirmed that `$status` is NOT wired as a method on
     * the Subscription resource provider in our build of the HAPI JPA
     * starter image — every `GET /fhir/Subscription/{id}/$status` comes
     * back with HAPI's "No methods exist" warning. We therefore tolerate a
     * failure on the operation call and fall back to a metadata-only view
     * synthesized from the Subscription resource itself (status, error,
     * channel.endpoint). The DeliveryEvent parser is kept in place so a
     * future HAPI bump (or a custom `IResourceProvider` in `hapi-auth`)
     * that wires `$status` will start producing events automatically.
     *
     * Returns null when the Subscription id is not found at all (HAPI 404).
     */
    fun statusFor(id: String): SubscriptionStatusView?

    /**
     * Update the `Subscription.status` field on HAPI. Used by the operator
     * UI (ticket #404) to flip a subscription between `active` and `off`
     * without requiring the operator to round-trip through a FHIR PUT
     * themselves. Returns the updated [SubscriptionStatusView] on success,
     * or null when the id is not found.
     *
     * `newStatus` must be one of `active`, `off`, `requested`, `error` —
     * the FHIR R4 Subscription.status vocabulary. Other values throw
     * [IllegalArgumentException]; the controller maps that to 400.
     */
    fun setStatus(id: String, newStatus: String): SubscriptionStatusView?
}

/**
 * Structured view assembled from a HAPI SubscriptionStatus response (or
 * Subscription read fallback). Pure data, no HAPI types — the controller
 * serializes this to JSON directly.
 */
data class SubscriptionStatusView(
    val subscriptionId: String,
    val active: Boolean,
    /**
     * Raw HAPI Subscription.status code: `active` / `off` / `requested` /
     * `error`. Useful for the operator UI which surfaces this as a pill
     * with the same vocabulary the FHIR spec defines. `active` here is the
     * derived boolean ([status] == "active"); keep both since callers
     * which only need the boolean shouldn't have to know the string.
     */
    val status: String,
    /**
     * The Subscription.criteria string (the query that triggers
     * notifications, e.g. `Patient?` or
     * `SubscriptionTopic?topic=...`). Empty string when not set on the
     * resource so the JSON shape stays predictable.
     */
    val criteria: String,
    val channelType: String,
    val endpoint: String?,
    val deliverySuccessCount: Long,
    val deliveryFailureCount: Long,
    val lastAttemptAt: OffsetDateTime?,
    val lastAttemptOutcome: String?,
    val lastError: String?,
    val events: List<DeliveryEvent>,
)

/**
 * One delivery attempt — what we want to put on the per-Subscription
 * history endpoint. The fields mirror the spec in the #390 plan.
 */
data class DeliveryEvent(
    val attemptedAt: OffsetDateTime?,
    val outcome: String,
    val httpStatus: Int?,
    val error: String?,
    val durationMs: Long?,
)

/**
 * Real implementation that talks to HAPI via the [IGenericClient] wired in
 * [FhirConfig]. The class is package-private (Kotlin internal) because all
 * callers go through the [HapiSubscriptionStatusClient] interface; making
 * it internal also helps the tests (which live in a different package via
 * a `@TestConfiguration` `@Primary` bean) avoid accidentally taking a
 * dependency on it directly.
 */
@Component
class HapiSubscriptionStatusClientImpl(
    private val hapiClient: IGenericClient,
) : HapiSubscriptionStatusClient {

    private val log = LoggerFactory.getLogger(HapiSubscriptionStatusClientImpl::class.java)

    override fun listSubscriptions(): List<Subscription> {
        // Single GET against /fhir/Subscription, paging up to MAX_LIST so an
        // operator with thousands of stale Subscription resources doesn't
        // hang the admin call. The expected scale is dozens to low hundreds;
        // if anyone ever exceeds MAX_LIST we'll see a warn log and cut over
        // to proper paging.
        val bundle: Bundle = try {
            hapiClient.search<Bundle>()
                .forResource(Subscription::class.java)
                .count(MAX_LIST)
                .returnBundle(Bundle::class.java)
                .execute()
        } catch (ex: BaseServerResponseException) {
            log.warn("HAPI Subscription search failed status={} message={}", ex.statusCode, ex.message)
            return emptyList()
        }
        val result = mutableListOf<Subscription>()
        for (entry in bundle.entry) {
            val resource = entry.resource
            if (resource is Subscription) {
                result += resource
            }
        }
        if (bundle.total > MAX_LIST) {
            log.warn(
                "HAPI Subscription search returned total={} but our cap is {}; admin summary is incomplete. " +
                    "File a ticket to add proper paging.",
                bundle.total,
                MAX_LIST,
            )
        }
        return result
    }

    override fun readSubscription(id: String): Subscription? {
        return try {
            hapiClient.read()
                .resource(Subscription::class.java)
                .withId(id)
                .execute()
        } catch (notFound: ResourceNotFoundException) {
            null
        } catch (ex: BaseServerResponseException) {
            log.warn("HAPI Subscription read id={} failed status={}", id, ex.statusCode)
            null
        }
    }

    override fun statusFor(id: String): SubscriptionStatusView? {
        val subscription = readSubscription(id) ?: return null

        // Try the R5 Backport `$status` operation. In HAPI 7.6 this is
        // wired by the Subscription Backport IG when the topic-based
        // subscription engine is on. For legacy R4 criteria subscriptions
        // HAPI returns either an OperationOutcome (NOT_IMPLEMENTED) or an
        // empty Parameters; we tolerate either and fall through to the
        // metadata-only view.
        val parameters: Parameters? = try {
            hapiClient.operation()
                .onInstance("Subscription/$id")
                .named("\$status")
                .withNoParameters(Parameters::class.java)
                .useHttpGet()
                .execute()
        } catch (ex: BaseServerResponseException) {
            // 404 / 501 / 400 — the operation isn't implemented for this
            // subscription type. That's not an error per se; we just
            // synthesize from Subscription metadata instead.
            if (log.isDebugEnabled) {
                log.debug(
                    "HAPI \$status op for Subscription/{} returned status={}; falling back to metadata-only view",
                    id,
                    ex.statusCode,
                )
            }
            null
        } catch (ex: DataFormatException) {
            log.warn("HAPI \$status op for Subscription/{} returned unparseable body: {}", id, ex.message)
            null
        }

        return subscriptionStatusView(subscription, parameters)
    }

    override fun setStatus(id: String, newStatus: String): SubscriptionStatusView? {
        // Validate `newStatus` against the FHIR R4 vocabulary before
        // round-tripping to HAPI. fromCode throws FHIRException on bad
        // input; we surface that as IllegalArgumentException so the
        // controller can map to 400 without leaking HAPI internals.
        val statusEnum = try {
            Subscription.SubscriptionStatus.fromCode(newStatus)
        } catch (ex: Exception) {
            throw IllegalArgumentException(
                "invalid Subscription.status value '$newStatus' (expected active|off|requested|error)",
            )
        }

        val existing = readSubscription(id) ?: return null
        existing.status = statusEnum
        // Clear `error` when transitioning out of an error state — keeping
        // a stale error string on an active subscription is confusing.
        if (statusEnum != Subscription.SubscriptionStatus.ERROR) {
            existing.error = null
        }

        val updated = try {
            hapiClient.update().resource(existing).execute().resource as? Subscription
        } catch (ex: BaseServerResponseException) {
            log.warn("HAPI update Subscription/{} failed status={}", id, ex.statusCode)
            return null
        }
        // After a successful write, return a fresh status view so the
        // UI doesn't have to chain a read.
        return statusFor(id) ?: updated?.let { subscriptionStatusView(it, null) }
    }

    companion object {
        /**
         * Hard cap on the size of the Subscription list returned to the admin
         * summary. Dozens-to-low-hundreds is the expected scale; anything past
         * this is logged as a warn and silently truncated. Pagination on the
         * admin endpoint is in-process so this also bounds in-memory usage.
         */
        const val MAX_LIST = 500
    }

    // -- view assembly ------------------------------------------------------

    internal fun subscriptionStatusView(
        subscription: Subscription,
        statusParameters: Parameters?,
    ): SubscriptionStatusView {
        val id = subscription.idElement?.idPart ?: ""
        val channel = subscription.channel
        val active = subscription.status == Subscription.SubscriptionStatus.ACTIVE
        // Raw FHIR R4 code: active|off|requested|error. fromCode throws on
        // null, so we fall back to "unknown" — the UI displays this as a
        // neutral grey pill.
        val statusCode = subscription.status?.toCode() ?: "unknown"
        val criteria = subscription.criteria ?: ""
        val channelType = channel?.type?.toCode() ?: "unknown"
        val endpoint = channel?.endpoint?.takeUnless { it.isNullOrBlank() }
        val lastError = subscription.error?.takeUnless { it.isNullOrBlank() }

        val events = extractEvents(statusParameters)
        // HAPI's `$status` operation exposes `eventsSinceSubscriptionStart`
        // as a counter on the contained SubscriptionStatus. We don't get a
        // breakdown of success vs failure from there — the spec only
        // models a single counter — so we count by per-event outcome on
        // the events we did get back. For subscriptions where HAPI gave us
        // no events (legacy R4 criteria), both counters are zero and the
        // operator should look at the Prometheus
        // `hapi_subscription_delivery_total` series for aggregate counts.
        val successCount = events.count { it.outcome == "success" }.toLong()
        val failureCount = events.count { it.outcome == "failure" }.toLong()
        val lastEvent = events.firstOrNull()
        val lastAttemptOutcome = when {
            lastEvent != null -> lastEvent.outcome
            // Subscription.status == error tells us the LAST attempt failed
            // even without notificationEvent history.
            subscription.status == Subscription.SubscriptionStatus.ERROR -> "failure"
            else -> null
        }

        return SubscriptionStatusView(
            subscriptionId = "Subscription/$id",
            active = active,
            status = statusCode,
            criteria = criteria,
            channelType = channelType,
            endpoint = endpoint,
            deliverySuccessCount = successCount,
            deliveryFailureCount = failureCount,
            lastAttemptAt = lastEvent?.attemptedAt,
            lastAttemptOutcome = lastAttemptOutcome,
            lastError = lastError ?: lastEvent?.error,
            events = events,
        )
    }

    /**
     * Pull the notificationEvent[] entries from the Parameters response and
     * map them to our DeliveryEvent type. The HAPI 7.6 response shape we
     * key on is what the R5 Backport IG specifies — a Parameters with a
     * parameter named `notificationEvent`, repeating, each value being a
     * BackboneElement carrying `eventNumber`, `timestamp`, and (for
     * failures) an `error` extension.
     *
     * Several mapping shortcuts are intentional:
     *
     *   - We treat "any value with a non-null error extension" as a failure
     *     and everything else as a success. HAPI does not expose an outcome
     *     enum on the notificationEvent itself.
     *   - `httpStatus` and `durationMs` aren't on the IG-defined shape, so
     *     they come back null. The fields exist in our DeliveryEvent so we
     *     can populate them if/when we switch to the own-table implementation.
     */
    private fun extractEvents(parameters: Parameters?): List<DeliveryEvent> {
        if (parameters == null) return emptyList()
        val result = mutableListOf<DeliveryEvent>()
        for (param in parameters.parameter) {
            if (param.name != "notificationEvent") continue
            val partMap = param.part.associateBy { it.name }
            val timestamp = (partMap["timestamp"]?.value as? org.hl7.fhir.r4.model.InstantType)?.value
            val error = (partMap["error"]?.value as? org.hl7.fhir.r4.model.StringType)?.value
            val outcome = if (error.isNullOrBlank()) "success" else "failure"
            result += DeliveryEvent(
                attemptedAt = timestamp?.toInstant()?.atOffset(ZoneOffset.UTC),
                outcome = outcome,
                httpStatus = null,
                error = error?.takeUnless { it.isBlank() },
                durationMs = null,
            )
        }
        // The IG declares notificationEvent[] as newest-last; our admin API
        // contract is newest-first, so reverse.
        return result.asReversed()
    }
}

// Convenience for tests / debug — converts an Instant to a UTC OffsetDateTime.
internal fun Instant?.utcOffset(): OffsetDateTime? = this?.atOffset(ZoneOffset.UTC)
