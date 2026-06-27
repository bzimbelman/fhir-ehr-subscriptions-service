package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import ca.uhn.hl7v2.AcknowledgmentCode
import ca.uhn.hl7v2.model.Message
import ca.uhn.hl7v2.parser.PipeParser
import ca.uhn.hl7v2.util.Terser
import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config.Hl7V2MllpProperties
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import org.apache.camel.Exchange
import org.apache.camel.builder.RouteBuilder
import org.apache.camel.component.mllp.MllpConstants
import org.slf4j.LoggerFactory
import java.time.Instant
import java.util.UUID

/**
 * Camel route builder for the HL7 v2 MLLP listener (ticket #431).
 *
 * This class is what actually binds the MLLP TCP port and runs the
 * synchronous receive cycle. It's a refactor of the legacy
 * `interface-engine/.../routes/IngestRoutes.kt` — same Camel URI, same
 * `.unmarshal().hl7()` DSL, same header extraction, same ACK semantics
 * — but the persist step is now expressed as a SPI callback the plugin
 * fires per inbound message. The interface-engine wraps that callback
 * around its `IngestPersistService` so we keep the existing JPA persist
 * + OTel span + correlation-id-swap behaviour byte-for-byte.
 *
 * ## What stays the same as the legacy route
 *
 *   - Route id: [Hl7V2MllpIngestSource.ROUTE_ID] = `"mllp-ingest"`
 *     (existing IngestRoutesTest reads this).
 *   - URI: `mllp://${host}:${port}?autoAck=false&charsetName=${characterSet}`
 *     — `autoAck=false` so we decide AA vs AE, not "we got bytes".
 *   - Parse via `.unmarshal().hl7()` (IPF DSL on RouteBuilder).
 *   - Extract MSH-3/9/10 into Camel headers for log lines and the ACK
 *     generation downstream.
 *   - ACK AA on success, AE on any thrown exception inside the persist
 *     callback.
 *
 * ## What's new
 *
 *   - The persist step is no longer hard-wired to `IngestPersistService`.
 *     It's a `(PipelineMessage) -> Unit` callback the route invokes,
 *     supplied by [Hl7V2MllpIngestSource.start]. The callback can throw;
 *     the Camel error handler catches the exception and converts it to
 *     AE, same as before.
 *   - Correlation id generation happens INSIDE the parser
 *     ([Hl7V2MessageParser.parse]) instead of being a route-level
 *     processor. The route still pulls the value back out for the ACK
 *     log line and for the AE handler.
 *
 * ## Why we don't drive @Component scanning here
 *
 * This RouteBuilder is constructed by [Hl7V2MllpIngestSource] inside
 * `start()`, not auto-discovered as a Spring bean. Two reasons:
 *
 *   1. The SPI contract says the IngestSource owns the route lifecycle
 *      — it picks when to add the builder to the CamelContext (in
 *      `start()`) and when to remove the routes (in `stop()`).
 *      Auto-discovered RouteBuilders would be added at context startup,
 *      bypassing the SPI lifecycle.
 *   2. A future story may add multiple IngestSource instances on
 *      different ports (think: separate MLLP listeners for ADT vs ORM
 *      traffic). Each instance constructs its own RouteBuilder; Spring
 *      auto-scanning a `@Component` RouteBuilder would only register
 *      ONE instance.
 */
class Hl7V2MllpCamelRouteBuilder(
    private val properties: Hl7V2MllpProperties,
    private val callback: (PipelineMessage) -> Unit,
    // Route id is configurable so tests and a future "multiple MLLP
    // listeners" deployment can disambiguate. Defaults to the canonical
    // `mllp-ingest` value to preserve compatibility with the legacy
    // IngestRoutesTest assertions.
    private val routeId: String = Hl7V2MllpIngestSource.ROUTE_ID,
) : RouteBuilder() {

    private val log = LoggerFactory.getLogger(Hl7V2MllpCamelRouteBuilder::class.java)

    // PipeParser used by extractHeaders() to terse fields off the parsed
    // HAPI Message. We don't re-parse the wire bytes — `.unmarshal().hl7()`
    // has already produced a Message object that we read from. Thread-safe
    // per HAPI docs.
    private val pipeParser: PipeParser = PipeParser()

    override fun configure() {

        // Error handler: any unhandled exception becomes AE. Logs the
        // failure with the captured headers so an operator can grep by
        // control id. handled(true) stops the exception from bubbling
        // further; the MLLP consumer just writes the ACK property we
        // set back to the sender.
        onException(Exception::class.java)
            .handled(true)
            .process { exchange ->
                val cause = exchange.getProperty(Exchange.EXCEPTION_CAUGHT, Throwable::class.java)
                val stage = (exchange.getProperty(PROP_STAGE) ?: "unknown").toString()
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

        // Main MLLP ingest route. Same linear flow as the legacy
        // IngestRoutes:
        //
        //   raw bytes off socket
        //     -> snapshot raw bytes (BEFORE unmarshal — they're still
        //        the original buffer at this point)
        //     -> .unmarshal().hl7() (IPF DSL → HAPI Message on body)
        //     -> extractHeaders (MSH-3/9/10 → Camel headers)
        //     -> persist (SPI callback)
        //     -> setAckProperty AA
        //
        // Camel-MLLP component normalizes the body to a byte[] before
        // delivering the Exchange to the route. We snapshot the byte[]
        // ourselves rather than letting it travel as the body, because
        // .unmarshal().hl7() replaces the body with the parsed HAPI
        // Message — and we want the original wire bytes to flow into
        // PipelineMessage.raw.
        from(buildEndpointUri())
            .routeId(routeId)
            .process { exchange -> snapshotRawBytes(exchange) }
            .unmarshal().hl7()
            .process { exchange -> extractHeaders(exchange) }
            .process { exchange -> logIncoming(exchange) }
            .setProperty(PROP_STAGE, constant("persist"))
            .process { exchange -> invokeCallback(exchange) }
            .process { exchange -> setAckProperty(exchange, AcknowledgmentCode.AA) }
    }

    /**
     * Assemble the Camel endpoint URI for the MLLP listener.
     *
     * Format reproduces what the legacy route used:
     *   `mllp://${host}:${port}?autoAck=false&charsetName=${characterSet}`
     *
     * `autoAck=false` is critical — without it, Camel writes a generic
     * AA back the moment it has bytes on the socket, bypassing our
     * persist+decide-the-ACK flow. With it, the route's responsibility
     * is to populate the [MllpConstants.MLLP_ACKNOWLEDGEMENT_STRING]
     * exchange property; Camel writes that property's value to the
     * wire.
     */
    private fun buildEndpointUri(): String =
        "mllp://${properties.host}:${properties.port}" +
            "?autoAck=false" +
            "&charsetName=${properties.characterSet}"

    /**
     * Capture the raw inbound byte buffer onto an exchange property
     * BEFORE .unmarshal().hl7() replaces the body with a parsed HAPI
     * Message. This is the buffer we hand to the SPI callback as
     * PipelineMessage.raw — the SPI contract requires "the unmodified
     * message bytes as they arrived."
     */
    private fun snapshotRawBytes(exchange: Exchange) {
        val body = exchange.message.body
        val bytes: ByteArray = when (body) {
            is ByteArray -> body
            is String -> body.toByteArray(charset(properties.characterSet))
            else -> throw IllegalStateException(
                "MLLP route received unexpected body type: ${body?.javaClass?.name ?: "null"}",
            )
        }
        exchange.setProperty(PROP_RAW_BYTES, bytes)
    }

    private fun extractHeaders(exchange: Exchange) {
        val msg = exchange.message.body as? Message ?: return
        val terser = Terser(msg)
        val msgType = safeTerse(terser, "/MSH-9-1")
        val triggerEvent = safeTerse(terser, "/MSH-9-2")
        val composedType =
            if (triggerEvent.isNotEmpty()) "${msgType}_$triggerEvent" else msgType
        exchange.message.setHeader(HDR_MESSAGE_TYPE, composedType)
        exchange.message.setHeader(HDR_CONTROL_ID, safeTerse(terser, "/MSH-10"))
        exchange.message.setHeader(HDR_SENDING_APP, safeTerse(terser, "/MSH-3"))
    }

    private fun logIncoming(exchange: Exchange) {
        log.info(
            "incoming type={} controlId={} sendingApp={}",
            exchange.message.getHeader(HDR_MESSAGE_TYPE),
            exchange.message.getHeader(HDR_CONTROL_ID),
            exchange.message.getHeader(HDR_SENDING_APP),
        )
    }

    /**
     * Build the [PipelineMessage] and hand it to the SPI callback.
     *
     * The callback is where the host (interface-engine) does its
     * persist + OTel-span work. If it throws, the Camel error handler
     * catches and converts to AE.
     *
     * Defensive check: blank MSH-3 or MSH-10 means we can't form a
     * (source_system, source_id) idempotency key downstream. The
     * legacy route raised here with an explicit IllegalArgumentException;
     * we keep that contract so the error handler's AE includes the
     * same reason string operators are used to.
     */
    private fun invokeCallback(exchange: Exchange) {
        val sendingApp = (exchange.message.getHeader(HDR_SENDING_APP) ?: "").toString()
        val controlId = (exchange.message.getHeader(HDR_CONTROL_ID) ?: "").toString()
        val messageType = (exchange.message.getHeader(HDR_MESSAGE_TYPE) ?: "").toString()
        require(sendingApp.isNotEmpty()) { "MSH-3 (sending application) is required" }
        require(controlId.isNotEmpty()) { "MSH-10 (message control id) is required" }
        require(messageType.isNotEmpty()) { "MSH-9 (message type) is required" }

        val rawBytes = exchange.getProperty(PROP_RAW_BYTES) as? ByteArray
            ?: throw IllegalStateException("raw bytes snapshot missing from exchange")

        val pipelineMessage = PipelineMessage(
            // Correlation id generated here per the SPI's "generate when
            // protocol has no native correlation header" rule. HL7 v2
            // has none. UUID v4 matches CorrelationId.generate() in the
            // interface-engine module — the host's callback may swap
            // this for an existing row's correlation id if the persist
            // returns an idempotent-duplicate; that's the host's
            // concern, not ours.
            correlationId = UUID.randomUUID().toString(),
            receivedAt = Instant.now(),
            sourceProtocol = Hl7V2MessageParser.SOURCE_PROTOCOL,
            sourceSystem = sendingApp,
            sourceId = controlId,
            raw = rawBytes,
            contentType = Hl7V2MessageParser.CONTENT_TYPE,
            attributes = mapOf(
                Hl7V2MessageParser.ATTR_MESSAGE_TYPE to messageType,
                Hl7V2MessageParser.ATTR_CONTROL_ID to controlId,
                Hl7V2MessageParser.ATTR_SENDING_APP to sendingApp,
            ),
        )

        callback(pipelineMessage)
    }

    /**
     * Set the MLLP ACK property Camel will write back to the sender.
     *
     * The MLLP component reads [MllpConstants.MLLP_ACKNOWLEDGEMENT_STRING]
     * off the exchange and frames it. We use HAPI's `generateACK` to
     * produce an MSA segment that echoes MSH-10 — exactly what the
     * legacy route did.
     *
     * No-op if there's no parsed Message on the body (e.g. an exception
     * thrown before .unmarshal().hl7() succeeded). Camel-MLLP will then
     * send its default error response — the legacy route had the same
     * "no Message → no explicit ACK" behaviour.
     */
    private fun setAckProperty(exchange: Exchange, code: AcknowledgmentCode) {
        val inbound = exchange.message.body as? Message ?: return
        val ack: Message = inbound.generateACK(code, null)
        val encoded: String = ack.encode()
        exchange.setProperty(MllpConstants.MLLP_ACKNOWLEDGEMENT_STRING, encoded)
    }

    private fun safeTerse(terser: Terser, path: String): String =
        try {
            terser.get(path).orEmpty()
        } catch (_: Exception) {
            ""
        }

    companion object {
        // Camel header names — match the legacy IngestRoutes constants
        // so existing tests / downstream code see the same names.
        const val HDR_MESSAGE_TYPE: String = "hl7.messageType"
        const val HDR_CONTROL_ID: String = "hl7.controlId"
        const val HDR_SENDING_APP: String = "hl7.sendingApp"

        // Internal exchange properties — not exposed to anything outside
        // this route.
        const val PROP_RAW_BYTES: String = "hl7.rawBytes"
        const val PROP_STAGE: String = "stage"
    }
}
