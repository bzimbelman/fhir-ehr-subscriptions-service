package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig
import com.bzonfhir.subscriptionservice.spi.IngestSource
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.slf4j.LoggerFactory

/**
 * FHIR R4 polling [IngestSource] (Epic #425, ticket #434).
 *
 * Periodically queries a FHIR R4 server and emits one
 * [PipelineMessage] per resource returned. Foundation for the Athena
 * vendor profile in Epic #426 and any other polling-based source.
 *
 * ## One bean per configured source
 *
 * The plugin's auto-configuration registers one
 * `FhirPollingIngestSource` bean for each entry in
 * `subscription-service.ingest.fhir-polling.sources[]`. A deployment
 * polling Observation and Encounter on separate cadences ends up with
 * two beans, two HAPI clients, two schedulers, two high-water marks.
 *
 * ## SPI lifecycle
 *
 *   1. **Construct** — `FhirPollingAutoConfiguration` builds one
 *      instance per configured [FhirPollingSourceConfig].
 *   2. **start(callback)** — the host's `IngestSourceRegistry`
 *      invokes this with a callback that funnels into
 *      `IngestPersistService.persistReceived`. We delegate to
 *      [FhirPollingScheduler] which spins up a single-threaded
 *      ScheduledExecutorService. Returns promptly per the SPI
 *      contract.
 *   3. **(on each tick)** — the scheduler queries the FHIR server,
 *      builds one PipelineMessage per Bundle entry, fires the
 *      callback. If the callback throws, the scheduler logs and
 *      moves on without advancing the high-water mark — same shape
 *      as the HL7 v2 plugin's "callback failed -> next sender retry
 *      wins" semantics.
 *   4. **stop()** — shuts the executor down (5s grace, then
 *      shutdownNow). Idempotent; safe under multiple shutdown calls.
 *
 * ## Plugin metadata
 *
 *   - `id = "fhir-polling:<source-id>"` — namespaced by the configured
 *     source id so each polling bean has a distinct identity in the
 *     operator UI's plugin listing. The HL7 v2 plugin gets one id
 *     because there's only ever one MLLP listener; FHIR polling
 *     plausibly has many.
 *   - `supplier = FIRST_PARTY` — bundled in the canonical
 *     subscription-service distribution.
 *   - `schemaVersion = 1` — first SPI shape we author against.
 *   - `version` — module artifact version read from the JAR manifest;
 *     falls back to `0.1.0-SNAPSHOT` when running from an IDE / unit
 *     tests with no manifest available.
 */
class FhirPollingIngestSource(
    private val config: FhirPollingSourceConfig,
    searchExecutor: FhirSearchExecutor,
    highWaterMarkStore: HighWaterMarkStore,
) : IngestSource {

    private val log = LoggerFactory.getLogger(FhirPollingIngestSource::class.java)

    /**
     * The scheduler does the actual work — this class is a thin SPI
     * adapter. Constructed up-front so `meta`/`protocol` reads don't
     * race with start(): both are queried before start() during the
     * registry's logging phase.
     */
    private val scheduler: FhirPollingScheduler = FhirPollingScheduler(
        config = config,
        searchExecutor = searchExecutor,
        highWaterMarkStore = highWaterMarkStore,
    )

    override val meta: PluginMeta = PluginMeta(
        id = "fhir-polling:${config.id}",
        version = readPackageVersion(),
        schemaVersion = 1,
        supplier = PluginSupplier.FIRST_PARTY,
        description = "FHIR R4 polling ingest source (Athena foundation, Epic #425)",
    )

    override val protocol: String = FhirPollingScheduler.SOURCE_PROTOCOL

    override fun start(callback: (PipelineMessage) -> Unit) {
        if (!config.enabled) {
            log.info("source id={} is disabled — skipping start", config.id)
            return
        }
        scheduler.start(callback)
    }

    override fun stop() {
        scheduler.stop()
    }

    companion object {
        /**
         * Read the plugin's version from the JAR manifest's
         * `Implementation-Version` attribute. Same trick the HL7 v2
         * plugin uses — when running from exploded test classes the
         * manifest is absent, so we fall back to a semver-shaped
         * sentinel that satisfies the `meta.version` regex check.
         */
        private fun readPackageVersion(): String {
            val v = FhirPollingIngestSource::class.java.`package`?.implementationVersion
            return v ?: "0.1.0-SNAPSHOT"
        }
    }
}
