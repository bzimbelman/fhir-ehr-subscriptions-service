package com.bzonfhir.subscriptionservice.plugins.fhirpolling

import com.bzonfhir.subscriptionservice.plugins.fhirpolling.config.FhirPollingSourceConfig
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import org.hl7.fhir.r4.model.Resource
import org.slf4j.LoggerFactory
import java.time.Instant
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * Drives one polling source. Ticks every
 * [FhirPollingSourceConfig.pollIntervalSeconds]; on each tick:
 *
 *   1. Reads the high-water mark for this source's id.
 *   2. Executes the configured search via [FhirSearchExecutor].
 *   3. For each Bundle entry, builds a [PipelineMessage] and hands it
 *      to the [callback] supplied at start time.
 *   4. Advances the high-water mark to the newest
 *      `Resource.meta.lastUpdated` in the bundle (or leaves it alone
 *      if the bundle is empty).
 *
 * ## Why a manual ScheduledExecutorService, not @Scheduled
 *
 * Spring's `@Scheduled` is a context-startup feature — the framework
 * scans @Scheduled methods on bean post-processing. But the SPI
 * lifecycle is `construct -> start(callback) -> stop`, and the
 * callback isn't known until start() runs. We could keep a nullable
 * callback field and have @Scheduled call it (no-op when null), but
 * that's two state machines (scheduler-state and callback-state) that
 * need to stay in sync. A `ScheduledExecutorService` whose lifecycle
 * mirrors start/stop is simpler: one state machine, no concept of
 * "ticking but with nothing to do."
 *
 * ## fixedDelay, not fixedRate
 *
 * `scheduleWithFixedDelay` queues the next tick `delay` after the
 * PREVIOUS tick finishes. `scheduleAtFixedRate` queues it `delay`
 * after the previous tick STARTED. We pick fixedDelay because if
 * the FHIR server is slow (say 30s) but the configured interval is
 * 60s, fixedRate would let two polls overlap; fixedDelay serializes
 * them so we never have two in-flight requests for the same source.
 * For low-volume backends that's a safer default; an operator who
 * wants overlap can lower the interval.
 *
 * ## Failure handling
 *
 * If the search execute() throws (HTTP 4xx, network blip, parse
 * error), the tick logs the failure and returns without advancing
 * the high-water mark. The next tick retries the same search. This
 * is the cheapest possible retry policy — no exponential backoff,
 * no max-retry counter — because a polling source is intrinsically
 * eventually-consistent: if a tick fails, the next one (60s later
 * by default) will catch the same set of resources.
 *
 * Individual callback failures (the host's persist throws) ALSO
 * don't advance the mark — same logic, the next tick will retry.
 */
class FhirPollingScheduler(
    private val config: FhirPollingSourceConfig,
    private val searchExecutor: FhirSearchExecutor,
    private val highWaterMarkStore: HighWaterMarkStore,
) {

    private val log = LoggerFactory.getLogger(FhirPollingScheduler::class.java)

    /**
     * Holds the running executor service when started. Null when
     * stopped. AtomicReference to make start/stop idempotent and
     * thread-safe — the SPI says stop() may be called multiple times
     * during shutdown, and our restart-cycle test (start after stop)
     * needs this to swap cleanly.
     */
    private val executor: AtomicReference<ScheduledExecutorService?> = AtomicReference(null)

    /**
     * Start ticking. [callback] is invoked once per Bundle entry the
     * scheduler fetches. Returns promptly; the tick loop runs on the
     * executor's thread.
     *
     * No-op if already started — defensive against a double-call from
     * the host's lifecycle (the SPI contract doesn't forbid it but
     * doing the wrong thing here would queue two parallel pollers).
     */
    fun start(callback: (PipelineMessage) -> Unit) {
        if (executor.get() != null) {
            log.warn("scheduler for source {} already started; ignoring", config.id)
            return
        }
        val svc = Executors.newSingleThreadScheduledExecutor { runnable ->
            Thread(runnable, "fhir-polling-${config.id}").apply {
                // Daemon so a hung tick doesn't keep the JVM up at
                // shutdown — stop() will request shutdown but the
                // host's @PreDestroy ordering can be flaky.
                isDaemon = true
            }
        }
        if (!executor.compareAndSet(null, svc)) {
            // Lost a race with a parallel start(). Discard our svc
            // and bail — the winning start() owns the lifecycle.
            svc.shutdown()
            log.warn("scheduler for source {} lost start race; backing off", config.id)
            return
        }

        log.info(
            "starting fhir polling source id={} interval={}s baseUrl={} search={}",
            config.id,
            config.pollIntervalSeconds,
            config.baseUrl,
            config.search,
        )

        // fixedDelay so a slow FHIR server can't cause overlapping
        // polls. Initial delay 0 — first tick happens immediately so
        // the operator sees activity right away after boot.
        svc.scheduleWithFixedDelay(
            { tickSafe(callback) },
            0L,
            config.pollIntervalSeconds,
            TimeUnit.SECONDS,
        )
    }

    /**
     * Stop ticking. Idempotent. Waits up to 5s for the in-flight tick
     * (if any) to finish before forcing shutdown.
     */
    fun stop() {
        val svc = executor.getAndSet(null) ?: return
        log.info("stopping fhir polling source id={}", config.id)
        svc.shutdown()
        if (!svc.awaitTermination(5, TimeUnit.SECONDS)) {
            log.warn("scheduler for source {} did not stop within 5s; forcing", config.id)
            svc.shutdownNow()
        }
    }

    /**
     * Wrap [tick] so a thrown exception doesn't kill the scheduled
     * task. ScheduledExecutorService cancels the task on the FIRST
     * uncaught exception from its runnable — a single 500 from the
     * FHIR server would silently stop polling forever without this
     * guard.
     */
    private fun tickSafe(callback: (PipelineMessage) -> Unit) {
        try {
            tick(callback)
        } catch (ex: Exception) {
            log.error(
                "tick failed for source id={}: {} — will retry on next interval",
                config.id,
                ex.message,
                ex,
            )
        }
    }

    /**
     * One poll: fetch, emit, advance. Visible-for-testing — the
     * end-to-end test calls this directly so it doesn't have to wait
     * for the scheduler's interval to elapse.
     */
    internal fun tick(callback: (PipelineMessage) -> Unit) {
        val mark = highWaterMarkStore.getMark(config.id)
        log.debug("polling source id={} from mark={}", config.id, mark)

        val bundle = searchExecutor.execute(config.search, mark)
        val entryCount = bundle.entry?.size ?: 0
        log.info(
            "poll completed source id={} entries={} mark={}",
            config.id,
            entryCount,
            mark,
        )

        for (entry in bundle.entry.orEmpty()) {
            val resource = entry.resource ?: continue
            val msg = buildPipelineMessage(resource)
            callback(msg)
        }

        searchExecutor.newestLastUpdated(bundle)?.let { newest ->
            highWaterMarkStore.updateMark(config.id, newest)
            log.debug("advanced mark source id={} -> {}", config.id, newest)
        }
    }

    /**
     * Turn a FHIR resource into the canonical [PipelineMessage] the
     * engine expects.
     *
     * Attribute keys:
     *
     *   - `fhir.resourceType` / `fhir.resourceId` — the namespaced
     *     keys per ticket #434's spec.
     *   - `fhir.lastUpdated` — ISO-8601 instant; helps the operator UI
     *     reason about which version of the resource we have.
     *   - `hl7.messageType` — a compatibility shim, set to the FHIR
     *     resource type. The engine's `IngestSourceRegistry` (pre-#434
     *     code) requires this attribute to be non-empty so it can
     *     stamp the row's `message_type` column. A follow-up ticket
     *     will refactor the registry to be source-agnostic; until
     *     then the shim lets the FHIR plugin coexist with the HL7 v2
     *     plugin in the same host without registry changes.
     */
    private fun buildPipelineMessage(resource: Resource): PipelineMessage {
        val resourceType = resource.fhirType()
        val resourceId = resource.idElement?.idPart.orEmpty()
        val lastUpdated = resource.meta?.lastUpdated?.toInstant()

        // The (sourceSystem, sourceId) pair MUST be unique within a
        // tenant — that's the engine's idempotency key. For polled
        // FHIR resources the natural sourceId is the resource's
        // logical id; sourceSystem is the configured value from YAML
        // (e.g. "athena" or "lab-x"). If the resource has no id
        // (impossible per FHIR R4 spec, but defensive), fall back to
        // a random UUID — the row gets persisted, the operator can
        // chase the missing id later.
        val sourceId = if (resourceId.isNotEmpty()) {
            resourceId
        } else {
            log.warn(
                "fhir resource without id encountered source={} type={}; assigning random sourceId",
                config.id,
                resourceType,
            )
            UUID.randomUUID().toString()
        }

        val attributes = buildMap {
            put("fhir.resourceType", resourceType)
            put("fhir.resourceId", resourceId.ifEmpty { sourceId })
            put("fhir.pollingSourceId", config.id)
            lastUpdated?.let { put("fhir.lastUpdated", it.toString()) }
            // Compatibility shim — see KDoc on buildPipelineMessage().
            put("hl7.messageType", resourceType)
        }

        return PipelineMessage(
            correlationId = UUID.randomUUID().toString(),
            receivedAt = Instant.now(),
            sourceProtocol = SOURCE_PROTOCOL,
            sourceSystem = config.sourceSystem,
            sourceId = sourceId,
            raw = searchExecutor.serializeResource(resource),
            contentType = CONTENT_TYPE,
            attributes = attributes,
        )
    }

    companion object {
        const val SOURCE_PROTOCOL: String = "fhir-r4-polling"
        const val CONTENT_TYPE: String = "application/fhir+json"
    }
}
