package com.bzonfhir.subscription.routes

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.gclient.ITransaction
import ca.uhn.fhir.rest.gclient.ITransactionTyped
import org.apache.camel.CamelContext
import org.apache.camel.Exchange
import org.apache.camel.ProducerTemplate
import org.apache.camel.builder.AdviceWith
import org.apache.camel.builder.AdviceWithRouteBuilder
import org.apache.camel.support.DefaultExchange
import org.apache.camel.test.spring.junit5.UseAdviceWith
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.Bundle
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import org.mockito.kotlin.any
import org.mockito.kotlin.eq
import org.mockito.kotlin.mock
import org.mockito.kotlin.reset
import org.mockito.kotlin.times
import org.mockito.kotlin.verify
import org.mockito.kotlin.whenever
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.context.TestConfiguration
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Primary
import org.springframework.test.annotation.DirtiesContext
import org.springframework.test.context.DynamicPropertyRegistry
import org.springframework.test.context.DynamicPropertySource
import java.net.ServerSocket
import java.nio.charset.StandardCharsets

/**
 * Tests for the ADT^A01 transform sub-route (`direct:transform-adt-a01`).
 *
 * Strategy:
 *   - `@UseAdviceWith` defers Camel startup so we can rewrite routes before
 *     they start. We replace the live `to("http://...")` (Matchbox) with a
 *     `mock://matchbox` so the test never touches the network.
 *   - The HAPI client is a Mockito mock injected via a @TestConfiguration
 *     that takes precedence over the real bean from FhirConfig.
 *   - We send a HAPI HL7v2 Message (parsed from the canonical ADT^A01
 *     fixture in IngestRoutesTest) directly to the `direct:` endpoint with
 *     a ProducerTemplate. That skips the MLLP listener and lets us focus
 *     the assertions on the transform → HAPI hand-off.
 */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.NONE)
@UseAdviceWith
// Each test method gets a fresh Spring context (and therefore a fresh Camel
// context + fresh route definitions). Without this the second test's call to
// AdviceWith.weaveByToUri("http://placeholder*") doesn't find a match — the
// first test already replaced that endpoint with mock://matchbox.
@DirtiesContext(classMode = DirtiesContext.ClassMode.BEFORE_EACH_TEST_METHOD)
class TransformRouteTest {

    @Autowired
    private lateinit var camelContext: CamelContext

    @Autowired
    private lateinit var producerTemplate: ProducerTemplate

    @Autowired
    private lateinit var hapiClient: IGenericClient

    @Autowired
    private lateinit var fhirContext: FhirContext

    @TestConfiguration
    class HapiClientMockConfig {
        @Bean
        @Primary
        fun mockHapiClient(): IGenericClient = mock()
    }

    companion object {
        @JvmStatic
        @DynamicPropertySource
        fun overrideMllpPort(registry: DynamicPropertyRegistry) {
            // Bind the MLLP listener to a free port so this test class can run
            // in the same JVM as IngestRoutesTest without port collisions.
            val freePort = ServerSocket(0).use { it.localPort }
            registry.add("subscription-service.mllp.port") { freePort }
        }
    }

    @BeforeEach
    fun setUp() {
        reset(hapiClient)
    }

    @Test
    fun `successful matchbox + hapi roundtrip ACKs AA and posts bundle`() {
        val bundleJson = TransformRouteTest::class.java
            .getResourceAsStream("/fixtures/adt-a01-bundle.json")!!
            .readBytes()
            .toString(StandardCharsets.UTF_8)

        stubMatchboxReturning(status = 200, body = bundleJson)
        stubHapiClientReturningEmptyResponse()

        camelContext.start()

        val exchange = sendAdtA01("MSGCTRL00001")

        // Stage advanced past matchbox and hapi to the final processor.
        assertThat(exchange.getProperty(IngestRoutes.STAGE)).isEqualTo("hapi")

        // Matchbox got hit exactly once. The headers we set in
        // prepareMatchboxCall() should be on the in-flight exchange.
        val matchboxMock = camelContext.getEndpoint(
            "mock://matchbox",
            org.apache.camel.component.mock.MockEndpoint::class.java,
        )
        matchboxMock.assertIsSatisfied()
        val received = matchboxMock.receivedExchanges.single()
        assertThat(received.message.getHeader(IngestRoutes.HDR_MATCHBOX_URL).toString())
            .contains("/StructureMap/\$transform")
            .contains("source=")
        assertThat(received.message.getHeader(Exchange.CONTENT_TYPE)).isEqualTo("x-application/hl7-v2+er7")

        // HAPI client got a transaction with our Bundle. The stub captures
        // the bundle inside withBundle(); assert on its idempotency marker
        // and entry count.
        verify(hapiClient).transaction()
        val captured = capturedBundle
        assertThat(captured).isNotNull
        assertThat(captured!!.identifier.value).isEqualTo("urn:hl7-controlId:MSGCTRL00001")
        assertThat(captured.identifier.system).isEqualTo("urn:ietf:rfc:3986")
        assertThat(captured.entry).hasSize(2)

        // Final ACK is AA.
        val ackProp = exchange.getProperty("CamelMllpAcknowledgementString").toString()
        assertThat(ackProp).contains("MSA|AA|MSGCTRL00001")
    }

    @Test
    fun `matchbox 500 leads to AE ACK and no HAPI call`() {
        stubMatchboxReturning(status = 500, body = """{"resourceType":"OperationOutcome"}""")
        stubHapiClientReturningEmptyResponse()

        camelContext.start()

        val exchange = sendAdtA01("MSGCTRL00002")

        val ackProp = exchange.getProperty("CamelMllpAcknowledgementString").toString()
        assertThat(ackProp).contains("MSA|AE|MSGCTRL00002")
        verify(hapiClient, times(0)).transaction()
        assertThat(exchange.getProperty(IngestRoutes.STAGE)).isEqualTo("matchbox")
    }

    @Test
    fun `hapi failure leads to AE ACK`() {
        val bundleJson = TransformRouteTest::class.java
            .getResourceAsStream("/fixtures/adt-a01-bundle.json")!!
            .readBytes()
            .toString(StandardCharsets.UTF_8)
        stubMatchboxReturning(status = 200, body = bundleJson)
        // FhirClientConnectionException is a HAPI-FHIR unchecked exception that
        // wraps connection-level failures. IGenericClient.execute() declares no
        // checked exceptions, so a plain IOException would be rejected by
        // Mockito. This is the actual exception HAPI throws when HAPI FHIR
        // can't talk to its target server.
        stubHapiClientThrowing(
            ca.uhn.fhir.rest.client.exceptions.FhirClientConnectionException("connection refused"),
        )

        camelContext.start()

        val exchange = sendAdtA01("MSGCTRL00003")

        val ackProp = exchange.getProperty("CamelMllpAcknowledgementString").toString()
        assertThat(ackProp).contains("MSA|AE|MSGCTRL00003")
        assertThat(exchange.getProperty(IngestRoutes.STAGE)).isEqualTo("hapi")
    }

    // -- Test helpers ----------------------------------------------------

    private fun sendAdtA01(controlId: String): Exchange {
        val adtA01 = listOf(
            "MSH|^~\\&|EPIC|HOSP|RECEIVER|CDS|20260625120000||ADT^A01|$controlId|P|2.5",
            "EVN|A01|20260625120000",
            "PID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M",
            "PV1|1|I|2000^2012^01",
        ).joinToString("\r") + "\r"

        // Parse to a HAPI Message — the route expects that body type because
        // the MLLP listener already runs `.unmarshal().hl7()` upstream.
        val parser = ca.uhn.hl7v2.DefaultHapiContext().genericParser
        val message = parser.parse(adtA01)

        val exchange: Exchange = DefaultExchange(camelContext)
        exchange.message.body = message
        // The MLLP listener extracts these headers via extractHeaders(); the
        // direct: entry-point skips that processor, so we mirror what would
        // be set if a real MLLP message had arrived.
        exchange.message.setHeader(IngestRoutes.HDR_MESSAGE_TYPE, "ADT_A01")
        exchange.message.setHeader(IngestRoutes.HDR_CONTROL_ID, controlId)
        exchange.message.setHeader(IngestRoutes.HDR_SENDING_APP, "EPIC")
        return producerTemplate.send("direct:transform-adt-a01", exchange)
    }

    private fun stubMatchboxReturning(status: Int, body: String) {
        // Replace the live HTTP call with a mock endpoint. After the mock
        // runs we install a small processor that drops the configured
        // status + body onto the exchange in the shape camel-http would
        // have produced. For non-2xx the processor throws
        // HttpOperationFailedException, which is exactly what camel-http
        // would throw, so the onException handler kicks in.
        AdviceWith.adviceWith(
            camelContext,
            IngestRoutes.ROUTE_TRANSFORM_ADT_A01,
        ) { rb: AdviceWithRouteBuilder ->
            // toD() registers as a "toD[$header.matchbox.url}...]" step.
            // We match by URI prefix; the trailing wildcard catches the
            // toD query string options.
            rb.weaveByToUri("\${header.matchbox.url}*").replace()
                .to("mock://matchbox")
                .process { exchange: Exchange ->
                    if (status >= 400) {
                        val uri = (exchange.message.getHeader(IngestRoutes.HDR_MATCHBOX_URL) ?: "").toString()
                        throw org.apache.camel.http.base.HttpOperationFailedException(
                            uri,
                            status,
                            "stubbed",
                            null,
                            emptyMap(),
                            body,
                        )
                    }
                    exchange.message.body = body
                    exchange.message.setHeader(Exchange.HTTP_RESPONSE_CODE, status)
                    exchange.message.setHeader(Exchange.CONTENT_TYPE, "application/fhir+json")
                }
        }
    }

    private val capturedBundles = mutableListOf<Bundle>()
    private val capturedBundle: Bundle?
        get() = capturedBundles.lastOrNull()

    private fun stubHapiClientReturningEmptyResponse() {
        val transactionApi = mock<ITransaction>()
        val typed = mock<ITransactionTyped<Bundle>>()
        whenever(hapiClient.transaction()).thenReturn(transactionApi)
        whenever(transactionApi.withBundle(any<Bundle>())).thenAnswer { invocation ->
            capturedBundles.add(invocation.arguments[0] as Bundle)
            typed
        }
        whenever(typed.execute()).thenReturn(Bundle().apply {
            type = Bundle.BundleType.TRANSACTIONRESPONSE
        })
    }

    private fun stubHapiClientThrowing(ex: Throwable) {
        val transactionApi = mock<ITransaction>()
        val typed = mock<ITransactionTyped<Bundle>>()
        whenever(hapiClient.transaction()).thenReturn(transactionApi)
        whenever(transactionApi.withBundle(any<Bundle>())).thenReturn(typed)
        whenever(typed.execute()).thenThrow(ex)
    }
}
