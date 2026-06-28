package com.bzonfhir.subscriptionservice.interfaceengine.entitlements

import com.fasterxml.jackson.annotation.JsonCreator
import com.fasterxml.jackson.annotation.JsonProperty
import com.fasterxml.jackson.databind.DeserializationFeature
import com.fasterxml.jackson.databind.ObjectMapper
import com.nimbusds.jose.jwk.JWKSet
import org.slf4j.LoggerFactory
import java.io.IOException
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.text.ParseException
import java.time.Duration
import java.time.Instant

/**
 * HTTP client for the license server (ticket #460, Epic #428).
 *
 * One client = one license-server URL. The client owns:
 *
 *   - A pinned [HttpClient] (JDK 11+ built-in, no okhttp on the runtime path).
 *   - An ObjectMapper for parsing the JSON entitlement response.
 *   - A lazily-loaded JWKS cache (1h TTL — matches the UI's
 *     `JWKS_CACHE_TTL_MS` in `signatureVerifier.ts`).
 *   - A stateless [JwksVerifier] for the actual signature check.
 *
 * The client is intentionally I/O-shaped: it throws on every failure path
 * (network, non-2xx, malformed body, signature mismatch). The
 * [EntitlementHolder] catches those and decides whether to degrade to cached
 * or FOSS — see the failure-mode tree in ticket #460.
 *
 * Server contract (mirrors `subscription-service-commercial/license-server`):
 *
 *   - `POST /entitlements` with body `{"licenseKey":"...","productSlug":"subscription-service"}`.
 *     Response is `{"entitlements":[...], "expiresAt":"...", "tierName":"...", "signature":"<compact JWS>"}`.
 *     The signature is over `{entitlements, expiresAt, tierName}` as the
 *     license-server's `SigningService.signEntitlements` produces it.
 *   - `GET /.well-known/jwks.json` returns the JWKSet that verifies the signature.
 *
 * The 401 path indicates a revoked or invalid key. Per ticket #460 the holder
 * treats that as "instant FOSS mode + log ERROR".
 */
class LicenseClient(
    licenseServerUrl: String,
    private val httpClient: HttpClient = defaultHttpClient(),
    private val verifier: JwksVerifier = JwksVerifier(),
    private val productSlug: String = PRODUCT_SLUG,
    private val jwksTtl: Duration = JWKS_CACHE_TTL,
    private val clock: () -> Instant = Instant::now,
) {
    private val baseUrl: String = licenseServerUrl.trimEnd('/')
    private val entitlementsUrl: URI = URI.create("$baseUrl/entitlements")
    private val jwksUrl: URI = URI.create("$baseUrl/.well-known/jwks.json")

    private val mapper: ObjectMapper = ObjectMapper()
        .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES)

    @Volatile
    private var cachedJwks: CachedJwks? = null

    /**
     * Call `POST /entitlements`, verify the signature against the JWKS, and
     * return a [VerifiedLicenseResponse]. Throws [LicenseClientException] on
     * any failure (network, non-2xx, malformed body, signature mismatch,
     * unreachable JWKS).
     */
    fun fetchEntitlements(licenseKey: String): VerifiedLicenseResponse {
        val request = HttpRequest.newBuilder()
            .uri(entitlementsUrl)
            .timeout(REQUEST_TIMEOUT)
            .header("Content-Type", "application/json")
            .header("Accept", "application/json")
            .POST(
                HttpRequest.BodyPublishers.ofString(
                    """{"licenseKey":${quote(licenseKey)},"productSlug":${quote(productSlug)}}"""
                )
            )
            .build()

        val response: HttpResponse<String> = try {
            httpClient.send(request, HttpResponse.BodyHandlers.ofString())
        } catch (e: IOException) {
            throw LicenseClientException("license server unreachable: ${e.message}", e)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            throw LicenseClientException("license-server request interrupted", e)
        }

        if (response.statusCode() == 401) {
            throw LicenseRevokedException("license server returned 401 (invalid or revoked key)")
        }
        if (response.statusCode() !in 200..299) {
            throw LicenseClientException("license server returned HTTP ${response.statusCode()}")
        }

        val parsed: EntitlementsResponseDto = try {
            mapper.readValue(response.body(), EntitlementsResponseDto::class.java)
        } catch (e: Exception) {
            throw LicenseClientException("license server returned a malformed entitlement response", e)
        }

        val jwks = loadJwks()
        val verified = try {
            verifier.verify(parsed.signature, jwks)
        } catch (e: JwksVerificationException) {
            throw LicenseClientException("license response signature verification failed: ${e.message}", e)
        }

        return VerifiedLicenseResponse(
            entitlements = parsed.entitlements,
            tierName = parsed.tierName,
            expiresAt = parsed.expiresAt,
            signature = parsed.signature,
            signatureKid = verified.kid,
        )
    }

    private fun loadJwks(): JWKSet {
        val now = clock()
        val current = cachedJwks
        if (current != null && Duration.between(current.fetchedAt, now) < jwksTtl) {
            return current.jwks
        }
        val request = HttpRequest.newBuilder()
            .uri(jwksUrl)
            .timeout(REQUEST_TIMEOUT)
            .header("Accept", "application/json")
            .GET()
            .build()
        val response: HttpResponse<String> = try {
            httpClient.send(request, HttpResponse.BodyHandlers.ofString())
        } catch (e: IOException) {
            throw LicenseClientException("failed to fetch JWKS: ${e.message}", e)
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            throw LicenseClientException("JWKS request interrupted", e)
        }
        if (response.statusCode() !in 200..299) {
            throw LicenseClientException("failed to fetch JWKS: HTTP ${response.statusCode()}")
        }
        val parsed: JWKSet = try {
            JWKSet.parse(response.body())
        } catch (e: ParseException) {
            throw LicenseClientException("license server returned a malformed JWKS document", e)
        }
        cachedJwks = CachedJwks(parsed, now)
        return parsed
    }

    private data class CachedJwks(val jwks: JWKSet, val fetchedAt: Instant)

    companion object {
        const val PRODUCT_SLUG = "subscription-service"
        /** Matches the UI's `JWKS_CACHE_TTL_MS = 60 * 60 * 1000`. */
        val JWKS_CACHE_TTL: Duration = Duration.ofHours(1)
        val REQUEST_TIMEOUT: Duration = Duration.ofSeconds(10)

        private val log = LoggerFactory.getLogger(LicenseClient::class.java)

        fun defaultHttpClient(): HttpClient = HttpClient.newBuilder()
            .connectTimeout(Duration.ofSeconds(5))
            .build()

        private fun quote(s: String): String {
            // Minimal JSON-string escape — the license key is a base64-ish
            // token in practice. Backslash + quote are the only chars we
            // need to handle to keep the body legal JSON.
            val escaped = s.replace("\\", "\\\\").replace("\"", "\\\"")
            return "\"$escaped\""
        }
    }
}

/**
 * The parsed + verified license response. The bytes are NOT cached here;
 * caching lives in EntitlementHolder + LicenseCache.
 */
data class VerifiedLicenseResponse(
    val entitlements: List<String>,
    val tierName: String,
    val expiresAt: String,
    val signature: String,
    val signatureKid: String,
)

/** Internal DTO matching `EntitlementsResponse` on the license-server side. */
internal data class EntitlementsResponseDto @JsonCreator constructor(
    @param:JsonProperty("entitlements") val entitlements: List<String>,
    @param:JsonProperty("expiresAt") val expiresAt: String,
    @param:JsonProperty("tierName") val tierName: String,
    @param:JsonProperty("signature") val signature: String,
)

/** Generic license-client failure. */
open class LicenseClientException(message: String, cause: Throwable? = null) :
    RuntimeException(message, cause)

/**
 * The license server explicitly rejected the key (HTTP 401). Indicates a
 * revoked or invalid license. The holder treats this as instant FOSS + ERROR.
 */
class LicenseRevokedException(message: String) : LicenseClientException(message)
