package com.bzonfhir.subscription.routes

import org.apache.camel.CamelContext
import org.assertj.core.api.Assertions.assertThat
import org.awaitility.Awaitility.await
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import java.net.ServerSocket
import java.net.Socket
import java.time.Duration

/**
 * End-to-end MLLP round-trip:
 *   1. Spring Boot brings up the Camel context with the IngestRoutes route.
 *   2. We bind it to a free port (so parallel test runs don't collide).
 *   3. We open a raw TCP socket and write an ADT^A01 framed with MLLP block
 *      markers (0x0B start, 0x1C 0x0D end) — exactly what `nc` would do.
 *   4. We assert the response back is an AA ACK with our MSH-10 control id.
 *
 * This is a deliberate choice over `producerTemplate.requestBody("mllp://...")`
 * because Camel's MLLP producer InOnly/InOut semantics complicate the assertion:
 * the producer can swallow the ACK into a header/property rather than returning
 * it as the result body, which makes a "what would nc see?" test brittle. The
 * raw socket approach mirrors the smoke-test step in the ticket exactly.
 */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.NONE)
class IngestRoutesTest {

    companion object {
        // MLLP framing bytes per HL7 v2 spec.
        private const val SB: Byte = 0x0B  // start block
        private const val EB: Byte = 0x1C  // end block
        private const val CR: Byte = 0x0D  // carriage return (segment + post-EB)

        // Pick a free port once per test class. Bound + immediately released so
        // Camel's MLLP consumer can grab it ~100ms later. Held in a JVM static so
        // both @DynamicPropertySource and the test method see the same value.
        @JvmStatic
        private val mllpPort: Int = ServerSocket(0).use { it.localPort }

        @JvmStatic
        @DynamicPropertySource
        fun properties(registry: DynamicPropertyRegistry) {
            registry.add("subscription-service.mllp.port") { mllpPort }
        }
    }

    @Autowired
    private lateinit var camelContext: CamelContext

    @Test
    fun `route accepts ADT_A01 and returns an AA ACK`() {
        // Wait for the route to actually be running (Camel starts asynchronously).
        await().atMost(Duration.ofSeconds(10)).until {
            camelContext.getRoute("mllp-ingest") != null &&
                camelContext.routeController.getRouteStatus("mllp-ingest").isStarted
        }

        // Canonical ADT^A01 from the HL7 v2.5 spec, slightly trimmed.
        // \r-delimited per MLLP convention.
        val adtA01 = listOf(
            "MSH|^~\\&|EPIC|HOSP|RECEIVER|CDS|20260625120000||ADT^A01|MSGCTRL00001|P|2.5",
            "EVN|A01|20260625120000",
            "PID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M|||123 MAIN ST^^ANYTOWN^CA^94000",
            "PV1|1|I|2000^2012^01||||NPI001^WELBY^MARCUS|||SUR||||ADM|A0",
        ).joinToString("\r") + "\r"

        val ackString = sendMllp(adtA01)

        assertThat(ackString)
            .describedAs("ACK should be MSA|AA|MSGCTRL00001 echoing our control id")
            .contains("MSA|AA|MSGCTRL00001")
            .contains("ACK^A01")
    }

    /**
     * Open a TCP socket to the MLLP listener, frame and send `payload`, read
     * the response until we see the MLLP end-block byte. Returns the ACK
     * body (everything between the start- and end-block markers).
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
                        EB -> return sink.toString()  // end block — done
                        else -> sink.append(b.toInt().toChar())
                    }
                }
            }
            return sink.toString()
        }
    }
}
