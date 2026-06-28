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
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.junit.jupiter.api.Test

/**
 * ES256 JWS signature verifier tests (ticket #460, Epic #428).
 *
 * Mirrors the UI's `signatureVerifier.ts` contract: a compact JWS produced
 * by the license server's `SigningService.sign(...)` must verify against the
 * matching public JWKS. A tampered byte must fail. A mismatched JWKS must
 * fail. Anything else throws.
 */
class JwksVerifierTest {

    private val signingKey: ECKey = ECKeyGenerator(Curve.P_256)
        .keyID("test-key-1")
        .generate()
    private val signer = ECDSASigner(signingKey.toECPrivateKey())
    private val jwks: JWKSet = JWKSet(signingKey.toPublicJWK())

    private fun signPayload(payload: Map<String, Any?>, kid: String = signingKey.keyID): String {
        val header = JWSHeader.Builder(JWSAlgorithm.ES256).keyID(kid).build()
        val jws = JWSObject(header, Payload(payload))
        jws.sign(signer)
        return jws.serialize()
    }

    @Test
    fun `verifies a well-formed compact JWS against the matching JWKS`() {
        val verifier = JwksVerifier()
        val compact = signPayload(mapOf("entitlements" to listOf("audit.export.iti20")))

        val verified = verifier.verify(compact, jwks)

        assertThat(verified.kid).isEqualTo("test-key-1")
        // The payload survives the round-trip.
        val claims = JWSObject.parse(compact).payload.toJSONObject()
        assertThat(claims["entitlements"]).isEqualTo(listOf("audit.export.iti20"))
    }

    @Test
    fun `tampered signature fails verification`() {
        val verifier = JwksVerifier()
        val compact = signPayload(mapOf("entitlements" to listOf("a")))
        // Substitute the entire signature segment with a fresh ECDSA signature
        // over a DIFFERENT payload but the same alg/kid header — the sig is
        // well-formed (right length, valid base64) but won't match the
        // header/payload pair under test.
        val parts = compact.split(".")
        val otherCompact = signPayload(mapOf("entitlements" to listOf("z")))
        val otherSig = otherCompact.split(".")[2]
        val tampered = "${parts[0]}.${parts[1]}.$otherSig"

        assertThatThrownBy { verifier.verify(tampered, jwks) }
            .isInstanceOf(JwksVerificationException::class.java)
    }

    @Test
    fun `tampered payload fails verification`() {
        val verifier = JwksVerifier()
        val compact = signPayload(mapOf("entitlements" to listOf("a")))
        val parts = compact.split(".")
        // Replace the payload with a different base64url-encoded JSON object.
        val newPayload = java.util.Base64.getUrlEncoder().withoutPadding()
            .encodeToString("""{"entitlements":["b"]}""".toByteArray())
        val tampered = "${parts[0]}.$newPayload.${parts[2]}"

        assertThatThrownBy { verifier.verify(tampered, jwks) }
            .isInstanceOf(JwksVerificationException::class.java)
    }

    @Test
    fun `unknown kid fails verification`() {
        val verifier = JwksVerifier()
        val compact = signPayload(mapOf("entitlements" to listOf("a")), kid = "unknown-kid")

        assertThatThrownBy { verifier.verify(compact, jwks) }
            .isInstanceOf(JwksVerificationException::class.java)
            .hasMessageContaining("unknown-kid")
    }

    @Test
    fun `missing kid header fails verification`() {
        val header = JWSHeader.Builder(JWSAlgorithm.ES256).build()
        val jws = JWSObject(header, Payload(mapOf("entitlements" to listOf("a"))))
        jws.sign(signer)
        val compact = jws.serialize()

        val verifier = JwksVerifier()
        assertThatThrownBy { verifier.verify(compact, jwks) }
            .isInstanceOf(JwksVerificationException::class.java)
            .hasMessageContaining("kid")
    }

    @Test
    fun `non-ES256 alg fails`() {
        // Generate an RS256 keypair and try to sneak it past the verifier.
        // We sign with the same Nimbus library but a different algo header;
        // since the JWKS only advertises ES256 keys, lookup of the kid
        // succeeds but the algo enforcement rejects it.
        // Simpler version: corrupt the alg header.
        val ok = signPayload(mapOf("entitlements" to listOf("a")))
        val parts = ok.split(".")
        // Replace the alg header with HS256 by hand-crafting a new b64 header.
        val newHeaderBytes = """{"alg":"HS256","kid":"test-key-1"}""".toByteArray()
        val newHeader = java.util.Base64.getUrlEncoder().withoutPadding().encodeToString(newHeaderBytes)
        val tampered = "$newHeader.${parts[1]}.${parts[2]}"

        val verifier = JwksVerifier()
        assertThatThrownBy { verifier.verify(tampered, jwks) }
            .isInstanceOf(JwksVerificationException::class.java)
    }
}
