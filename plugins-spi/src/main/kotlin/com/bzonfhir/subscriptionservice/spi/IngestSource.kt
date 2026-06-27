package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta

/**
 * SPI #2 — Pluggable inbound source.
 *
 * An [IngestSource] is the thing that turns "messages from somewhere out
 * there" into [PipelineMessage]s our pipeline understands. The built-in
 * `hl7v2-mllp` source (today's only ingest, see
 * `interface-engine/.../routes/IngestRoutes.kt`) will be refactored into
 * one of these in a later story; new sources like `fhir-r4-polling`,
 * `athena-native-rest`, or `csv-folder-drop` plug in here.
 *
 * # Lifecycle
 *
 * 1. The runtime instantiates the source at boot and inspects [meta] +
 *    [protocol] to log "starting ingest source X".
 * 2. It calls [start] with a callback the source invokes once per
 *    message it receives. The source MUST NOT block in [start] — that
 *    method's job is to spin up listeners / pollers and return.
 * 3. The runtime calls [stop] on shutdown. [stop] MUST drain any
 *    in-flight work and release the underlying transport (close
 *    sockets, cancel scheduled polls).
 *
 * # Threading
 *
 * The callback may be invoked from any thread the source chooses. The
 * runtime takes care of synchronization downstream (durable persist is
 * `REQUIRES_NEW`); a source can fire the callback from N worker
 * threads in parallel without further coordination.
 *
 * # Backpressure
 *
 * v1 of the SPI does not surface backpressure on this callback. If
 * downstream persistence is overwhelmed the runtime will eventually
 * throw from inside the callback, which the source can choose to
 * propagate to its underlying transport (e.g. send AE on MLLP, return
 * 503 on REST). A future minor version may add an explicit
 * `BackpressureSignal` parameter.
 *
 * # Stability: EXPERIMENTAL
 */
interface IngestSource {

    /**
     * Identity.
     */
    val meta: PluginMeta

    /**
     * Stable protocol identifier this source speaks. Examples:
     *
     *  - `"hl7v2-mllp"` — Apache Camel MLLP listener (today's only source)
     *  - `"fhir-r4-polling"` — periodic GET against a FHIR server
     *  - `"athena-native-rest"` — Athena's proprietary changed-resources API
     *  - `"csv-folder-drop"` — watch a directory for new CSV files
     *
     * Profile YAML's `ingest[].type` references this string. The
     * profile loader matches `ingest[].type` against registered
     * sources' `protocol` to pick which source implements which
     * configured ingest.
     */
    val protocol: String

    /**
     * Spin up the underlying transport and start receiving messages.
     * Invoke [callback] once per message received.
     *
     * MUST return promptly; long-running work belongs in the
     * source's own threads/loops.
     *
     * @param callback Invoked once per inbound message. The source is
     *   expected to construct a [PipelineMessage] with at least
     *   `correlationId`, `receivedAt`, `sourceProtocol = protocol`,
     *   `sourceSystem`, `sourceId`, `raw`, and `contentType` filled
     *   in; [PipelineMessage.attributes] may carry source-specific
     *   metadata.
     */
    fun start(callback: (PipelineMessage) -> Unit)

    /**
     * Tear down. Close sockets, cancel pollers, release thread pools.
     * MUST be idempotent — the runtime may call [stop] multiple times
     * during shutdown.
     */
    fun stop()
}
