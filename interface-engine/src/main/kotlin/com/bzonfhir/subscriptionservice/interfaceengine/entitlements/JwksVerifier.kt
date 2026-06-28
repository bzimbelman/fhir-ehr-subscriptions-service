package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.nimbusds.jose.JOSEException
import com.nimbusds.jose.JWSAlgorithm
import com.nimbusds.jose.JWSObject
import com.nimbusds.jose.crypto.ECDSAVerifier
import com.nimbusds.jose.jwk.ECKey
import com.nimbusds.jose.jwk.JWKSet
import java.text.ParseException

/**
 * ES256 JWS signature verifier (ticket #460, Epic #428).
 *
 * JVM-side mirror of `ui/src/lib/license/signatureVerifier.ts`. The license
 * server (ticket #458, `SigningService`) signs every entitlement response as
 * a compact JWS using ES256 over a P-256 keypair and publishes the matching
 * public JWKS at `${licenseServerUrl}/.well-known/jwks.json`. This verifier:
 *
 *   1. Reads the protected header to discover the `kid`.
 *   2. Confirms `alg == ES256` (anything else is treated as tampering).
 *   3. Looks the kid up in the caller-supplied [JWKSet] — unknown kid is a
 *      hard failure, NEVER a silent pass.
 *   4. Verifies the signature with Nimbus's [ECDSAVerifier].
 *
 * Verification failure is signalled as a [JwksVerificationException]. The
 * caller (LicenseClient) treats any verification failure the same as an
 * unreachable license server: warn + degrade to cached / FOSS.
 *
 * The verifier itself is stateless — the JWKS cache lives in the caller
 * (LicenseClient) since the JWKS is fetched from the license server on the
 * same code path. Keeping the verifier stateless makes the test surface
 * trivial and rules out a class of "stale-cache-after-key-rotation" bugs.
 */
class JwksVerifier {

    /**
     * Verify [compactJws] against [jwks]. Returns the verified payload + the
     * `kid` of the JWK that succeeded. Throws [JwksVerificationException] on
     * any failure.
     */
    fun verify(compactJws: String, jwks: JWKSet): VerifiedJws {
        require(compactJws.isNotBlank()) { "compactJws must be non-empty" }

        val parsed: JWSObject = try {
            JWSObject.parse(compactJws)
        } catch (e: ParseException) {
            throw JwksVerificationException("failed to parse compact JWS", e)
        }

        val header = parsed.header
        if (header.algorithm != JWSAlgorithm.ES256) {
            throw JwksVerificationException(
                "unsupported alg \"${header.algorithm}\"; expected ES256"
            )
        }
        val kid = header.keyID
            ?: throw JwksVerificationException("JWS is missing a `kid` protected header")

        val key = jwks.getKeyByKeyId(kid)
            ?: throw JwksVerificationException("JWS kid \"$kid\" is not in the JWKS")
        if (key !is ECKey) {
            throw JwksVerificationException("JWK \"$kid\" is not an EC key")
        }

        val ok: Boolean = try {
            parsed.verify(ECDSAVerifier(key.toECPublicKey()))
        } catch (e: JOSEException) {
            throw JwksVerificationException("signature verification threw", e)
        }
        if (!ok) {
            throw JwksVerificationException("signature verification failed for kid \"$kid\"")
        }

        return VerifiedJws(payloadJson = parsed.payload.toString(), kid = kid)
    }
}

/** Result of a successful verification. */
data class VerifiedJws(
    /** The signed payload, serialised back to JSON. */
    val payloadJson: String,
    /** The `kid` of the JWK that verified the signature. */
    val kid: String,
)

/** Any signature-verification failure. */
class JwksVerificationException(message: String, cause: Throwable? = null) :
    RuntimeException(message, cause)
