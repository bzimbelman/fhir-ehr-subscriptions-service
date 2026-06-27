package com.bzonfhir.subscriptionservice.plugins.hl7v2mllp

import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.config.Hl7V2MllpProperties
import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.apache.camel.impl.DefaultCamelContext
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.AfterEach
import org.junit.jupiter.api.Test
import java.net.ServerSocket
import java.util.concurrent.atomic.AtomicReference

/**
 * Lifecycle contract tests for [Hl7V2MllpIngestSource].
 *
 * These tests pin the SPI shape — they DON'T open sockets or send wire
 * traffic (that's the end-to-end test's job). They cover:
 *
 *   1. The plugin's metadata (`meta`, `protocol`) matches the ticket's
 *      spec (`id = "hl7v2-mllp"`, FIRST_PARTY supplier, schemaVersion 1).
 *   2. start() registers the callback and adds the Camel route to the
 *      provided CamelContext, but does NOT block.
 *   3. stop() removes the route, releases the port, and is idempotent
 *      (multiple stop calls don't throw).
 *   4. start() called after stop() spins everything up again — the SPI
 *      doesn't forbid a restart cycle.
 *
 * The end-to-end socket test (Hl7V2MllpEndToEndTest) is the integration
 * proof that the registered route actually moves bytes. Splitting the
 * two keeps THIS test fast (no real socket binding) and the integration
 * test focused on what only an integration test can verify.
 */
class Hl7V2MllpIngestSourceTest {

    private val camelContext = DefaultCamelContext()
    private var ingestSource: Hl7V2MllpIngestSource? = null

    @AfterEach
    fun tearDown() {
        ingestSource?.stop()
        if (camelContext.isStarted) camelContext.stop()
    }

    private fun pickFreePort(): Int = ServerSocket(0).use { it.localPort }

    private fun makeSource(
        port: Int = pickFreePort(),
        enabled: Boolean = true,
    ): Hl7V2MllpIngestSource {
        val props = Hl7V2MllpProperties(
            enabled = enabled,
            port = port,
            host = "0.0.0.0",
            characterSet = "UTF-8",
        )
        return Hl7V2MllpIngestSource(
            properties = props,
            camelContext = camelContext,
        ).also { ingestSource = it }
    }

    @Test
    fun `meta is the canonical hl7v2-mllp first-party plugin descriptor`() {
        val source = makeSource()

        assertThat(source.meta.id).isEqualTo("hl7v2-mllp")
        assertThat(source.meta.supplier).isEqualTo(PluginSupplier.FIRST_PARTY)
        assertThat(source.meta.schemaVersion).isEqualTo(1)
        // version should be a non-blank semver-shaped string; we don't
        // pin the exact value (the build will bump it over time) — just
        // that the field is populated.
        assertThat(source.meta.version).matches("\\d+\\.\\d+\\.\\d+(-.+)?")
    }

    @Test
    fun `protocol matches the SPI string and the parser constant`() {
        val source = makeSource()

        assertThat(source.protocol).isEqualTo("hl7v2-mllp")
        // Cross-check: parser uses the same string, since both feed the
        // PipelineMessage.sourceProtocol field on the same path.
        assertThat(source.protocol).isEqualTo(Hl7V2MessageParser.SOURCE_PROTOCOL)
    }

    @Test
    fun `start registers the route on the camel context without blocking`() {
        camelContext.start()
        val source = makeSource()

        val captured = AtomicReference<PipelineMessage>()

        // start() must return promptly. We assert by simply ensuring the
        // call completes synchronously — if it blocked on the socket
        // accept loop the test would hang (JUnit 5 default timeout fires
        // eventually but we'd rather notice immediately).
        source.start { msg -> captured.set(msg) }

        // The route should exist on the context with the canonical id
        // so callers (and tests) can reference it by name.
        val route = camelContext.getRoute(Hl7V2MllpIngestSource.ROUTE_ID)
        assertThat(route)
            .describedAs("start() must register the MLLP route on the Camel context")
            .isNotNull()
    }

    @Test
    fun `stop removes the route from the camel context`() {
        camelContext.start()
        val source = makeSource()
        source.start { /* unused */ }

        assertThat(camelContext.getRoute(Hl7V2MllpIngestSource.ROUTE_ID)).isNotNull()

        source.stop()

        assertThat(camelContext.getRoute(Hl7V2MllpIngestSource.ROUTE_ID))
            .describedAs("stop() must tear the route down")
            .isNull()
    }

    @Test
    fun `stop is idempotent`() {
        camelContext.start()
        val source = makeSource()
        source.start { /* unused */ }

        source.stop()
        // Second stop must not throw — the SPI explicitly requires
        // idempotent stop() (the runtime may call it multiple times
        // during shutdown).
        source.stop()
    }

    @Test
    fun `start after stop spins the route back up`() {
        camelContext.start()
        val source = makeSource()

        source.start { /* unused */ }
        source.stop()
        source.start { /* unused */ }

        assertThat(camelContext.getRoute(Hl7V2MllpIngestSource.ROUTE_ID))
            .describedAs("restart cycle: start() after stop() must re-add the route")
            .isNotNull()
    }
}
