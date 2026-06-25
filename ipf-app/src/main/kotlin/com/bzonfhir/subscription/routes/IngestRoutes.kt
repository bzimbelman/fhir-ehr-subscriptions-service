package com.bzonfhir.subscription.routes

import ca.uhn.hl7v2.AcknowledgmentCode
import ca.uhn.hl7v2.model.Message
import ca.uhn.hl7v2.util.Terser
import org.apache.camel.Exchange
import org.apache.camel.builder.RouteBuilder
import org.apache.camel.component.mllp.MllpConstants
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.stereotype.Component

/**
 * MLLP ingress for HL7 v2 messages.
 *
 * Ticket #360 — scaffold only. This route just receives, parses, logs, and ACKs.
 * The transform-to-FHIR / Matchbox / HAPI POST work happens in ticket #361.
 *
 * We use IPF's HL7 DSL (`.unmarshal().hl7()` decodes raw MLLP bytes into a
 * typed HAPI `Message`) but bind the listener with Camel's native `mllp://`
 * component, not IPF's IHE-flavored one (which requires a `hl7TransactionConfig`).
 *
 * Camel's MLLP consumer with `autoAck=false` looks for the ACK in an exchange
 * property — NOT the message body — and writes that property back to the
 * sender with proper MLLP framing. We set the property to the encoded ACK
 * string produced by HAPI's `Message.generateACK()`.
 *
 * Why not `autoAck=true`? Camel's auto-generated ACK is built BEFORE the route
 * runs, so if a #361 transform step throws, the sender still gets AA. We want
 * `autoAck=false` so the ACK reflects whether the rest of the route succeeded.
 */
@Component
class IngestRoutes(
    @Value("\${subscription-service.mllp.port:2575}") private val mllpPort: Int,
) : RouteBuilder() {

    private val log = LoggerFactory.getLogger(IngestRoutes::class.java)

    override fun configure() {
        from("mllp://0.0.0.0:$mllpPort?autoAck=false")
            .routeId("mllp-ingest")
            // Parse raw MLLP bytes into a typed HAPI Message via IPF's HL7 DSL.
            .unmarshal().hl7()
            .process { exchange -> logReceivedMessage(exchange) }
            // Build an AA ACK from the inbound and set the MLLP consumer's
            // CamelMllpAcknowledgementString exchange property.
            .process { exchange -> setAckProperty(exchange, AcknowledgmentCode.AA) }
    }

    private fun logReceivedMessage(exchange: Exchange) {
        val body = exchange.message.body
        val msg = body as? Message
        if (msg == null) {
            log.warn("MLLP message body was not a HAPI Message; class={}", body?.javaClass?.name)
            return
        }
        val terser = Terser(msg)
        val msgType = safe(terser, "/MSH-9-1")
        val triggerEvent = safe(terser, "/MSH-9-2")
        val controlId = safe(terser, "/MSH-10")
        val sendingApp = safe(terser, "/MSH-3")
        log.info(
            "Received HL7 v2 message type={}^{} controlId={} sendingApp={}",
            msgType,
            triggerEvent,
            controlId,
            sendingApp,
        )
    }

    private fun setAckProperty(exchange: Exchange, code: AcknowledgmentCode) {
        val inbound = exchange.message.body as? Message ?: return
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
}
