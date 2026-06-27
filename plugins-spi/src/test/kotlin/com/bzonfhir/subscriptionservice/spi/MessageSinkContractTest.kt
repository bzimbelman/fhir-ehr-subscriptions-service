package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import com.bzonfhir.subscriptionservice.spi.meta.SinkOutcome
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionEvent
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionTrigger
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Instant
import java.util.UUID

class MessageSinkContractTest {

    @Test
    fun `MessageSink shape compiles and Delivered outcome carries external id`() {
        val sink: MessageSink = object : MessageSink {
            override val meta = PluginMeta(
                id = "test-kafka",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMUNITY,
                description = "Test stub Kafka sink",
            )

            override fun handle(event: SubscriptionEvent): SinkOutcome =
                SinkOutcome.Delivered(externalId = "kafka-offset-42")
        }

        val outcome = sink.handle(sampleEvent())

        // Sealed `when` is exhaustive — that's the whole point.
        val externalId = when (outcome) {
            is SinkOutcome.Delivered -> outcome.externalId
            is SinkOutcome.Failed -> null
        }
        assertThat(externalId).isEqualTo("kafka-offset-42")
    }

    @Test
    fun `MessageSink failed outcome distinguishes retryable from permanent`() {
        val sink: MessageSink = object : MessageSink {
            override val meta = PluginMeta(
                id = "test-rest",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMUNITY,
                description = "Test stub REST sink",
            )

            override fun handle(event: SubscriptionEvent): SinkOutcome =
                SinkOutcome.Failed(reason = "downstream returned 400", retryable = false)
        }

        val outcome = sink.handle(sampleEvent()) as SinkOutcome.Failed
        assertThat(outcome.retryable).isFalse()
        assertThat(outcome.reason).contains("400")
    }

    private fun sampleEvent() = SubscriptionEvent(
        eventId = UUID.randomUUID().toString(),
        occurredAt = Instant.now(),
        correlationId = UUID.randomUUID().toString(),
        subscriptionId = "Subscription/abc",
        subscriptionCriteria = "Patient?",
        channelType = "rest-hook",
        trigger = SubscriptionTrigger.UPDATE,
        resourceType = "Patient",
        resourceId = "123",
        resourceJson = """{"resourceType":"Patient","id":"123"}""",
    )
}
