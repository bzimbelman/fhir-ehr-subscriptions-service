package com.bzonfhir.subscription.routes

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.hl7v2.AcknowledgmentCode
import ca.uhn.hl7v2.model.Message
import ca.uhn.hl7v2.util.Terser
import org.apache.camel.Exchange
import org.apache.camel.builder.RouteBuilder
import org.apache.camel.component.mllp.MllpConstants
import org.hl7.fhir.r4.model.Bundle
import org.hl7.fhir.r4.model.Identifier
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.stereotype.Component
import java.net.URLEncoder
import java.nio.charset.StandardCharsets

/**
 * MLLP ingress and HL7 v2 → FHIR transform pipeline.
 *
 * Flow:
 *   1. MLLP listener on :2575 receives an HL7 v2 ER7 message.
 *   2. IPF's `.unmarshal().hl7()` parses it into a HAPI `Message`.
 *   3. We pull MSH-3/9/10 into Camel headers (`hl7.sendingApp`,
 *      `hl7.messageType`, `hl7.controlId`) so every downstream stage and
 *      log line has them without re-parsing.
 *   4. A `choice()` dispatches by message type.
 *      - `ADT_A01` → call Matchbox `$transform`, parse the resulting Bundle,
 *        stamp an idempotency identifier, POST as a transaction to HAPI,
 *        then ACK `AA`.
 *      - Everything else → log and ACK `AA`. This is the pass-through path
 *        from ticket #360. As we add maps (ORU, ORM, SIU, MDM, VXU, etc.)
 *        each gets its own `when()` clause that targets a different
 *        StructureMap URL but otherwise reuses the same `transformAndPost`
 *        direct route.
 *   5. Any exception during transform/persist → `onException` handler ACKs
 *      `AE` (application error) with a short reason. MLLP `autoAck=false`
 *      ensures the ACK reflects the actual outcome, not just "we received
 *      bytes".
 *
 * Idempotency:
 *   We set `Bundle.identifier = {system: urn:ietf:rfc:3986,
 *   value: urn:hl7-controlId:<MSH-10>}` on every transformed bundle. HAPI's
 *   transaction processing does NOT actually de-dupe on `Bundle.identifier`
 *   (that field is metadata for the *message*, not for the contained
 *   resources). True idempotency would require either (a) conditional
 *   creates inside the Bundle (`ifNoneExist`), or (b) recording the
 *   processed control IDs in a side table and short-circuiting before we
 *   POST. We pick (a) where the StructureMap supports it; for now the
 *   identifier we set is purely a tracing aid. Documented as a follow-up
 *   in docs/architecture.md "Mapping strategy" → "Idempotency".
 */
@Component
class IngestRoutes(
    @Value("\${subscription-service.mllp.port:2575}") private val mllpPort: Int,
    @Value("\${subscription-service.matchbox.base-url}") private val matchboxBaseUrl: String,
    @Value("\${subscription-service.matchbox.structuremap.adt-a01}") private val adtA01StructureMap: String,
    private val fhirContext: FhirContext,
    private val hapiClient: IGenericClient,
) : RouteBuilder() {

    // Note: matchbox HTTP timeouts (MATCHBOX_TIMEOUT_MS) are configured
    // component-wide in application.yaml under `camel.component.http.*`,
    // not on the toD URI here. See the comment above the toD step.

    private val log = LoggerFactory.getLogger(IngestRoutes::class.java)

    override fun configure() {

        // Single error handler for the whole pipeline: any unhandled exception
        // becomes an AE ACK. The exception is logged with the captured headers
        // so we can correlate by control id. `handled(true)` stops the
        // exception from bubbling further; the MLLP consumer just sees the
        // ACK property we set and writes it back to the sender.
        onException(Exception::class.java)
            .handled(true)
            .process { exchange ->
                val cause = exchange.getProperty(Exchange.EXCEPTION_CAUGHT, Throwable::class.java)
                val stage = (exchange.getProperty(STAGE) ?: "unknown").toString()
                val controlId = (exchange.message.getHeader(HDR_CONTROL_ID) ?: "").toString()
                val messageType = (exchange.message.getHeader(HDR_MESSAGE_TYPE) ?: "").toString()
                log.warn(
                    "failed type={} controlId={} stage={} reason={}",
                    messageType,
                    controlId,
                    stage,
                    cause?.message ?: cause?.javaClass?.simpleName ?: "unknown",
                )
                setAckProperty(exchange, AcknowledgmentCode.AE)
            }

        // ---- Main MLLP ingest route ----
        from("mllp://0.0.0.0:$mllpPort?autoAck=false")
            .routeId(ROUTE_MLLP_INGEST)
            .unmarshal().hl7()
            .process { exchange -> extractHeaders(exchange) }
            .process { exchange -> logIncoming(exchange) }
            .choice()
                .`when`(header(HDR_MESSAGE_TYPE).isEqualTo("ADT_A01"))
                    .to("direct:transform-adt-a01")
                .otherwise()
                    // No transform yet for these types — log + ACK AA, same
                    // behaviour as ticket #360. Each new supported type will
                    // get its own `when()` above with its own StructureMap.
                    .process { exchange ->
                        log.info(
                            "passthrough type={} controlId={} (no transform configured)",
                            exchange.message.getHeader(HDR_MESSAGE_TYPE),
                            exchange.message.getHeader(HDR_CONTROL_ID),
                        )
                        setAckProperty(exchange, AcknowledgmentCode.AA)
                    }
            .end()

        // ---- ADT^A01 transform sub-route ----
        //
        // Pulled out into `direct:transform-adt-a01` for two reasons:
        //   1. AdviceWith can mock the matchbox call cleanly in tests without
        //      having to interpose on the MLLP listener.
        //   2. When ORU/ORM/etc. land, each gets its own `direct:transform-*`
        //      sub-route — the dispatcher stays small and readable.
        //
        // The route uses Camel's `http://` component via `toD()`. We compute
        // the absolute URL in `prepareMatchboxCall()` and stash it in the
        // header `matchbox.url`, then toD evaluates `${header.matchbox.url}`
        // at runtime. camel-http's toD optimization recognizes the static
        // host prefix and reuses the same producer for all calls; only the
        // path + query change per message.
        //
        // Timeouts cannot be appended as `?httpClient.*` to this toD URI
        // because the matchbox URL already includes a `?source=…` query
        // string, and Camel would treat the timeout options as additional
        // query parameters of *that* URL instead of endpoint options.
        // Instead we configure the timeouts globally on the http component
        // (set in HttpComponentConfig).
        val matchboxToDUri = "\${header.${HDR_MATCHBOX_URL}}"

        from("direct:transform-adt-a01")
            .routeId(ROUTE_TRANSFORM_ADT_A01)
            // Stash the original v2 message text so we can re-marshal it
            // for matchbox AND still have the HAPI Message around for
            // ACK generation later. Camel's HL7 DSL marshals the Message
            // back into ER7 text via `.marshal().hl7()`.
            .process { exchange -> exchange.setProperty(PROP_HL7_MESSAGE, exchange.message.body) }
            .setProperty(STAGE, constant("matchbox"))
            .marshal().hl7()
            .convertBodyTo(String::class.java, "UTF-8")
            .process { exchange -> prepareMatchboxCall(exchange) }
            // throwExceptionOnFailure is the default; on 4xx/5xx
            // HttpOperationFailedException is thrown and routes into the
            // onException handler → AE ACK.
            .toD(matchboxToDUri)
            .convertBodyTo(String::class.java, "UTF-8")
            .setProperty(STAGE, constant("hapi"))
            .process { exchange -> postBundleToHapi(exchange) }
            .process { exchange ->
                log.info(
                    "transformed type={} controlId={} resources={}",
                    exchange.message.getHeader(HDR_MESSAGE_TYPE),
                    exchange.message.getHeader(HDR_CONTROL_ID),
                    exchange.getProperty(PROP_RESOURCE_COUNT) ?: 0,
                )
                // Restore the HAPI Message so generateACK() works.
                exchange.message.body = exchange.getProperty(PROP_HL7_MESSAGE)
                setAckProperty(exchange, AcknowledgmentCode.AA)
            }
    }

    // -- Header extraction ------------------------------------------------

    private fun extractHeaders(exchange: Exchange) {
        val msg = exchange.message.body as? Message ?: return
        val terser = Terser(msg)
        val msgType = safe(terser, "/MSH-9-1")
        val triggerEvent = safe(terser, "/MSH-9-2")
        val composedType = if (triggerEvent.isNotEmpty()) "${msgType}_$triggerEvent" else msgType
        exchange.message.setHeader(HDR_MESSAGE_TYPE, composedType)
        exchange.message.setHeader(HDR_CONTROL_ID, safe(terser, "/MSH-10"))
        exchange.message.setHeader(HDR_SENDING_APP, safe(terser, "/MSH-3"))
    }

    private fun logIncoming(exchange: Exchange) {
        log.info(
            "incoming type={} controlId={} sendingApp={}",
            exchange.message.getHeader(HDR_MESSAGE_TYPE),
            exchange.message.getHeader(HDR_CONTROL_ID),
            exchange.message.getHeader(HDR_SENDING_APP),
        )
    }

    // -- Matchbox call setup ---------------------------------------------

    private fun prepareMatchboxCall(exchange: Exchange) {
        val sourceParam = URLEncoder.encode(adtA01StructureMap, StandardCharsets.UTF_8)
        // matchboxBaseUrl is e.g. "http://matchbox:8080/matchboxv3/fhir".
        // The full URL fed to toD: that prefix plus the $transform path and
        // ?source=<encoded URL>. We keep the protocol on the front because
        // camel-http's toD pattern expects "http://..." (or "https://...").
        val uri = "$matchboxBaseUrl/StructureMap/\$transform?source=$sourceParam"
        exchange.message.setHeader(HDR_MATCHBOX_URL, uri)
        exchange.message.setHeader(Exchange.HTTP_METHOD, "POST")
        // Matchbox's StructureMap engine recognizes this Content-Type as
        // "interpret body as HL7 v2 ER7 text and parse before applying
        // the source StructureMap". This is the contract documented by
        // the v2-to-FHIR IG; ticket #361 description references it
        // explicitly. (See architecture decision: matchbox is "just
        // another FHIR endpoint that exposes $transform".)
        exchange.message.setHeader(Exchange.CONTENT_TYPE, "x-application/hl7-v2+er7")
        exchange.message.setHeader("Accept", "application/fhir+json")
    }

    // -- HAPI POST -------------------------------------------------------

    private fun postBundleToHapi(exchange: Exchange) {
        val json = exchange.message.body as? String
            ?: throw IllegalStateException("Matchbox response body was not a String")
        val bundle = fhirContext.newJsonParser().parseResource(Bundle::class.java, json)
        // Idempotency marker — see class-level comment.
        val controlId = (exchange.message.getHeader(HDR_CONTROL_ID) ?: "").toString()
        if (controlId.isNotEmpty()) {
            bundle.identifier = Identifier().apply {
                system = "urn:ietf:rfc:3986"
                value = "urn:hl7-controlId:$controlId"
            }
        }
        val response = hapiClient.transaction().withBundle(bundle).execute()
        val resourceCount = response.entry?.size ?: 0
        exchange.setProperty(PROP_RESOURCE_COUNT, resourceCount)
    }

    // -- ACK helper ------------------------------------------------------

    private fun setAckProperty(exchange: Exchange, code: AcknowledgmentCode) {
        val inbound = exchange.message.body as? Message
            ?: exchange.getProperty(PROP_HL7_MESSAGE) as? Message
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
        const val ROUTE_TRANSFORM_ADT_A01 = "transform-adt-a01"

        // Header names — Camel headers travel with the Exchange and outlive
        // each processor, so logs + ACK generation can read them without
        // re-parsing the HL7 message.
        const val HDR_MESSAGE_TYPE = "hl7.messageType"
        const val HDR_CONTROL_ID = "hl7.controlId"
        const val HDR_SENDING_APP = "hl7.sendingApp"
        const val HDR_MATCHBOX_URL = "matchbox.url"

        // Exchange properties — internal to the route, not exposed in logs.
        const val PROP_HL7_MESSAGE = "hl7.message"
        const val PROP_RESOURCE_COUNT = "hapi.resourceCount"
        const val STAGE = "stage"
    }
}
