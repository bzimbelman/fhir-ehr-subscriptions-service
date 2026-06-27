package com.bzonfhir.subscriptionservice.spi

import com.bzonfhir.subscriptionservice.spi.meta.PluginMeta
import com.bzonfhir.subscriptionservice.spi.meta.PluginSupplier
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionContext
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionEvent
import com.bzonfhir.subscriptionservice.spi.meta.SubscriptionTrigger
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Instant
import java.util.UUID

class SubscriptionFilterContractTest {

    @Test
    fun `SubscriptionFilter shape compiles and shouldFire receives event and context`() {
        val filter: SubscriptionFilter = object : SubscriptionFilter {
            override val meta = PluginMeta(
                id = "tenant-gate",
                version = "0.0.1",
                schemaVersion = 1,
                supplier = PluginSupplier.COMMERCIAL,
                description = "Test stub tenant gate filter",
            )

            // Only fire if the subscription has a specific tag.
            override fun shouldFire(event: SubscriptionEvent, subscription: SubscriptionContext): Boolean =
                subscription.tags["urgency"] == "stat"
        }

        val event = SubscriptionEvent(
            eventId = UUID.randomUUID().toString(),
            occurredAt = Instant.now(),
            correlationId = UUID.randomUUID().toString(),
            subscriptionId = "Subscription/abc",
            subscriptionCriteria = "Patient?",
            channelType = "rest-hook",
            trigger = SubscriptionTrigger.CREATE,
            resourceType = "Patient",
            resourceId = "123",
            resourceJson = "{}",
        )

        val matching = SubscriptionContext(
            subscriptionId = "Subscription/abc",
            tenantId = "tenantA",
            channelType = "rest-hook",
            tags = mapOf("urgency" to "stat"),
        )
        val nonMatching = matching.copy(tags = mapOf("urgency" to "routine"))

        assertThat(filter.shouldFire(event, matching)).isTrue()
        assertThat(filter.shouldFire(event, nonMatching)).isFalse()
    }
}
