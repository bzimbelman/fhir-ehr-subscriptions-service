package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config.Hl7V2MllpProperties
import com.bzonfhir.subscriptionservice.spi.IngestSource
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.apache.camel.CamelContext
import org.slf4j.LoggerFactory

/**
 * Default HL7 v2 MLLP `IngestSource` (Epic #425, ticket #431).
 *
 * Listens on a TCP port, parses inbound v2 messages with HAPI, and hands
 * each one to the SPI callback wrapped in a [PipelineMessage]. This is
 * the SPI-shaped re-expression of the legacy `interface-engine/.../routes/
 * IngestRoutes.kt` — same wire protocol, same parse, same ACK semantics.
 *
 * ## SPI lifecycle
 *
 * 1. **Construct** — Spring auto-config builds one instance per host
 *    when the plugin's JAR is on the classpath and
 *    `subscription-service.ingest.hl7v2-mllp.enabled` is true.
 * 2. **start(callback)** — the host's `IngestSourceRegistry`
 *    invokes this with a callback that funnels into
 *    `IngestPersistService.persistReceived`. We build a
 *    `Hl7V2MllpCamelRouteBuilder` with the callback baked in and add it
 *    to the Camel context. The Camel-MLLP component spins up its listen
 *    socket on a worker thread; this method returns promptly per the
 *    SPI contract.
 * 3. **(on each message)** — the route's persist step constructs a
 *    PipelineMessage and invokes the callback. If the callback throws,
 *    the route's error handler converts the exception to an AE ACK on
 *    the wire.
 * 4. **stop()** — remove the route from the Camel context, which
 *    closes the MLLP listen socket. Idempotent; safe to call multiple
 *    times during host shutdown.
 *
 * ## What's NOT here
 *
 *   - OpenTelemetry span lifecycle (the host's callback wraps this).
 *   - JPA persistence (the host's callback wraps this).
 *   - Correlation-id swap on idempotent-duplicate (the host's callback
 *     handles this).
 *
 * The plugin's responsibility is the wire-level receive: bytes in,
 * PipelineMessage out, ACK back. Everything else is host concerns
 * deliberately layered above the SPI.
 *
 * ## Plugin metadata
 *
 * Pinned values:
 *
 *   - `id = "hl7v2-mllp"` — stable identifier; matches
 *     [Hl7V2MessageParser.SOURCE_PROTOCOL].
 *   - `supplier = FIRST_PARTY` — bundled in the canonical
 *     subscription-service distribution.
 *   - `schemaVersion = 1` — first SPI shape we author against.
 *   - `version` — module artifact version. Bumped by the build, not
 *     manually maintained here. Read from the package manifest if
 *     available; falls back to a hard-coded sentinel for unit tests
 *     that don't ship in a JAR.
 */
class Hl7V2MllpIngestSource(
    private val properties: Hl7V2MllpProperties,
    private val camelContext: CamelContext,
) : IngestSource {

    private val log = LoggerFactory.getLogger(Hl7V2MllpIngestSource::class.java)

    /**
     * Holds the route builder we added to the CamelContext in start(),
     * so stop() can find the route ids to remove. Null when stopped.
     *
     * Volatile because start/stop may be called from different threads
     * (host shutdown is invoked from the JVM's shutdown thread, not
     * the thread that ran start()).
     */
    @Volatile
    private var activeRouteBuilder: Hl7V2MllpCamelRouteBuilder? = null

    override val meta: PluginMeta = PluginMeta(
        id = "hl7v2-mllp",
        version = readPackageVersion(),
        schemaVersion = 1,
        supplier = PluginSupplier.FIRST_PARTY,
        description = "Default HL7 v2 MLLP listener (Camel + HAPI HL7 v2)",
    )

    override val protocol: String = "hl7v2-mllp"

    override fun start(callback: (PipelineMessage) -> Unit) {
        if (activeRouteBuilder != null) {
            // Defensive: a re-entrant start() while already running
            // would attempt to bind the same port twice. The SPI
            // doesn't explicitly forbid this — but it would never
            // succeed, so we treat it as a no-op and log.
            log.warn(
                "start() called while already active (route={}); ignoring",
                ROUTE_ID,
            )
            return
        }

        val builder = Hl7V2MllpCamelRouteBuilder(
            properties = properties,
            callback = callback,
        )

        log.info(
            "starting MLLP ingest source on {}:{} (charset={})",
            properties.host,
            properties.port,
            properties.characterSet,
        )

        // addRoutes() compiles and registers the route. The Camel
        // context must already be started (or be in the middle of
        // starting) for the route to come up. The host starts the
        // context as part of normal Spring Boot startup; for unit tests
        // that drive a CamelContext directly, the test fixture starts
        // it first.
        camelContext.addRoutes(builder)
        activeRouteBuilder = builder
    }

    override fun stop() {
        val builder = activeRouteBuilder ?: run {
            log.debug("stop() called while not running — no-op")
            return
        }
        activeRouteBuilder = null

        // Tear down every route the builder contributed. There's only
        // one today (ROUTE_ID), but iterating the builder's route
        // collection keeps us honest if someone adds a sibling route.
        for (definition in builder.routeCollection.routes) {
            val id = definition.id
            log.info("stopping MLLP ingest route id={}", id)
            // Camel requires stopping before removal — `removeRoute`
            // throws when the route is still active. We swallow stop
            // failures (best-effort during shutdown) but always attempt
            // remove.
            runCatching { camelContext.routeController.stopRoute(id) }
                .onFailure { log.warn("error stopping route {}: {}", id, it.message) }
            runCatching { camelContext.removeRoute(id) }
                .onFailure { log.warn("error removing route {}: {}", id, it.message) }
        }
    }

    companion object {
        /**
         * Canonical Camel route id for the MLLP listener. Matches the
         * legacy `IngestRoutes.ROUTE_MLLP_INGEST` value so existing
         * tests that look up the route by id (notably
         * `IngestRoutesTest.setUp()`) continue to pass.
         */
        const val ROUTE_ID: String = "mllp-ingest"

        /**
         * Read the plugin's version from the JAR manifest's
         * `Implementation-Version` attribute. Falls back to a sentinel
         * value when no manifest is available (e.g. running from an
         * IDE's exploded build output, or unit tests). The fallback is
         * a semver-shaped string so consumers that match against a
         * regex still see something sensible.
         */
        private fun readPackageVersion(): String {
            val v = Hl7V2MllpIngestSource::class.java.`package`?.implementationVersion
            return v ?: "0.1.0-SNAPSHOT"
        }
    }
}
