package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import ca.uhn.hl7v2.DefaultHapiContext
import ca.uhn.hl7v2.HapiContext
import ca.uhn.hl7v2.parser.PipeParser
import ca.uhn.hl7v2.util.Terser
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import java.time.Instant
import java.util.UUID

/**
 * Bytes → [PipelineMessage] converter for HL7 v2 ER7 (pipe-delimited)
 * messages.
 *
 * Lives at the boundary between the Camel MLLP component (which hands us
 * raw bytes off the socket) and the plugins-spi's [PipelineMessage]
 * canonical envelope. The parser intentionally does the minimum needed to
 * populate the SPI's required fields:
 *
 *   - parse the inbound v2 message with HAPI's `PipeParser`
 *   - pull MSH-3 / MSH-9 / MSH-10 into the right SPI slots
 *   - mirror those into the per-message attributes map (the SPI's
 *     convention for "everything else the ingest knows")
 *   - generate a UUID v4 correlation id (HL7 v2 has no on-wire
 *     correlation header)
 *
 * It does NOT:
 *
 *   - re-encode the parsed Message back to bytes. The SPI's `raw` field
 *     is contractually "the bytes as they came off the wire" — preserving
 *     the inbound buffer verbatim is what the downstream replay path
 *     wants.
 *   - decide what to do on parse failure. The route's onException handler
 *     owns AE ACK semantics; we just let the parser exception bubble.
 *   - persist anything. That's the registry-side callback's job.
 *
 * ## HAPI parsing knobs
 *
 * Defaults across the board. The legacy receive path used Camel's
 * `.unmarshal().hl7()` DSL, which delegates to a default HapiContext
 * exactly like the one constructed here. Diverging from defaults (e.g.
 * turning validation off) would change which inbound traffic is accepted
 * and violate ticket #431's "no behavioural changes" mandate.
 *
 * ## Thread safety
 *
 * The HAPI `PipeParser` instance is reused across [parse] calls — it's
 * documented as thread-safe by HAPI. We hold one [HapiContext] for the
 * lifetime of the parser; constructing a fresh context per message is
 * expensive enough to show up under load.
 */
class Hl7V2MessageParser {

    // Default HapiContext: matches what Camel's `.unmarshal().hl7()` DSL
    // configures internally, so a message that round-trips through the
    // legacy IngestRoutes will parse identically here. PipeParser
    // instances obtained from a HapiContext are documented as thread-
    // safe; we hold one for the parser's lifetime.
    private val hapiContext: HapiContext = DefaultHapiContext()
    private val pipeParser: PipeParser = hapiContext.pipeParser

    /**
     * Parse [raw] into a [PipelineMessage]. The caller passes the
     * exact bytes Camel pulled off the MLLP socket (after MLLP framing
     * has been stripped — that's the MLLP component's job, not ours).
     *
     * Throws if [raw] isn't parseable as v2 ER7. The route handles the
     * exception by replying AE on the wire.
     */
    fun parse(raw: ByteArray): PipelineMessage {
        // HAPI's PipeParser takes a String. We decode with the platform
        // default for now (UTF-8 in our container images); a future
        // story may surface a per-listener charset override on the
        // Hl7V2MllpProperties, but the legacy code didn't expose one
        // either.
        val text = raw.toString(Charsets.UTF_8)
        val message = pipeParser.parse(text)
        val terser = Terser(message)

        // The composed MSH-9 form is what the downstream worker keys on;
        // see IngestRoutes.extractHeaders() for the legacy behaviour we
        // need to preserve. MSH-9-1 alone is "ADT"; combined with MSH-9-2
        // ("A04") it becomes "ADT_A04" — the v2-to-FHIR StructureMap key.
        val msgType = safeTerse(terser, "/MSH-9-1")
        val triggerEvent = safeTerse(terser, "/MSH-9-2")
        val composedType =
            if (triggerEvent.isNotEmpty()) "${msgType}_$triggerEvent" else msgType

        val sendingApp = safeTerse(terser, "/MSH-3")
        val controlId = safeTerse(terser, "/MSH-10")

        return PipelineMessage(
            // HL7 v2 has no transport-level correlation header. Generate
            // a fresh UUID v4 per message — same as
            // CorrelationId.generate() does in the legacy receive path.
            correlationId = UUID.randomUUID().toString(),
            receivedAt = Instant.now(),
            sourceProtocol = SOURCE_PROTOCOL,
            sourceSystem = sendingApp,
            sourceId = controlId,
            // Verbatim wire bytes. The SPI's PipelineMessage.raw KDoc:
            // "the unmodified message bytes as they arrived" — we honour
            // that here, NOT re-encoding through HAPI. The HL7 v2 parser
            // is forgiving enough that re-encoding can change whitespace
            // / escape encoding subtly; for replay correctness we keep
            // the original buffer.
            raw = raw,
            contentType = CONTENT_TYPE,
            // Namespaced attribute keys per the SPI convention
            // (`hl7.controlId`, `hl7.messageType`, `hl7.sendingApp`).
            // Mirroring sourceSystem/sourceId here is intentional: it
            // makes the message self-describing even when downstream
            // code traffics in attributes instead of the canonical SPI
            // fields.
            attributes = mapOf(
                ATTR_MESSAGE_TYPE to composedType,
                ATTR_CONTROL_ID to controlId,
                ATTR_SENDING_APP to sendingApp,
            ),
        )
    }

    /**
     * `terser.get(path)` may throw or return null for absent fields. The
     * legacy route's `safe()` helper folds both into "" (empty string).
     * We match that here so a missing MSH-9-2 (a single-field message
     * type like "ACK") yields composedType="ACK" not "ACK_".
     */
    private fun safeTerse(terser: Terser, path: String): String =
        try {
            terser.get(path).orEmpty()
        } catch (_: Exception) {
            ""
        }

    companion object {
        /** Matches [Hl7V2MllpIngestSource.protocol]. */
        const val SOURCE_PROTOCOL: String = "hl7v2-mllp"

        /**
         * IANA-style media type stored on PipelineMessage.contentType.
         * Matches the legacy `IngestRoutes.RAW_CONTENT_TYPE_HL7V2`
         * constant so the persistence layer's `raw_content_type` column
         * gets the same value it always has.
         */
        const val CONTENT_TYPE: String = "application/hl7-v2"

        // Namespaced attribute keys — SPI convention.
        const val ATTR_MESSAGE_TYPE: String = "hl7.messageType"
        const val ATTR_CONTROL_ID: String = "hl7.controlId"
        const val ATTR_SENDING_APP: String = "hl7.sendingApp"
    }
}
