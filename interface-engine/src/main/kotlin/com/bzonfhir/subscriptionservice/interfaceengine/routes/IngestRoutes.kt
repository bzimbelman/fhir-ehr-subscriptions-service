package com.bzonfhir.subscriptionservice.interfaceengine.routes

import ca.uhn.hl7v2.AcknowledgmentCode
import ca.uhn.hl7v2.model.Message
import ca.uhn.hl7v2.util.Terser
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestPersistService
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import org.apache.camel.Exchange
import org.apache.camel.builder.RouteBuilder
import org.apache.camel.component.mllp.MllpConstants
import org.slf4j.LoggerFactory
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
        // anymore. Every message we receive takes the same path: parse,
        // capture headers + raw text, persist, ACK. Type-specific behaviour
        // belongs in the async worker (#382), not here.
        from("mllp://0.0.0.0:$mllpPort?autoAck=false")
            .routeId(ROUTE_MLLP_INGEST)
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

        val saved = persistService.persistReceived(
            sourceProtocol = IngestedMessageSourceProtocol.HL7V2_MLLP,
            sourceSystem = sendingApp,
            sourceId = controlId,
            messageType = messageType,
            rawMessage = raw,
            rawContentType = RAW_CONTENT_TYPE_HL7V2,
        )
        exchange.setProperty(PROP_PERSISTED_ID, saved.id)
        log.info(
            "received id={} type={} controlId={} sourceSystem={}",
            saved.id,
            messageType,
            controlId,
            sendingApp,
        )
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
        const val STAGE = "stage"

        // Stored verbatim in `ingested_messages.raw_content_type`. The async
        // worker (#382) uses this to pick the right parser / StructureMap
        // family. v1 only writes HL7v2 over MLLP; FHIR-REST inbound would
        // write "application/fhir+json" here, etc.
        const val RAW_CONTENT_TYPE_HL7V2 = "application/hl7-v2"
    }
}
