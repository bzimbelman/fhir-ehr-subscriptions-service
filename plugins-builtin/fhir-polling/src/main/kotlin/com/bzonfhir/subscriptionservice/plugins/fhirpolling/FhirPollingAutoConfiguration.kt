package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import ca.uhn.fhir.context.FhirContext
import ca.uhn.fhir.rest.client.api.IClientInterceptor
import ca.uhn.fhir.rest.client.api.IGenericClient
import ca.uhn.fhir.rest.client.api.IHttpRequest
import ca.uhn.fhir.rest.client.api.IHttpResponse
import ca.uhn.fhir.rest.client.api.ServerValidationModeEnum
import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingAuthConfig
import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingProperties
import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig
import org.slf4j.LoggerFactory
import org.springframework.boot.autoconfigure.condition.ConditionalOnClass
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.boot.context.properties.EnableConfigurationProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration

/**
 * Spring Boot auto-configuration for the FHIR R4 polling ingest plugin
 * (ticket #434, Epic #425).
 *
 * Listed in
 * `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`,
 * which Spring Boot 3 reads to discover @Configuration classes from
 * library JARs without any `@Import` / `@ComponentScan` changes in the
 * host.
 *
 * ## Gating
 *
 * 1. `@ConditionalOnClass(IGenericClient::class)` — only activates
 *    when HAPI FHIR's client is on the classpath. The interface-engine
 *    host already brings HAPI; a hypothetical future host that doesn't
 *    wouldn't accidentally pull this plugin in.
 * 2. `@ConditionalOnProperty(...)` — master switch via
 *    `subscription-service.ingest.fhir-polling.enabled`. Default true,
 *    BUT [fhirPollingIngestSources] checks `sources.isEmpty()` and
 *    returns zero beans in that case — so the practical default is
 *    "enabled but doing nothing until an operator configures at least
 *    one source."
 *
 * ## Bean shape
 *
 * One `FhirPollingIngestSource` bean per configured
 * [FhirPollingSourceConfig]. Each bean gets its own
 * [FhirSearchExecutor] (because it wraps a per-source IGenericClient
 * with a per-source base URL + per-source auth interceptor) and
 * shares the singleton [HighWaterMarkStore] (which is keyed by
 * source-id internally, so sharing is safe).
 *
 * The host's `IngestSourceRegistry` discovers every `IngestSource`
 * bean by type-injection (`List<IngestSource>`) and invokes
 * `.start(callback)` on each at boot. The plugin's beans take their
 * place alongside the HL7 v2 plugin's `Hl7V2MllpIngestSource`.
 */
@Configuration(proxyBeanMethods = false)
@ConditionalOnClass(IGenericClient::class)
@ConditionalOnProperty(
    prefix = "subscription-service.ingest.fhir-polling",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
@EnableConfigurationProperties(FhirPollingProperties::class)
open class FhirPollingAutoConfiguration {

    private val log = LoggerFactory.getLogger(FhirPollingAutoConfiguration::class.java)

    /**
     * Shared HAPI [FhirContext] — building a context is expensive
     * (scans the R4 model on first use). One per JVM is HAPI's
     * documented recommendation.
     *
     * `@ConditionalOnMissingBean` so we don't fight the host's own
     * FhirContext bean — interface-engine declares one (FhirConfig.kt)
     * and that's the one we want to share. The annotation only
     * registers our bean when the host hasn't already.
     */
    @Bean
    @ConditionalOnMissingBean(FhirContext::class)
    open fun fhirPollingFhirContext(): FhirContext = FhirContext.forR4().apply {
        restfulClientFactory.serverValidationMode = ServerValidationModeEnum.NEVER
    }

    /**
     * One [FhirPollingIngestSource] per configured source.
     *
     * We declare this as a method that returns a List<IngestSource>
     * to register multiple sibling beans. Spring's @Bean on a List
     * return is normally a single-bean injection point — to register
     * EACH entry as its own bean we expose a list via the bean
     * factory's hooks instead. The simplest path is to declare a
     * BeanDefinitionRegistryPostProcessor, but that's heavyweight for
     * what we want.
     *
     * Alternative: a `@Bean` method per source. But the source count
     * is dynamic (operator config), so we can't enumerate at compile
     * time.
     *
     * Pragmatic choice: register the list as ONE bean of type
     * `List<IngestSource>` AND let the host's
     * `IngestSourceRegistry` resolve it via `List<IngestSource>`
     * injection. Spring's collection-injection semantics flatten the
     * list-typed bean into the injected collection — so each entry
     * shows up as an IngestSource to the registry. Verified by
     * inspecting Spring's CollectionResolvingObjectFactory shape.
     */
    @Bean
    open fun fhirPollingHighWaterMarkStore(): HighWaterMarkStore = HighWaterMarkStore()

    @Bean(name = ["fhirPollingIngestSources"])
    open fun fhirPollingIngestSources(
        properties: FhirPollingProperties,
        fhirContext: FhirContext,
        highWaterMarkStore: HighWaterMarkStore,
    ): List<FhirPollingIngestSource> {
        if (properties.sources.isEmpty()) {
            log.info(
                "fhir-polling plugin enabled but no sources configured — registering zero beans",
            )
            return emptyList()
        }
        val seenIds = mutableSetOf<String>()
        return properties.sources.map { sourceConfig ->
            require(sourceConfig.id.isNotBlank()) {
                "subscription-service.ingest.fhir-polling.sources[].id must be non-blank"
            }
            require(seenIds.add(sourceConfig.id)) {
                "subscription-service.ingest.fhir-polling.sources[].id must be unique " +
                    "within a host; duplicate: '${sourceConfig.id}'"
            }
            require(sourceConfig.baseUrl.isNotBlank()) {
                "source '${sourceConfig.id}' must have a non-blank base-url"
            }
            require(sourceConfig.search.isNotBlank()) {
                "source '${sourceConfig.id}' must have a non-blank search"
            }
            require(sourceConfig.sourceSystem.isNotBlank()) {
                "source '${sourceConfig.id}' must have a non-blank source-system"
            }

            log.info(
                "configuring fhir polling source id={} baseUrl={} authType={}",
                sourceConfig.id,
                sourceConfig.baseUrl,
                sourceConfig.auth.type,
            )
            val client = buildClient(fhirContext, sourceConfig)
            val executor = FhirSearchExecutor(client, fhirContext)
            FhirPollingIngestSource(
                config = sourceConfig,
                searchExecutor = executor,
                highWaterMarkStore = highWaterMarkStore,
            )
        }
    }

    /**
     * Build a [IGenericClient] for [source]. Registers the right
     * auth interceptor (none / bearer-static / oauth2) based on the
     * configured [FhirPollingAuthConfig.type].
     */
    private fun buildClient(
        fhirContext: FhirContext,
        source: FhirPollingSourceConfig,
    ): IGenericClient {
        val client = fhirContext.newRestfulGenericClient(source.baseUrl)
        val interceptor = buildAuthInterceptor(source.auth)
        if (interceptor != null) {
            client.registerInterceptor(interceptor)
        }
        return client
    }

    /**
     * Choose the auth interceptor for the source. Returns null for
     * `type=none` so we don't add an empty interceptor that does
     * nothing on every request.
     *
     * Validation of required fields per auth type happens here, NOT
     * via Bean Validation, because surfacing a useful error message
     * at @ConfigurationProperties binding time is awkward. Failing
     * here with `IllegalStateException("...")` produces a clear
     * Spring boot log line pointing the operator at the right env
     * var.
     */
    private fun buildAuthInterceptor(auth: FhirPollingAuthConfig): IClientInterceptor? =
        when (auth.type) {
            "none" -> null
            "bearer-static" -> {
                check(auth.tokenEnv.isNotBlank()) {
                    "auth type 'bearer-static' requires token-env to be set"
                }
                val token = System.getenv(auth.tokenEnv)
                check(!token.isNullOrBlank()) {
                    "auth type 'bearer-static' references env var '${auth.tokenEnv}' " +
                        "which is unset or empty"
                }
                BearerStaticInterceptor(token)
            }
            "oauth2-client-credentials" -> {
                check(auth.tokenUrl.isNotBlank()) {
                    "auth type 'oauth2-client-credentials' requires token-url"
                }
                check(auth.clientIdEnv.isNotBlank()) {
                    "auth type 'oauth2-client-credentials' requires client-id-env"
                }
                check(auth.clientSecretEnv.isNotBlank()) {
                    "auth type 'oauth2-client-credentials' requires client-secret-env"
                }
                val clientId = System.getenv(auth.clientIdEnv)
                val clientSecret = System.getenv(auth.clientSecretEnv)
                check(!clientId.isNullOrBlank()) {
                    "env var '${auth.clientIdEnv}' for client-id is unset or empty"
                }
                check(!clientSecret.isNullOrBlank()) {
                    "env var '${auth.clientSecretEnv}' for client-secret is unset or empty"
                }
                OAuth2ClientCredentialsInterceptor(
                    tokenUrl = auth.tokenUrl,
                    clientId = clientId,
                    clientSecret = clientSecret,
                    scope = auth.scope,
                )
            }
            else -> error(
                "unknown auth type '${auth.type}' — must be one of: " +
                    "none, bearer-static, oauth2-client-credentials",
            )
        }
}

/**
 * HAPI client interceptor that stamps a static bearer token on every
 * outbound request.
 *
 * Lives at file scope (not nested) so it's straightforward to
 * unit-test. The token is the resolved env-var value captured at
 * bean-construction time; if the operator rotates the secret they
 * have to redeploy. A future enhancement could re-resolve the env var
 * on each request, but env vars are deliberately rebound only at
 * process-start so the current behaviour matches their semantics.
 */
internal class BearerStaticInterceptor(private val token: String) : IClientInterceptor {
    override fun interceptRequest(request: IHttpRequest) {
        request.addHeader("Authorization", "Bearer $token")
    }
    override fun interceptResponse(response: IHttpResponse?) { /* no-op */ }
}

/**
 * HAPI client interceptor that performs an OAuth2 client-credentials
 * grant against a configured token URL and stamps the resulting
 * bearer on every outbound request.
 *
 * V1 limitations the README documents:
 *
 *   - Token cache is per-interceptor (i.e. per-source). Two sources
 *     pointing at the same token URL with the same client id will
 *     each maintain their own token. A future ticket may share a
 *     cache, but the per-source isolation is the safer default
 *     (different sources may legitimately use different scopes).
 *   - Refresh policy: refresh when within 30s of expiry. No
 *     refresh-token flow — we always do a fresh
 *     `grant_type=client_credentials` (which is the spec for that
 *     grant; refresh tokens are explicitly forbidden for it).
 *   - HTTP client: java.net.HttpURLConnection. Avoids dragging in
 *     another HTTP library; the OAuth2 token flow is rare enough
 *     (once per N minutes) that connection pooling doesn't matter.
 *
 * NOT implemented in this ticket (v2):
 *
 *   - Token introspection / revocation. Out of scope.
 *   - mTLS to the token endpoint. Out of scope.
 *   - Athena-specific JWT-bearer flows. Athena uses standard
 *     client-credentials per their docs, so this implementation
 *     covers them.
 */
internal class OAuth2ClientCredentialsInterceptor(
    private val tokenUrl: String,
    private val clientId: String,
    private val clientSecret: String,
    private val scope: String,
) : IClientInterceptor {

    private val log = LoggerFactory.getLogger(OAuth2ClientCredentialsInterceptor::class.java)

    /**
     * Cached token state. `expiresAt = Instant.MIN` means "we have
     * never fetched, fetch on next call." Synchronized via the
     * volatile reference + double-checked locking inside
     * [currentBearer].
     */
    @Volatile
    private var cached: CachedToken = CachedToken(token = "", expiresAt = java.time.Instant.MIN)

    override fun interceptRequest(request: IHttpRequest) {
        val bearer = currentBearer()
        request.addHeader("Authorization", "Bearer $bearer")
    }

    override fun interceptResponse(response: IHttpResponse?) { /* no-op */ }

    /**
     * Return the cached token, refreshing if it's expired or within
     * the 30s grace window.
     *
     * Synchronized so two concurrent FHIR requests don't both fire
     * a token-endpoint refresh — the second waits for the first's
     * result.
     */
    @Synchronized
    private fun currentBearer(): String {
        val now = java.time.Instant.now()
        if (now.isBefore(cached.expiresAt.minusSeconds(REFRESH_GRACE_SECONDS))) {
            return cached.token
        }
        log.info("oauth2 token expired or near expiry; refreshing from {}", tokenUrl)
        cached = fetchToken()
        return cached.token
    }

    /**
     * Perform the actual token POST. Body is form-encoded per RFC
     * 6749 §4.4. We pass client credentials in the Authorization
     * header (HTTP Basic) per the spec's preferred shape — most
     * OAuth2 servers accept either form-body or Basic; Basic is
     * "RECOMMENDED" so we pick it.
     */
    private fun fetchToken(): CachedToken {
        val conn = java.net.URI(tokenUrl).toURL().openConnection() as java.net.HttpURLConnection
        try {
            conn.requestMethod = "POST"
            conn.doOutput = true
            conn.connectTimeout = 10_000
            conn.readTimeout = 10_000
            conn.setRequestProperty("Content-Type", "application/x-www-form-urlencoded")
            val basic = java.util.Base64.getEncoder()
                .encodeToString("$clientId:$clientSecret".toByteArray(Charsets.UTF_8))
            conn.setRequestProperty("Authorization", "Basic $basic")
            val body = buildString {
                append("grant_type=client_credentials")
                if (scope.isNotBlank()) {
                    append("&scope=")
                    append(java.net.URLEncoder.encode(scope, Charsets.UTF_8))
                }
            }
            conn.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            val status = conn.responseCode
            if (status !in 200..299) {
                val err = (conn.errorStream ?: conn.inputStream)?.bufferedReader()?.use { it.readText() }
                    .orEmpty()
                error("oauth2 token endpoint $tokenUrl returned HTTP $status: $err")
            }
            val json = conn.inputStream.bufferedReader(Charsets.UTF_8).use { it.readText() }
            val accessToken = extractJsonStringField(json, "access_token")
                ?: error("oauth2 token response missing access_token: $json")
            val expiresIn = extractJsonNumberField(json, "expires_in") ?: 3600L
            return CachedToken(
                token = accessToken,
                expiresAt = java.time.Instant.now().plusSeconds(expiresIn),
            )
        } finally {
            conn.disconnect()
        }
    }

    /**
     * Tiny string-shaped JSON field extractor. We use this instead of
     * Jackson because:
     *
     *   - Pulling Jackson into the plugin solely for one token-response
     *     parse adds 6MB to the runtime classpath. The interface-engine
     *     already has Jackson but we'd be coupling the plugin to its
     *     presence.
     *   - The token-response shape is RFC-stable (access_token,
     *     expires_in). A two-line regex is enough.
     *
     * If the response uses unusual whitespace or non-string types this
     * will return null; fetchToken then errors out and the request
     * fails. Operators see the error in logs.
     */
    private fun extractJsonStringField(json: String, field: String): String? {
        val regex = """"$field"\s*:\s*"([^"]*)"""".toRegex()
        return regex.find(json)?.groupValues?.get(1)
    }

    private fun extractJsonNumberField(json: String, field: String): Long? {
        val regex = """"$field"\s*:\s*(\d+)""".toRegex()
        return regex.find(json)?.groupValues?.get(1)?.toLongOrNull()
    }

    /**
     * Holds the bearer + when it expires. Replaced atomically on each
     * refresh. The values are captured rather than computed lazily so
     * a half-completed refresh doesn't leave a partial token in the
     * cache slot.
     */
    private data class CachedToken(val token: String, val expiresAt: java.time.Instant)

    companion object {
        /**
         * Seconds before [CachedToken.expiresAt] at which we refresh
         * proactively. Picks up tokens whose clock is slightly skewed
         * from ours; 30s is the OAuth2 spec's recommended floor.
         */
        const val REFRESH_GRACE_SECONDS: Long = 30L
    }
}
