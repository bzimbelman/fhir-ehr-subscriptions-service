package com.bzonfhir.subscriptionservice.interfaceengine.routes

import ca.uhn.hl7v2.AcknowledgmentCode
import ca.uhn.hl7v2.model.Message
import ca.uhn.hl7v2.util.Terser
import com.bzonfhir.subscriptionservice.interfaceengine.observability.CorrelationId
import com.bzonfhir.subscriptionservice.interfaceengine.observability.OtelTracing
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestPersistService
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import io.opentelemetry.api.trace.Span
import io.opentelemetry.api.trace.StatusCode
import io.opentelemetry.context.Scope
import org.apache.camel.Exchange
import org.apache.camel.builder.RouteBuilder
import org.apache.camel.component.mllp.MllpConstants
import org.slf4j.LoggerFactory
import org.slf4j.MDC
import org.springframework.beans.factory.annotation.Value
import org.springframework.stereotype.Component

/**
 * MLLP ingress: parse → persist → ACK.
 *
 * As of ticket #381 the synchronous receive path's contract is narrow and
 * fully owned by this class:
 *
 *   1. Listen on `mllp://0.0.0.0:${MLLP_PORT}` with `autoAck=false`, so the
 *      ACK we send is the one we *decide*, not "we got bytes".
 *   2. Parse the inbound v2 message via IPF's `.unmarshal().hl7()`.
 *   3. Pull MSH-3 / MSH-9 / MSH-10 into Camel headers via `Terser` — the
 *      sending app (`hl7.sendingApp`), composed message type (`hl7.messageType`,
 *      e.g. `ADT_A01`), and control id (`hl7.controlId`).
 *   4. Stash the original ER7 text on the exchange (re-marshalled HAPI Message
 *      → string) so we can persist exactly what arrived. The HAPI `Message`
 *      itself stays on the in-message so ACK generation can use it later.
 *   5. Insert one row into `ingested_messages` with `status = RECEIVED` via
 *      [IngestPersistService.persistReceived]. The service is `@Transactional`
 *      with `REQUIRES_NEW`, so the row is durable before we return; the ACK
 *      we send to the sender corresponds to a committed write, not a
 *      pending-commit one.
 *   6. ACK `AA` to the sender, echoing their MSH-10.
 *
 * The transform-and-POST-to-HAPI work that this route used to do has moved
 * out of the synchronous path. The async worker (ticket #382, next story)
 * polls rows in `status = RECEIVED`, drives the v2→FHIR transform, posts to
 * HAPI, and progresses the row through TRANSFORMING → DELIVERED. That work
 * is not started yet — this commit only sets up the receive half so the
 * worker has rows to chew on once it lands.
 *
 * ## Idempotency
 *
 * The EHR is allowed (and expected) to retry on connection failures. A
 * retry resends the same MSH-10. Our table has a UNIQUE constraint on
 * (`source_system`, `source_id`); duplicates are detected inside
 * [IngestPersistService.persistReceived] which catches the
 * `DataIntegrityViolationException`, looks up the existing row, and returns
 * it. From this route's point of view, idempotent duplicates and first-time
 * inserts both succeed → we ACK AA in both cases. This is the right thing
 * to do: the sender has already done its job, and ACKing AE would tell it
 * to keep retrying indefinitely.
 *
 * ## Failure model
 *
 *   - JPA persist succeeds (insert or idempotent lookup-after-duplicate) → AA.
 *   - JPA persist throws (DB unreachable, NOT NULL violation, FK violation,
 *     anything other than the duplicate-control-id case the service has
 *     already handled) → AE with a short reason string. This is the *only*
 *     way the route emits AE.
 *
 * If we can't take durable ownership of the message, AE is honest — it tells
 * the sender to retry, which is correct because the message is not in our
 * store yet and won't be processed without their retry. AE on anything else
 * (parse error inside `.unmarshal().hl7()`, missing MSH fields, etc.) would
 * be the wrong call here: those messages are bad-input that retrying will
 * not fix. We could route those to a poison-pill table later; for ticket
 * #381's scope, we let Camel's default error handling kick in (which still
 * results in AE for unhandled exceptions — same final wire behaviour, just
 * with a different reason in the logs). When #383 adds a poison-pill table
 * it will branch on exception type here.
 */
@Component
class IngestRoutes(
    @Value("\${subscription-service.mllp.port:2575}") private val mllpPort: Int,
    private val persistService: IngestPersistService,
    // OpenTelemetry tracer (Epic #387, ticket #394). Starts the
    // `mllp.receive` root span before the persist call so the
    // captured trace context can be stored on the row alongside the
    // correlation_id, and the async worker can later restore the same
    // trace context to make its `worker.process` span a CHILD of the
    // receive span. The tracing wrapper handles the SDK-disabled case
    // transparently: encode() returns null, persist stores null, the
    // worker tolerates null by starting a fresh root.
    private val otelTracing: OtelTracing,
) : RouteBuilder() {

    private val log = LoggerFactory.getLogger(IngestRoutes::class.java)

    override fun configure() {

        // Single error handler for the route: any unhandled exception becomes
        // an AE ACK. The exception is logged with the captured headers so we
        // can correlate by control id. `handled(true)` stops the exception
        // from bubbling further; the MLLP consumer just sees the ACK property
        // we set and writes it back to the sender.
        onException(Exception::class.java)
            .handled(true)
            .process { exchange ->
                val cause = exchange.getProperty(Exchange.EXCEPTION_CAUGHT, Throwable::class.java)
                val stage = (exchange.getProperty(STAGE) ?: "unknown").toString()
                val controlId = (exchange.message.getHeader(HDR_CONTROL_ID) ?: "").toString()
                val messageType = (exchange.message.getHeader(HDR_MESSAGE_TYPE) ?: "").toString()
                val reason = cause?.message ?: cause?.javaClass?.simpleName ?: "unknown"
                log.warn(
                    "receive failed type={} controlId={} stage={} reason={}",
                    messageType,
                    controlId,
                    stage,
                    reason,
                )
                setAckProperty(exchange, AcknowledgmentCode.AE)
            }

        // ---- Main MLLP ingest route ----
        //
        // The flow is deliberately linear — no `choice()` by message type
        // anymore. Every message we receive takes the same path: assign a
        // correlation id → parse → capture headers + raw text → persist →
        // ACK. Type-specific behaviour belongs in the async worker (#382),
        // not here.
        from("mllp://0.0.0.0:$mllpPort?autoAck=false")
            .routeId(ROUTE_MLLP_INGEST)
            // Establish the correlation id BEFORE we do any work, so every
            // log line below (parse, persist, ACK) carries the same id and
            // matches the id we'll persist on the row and send as
            // X-Correlation-Id on downstream HTTP calls in the worker.
            // HL7 v2 has no transport-level header; we generate UUID v4.
            // The MDC is cleared on the way out (success OR failure) via
            // an onCompletion processor, so we don't leak the id to a
            // sibling exchange running on the same thread.
            .process { exchange -> assignCorrelationId(exchange) }
            .onCompletion()
                .process { _ -> MDC.remove(CorrelationId.MDC_KEY) }
                .end()
            .unmarshal().hl7()
            .process { exchange -> extractHeaders(exchange) }
            // Snapshot the original ER7 text NOW, while body is still the
            // parsed HAPI Message. We need to persist what the sender sent
            // (re-encoded losslessly via HAPI's own encoder), not whatever a
            // later processor might have set the body to. The Message itself
            // is preserved on the in-message so setAckProperty() can use it.
            .process { exchange -> snapshotRawMessage(exchange) }
            .process { exchange -> logIncoming(exchange) }
            .setProperty(STAGE, constant("persist"))
            .process { exchange -> persistMessage(exchange) }
            .process { exchange -> setAckProperty(exchange, AcknowledgmentCode.AA) }
    }

    /**
     * First processor in the route — generate (or accept inbound, when one
     * exists on a future protocol) the correlation id and set the MDC. The
     * value travels:
     *
     *   - in MDC so logs join the receive scope,
     *   - on the Camel exchange property so [persistMessage] can write it
     *     to the row,
     *   - returned to the sender? — no, HL7 v2 has no slot for arbitrary
     *     metadata in the ACK; the sender uses MSH-10 to correlate. The
     *     correlation id is server-only and surfaces via the admin API.
     *
     * Sets the MDC up-front; the matching cleanup runs in `onCompletion`
     * on the route (see [configure]).
     */
    private fun assignCorrelationId(exchange: Exchange) {
        val id = CorrelationId.generate()
        exchange.setProperty(PROP_CORRELATION_ID, id)
        MDC.put(CorrelationId.MDC_KEY, id)
    }

    // -- Header extraction ------------------------------------------------

    private fun extractHeaders(exchange: Exchange) {
        val msg = exchange.message.body as? Message ?: return
        val terser = Terser(msg)
        val msgType = safe(terser, "/MSH-9-1")
        val triggerEvent = safe(terser, "/MSH-9-2")
        // Composed message type matches what the v2-to-FHIR IG uses to key
        // StructureMaps (e.g. ADT_A01), and what the async worker (#382) will
        // dispatch on. Storing it in the row's message_type column means the
        // worker doesn't have to re-parse the raw message just to route.
        val composedType = if (triggerEvent.isNotEmpty()) "${msgType}_$triggerEvent" else msgType
        exchange.message.setHeader(HDR_MESSAGE_TYPE, composedType)
        exchange.message.setHeader(HDR_CONTROL_ID, safe(terser, "/MSH-10"))
        exchange.message.setHeader(HDR_SENDING_APP, safe(terser, "/MSH-3"))
    }

    private fun snapshotRawMessage(exchange: Exchange) {
        val msg = exchange.message.body as? Message ?: return
        // HAPI re-encodes deterministically from the parsed object graph; the
        // result is byte-equivalent to the wire input for well-formed v2.
        // For ill-formed input the parser would already have thrown, so we
        // don't have a "raw bytes we couldn't parse" case to worry about
        // here. (#383 may add a poison-pill column for unparseable input.)
        val raw = msg.encode()
        exchange.setProperty(PROP_RAW_MESSAGE, raw)
        // Preserve the parsed Message on the body so generateACK() can read
        // MSH-10/MSH-9 from it later without re-parsing.
    }

    private fun logIncoming(exchange: Exchange) {
        log.info(
            "incoming type={} controlId={} sendingApp={}",
            exchange.message.getHeader(HDR_MESSAGE_TYPE),
            exchange.message.getHeader(HDR_CONTROL_ID),
            exchange.message.getHeader(HDR_SENDING_APP),
        )
    }

    // -- Persistence ------------------------------------------------------

    private fun persistMessage(exchange: Exchange) {
        val sendingApp = (exchange.message.getHeader(HDR_SENDING_APP) ?: "").toString()
        val controlId = (exchange.message.getHeader(HDR_CONTROL_ID) ?: "").toString()
        val messageType = (exchange.message.getHeader(HDR_MESSAGE_TYPE) ?: "").toString()
        val raw = exchange.getProperty(PROP_RAW_MESSAGE) as? String
            ?: throw IllegalStateException("raw message snapshot missing from exchange")

        // Defensive: blank source_system/source_id would defeat the
        // idempotency key entirely. We require both. If a sender omits MSH-3
        // or MSH-10 we can't safely de-dupe their traffic, so we treat it
        // as a malformed-message AE — which is correct: retrying without
        // fixing MSH won't help, but we'd rather alert on it than silently
        // accept un-dedup'able rows.
        require(sendingApp.isNotEmpty()) { "MSH-3 (sending application) is required" }
        require(controlId.isNotEmpty()) { "MSH-10 (message control id) is required" }
        require(messageType.isNotEmpty()) { "MSH-9 (message type) is required" }

        val correlationId = exchange.getProperty(PROP_CORRELATION_ID, String::class.java)
            ?: throw IllegalStateException("correlation_id missing from exchange — assignCorrelationId() did not run")

        // Start the OTel root span (`mllp.receive`). MLLP isn't trace-aware
        // so we always start a new trace here — no parent extraction. The
        // span is `setAttribute`'d with the OTel messaging semantic
        // conventions so a Jaeger filter on `messaging.system=hl7v2`
        // returns every inbound message. We hold the span open through the
        // persist call so the trace context the SDK encodes from
        // Context.current() (line below: encodeCurrentContext) actually
        // points at THIS span — that string is what the worker decodes to
        // make its `worker.process` span a child of receive.
        val receiveSpan: Span = otelTracing.startReceiveSpan(
            sourceSystem = sendingApp,
            sourceId = controlId,
            messageType = messageType,
            correlationId = correlationId,
        )
        val scope: Scope = receiveSpan.makeCurrent()
        try {
            // Capture the W3C traceparent string for THIS span and persist
            // it on the row. When the SDK is disabled this is null and the
            // row's trace_context column stays NULL — the worker tolerates
            // that and starts a fresh root (no-op) span on its side.
            val traceContext = otelTracing.encodeCurrentContext()

            val saved = persistService.persistReceived(
                sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
                sourceSystem = sendingApp,
                sourceId = controlId,
                messageType = messageType,
                rawMessage = raw,
                rawContentType = RAW_CONTENT_TYPE_HL7V2,
                correlationId = correlationId,
                traceContext = traceContext,
            )
            exchange.setProperty(PROP_PERSISTED_ID, saved.id)
            // Idempotent-duplicate handling: persistReceived returns the
            // previously-persisted row when the (source_system, source_id)
            // unique constraint trips. That row's correlation_id is from
            // the ORIGINAL receive; we discard the freshly-minted id and
            // adopt the persisted one so the worker's log lines (and any
            // downstream X-Correlation-Id headers) line up with the
            // original trace rather than the duplicate one. We do NOT,
            // however, swap in the persisted row's trace_context: this
            // pod's receive span (the one currently active) is the actual
            // ancestor span for any log line emitted right now; the
            // duplicate's trace context belongs to a previous receive
            // that may have happened on a different pod.
            val effective = saved.correlationId ?: correlationId
            if (effective != correlationId) {
                MDC.put(CorrelationId.MDC_KEY, effective)
                exchange.setProperty(PROP_CORRELATION_ID, effective)
            }
            log.info(
                "received id={} type={} controlId={} sourceSystem={}",
                saved.id,
                messageType,
                controlId,
                sendingApp,
            )
        } catch (ex: Exception) {
            // Record the failure on the span so Jaeger surfaces a red
            // marker rather than silently succeeding. We rethrow — the
            // Camel error handler still owns the AE-vs-AA decision.
            receiveSpan.setStatus(StatusCode.ERROR, ex.message ?: ex.javaClass.simpleName)
            receiveSpan.recordException(ex)
            throw ex
        } finally {
            // Always close the scope first (so Context.current() returns
            // the parent), then end the span. Reversed order would leak
            // the span as the current context to whatever runs next on
            // this thread.
            scope.close()
            receiveSpan.end()
        }
    }

    // -- ACK helper ------------------------------------------------------

    private fun setAckProperty(exchange: Exchange, code: AcknowledgmentCode) {
        val inbound = exchange.message.body as? Message
            ?: return
        val ack: Message = inbound.generateACK(code, null)
        val encoded: String = ack.encode()
        exchange.setProperty(MllpConstants.MLLP_ACKNOWLEDGEMENT_STRING, encoded)
    }

    private fun safe(terser: Terser, path: String): String =
        try {
            terser.get(path).orEmpty()
        } catch (_: Exception) {
            ""
        }

    companion object {
        const val ROUTE_MLLP_INGEST = "mllp-ingest"

        // Header names — Camel headers travel with the Exchange and outlive
        // each processor, so logs + ACK generation can read them without
        // re-parsing the HL7 message.
        const val HDR_MESSAGE_TYPE = "hl7.messageType"
        const val HDR_CONTROL_ID = "hl7.controlId"
        const val HDR_SENDING_APP = "hl7.sendingApp"

        // Exchange properties — internal to the route, not exposed in logs.
        const val PROP_RAW_MESSAGE = "hl7.rawMessage"
        const val PROP_PERSISTED_ID = "ingested.id"
        const val PROP_CORRELATION_ID = "observability.correlationId"
        const val STAGE = "stage"

        // Stored verbatim in `ingested_messages.raw_content_type`. The async
        // worker (#382) uses this to pick the right parser / StructureMap
        // family. v1 only writes HL7v2 over MLLP; FHIR-REST inbound would
        // write "application/fhir+json" here, etc.
        const val RAW_CONTENT_TYPE_HL7V2 = "application/hl7-v2"
    }
}
