package com.bzonfhir.subscriptionservice.plugins.fhirpolling.config

import org.springframework.boot.context.properties.ConfigurationProperties

/**
 * Configurable knobs for the FHIR R4 polling ingest plugin (ticket #434).
 *
 * Property prefix: `subscription-service.ingest.fhir-polling`.
 *
 * The plugin supports MULTIPLE configured polling sources — one
 * [FhirPollingSourceConfig] per entry in [sources]. A customer might
 * poll Observation AND Encounter AND DocumentReference on different
 * cadences against the same FHIR server; each becomes a separate
 * `IngestSource` bean with its own scheduler tick. The HL7 v2 MLLP
 * plugin was single-instance because one MLLP listen socket is the
 * shape of MLLP traffic; FHIR polling is one shape per resource type
 * per server, hence the list.
 *
 * ## Master switch vs per-source enable
 *
 * [enabled] is the auto-config gate. When false the plugin doesn't
 * register any beans at all (no schedulers, no clients). The
 * per-source [FhirPollingSourceConfig.enabled] is for temporarily
 * disabling ONE source without rewriting the whole list — e.g.
 * pausing the Athena Observation feed while keeping the Lab Encounter
 * feed running.
 *
 * ## Why a ConfigurationProperties class and not plain @Value
 *
 * The config structure is too nested for `@Value`. A list of nested
 * structures (sources -> source -> auth -> auth-specific fields) is
 * what @ConfigurationProperties was designed for; plain @Value
 * doesn't bind lists of complex types at all.
 *
 * Spring Boot 3 constructor-binding mode: a single primary
 * constructor with default values per property. No setters, no
 * kotlin-noarg plugin needed.
 */
@ConfigurationProperties(prefix = "subscription-service.ingest.fhir-polling")
data class FhirPollingProperties(

    /**
     * Master enable switch. When false the auto-config skips bean
     * registration entirely — no schedulers, no clients, no high-water
     * marks. Default `true` for parity with the HL7 v2 plugin (built-in
     * plugins enable themselves by default; operators flip the switch
     * off via env var when they don't want the source).
     *
     * Note: with [sources] empty (the default), enabled=true still
     * results in zero polling activity — there's nothing to poll. The
     * gate is only useful in deployments that DO configure sources but
     * want to temporarily disable the whole family.
     */
    val enabled: Boolean = true,

    /**
     * The actual polling sources. Each becomes a separate
     * `FhirPollingIngestSource` bean.
     *
     * Empty by default — operators have to opt in by listing at least
     * one source. The HL7 v2 plugin is enabled+running out of the box
     * because every interface-engine deployment has an MLLP listener;
     * FHIR polling, by contrast, is meaningless without knowing which
     * server to poll. Empty-list-by-default fits the shape better.
     */
    val sources: List<FhirPollingSourceConfig> = emptyList(),
)

/**
 * One configured polling source — one (FHIR server, search, schedule)
 * triple.
 *
 * @property id Stable identifier for this source. Used as the
 *   high-water-mark store key, as the [com.bzonfhir.subscriptionservice.spi.meta.PluginMeta.id]
 *   suffix so the plugin instance is uniquely named in logs / the
 *   operator UI, and as the source's bean name. MUST be unique within
 *   a host. Lowercase + hyphen-separated by convention
 *   (e.g. `"athena-observations"`).
 * @property enabled Per-source enable. False keeps the bean registered
 *   but the scheduler stays parked — no requests are issued. Useful for
 *   on-the-fly disabling without redeploying.
 * @property baseUrl The FHIR server's base URL — the URL HAPI's
 *   IGenericClient appends `/Observation?...` etc. to. No trailing
 *   slash by convention; HAPI tolerates both.
 * @property pollIntervalSeconds How often the scheduler ticks. A tick
 *   issues exactly one search; if the underlying server is slow the
 *   next tick is delayed until the in-flight one returns (the
 *   scheduler uses fixedDelay semantics, not fixedRate). 60s is a
 *   reasonable default for clinical data freshness; bump to 300s for
 *   noisier endpoints. The minimum sensible value is bounded by your
 *   FHIR server's rate-limit; values below 10s are accepted but will
 *   probably trip server-side throttling.
 * @property search The FHIR search expression. Supports a `{{lastRun}}`
 *   placeholder that's replaced with the high-water-mark store's value
 *   for this source-id before each request. The high-water mark
 *   defaults to `1970-01-01T00:00:00Z` on first poll. After a
 *   successful fetch the mark advances to the most recent
 *   `Resource.meta.lastUpdated` in the returned Bundle.
 *
 *   Example: `"Observation?_lastUpdated=gt{{lastRun}}"` issues
 *   `GET Observation?_lastUpdated=gt1970-01-01T00:00:00Z` on the
 *   first poll, then `gt2026-06-25T14:30:01Z` (or whatever) on
 *   subsequent polls.
 *
 *   No FHIR syntax validation is done here — invalid searches surface
 *   at request time as the FHIR server's response (e.g. HTTP 400).
 *   The plugin logs the failure and the scheduler will retry on the
 *   next tick without advancing the high-water mark.
 * @property sourceSystem The (sourceSystem, sourceId) idempotency-key
 *   prefix the pipeline uses to deduplicate inbound rows. For Athena
 *   this is the literal `"athena"`; for a generic lab pull it might
 *   be `"lab-x"`. Persisted on every emitted PipelineMessage's
 *   [com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage.sourceSystem]
 *   field.
 * @property auth Authentication strategy for outbound FHIR calls.
 *   See [FhirPollingAuthConfig].
 */
data class FhirPollingSourceConfig(
    val id: String = "",
    val enabled: Boolean = true,
    val baseUrl: String = "",
    val pollIntervalSeconds: Long = 60L,
    val search: String = "",
    val sourceSystem: String = "",
    val auth: FhirPollingAuthConfig = FhirPollingAuthConfig(),
)

/**
 * Authentication strategy for outbound FHIR requests.
 *
 * v1 supports three [type] values:
 *
 *   - `"none"` — no authentication header sent. Local-dev / unsecured
 *     FHIR servers. Default.
 *   - `"bearer-static"` — adds `Authorization: Bearer ${tokenEnv}` on
 *     every request. The actual token value comes from the
 *     environment variable named by [tokenEnv] (NOT inlined in YAML
 *     — secrets in YAML check into git too often). Suitable for
 *     long-lived service-account tokens.
 *   - `"oauth2-client-credentials"` — performs an OAuth2 client-
 *     credentials grant against [tokenUrl] using the client id /
 *     secret resolved from env vars [clientIdEnv] / [clientSecretEnv].
 *     The resulting access token is cached in-memory and refreshed
 *     when within 30s of expiry. This matches the Athena platform
 *     auth flow described in Epic #426.
 *
 * The fields are a flat union — only the ones relevant to the chosen
 * [type] are read at runtime. Listing them all on one class keeps the
 * binding simple (Spring's relaxed binding handles a flat key shape
 * cleanly; nested sealed-class polymorphism would require either a
 * Jackson type-info annotation or a custom Converter). Validation that
 * the right fields are populated for the chosen type happens in the
 * auth strategy factory, not via Bean Validation, because the
 * `@ConfigurationProperties` path is run at bean-construction time
 * where surfacing a useful error message is awkward.
 */
data class FhirPollingAuthConfig(
    /**
     * One of `"none"`, `"bearer-static"`, `"oauth2-client-credentials"`.
     * Default `"none"` so an operator can leave the auth block out
     * entirely when polling an unsecured FHIR server (the embedded
     * server in our end-to-end test is one such case).
     */
    val type: String = "none",

    /**
     * For `bearer-static`: env var name whose value is the bearer
     * token to send. Empty string means "look up the env var with the
     * empty name" — which never resolves — so the auth strategy
     * fails fast with a descriptive error rather than emitting an
     * `Authorization: Bearer ` header with an empty value.
     */
    val tokenEnv: String = "",

    /**
     * For `oauth2-client-credentials`: the OAuth2 token endpoint to
     * POST `grant_type=client_credentials` to. Server-side validation
     * (URL shape, https-only) is the FHIR server's job — we POST the
     * URL as-is.
     */
    val tokenUrl: String = "",

    /**
     * For `oauth2-client-credentials`: env var name whose value is
     * the OAuth2 client id.
     */
    val clientIdEnv: String = "",

    /**
     * For `oauth2-client-credentials`: env var name whose value is
     * the OAuth2 client secret. NEVER inlined in YAML; the secret
     * lives in an env var the deployment platform injects from a
     * secret store.
     */
    val clientSecretEnv: String = "",

    /**
     * For `oauth2-client-credentials`: optional `scope` parameter on
     * the token request. Many FHIR servers require this (Athena's
     * scopes are `system/Patient.read system/Observation.read` for
     * example) but the spec allows it to be omitted. Empty string
     * = no scope on the request.
     */
    val scope: String = "",
)
