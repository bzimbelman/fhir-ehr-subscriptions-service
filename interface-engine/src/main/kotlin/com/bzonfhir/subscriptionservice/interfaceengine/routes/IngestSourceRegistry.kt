package com.bzonfhir.subscriptionservice.interfaceengine.routes

import com.bzonfhir.subscriptionservice.interfaceengine.observability.CorrelationId
import com.bzonfhir.subscriptionservice.interfaceengine.observability.OtelTracing
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestPersistService
import com.bzonfhir.subscriptionservice.interfaceengine.persistence.IngestedMessageSourceProtocol
import com.bzonfhir.subscriptionservice.spi.IngestSource
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import io.opentelemetry.api.trace.Span
import io.opentelemetry.api.trace.StatusCode
import io.opentelemetry.context.Scope
import jakarta.annotation.PreDestroy
import org.slf4j.LoggerFactory
import org.slf4j.MDC
import org.springframework.boot.context.event.ApplicationReadyEvent
import org.springframework.context.event.EventListener
import org.springframework.stereotype.Component

/**
 * Discovers every [IngestSource] Spring registered (from plugin
 * auto-configs) and wires each one into the interface engine's persist
 * pipeline (Epic #425, ticket #431).
 *
 * Before #431 the receive path was an inline `RouteBuilder`
 * (`IngestRoutes.kt`) that talked directly to `IngestPersistService`.
 * Refactoring the MLLP listener into the `hl7v2-mllp` built-in plugin
 * meant separating the "where bytes come from" half (now SPI) from the
 * "what we do with the parsed PipelineMessage" half (still here, this
 * registry's callback). The registry is the bridge.
 *
 * ## What the callback does
 *
 * For each [PipelineMessage] the plugin produces, the callback:
 *
 *   1. Establishes the correlation_id on MDC for the duration of the
 *      persist (so every log line emitted by the persist service +
 *      OTel span code carries the same id the plugin generated).
 *   2. Opens an OTel root span `mllp.receive` and stamps the
 *      messaging.* + source.* + correlation_id attributes (same
 *      attributes the legacy `IngestRoutes.persistMessage` set).
 *   3. Captures the W3C `traceparent` for the active span and persists
 *      it on the row alongside the correlation_id.
 *   4. Calls [IngestPersistService.persistReceived] which is
 *      REQUIRES_NEW so the row commits before the SPI callback returns.
 *   5. On idempotent-duplicate, swaps the freshly-minted correlation_id
 *      for the persisted row's value so subsequent log lines on this
 *      thread carry the original receive's id (matches legacy
 *      behaviour exactly).
 *   6. Always clears MDC on exit so a sibling exchange running on the
 *      same thread doesn't inherit the value.
 *
 * If the callback throws (DB unreachable, integrity violation that
 * isn't a duplicate), the SPI's route-level error handler catches it
 * and converts to an AE ACK. The PipelineMessage never lands in
 * `ingested_messages`, the metrics counter doesn't tick, and the span
 * is closed with status=ERROR.
 *
 * ## Why this lives in interface-engine, not in plugins-spi
 *
 * The registry knows things only the host knows: the OTel tracer the
 * SDK auto-configured, the JPA persist service, the correlation_id
 * MDC plumbing, the metric counter. None of that belongs in the
 * plugin module (it would force every plugin to drag in JPA + OTel +
 * metrics — defeating the per-plugin-isolation that Epic #425's
 * refactor is FOR). The registry sits in the host, the plugin sits in
 * its own module, and the SPI is the contract between them.
 *
 * ## Lifecycle
 *
 *   - Spring construction: this @Component is built; it injects every
 *     IngestSource bean Spring discovered (typically just
 *     Hl7V2MllpIngestSource today; future plugins add siblings).
 *   - [start]: invoked from the ApplicationReadyEvent listener so the
 *     plugin's `start()` runs AFTER the rest of the context has come
 *     up. Otherwise the route would try to bind its socket while
 *     Spring is still wiring beans, and a callback might fire before
 *     the JPA EntityManagerFactory is ready.
 *   - [stop]: invoked from @PreDestroy on JVM shutdown so each plugin
 *     gets a chance to drain in-flight work + close sockets cleanly.
 */
@Component
class IngestSourceRegistry(
    private val ingestSources: List<IngestSource>,
    private val persistService: IngestPersistService,
    private val otelTracing: OtelTracing,
) {

    private val log = LoggerFactory.getLogger(IngestSourceRegistry::class.java)

    /**
     * Track which sources we've already started so a duplicate
     * ApplicationReadyEvent (Spring may emit it more than once in
     * unusual contexts — e.g. a refresh) doesn't double-start the
     * underlying transport.
     */
    private val started: MutableSet<String> = mutableSetOf()

    /**
     * Start every registered IngestSource. Invoked from
     * ApplicationReadyEvent — by then JPA + OTel + Camel are all up.
     *
     * Each source gets the [persistCallback] wired in via a tiny
     * closure that captures the source's `protocol` for log lines.
     */
    @EventListener(ApplicationReadyEvent::class)
    fun start() {
        ingestSources.forEach { source ->
            val key = "${source.meta.id}:${source.protocol}"
            if (!started.add(key)) {
                log.warn("ingest source {} already started; skipping", key)
                return@forEach
            }
            log.info(
                "starting ingest source id={} version={} protocol={} supplier={}",
                source.meta.id,
                source.meta.version,
                source.protocol,
                source.meta.supplier,
            )
            // Per-source try/catch so one plugin's start failure
            // doesn't prevent sibling plugins from coming up. The
            // failed source just stays unstarted and the boot log
            // shows the error.
            try {
                source.start { msg -> persistCallback(source, msg) }
            } catch (ex: Exception) {
                log.error("failed to start ingest source {}: {}", key, ex.message, ex)
            }
        }
    }

    /**
     * Stop every started source. Idempotent — calling .stop() on a
     * source that's already stopped is a no-op per the SPI contract.
     */
    @PreDestroy
    fun stop() {
        ingestSources.forEach { source ->
            log.info("stopping ingest source id={} protocol={}", source.meta.id, source.protocol)
            runCatching { source.stop() }
                .onFailure { log.warn("error stopping {}: {}", source.meta.id, it.message) }
        }
        started.clear()
    }

    /**
     * The persist callback the plugin invokes per inbound message.
     *
     * Logic mirrors the legacy `IngestRoutes.persistMessage()` exactly:
     *
     *   - establish correlation_id on MDC,
     *   - open OTel root span + scope,
     *   - call IngestPersistService (REQUIRES_NEW; row commits
     *     before this method returns),
     *   - on idempotent-duplicate, swap to the row's original
     *     correlation_id,
     *   - record exception on span and re-throw on failure (the
     *     plugin's route error handler turns it into AE),
     *   - always close the scope + end the span + clear MDC.
     *
     * The only difference from the legacy method: the source of the
     * (sourceSystem, sourceId, messageType, raw, correlationId)
     * values is now [PipelineMessage] fields instead of Camel
     * exchange headers. Same values, different envelope.
     */
    private fun persistCallback(source: IngestSource, msg: PipelineMessage) {
        // Pre-flight check that the legacy code had as `require(...)`s.
        // We surface the same IllegalArgumentException so the plugin's
        // route error handler produces the same AE reason string.
        require(msg.sourceSystem.isNotEmpty()) { "MSH-3 (sending application) is required" }
        require(msg.sourceId.isNotEmpty()) { "MSH-10 (message control id) is required" }
        val messageType = msg.attributes[ATTR_MESSAGE_TYPE].orEmpty()
        require(messageType.isNotEmpty()) { "MSH-9 (message type) is required" }

        val correlationId = msg.correlationId
        val previousMdc = MDC.get(CorrelationId.MDC_KEY)
        MDC.put(CorrelationId.MDC_KEY, correlationId)

        val sourceProtocol = mapSourceProtocol(source.protocol)
        val rawContentType = msg.contentType
        val rawMessage = msg.raw.toString(Charsets.UTF_8)

        // Start the OTel root span (`mllp.receive`). MLLP isn't trace-
        // aware so we start a new trace here — no parent extraction.
        // The legacy code did this around persistService.persistReceived
        // and so do we; behaviour is byte-identical for tests asserting
        // on emitted spans (OtelTraceTest).
        val receiveSpan: Span = otelTracing.startReceiveSpan(
            sourceSystem = msg.sourceSystem,
            sourceId = msg.sourceId,
            messageType = messageType,
            correlationId = correlationId,
        )
        val scope: Scope = receiveSpan.makeCurrent()
        try {
            val traceContext = otelTracing.encodeCurrentContext()

            val saved = persistService.persistReceived(
                sourceProtocol = sourceProtocol,
                sourceSystem = msg.sourceSystem,
                sourceId = msg.sourceId,
                messageType = messageType,
                rawMessage = rawMessage,
                rawContentType = rawContentType,
                correlationId = correlationId,
                traceContext = traceContext,
            )

            // Idempotent-duplicate path: the persist service returned
            // the previously-persisted row whose correlation_id is
            // from the ORIGINAL receive. We swap the MDC value to
            // that one so any subsequent log lines on this thread
            // (and downstream HTTP X-Correlation-Id headers) line up
            // with the original trace.
            val effective = saved.correlationId ?: correlationId
            if (effective != correlationId) {
                MDC.put(CorrelationId.MDC_KEY, effective)
            }
            log.info(
                "received id={} type={} controlId={} sourceSystem={}",
                saved.id,
                messageType,
                msg.sourceId,
                msg.sourceSystem,
            )
        } catch (ex: Exception) {
            // Surface the failure on the span before re-throwing —
            // Jaeger marks it red rather than completing silently.
            receiveSpan.setStatus(StatusCode.ERROR, ex.message ?: ex.javaClass.simpleName)
            receiveSpan.recordException(ex)
            throw ex
        } finally {
            // Close scope first, then end span — reversed order would
            // leak the span as the active context to whatever runs
            // next on this thread.
            scope.close()
            receiveSpan.end()
            // Restore (or clear) the MDC entry. The legacy route did
            // this via onCompletion; here we do it inline because the
            // callback IS the unit of work.
            if (previousMdc == null) {
                MDC.remove(CorrelationId.MDC_KEY)
            } else {
                MDC.put(CorrelationId.MDC_KEY, previousMdc)
            }
        }
    }

    /**
     * Map the SPI's free-text [IngestSource.protocol] to the JPA
     * enum the row column requires. Today there's only one mapping
     * (`hl7v2-mllp` → `HL7V2_MLLP`); future plugins add cases.
     *
     * Falls back to `OTHER` for unknown protocols so a future plugin
     * doesn't fail-fast at receive time — the row gets persisted with
     * sourceProtocol=OTHER and the operator can fix the mapping in a
     * follow-up.
     */
    private fun mapSourceProtocol(protocol: String): IngestedMessageSourceProtocol =
        when (protocol) {
            "hl7v2-mllp" -> IngestedMessageSourceProtocol.HL7V2_MLLP
            "fhir-rest" -> IngestedMessageSourceProtocol.FHIR_REST
            // Ticket #434 — the fhir-polling plugin emits messages with
            // this protocol string. They share the FHIR_REST persisted
            // column value because, from the engine's perspective, both
            // are "FHIR resources delivered over HTTP" — replay /
            // mapping uses the same parser family.
            "fhir-r4-polling" -> IngestedMessageSourceProtocol.FHIR_REST
            "ehr-native-api" -> IngestedMessageSourceProtocol.EHR_NATIVE_API
            else -> IngestedMessageSourceProtocol.OTHER
        }

    companion object {
        /**
         * Attribute key the HL7 v2 plugin sets on [PipelineMessage.attributes]
         * for the composed message type (e.g. ADT_A04). Other ingest
         * plugins may use different attribute names; future code that
         * wants a generic "message type" abstraction would belong
         * downstream of this registry.
         */
        const val ATTR_MESSAGE_TYPE: String = "hl7.messageType"
    }
}
