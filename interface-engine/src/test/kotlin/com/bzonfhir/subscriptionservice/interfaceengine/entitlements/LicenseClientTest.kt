package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.nimbusds.jose.JWSAlgorithm
import com.nimbusds.jose.JWSHeader
import com.nimbusds.jose.JWSObject
import com.nimbusds.jose.Payload
import com.nimbusds.jose.crypto.ECDSASigner
import com.nimbusds.jose.jwk.Curve
import com.nimbusds.jose.jwk.ECKey
import com.nimbusds.jose.jwk.JWKSet
import com.nimbusds.jose.jwk.gen.ECKeyGenerator
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.BeforeEach
import org.junit.jupiter.api.Test
import java.util.concurrent.TimeUnit

/**
 * License-client HTTP tests (ticket #460, Epic #428).
 *
 * Boots a [MockWebServer] in-process, registers the license server's
 * `/entitlements` and `/.well-known/jwks.json` endpoints, and exercises
 * the client against it. The signature verification step is exercised
 * end-to-end — the mock signs with a fresh ECKey, the JWKS endpoint
 * publishes its public half, and the client verifies the response.
 */
class LicenseClientTest {

    private lateinit var server: MockWebServer
    private val signingKey: ECKey = ECKeyGenerator(Curve.P_256)
        .keyID("test-key-1")
        .generate()
    private val signer = ECDSASigner(signingKey.toECPrivateKey())

    /** URL-aware mock — pulls /entitlements from one queue, /.well-known/jwks.json from another. */
    private val entitlementsQueue = ArrayDeque<MockResponse>()
    private val jwksQueue = ArrayDeque<MockResponse>()

    @BeforeEach
    fun setUp() {
        server = MockWebServer()
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                return when (request.path) {
                    "/entitlements" ->
                        entitlementsQueue.removeFirstOrNull()
                            ?: MockResponse().setResponseCode(500).setBody("no /entitlements response queued")
                    "/.well-known/jwks.json" -> {
                        // Default behaviour: serve a fresh JWKS from `signingKey` if nothing is enqueued.
                        // Most tests want this — the holder only re-fetches the JWKS once per hour.
                        jwksQueue.removeFirstOrNull() ?: defaultJwksResponse()
                    }
                    else -> MockResponse().setResponseCode(404).setBody("unknown path ${request.path}")
                }
            }
        }
        server.start()
    }

    @AfterEach
    fun tearDown() {
        server.shutdown()
    }

    private fun defaultJwksResponse(): MockResponse {
        val jwks = JWKSet(signingKey.toPublicJWK())
        return MockResponse()
            .setResponseCode(200)
            .setHeader("Content-Type", "application/json")
            .setBody(jwks.toString())
    }

    private fun serverUrl(): String = server.url("/").toString().trimEnd('/')

    private fun signBody(body: Map<String, Any?>): String {
        val header = JWSHeader.Builder(JWSAlgorithm.ES256).keyID(signingKey.keyID).build()
        val jws = JWSObject(header, Payload(body))
        jws.sign(signer)
        return jws.serialize()
    }

    private fun enqueueEntitlements(
        entitlements: List<String> = listOf("audit.export.iti20"),
        tierName: String = "pro",
        expiresAt: String = "2027-06-28T00:00:00Z",
    ): String {
        val signature = signBody(
            mapOf(
                "entitlements" to entitlements,
                "expiresAt" to expiresAt,
                "tierName" to tierName,
            )
        )
        val body = """{"entitlements":${entitlements.joinToString(",", "[", "]") { "\"$it\"" }},
                      "expiresAt":"$expiresAt",
                      "tierName":"$tierName",
                      "signature":"$signature"}""".trimIndent()
        entitlementsQueue.addLast(
            MockResponse()
                .setResponseCode(200)
                .setHeader("Content-Type", "application/json")
                .setBody(body)
        )
        return signature
    }

    private fun enqueueRawEntitlements(response: MockResponse) {
        entitlementsQueue.addLast(response)
    }

    @Test
    fun `posts licenseKey and productSlug and parses a verified response`() {
        enqueueEntitlements(entitlements = listOf("audit.export.iti20"))

        val client = LicenseClient(serverUrl())
        val response = client.fetchEntitlements("license-abc")

        assertThat(response.entitlements).containsExactly("audit.export.iti20")
        assertThat(response.tierName).isEqualTo("pro")
        assertThat(response.signatureKid).isEqualTo("test-key-1")

        // Drain the recorded requests and assert the entitlements POST
        // carries the right body. The JWKS GET happens too (lazy-loaded
        // by the verifier), order is implementation-defined.
        val recorded = generateSequence { server.takeRequest(2, TimeUnit.SECONDS) }.toList()
        val entitlementsRequest = recorded.first { it.path == "/entitlements" }
        assertThat(entitlementsRequest.method).isEqualTo("POST")
        val bodyJson = entitlementsRequest.body.readUtf8()
        assertThat(bodyJson).contains("\"licenseKey\":\"license-abc\"")
        assertThat(bodyJson).contains("\"productSlug\":\"subscription-service\"")
        assertThat(recorded.map { it.path }).contains("/.well-known/jwks.json")
    }

    @Test
    fun `throws on non-2xx response`() {
        enqueueRawEntitlements(MockResponse().setResponseCode(401).setBody("""{"error":"invalid_license"}"""))

        val client = LicenseClient(serverUrl())

        assertThatThrownBy { client.fetchEntitlements("invalid-key") }
            .isInstanceOf(LicenseRevokedException::class.java)
    }

    @Test
    fun `throws on malformed JSON body`() {
        enqueueRawEntitlements(MockResponse().setResponseCode(200).setBody("not json"))

        val client = LicenseClient(serverUrl())

        assertThatThrownBy { client.fetchEntitlements("license-abc") }
            .isInstanceOf(LicenseClientException::class.java)
    }

    @Test
    fun `throws when the signature does not verify`() {
        // Hand-craft a response whose signature payload is signed by a
        // DIFFERENT key — that's the same failure shape as a tampered
        // signature in transit.
        val otherKey = ECKeyGenerator(Curve.P_256).keyID("test-key-1").generate()
        val otherSigner = ECDSASigner(otherKey.toECPrivateKey())
        val header = JWSHeader.Builder(JWSAlgorithm.ES256).keyID("test-key-1").build()
        val jws = JWSObject(header, Payload(mapOf("entitlements" to listOf("a"))))
        jws.sign(otherSigner)
        val badSig = jws.serialize()

        val body = """{"entitlements":["a"],"expiresAt":"2027-06-28T00:00:00Z","tierName":"pro","signature":"$badSig"}"""
        enqueueRawEntitlements(
            MockResponse()
                .setResponseCode(200)
                .setHeader("Content-Type", "application/json")
                .setBody(body)
        )

        val client = LicenseClient(serverUrl())

        assertThatThrownBy { client.fetchEntitlements("license-abc") }
            .isInstanceOf(LicenseClientException::class.java)
            .hasMessageContaining("signature")
    }

    @Test
    fun `caches the JWKS across calls`() {
        enqueueEntitlements()
        enqueueEntitlements()

        val client = LicenseClient(serverUrl())
        client.fetchEntitlements("license-abc")
        client.fetchEntitlements("license-abc")

        val recorded = generateSequence { server.takeRequest(2, TimeUnit.SECONDS) }.toList()
        val jwksHits = recorded.count { it.path == "/.well-known/jwks.json" }
        val entitlementsHits = recorded.count { it.path == "/entitlements" }
        assertThat(entitlementsHits).isEqualTo(2)
        // The point of the cache: only one JWKS fetch even though we made
        // two entitlement calls inside the 1h TTL window.
        assertThat(jwksHits).isEqualTo(1)
    }
}
