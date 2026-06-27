package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PipelineMessage
import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Instant
import java.util.UUID

class IngestSourceContractTest {

    @Test
    fun `IngestSource shape compiles and start delivers messages via callback`() {
        val received = mutableListOf<PipelineMessage>()
        var stopped = false

        val source: IngestSource = object : IngestSource {
            override val meta = PluginMeta(
                id = "test-mllp",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.FIRST_PARTY,
                description = "Test stub MLLP ingest",
            )
            override val protocol = "hl7v2-mllp"

            override fun start(callback: (PipelineMessage) -> Unit) {
                callback(
                    PipelineMessage(
                        correlationId = UUID.randomUUID().toString(),
                        receivedAt = Instant.now(),
                        sourceProtocol = protocol,
                        sourceSystem = "EPIC",
                        sourceId = "MSG0001",
                        raw = "MSH|^~\\&|EPIC|...".toByteArray(),
                        contentType = "application/hl7-v2",
                        attributes = mapOf("hl7.messageType" to "ADT_A04"),
                    ),
                )
            }

            override fun stop() {
                stopped = true
            }
        }

        source.start { received += it }
        source.stop()

        assertThat(received).hasSize(1)
        val msg = received.single()
        assertThat(msg.sourceProtocol).isEqualTo("hl7v2-mllp")
        assertThat(msg.sourceSystem).isEqualTo("EPIC")
        assertThat(msg.attributes["hl7.messageType"]).isEqualTo("ADT_A04")
        assertThat(stopped).isTrue()
    }
}
