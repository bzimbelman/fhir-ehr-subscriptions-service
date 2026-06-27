package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test

/**
 * Contract tests for [Hl7V2MessageParser] — the wire-bytes-to-PipelineMessage
 * converter at the heart of the HL7 v2 MLLP ingest plugin (ticket #431).
 *
 * The parser is the ONLY piece in this plugin that the SPI's
 * `PipelineMessage` contract gets surfaced through. Everything else
 * (Camel route, MLLP framing, ACK) feeds bytes here and gets back a fully
 * populated PipelineMessage that conforms to the plugins-spi contract.
 *
 * Why this is the first test we write:
 *
 *   - It's pure (bytes in → value object out, no Spring, no Camel, no
 *     sockets), so the red→green cycle takes milliseconds.
 *   - It pins the field-by-field mapping between MSH-* and the SPI's
 *     PipelineMessage shape — the SPI's `PipelineMessage` KDoc says
 *     `sourceSystem = MSH-3`, `sourceId = MSH-10`, `attributes["hl7.controlId"]`,
 *     etc. Getting those right is the contract; the route is just plumbing
 *     on top.
 *   - If the parser is wrong, no amount of route-level integration tests
 *     would surface it cleanly — downstream code would see "right shape,
 *     wrong contents" failures.
 */
class Hl7V2MessageParserTest {

    private val parser = Hl7V2MessageParser()

    /**
     * Build an ADT^A04 v2.5 message matching the shape the existing
     * `IngestRoutesTest` uses (MSH segment fields are identical). We use
     * `\r` segment terminators per the HL7 v2 spec — `\n` would break
     * HAPI's parser.
     */
    private fun adtA04(
        controlId: String = "MSGCTRL00001",
        sendingApp: String = "EPIC",
    ): ByteArray =
        listOf(
            "MSH|^~\\&|$sendingApp|HOSP|RECEIVER|CDS|20260625120000||ADT^A04|$controlId|P|2.5",
            "EVN|A04|20260625120000",
            "PID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M",
            "PV1|1|O|2000^2012^01",
        ).joinToString("\r").let { it + "\r" }.toByteArray(Charsets.UTF_8)

    @Test
    fun `parser extracts MSH-3 into sourceSystem`() {
        val msg = parser.parse(adtA04(sendingApp = "EPIC"))

        assertThat(msg.sourceSystem)
            .describedAs("sourceSystem must be MSH-3 (sending application)")
            .isEqualTo("EPIC")
    }

    @Test
    fun `parser extracts MSH-10 into sourceId`() {
        val msg = parser.parse(adtA04(controlId = "MSGCTRL12345"))

        assertThat(msg.sourceId)
            .describedAs("sourceId must be MSH-10 (message control id)")
            .isEqualTo("MSGCTRL12345")
    }

    @Test
    fun `parser composes MSH-9 into message type attribute`() {
        // The interface engine stores the composed message type
        // (`MSGTYPE_TRIGGER`, e.g. ADT_A04) on the row's message_type
        // column. That same composed form needs to flow through here so
        // downstream code (worker, admin UI) sees identical values to
        // before #431.
        val msg = parser.parse(adtA04())

        assertThat(msg.attributes)
            .describedAs("hl7.messageType should be the composed MSH-9-1 _ MSH-9-2")
            .containsEntry("hl7.messageType", "ADT_A04")
    }

    @Test
    fun `parser surfaces control id and sending app on attributes too`() {
        // Attributes are the SPI-defined per-protocol metadata bag. The
        // SPI's KDoc explicitly calls out `hl7.controlId` and
        // `hl7.sendingApp` as conventional namespaced keys. Even though
        // we already have these on sourceSystem/sourceId, mirroring them
        // into attributes keeps the SPI's PipelineMessage self-describing
        // for ingests that aren't HL7 v2 (where sourceSystem != MSH-3).
        val msg = parser.parse(adtA04(controlId = "ABC", sendingApp = "EPIC"))

        assertThat(msg.attributes)
            .containsEntry("hl7.controlId", "ABC")
            .containsEntry("hl7.sendingApp", "EPIC")
    }

    @Test
    fun `parser sets sourceProtocol to hl7v2-mllp`() {
        val msg = parser.parse(adtA04())

        assertThat(msg.sourceProtocol)
            .describedAs("sourceProtocol must match Hl7V2MllpIngestSource.protocol")
            .isEqualTo("hl7v2-mllp")
    }

    @Test
    fun `parser sets contentType to application slash hl7-v2`() {
        // The interface-engine persistence layer uses this exact string
        // in the `raw_content_type` column (see IngestRoutes.RAW_CONTENT_TYPE_HL7V2
        // in the legacy module). The async worker keys off it to pick
        // the HL7 v2 → FHIR mapping pipeline. We pin the value here so a
        // typo (`application/hl7v2`, `application/x-hl7`) can't slip in
        // without the test catching it.
        val msg = parser.parse(adtA04())

        assertThat(msg.contentType).isEqualTo("application/hl7-v2")
    }

    @Test
    fun `parser populates correlationId with a UUID-shaped string`() {
        // HL7 v2 carries no correlation header on the wire — the SPI
        // says the source should generate one when no upstream id
        // exists. UUID v4 is what the existing IngestRoutes does today
        // (via CorrelationId.generate()), so the plugin matches that
        // shape.
        val msg = parser.parse(adtA04())

        assertThat(msg.correlationId)
            .describedAs("correlationId should be a generated UUID v4 string")
            .matches("[0-9a-fA-F-]{36}")
    }

    @Test
    fun `parser preserves the original wire bytes on raw`() {
        // PipelineMessage.raw is contractually "the bytes as they came
        // off the wire, unmodified" (see PipelineMessage KDoc). For a
        // parser that re-encodes via HAPI the round-trip is byte-equal
        // for well-formed input, so we check both:
        //
        //   1. raw is not null / not empty,
        //   2. raw starts with MSH| (i.e. the encoded HL7 message),
        //   3. raw is byte-equal to the input we passed in.
        //
        // Choosing (3) means we DON'T re-encode through HAPI; we keep
        // the wire bytes verbatim and only PARSE via HAPI to extract
        // headers. That's a cleaner contract than the legacy IngestRoutes
        // which stored the re-encoded form — but it matches the SPI's
        // explicit "raw bytes as they came off the wire" requirement.
        val input = adtA04()

        val msg = parser.parse(input)

        assertThat(msg.raw)
            .describedAs("raw must be the exact bytes parser saw")
            .isEqualTo(input)
    }

    @Test
    fun `parser sets receivedAt to roughly now`() {
        val before = java.time.Instant.now()
        val msg = parser.parse(adtA04())
        val after = java.time.Instant.now()

        // Loose bound — receivedAt is wall-clock, just sanity-check it's
        // in [before, after] (with a 1s slop for clock granularity).
        assertThat(msg.receivedAt)
            .isAfterOrEqualTo(before.minusSeconds(1))
            .isBeforeOrEqualTo(after.plusSeconds(1))
    }

    @Test
    fun `parser throws on unparseable bytes`() {
        // Garbage in → exception out. The route's onException handler
        // converts this into AE ACK at the wire boundary. The parser
        // itself just propagates the parse error; deciding what to do
        // with it isn't its job.
        val garbage = "this is not an HL7 message".toByteArray()

        org.junit.jupiter.api.Assertions.assertThrows(Exception::class.java) {
            parser.parse(garbage)
        }
    }
}
