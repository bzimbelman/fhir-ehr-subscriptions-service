package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config.Hl7V2MllpProperties
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import org.apache.camel.impl.DefaultCamelContext
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Test
import java.net.ServerSocket
import java.net.Socket
import java.time.Duration
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicInteger

/**
 * Wire-level integration test for [Hl7V2MllpIngestSource]
 * (ticket #431, validation step 3 of the TDD plan).
 *
 * Boots a standalone Camel context with the plugin's route, opens a
 * raw TCP socket to it, frames an HL7 v2 message in MLLP block markers
 * (0x0B / 0x1C 0x0D — the same bytes `nc` would write), and verifies:
 *
 *   - the SPI callback fires once per message,
 *   - the [PipelineMessage] handed to the callback matches the wire
 *     data field-for-field,
 *   - an AA ACK comes back on the socket echoing MSH-10.
 *
 * Why we don't reuse the interface-engine's IngestRoutesTest as the
 * proof: that test boots the full Spring + JPA + Camel context to
 * verify the persist path. THIS test stays inside the plugin module,
 * boots only a Camel context, and is the boundary check on the SPI
 * shape — the plugin's job is to produce PipelineMessages, and we
 * verify it does. The interface-engine still has its own IngestRoutesTest
 * (kept green per ticket #431's "must keep green" list) that verifies
 * the host-side wiring through the SPI callback.
 *
 * Test runs in a few hundred milliseconds — no Postgres, no Spring, no
 * Testcontainers. The MLLP component boots its listen socket directly.
 */
class Hl7V2MllpEndToEndTest {

    companion object {
        // MLLP framing per HL7 v2 spec.
        private const val SB: Byte = 0x0B  // start block
        private const val EB: Byte = 0x1C  // end block
        private const val CR: Byte = 0x0D  // carriage return
    }

    private val camelContext = DefaultCamelContext()
    private var ingestSource: Hl7V2MllpIngestSource? = null
    private val mllpPort: Int = ServerSocket(0).use { it.localPort }

    @AfterEach
    fun tearDown() {
        ingestSource?.stop()
        if (camelContext.isStarted) {
            camelContext.stop()
        }
    }

    private fun start(callback: (PipelineMessage) -> Unit): Hl7V2MllpIngestSource {
        // Construct the source with the random free port we picked.
        val props = Hl7V2MllpProperties(port = mllpPort)
        val source = Hl7V2MllpIngestSource(props, camelContext).also { ingestSource = it }
        camelContext.start()
        source.start(callback)
        // Wait for the route to enter Started state — addRoutes() is
        // synchronous but route startup itself happens asynchronously
        // on Camel's executor. Without the wait, sendMllp() can race
        // the listener and the socket connect hits a closed port.
        await().atMost(Duration.ofSeconds(10)).until {
            val route = camelContext.routeController.getRouteStatus(Hl7V2MllpIngestSource.ROUTE_ID)
            route != null && route.isStarted
        }
        return source
    }

    /**
     * Build an ADT^A04 v2.5 message identical to the one the legacy
     * IngestRoutesTest uses. Reusing this shape ensures the plugin is
     * a behaviour-preserving rewrite — same bytes in, same fields out.
     */
    private fun adtA04(
        controlId: String = "MSGCTRL00001",
        sendingApp: String = "EPIC",
    ): String =
        listOf(
            "MSH|^~\\&|$sendingApp|HOSP|RECEIVER|CDS|20260625120000||ADT^A04|$controlId|P|2.5",
            "EVN|A04|20260625120000",
            "PID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M|||123 MAIN ST^^ANYTOWN^CA^94000",
            "PV1|1|O|2000^2012^01||||NPI001^WELBY^MARCUS|||AMB||||REG|A0",
        ).joinToString("\r") + "\r"

    /**
     * Open a TCP socket, frame and send `payload` per MLLP, read the
     * response until the EB marker. Returns the ACK body (everything
     * between the SB and EB bytes).
     */
    private fun sendMllp(payload: String): String {
        Socket("localhost", mllpPort).use { socket ->
            socket.soTimeout = 10_000
            val out = socket.getOutputStream()
            out.write(byteArrayOf(SB))
            out.write(payload.toByteArray(Charsets.UTF_8))
            out.write(byteArrayOf(EB, CR))
            out.flush()

            val buf = ByteArray(8192)
            val sink = StringBuilder()
            val input = socket.getInputStream()
            while (true) {
                val n = input.read(buf)
                if (n <= 0) break
                for (i in 0 until n) {
                    val b = buf[i]
                    when (b) {
                        SB -> { /* drop start block */ }
                        EB -> return sink.toString()
                        else -> sink.append(b.toInt().toChar())
                    }
                }
            }
            return sink.toString()
        }
    }

    @Test
    fun `route fires callback with a PipelineMessage matching the wire data`() {
        val captured = ConcurrentLinkedQueue<PipelineMessage>()

        start { msg -> captured.add(msg) }

        val controlId = "WIRE001"
        val ack = sendMllp(adtA04(controlId = controlId, sendingApp = "EPIC"))

        // ACK assertions: AA echoing our control id, message type ACK^A04.
        assertThat(ack)
            .describedAs("ACK should be AA echoing MSH-10")
            .contains("MSA|AA|$controlId")
            .contains("ACK^A04")

        // Wait for the callback to fire — the route processes the
        // exchange asynchronously after the socket write returns. Most
        // of the time it's there before we get the ACK back, but we
        // poll briefly to be safe under loaded CI.
        await().atMost(Duration.ofSeconds(5)).until { captured.isNotEmpty() }

        assertThat(captured)
            .describedAs("callback should fire exactly once for one MLLP message")
            .hasSize(1)

        val msg = captured.poll()
        assertThat(msg.sourceProtocol).isEqualTo("hl7v2-mllp")
        assertThat(msg.sourceSystem).isEqualTo("EPIC")
        assertThat(msg.sourceId).isEqualTo(controlId)
        assertThat(msg.contentType).isEqualTo("application/hl7-v2")
        assertThat(msg.attributes)
            .containsEntry("hl7.messageType", "ADT_A04")
            .containsEntry("hl7.controlId", controlId)
            .containsEntry("hl7.sendingApp", "EPIC")
        assertThat(msg.correlationId).matches("[0-9a-fA-F-]{36}")
        // raw bytes preserved verbatim — wire payload that came off the
        // socket, minus MLLP framing (which the MLLP component strips
        // before delivering to the route).
        assertThat(String(msg.raw, Charsets.UTF_8))
            .startsWith("MSH|")
            .contains("ADT^A04")
            .contains(controlId)
    }

    @Test
    fun `route ACKs AE when the callback throws`() {
        val attempts = AtomicInteger(0)

        start { _ ->
            attempts.incrementAndGet()
            // Simulate the host's persist call going boom (e.g. DB
            // unreachable). The route's onException handler should
            // convert this into an AE ACK on the wire.
            throw RuntimeException("simulated DB outage")
        }

        val ack = sendMllp(adtA04(controlId = "FAIL001", sendingApp = "EPIC"))

        assertThat(ack)
            .describedAs("callback failure should ACK AE")
            .contains("MSA|AE|FAIL001")

        // The callback ran exactly once before throwing.
        assertThat(attempts.get()).isEqualTo(1)
    }

    @Test
    fun `route ACKs AA for two distinct messages and callback sees both`() {
        val captured = ConcurrentLinkedQueue<PipelineMessage>()

        start { msg -> captured.add(msg) }

        val ack1 = sendMllp(adtA04(controlId = "MULTI001"))
        val ack2 = sendMllp(adtA04(controlId = "MULTI002"))

        assertThat(ack1).contains("MSA|AA|MULTI001")
        assertThat(ack2).contains("MSA|AA|MULTI002")

        await().atMost(Duration.ofSeconds(5)).until { captured.size == 2 }
        assertThat(captured.map { it.sourceId }).containsExactlyInAnyOrder("MULTI001", "MULTI002")
    }
}
