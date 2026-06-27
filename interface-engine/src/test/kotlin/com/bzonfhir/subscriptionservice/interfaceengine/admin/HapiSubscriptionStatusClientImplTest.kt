package com.bzonfhir.subscriptionservice.interfaceengine.admin

import ca.uhn.fhir.rest.client.api.IGenericClient
import org.assertj.core.api.Assertions.assertThat
import org.hl7.fhir.r4.model.InstantType
import org.hl7.fhir.r4.model.Parameters
import org.hl7.fhir.r4.model.StringType
import org.hl7.fhir.r4.model.Subscription
import org.junit.jupiter.api.Test
import java.util.Date

/**
 * Unit tests for [HapiSubscriptionStatusClientImpl]'s view-assembly logic.
 *
 * The full controller path is covered by [SubscriptionsAdminControllerAuthOffTest]
 * via a fake implementation of the interface. This class exercises the bit
 * that the fake bypasses: parsing a real HAPI R4 Parameters response into
 * the admin-facing [SubscriptionStatusView]. We construct Subscription and
 * Parameters resources by hand (HAPI's R4 model classes are plain
 * setters/getters) and call the package-private builder directly.
 *
 * Mocking the [IGenericClient] is not done here because we only test the
 * pure transformation; the controller-level tests exercise the wire path.
 */
class HapiSubscriptionStatusClientImplTest {

    private val impl: HapiSubscriptionStatusClientImpl =
        HapiSubscriptionStatusClientImpl(
            hapiClient = org.mockito.Mockito.mock(IGenericClient::class.java),
        )

    @Test
    fun `subscriptionStatusView with no Parameters falls back to subscription metadata`() {
        val sub = Subscription().apply {
            setId("Subscription/123")
            status = Subscription.SubscriptionStatus.ACTIVE
            criteria = "Patient?"
            channel = Subscription.SubscriptionChannelComponent().apply {
                type = Subscription.SubscriptionChannelType.RESTHOOK
                endpoint = "https://example.com/notify"
            }
        }
        val view = impl.subscriptionStatusView(sub, statusParameters = null)

        assertThat(view.subscriptionId).isEqualTo("Subscription/123")
        assertThat(view.active).isTrue()
        // Ticket #404: raw status + criteria propagate through.
        assertThat(view.status).isEqualTo("active")
        assertThat(view.criteria).isEqualTo("Patient?")
        assertThat(view.channelType).isEqualTo("rest-hook")
        assertThat(view.endpoint).isEqualTo("https://example.com/notify")
        assertThat(view.deliverySuccessCount).isZero()
        assertThat(view.deliveryFailureCount).isZero()
        assertThat(view.lastAttemptOutcome).isNull()
        assertThat(view.events).isEmpty()
    }

    @Test
    fun `subscriptionStatusView with Subscription error infers last_attempt_outcome=failure`() {
        val sub = Subscription().apply {
            setId("Subscription/err1")
            status = Subscription.SubscriptionStatus.ERROR
            error = "POST returned 503"
            channel = Subscription.SubscriptionChannelComponent().apply {
                type = Subscription.SubscriptionChannelType.RESTHOOK
                endpoint = "https://example.com/down"
            }
        }
        val view = impl.subscriptionStatusView(sub, statusParameters = null)

        assertThat(view.active).isFalse()
        assertThat(view.lastAttemptOutcome).isEqualTo("failure")
        assertThat(view.lastError).isEqualTo("POST returned 503")
    }

    @Test
    fun `subscriptionStatusView parses notificationEvent parameters into delivery events`() {
        val sub = Subscription().apply {
            setId("Subscription/p2")
            status = Subscription.SubscriptionStatus.ACTIVE
            channel = Subscription.SubscriptionChannelComponent().apply {
                type = Subscription.SubscriptionChannelType.RESTHOOK
                endpoint = "https://example.com/notify"
            }
        }

        // Two notificationEvent[] entries: the first is a success (no error
        // sub-part), the second is a failure. The IG-defined wire ordering
        // is oldest-first; we assert that subscriptionStatusView reverses
        // to newest-first.
        val params = Parameters().apply {
            addParameter().apply {
                name = "notificationEvent"
                addPart().apply {
                    name = "timestamp"
                    value = InstantType(Date.from(java.time.Instant.parse("2026-06-26T12:00:00Z")))
                }
            }
            addParameter().apply {
                name = "notificationEvent"
                addPart().apply {
                    name = "timestamp"
                    value = InstantType(Date.from(java.time.Instant.parse("2026-06-26T12:00:30Z")))
                }
                addPart().apply {
                    name = "error"
                    value = StringType("connection refused")
                }
            }
        }

        val view = impl.subscriptionStatusView(sub, statusParameters = params)
        assertThat(view.events).hasSize(2)
        // Newest first.
        assertThat(view.events[0].outcome).isEqualTo("failure")
        assertThat(view.events[0].error).isEqualTo("connection refused")
        assertThat(view.events[1].outcome).isEqualTo("success")
        assertThat(view.events[1].error).isNull()

        assertThat(view.deliverySuccessCount).isEqualTo(1L)
        assertThat(view.deliveryFailureCount).isEqualTo(1L)
        assertThat(view.lastAttemptOutcome).isEqualTo("failure")
        assertThat(view.lastError).isEqualTo("connection refused")
    }
}
