package com.bzonfhir.subscriptionservice.interfaceengine.observability

import org.slf4j.MDC
import java.util.UUID

/**
 * Shared constants + helpers for the `correlation_id` propagation
 * (Epic #387, ticket #388).
 *
 * One server-assigned UUID per inbound HL7 v2 message (or per HTTP request
 * on the admin API and on calls into HAPI). The same value travels through
 * every log line emitted while the message/request is being processed, and
 * across the wire as the `X-Correlation-Id` header on every outbound HTTP
 * call we make. Joining logs across services becomes one grep.
 *
 * ## Header name
 *
 * The header is deliberately `X-Correlation-Id`, NOT `X-Request-Id` or
 * `traceparent`. Reasons:
 *
 *   - Most EHRs and lab systems are already producing `X-Correlation-Id`
 *     in their REST APIs, so callers don't have to learn a new name.
 *   - HL7 v2 has no equivalent transport-level header, so the interface
 *     engine generates a UUID per inbound MLLP message and uses
 *     `X-Correlation-Id` on every downstream HTTP request it makes.
 *   - We deliberately stay clear of W3C traceparent — that's a four-part
 *     hex string carrying a trace + span id, used by full distributed-
 *     tracing systems (Zipkin, OTel). The correlation_id here is single-
 *     scope: same value for the whole lifetime of one request/message.
 *     Mixing the two would require us to also implement span ids, which
 *     #388's scope does not.
 *
 * ## MDC key
 *
 * The MDC key (`correlation_id`) matches the JSON field name in the log
 * layout, so log aggregators can index on the same identifier name in both
 * the log line and the wire header.
 *
 * ## Lifecycle invariants
 *
 *   - The MDC value MUST be set before any logging happens for a
 *     request/message, and MUST be cleared at the end of the scope.
 *     Leaking the MDC across threads is the most common bug here —
 *     every entry-point sets up a try/finally to clear.
 *   - When a value already exists on the inbound carrier (HTTP header or
 *     persisted row column), it is REUSED, not regenerated. This is what
 *     makes the trace cross service boundaries.
 *   - When no value exists (e.g. MLLP receive — HL7 v2 has no header), the
 *     interface engine generates a UUID and persists it on the row.
 */
object CorrelationId {

    /** HTTP header name carrying the correlation id between services. */
    const val HEADER = "X-Correlation-Id"

    /** MDC key. Matches the JSON field name. */
    const val MDC_KEY = "correlation_id"

    /**
     * Generate a fresh correlation id. Random UUIDv4 — universally unique
     * for our purposes, no coordination required between replicas.
     */
    fun generate(): String = UUID.randomUUID().toString()

    /**
     * Run [block] with [value] set as the MDC value under [MDC_KEY], and
     * always clear (or restore the prior value) on exit. The try/finally
     * shape is essential: throwing from inside [block] must NOT leak the
     * MDC value to whatever code runs next on this thread.
     */
    inline fun <T> withMdc(value: String, block: () -> T): T {
        val previous = MDC.get(MDC_KEY)
        MDC.put(MDC_KEY, value)
        try {
            return block()
        } finally {
            if (previous == null) {
                MDC.remove(MDC_KEY)
            } else {
                MDC.put(MDC_KEY, previous)
            }
        }
    }

    /**
     * Read the current MDC value. May return null at thread boundaries (a
     * fresh scheduled-task thread starts with an empty MDC).
     */
    fun current(): String? = MDC.get(MDC_KEY)

    /**
     * Sanitize an inbound header value before trusting it. Tight rules:
     * accept ASCII letters, digits, dash, underscore, and dot, with a
     * length cap. Anything else (control characters, log-injection
     * attempts) → generate a fresh id instead. The cap keeps a malicious
     * sender from blowing up log records with a multi-megabyte value.
     */
    fun sanitizeOrGenerate(headerValue: String?): String {
        if (headerValue.isNullOrBlank()) return generate()
        if (headerValue.length > MAX_HEADER_LENGTH) return generate()
        if (!headerValue.all { it.isAcceptable() }) return generate()
        return headerValue
    }

    private fun Char.isAcceptable(): Boolean =
        this.isLetterOrDigit() || this == '-' || this == '_' || this == '.'

    /**
     * Hard ceiling on a correlation id length. A UUID v4 is 36 chars; we
     * grant some headroom (96) for callers using prefixed ids like
     * `epic-2026-06-26-<uuid>`. Anything longer than this is suspicious
     * and gets dropped.
     */
    const val MAX_HEADER_LENGTH: Int = 96
}
