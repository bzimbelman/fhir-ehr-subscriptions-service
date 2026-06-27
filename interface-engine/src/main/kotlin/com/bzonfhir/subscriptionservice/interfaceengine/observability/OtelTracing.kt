package com.bzonfhir.subscriptionservice.interfaceengine.observability

import io.opentelemetry.api.trace.Span
import io.opentelemetry.api.trace.SpanKind
import io.opentelemetry.api.trace.Tracer
import io.opentelemetry.context.Context
import io.opentelemetry.context.propagation.ContextPropagators
import io.opentelemetry.context.propagation.TextMapGetter
import io.opentelemetry.context.propagation.TextMapSetter
import org.springframework.stereotype.Component

/**
 * Span-lifecycle helpers for the interface engine (Epic #387, ticket #394).
 *
 * Three things the rest of the codebase needs:
 *
 *   1. Start a root span at MLLP receive (`mllp.receive`) and stamp the
 *      MSH headers / correlation_id on it as attributes.
 *   2. Encode the current span context to a `traceparent` string so the
 *      receive path can persist it on the row, and decode it back on the
 *      worker side to continue the trace.
 *   3. Start a child span on the worker (`worker.process`) using the
 *      context restored from the row.
 *
 * The encoding helpers use the OTel SDK's [TextMapPropagator] machinery
 * rather than hand-rolling the W3C string format. Two reasons:
 *
 *   - The SDK is the source of truth for the format; if the W3C
 *     traceparent format ever bumps to v01 we get the change for free.
 *   - The same propagator the SDK uses for inbound HTTP traceparent
 *     headers is the one we use for the DB column round-trip, so the
 *     encode → decode cycle is symmetrical and exercised in the same
 *     code path.
 *
 * When the SDK is disabled (`OTEL_SDK_DISABLED=true`):
 *
 *   - `Span.fromContext(Context.current())` returns the no-op span.
 *   - `propagators.injectingTextMapPropagator.inject(...)` is a no-op,
 *     so [encodeCurrentContext] returns null. The receive path stores
 *     null in the row's `trace_context` column.
 *   - On the worker side, [decodeContext] returns `Context.root()` when
 *     the input is null/empty, and the worker starts a fresh (no-op)
 *     span — same code path as production, just no recording. The trace
 *     is silently absent, with zero overhead.
 */
@Component
class OtelTracing(
    private val tracer: Tracer,
    private val propagators: ContextPropagators,
) {

    /**
     * Encode the currently-active span context into a W3C `traceparent`
     * transport string. Returns null when:
     *
     *   - The SDK is disabled (the active context is the no-op root) →
     *     the propagator's inject(...) call adds nothing to the carrier,
     *     and we hand back null so the caller stores null in the DB.
     *   - The current context has no valid span (this is unusual but
     *     possible if encode is called from a code path that never
     *     started a span).
     *
     * The carrier here is a single-entry map (the W3C propagator writes
     * `traceparent` and optionally `tracestate`); we collapse the two
     * into one TEXT column by storing only `traceparent` for now. See
     * the V004 migration comment for the rationale on dropping
     * tracestate.
     */
    fun encodeCurrentContext(): String? {
        val carrier = mutableMapOf<String, String>()
        propagators.textMapPropagator.inject(
            Context.current(),
            carrier,
            MapSetter,
        )
        return carrier[TRACEPARENT]?.takeIf { it.isNotBlank() }
    }

    /**
     * Decode a `traceparent` string back into a [Context] suitable for
     * passing to [Span.makeCurrent] or [SpanBuilder.setParent].
     *
     * Tolerates null / blank input by returning `Context.root()` — the
     * worker will then start a new root span (no-op when the SDK is
     * disabled). This matches the V004 migration's comment that a NULL
     * `trace_context` is a valid value meaning "no parent".
     */
    fun decodeContext(traceparent: String?): Context {
        if (traceparent.isNullOrBlank()) return Context.root()
        val carrier = mapOf(TRACEPARENT to traceparent)
        return propagators.textMapPropagator.extract(
            Context.root(),
            carrier,
            MapGetter,
        )
    }

    /**
     * Start the root span for an inbound MLLP message.
     *
     * Span name: `mllp.receive` (constant — operators recognize it on
     * sight in Jaeger).
     * Span kind: SERVER (this is the entry point into our service from
     * an external sender).
     *
     * Standard OTel semantic attributes are stamped on the span:
     *
     *   - `messaging.system = hl7v2`
     *   - `messaging.operation = receive`
     *   - `source.system` / `source.id` / `message.type` (custom — we
     *     control these names; not in the OTel semantic-conventions
     *     reference but consistent with our metric labels).
     *   - `correlation_id` — the existing #388 identifier, stamped on
     *     the span so it's searchable in Jaeger by the same value
     *     operators paste into log grep.
     *
     * Caller is responsible for calling [Span.end] (typically through
     * [Span.makeCurrent]'s try-with-resources / `use { }` pattern).
     */
    fun startReceiveSpan(
        sourceSystem: String,
        sourceId: String,
        messageType: String,
        correlationId: String,
    ): Span =
        tracer.spanBuilder(SPAN_MLLP_RECEIVE)
            .setSpanKind(SpanKind.SERVER)
            .setNoParent() // MLLP isn't trace-aware — always start a fresh trace
            .setAttribute(ATTR_MESSAGING_SYSTEM, "hl7v2")
            .setAttribute(ATTR_MESSAGING_OPERATION, "receive")
            .setAttribute(ATTR_SOURCE_SYSTEM, sourceSystem)
            .setAttribute(ATTR_SOURCE_ID, sourceId)
            .setAttribute(ATTR_MESSAGE_TYPE, messageType)
            .setAttribute(ATTR_CORRELATION_ID, correlationId)
            .startSpan()

    /**
     * Start the worker's processing span, parented on the context
     * decoded from the row's `trace_context` column.
     *
     * Span name: `worker.process`.
     * Span kind: INTERNAL (this is in-process work, not a wire entry
     * point).
     *
     * When [parentContext] is `Context.root()` (e.g. the SDK was
     * disabled at receive time, or the row predates V004), this starts
     * a new root span. The trace then contains only the worker side of
     * the pipeline, which is honest reporting: we don't have the
     * receive context, so we don't claim a fake parent.
     */
    fun startWorkerProcessSpan(
        parentContext: Context,
        messageId: Long,
        messageType: String,
        correlationId: String,
    ): Span =
        tracer.spanBuilder(SPAN_WORKER_PROCESS)
            .setSpanKind(SpanKind.INTERNAL)
            .setParent(parentContext)
            .setAttribute(ATTR_MESSAGE_ID, messageId)
            .setAttribute(ATTR_MESSAGE_TYPE, messageType)
            .setAttribute(ATTR_CORRELATION_ID, correlationId)
            .startSpan()

    companion object {
        // Span names — referenced by tests so they're constants here.
        const val SPAN_MLLP_RECEIVE: String = "mllp.receive"
        const val SPAN_WORKER_PROCESS: String = "worker.process"

        // Attribute keys. The `messaging.*` ones come from OTel's
        // semantic-conventions reference; the `source.*` and
        // `message.*` ones are app-defined (we own them).
        const val ATTR_MESSAGING_SYSTEM: String = "messaging.system"
        const val ATTR_MESSAGING_OPERATION: String = "messaging.operation"
        const val ATTR_SOURCE_SYSTEM: String = "source.system"
        const val ATTR_SOURCE_ID: String = "source.id"
        const val ATTR_MESSAGE_TYPE: String = "message.type"
        const val ATTR_MESSAGE_ID: String = "message.id"
        const val ATTR_CORRELATION_ID: String = "correlation_id"

        // W3C header name.
        const val TRACEPARENT: String = "traceparent"
    }

    /**
     * TextMapSetter used when injecting trace context into a Map<String, String>
     * carrier. Static so we don't allocate one per call.
     */
    private object MapSetter : TextMapSetter<MutableMap<String, String>> {
        override fun set(carrier: MutableMap<String, String>?, key: String, value: String) {
            carrier?.put(key, value)
        }
    }

    /**
     * TextMapGetter that reads from a `Map<String, String>` carrier.
     * Used when [decodeContext] extracts traceparent from the single-entry
     * map we synthesize from the DB column.
     */
    private object MapGetter : TextMapGetter<Map<String, String>> {
        override fun keys(carrier: Map<String, String>): Iterable<String> = carrier.keys
        override fun get(carrier: Map<String, String>?, key: String): String? = carrier?.get(key)
    }
}
