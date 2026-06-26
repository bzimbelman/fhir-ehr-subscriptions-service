package com.bzonfhir.subscriptionservice.observability;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscriptionChannelType;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Timer;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;

/**
 * Unit tests for {@link SubscriptionMetricsInterceptor} (Epic #387, ticket #389).
 *
 * <p>Exercises the four observable surfaces of the interceptor:
 *
 * <ol>
 *   <li>The active gauge increments on REGISTERED and decrements on UNREGISTERED, with a
 *       floor at zero to defend against an unbalanced unregister.
 *   <li>{@code hapi_subscription_delivery_total{outcome="success", channel_type=...}}
 *       increments after a successful REST-hook delivery.
 *   <li>{@code outcome="failure"} variant increments after a failed delivery.
 *   <li>{@code hapi_subscription_delivery_duration_seconds} timer records a non-zero
 *       sample when BEFORE and AFTER fire in sequence.
 * </ol>
 *
 * <p>We use a {@link SimpleMeterRegistry} so the test stays in-process: no HAPI server,
 * no Prometheus registry. The contract being tested is the producer surface (call this
 * hook → see this metric in the registry); the Prometheus exposition format is exercised
 * elsewhere (production e2e + the interface-engine's PrometheusMetricsTest).
 */
class SubscriptionMetricsInterceptorTest {

  private SimpleMeterRegistry registry;
  private SubscriptionMetricsInterceptor interceptor;

  @BeforeEach
  void setUp() {
    registry = new SimpleMeterRegistry();
    interceptor = new SubscriptionMetricsInterceptor(registry);
  }

  // -- active gauge ----------------------------------------------------------

  @Test
  void activeGaugeIncrementsOnRegister() {
    CanonicalSubscription sub = restHookSubscription();
    interceptor.onSubscriptionRegistered(sub);
    interceptor.onSubscriptionRegistered(sub);
    interceptor.onSubscriptionRegistered(sub);

    assertThat(interceptor.activeCount()).isEqualTo(3L);

    // And via the public registry surface — what Prometheus would see.
    Double gaugeValue =
        registry.find(SubscriptionMetricsInterceptor.METRIC_ACTIVE).gauge().value();
    assertThat(gaugeValue).isEqualTo(3.0);
  }

  @Test
  void activeGaugeDecrementsOnUnregister() {
    CanonicalSubscription sub = restHookSubscription();
    interceptor.onSubscriptionRegistered(sub);
    interceptor.onSubscriptionRegistered(sub);
    interceptor.onSubscriptionUnregistered(sub);

    assertThat(interceptor.activeCount()).isEqualTo(1L);
  }

  @Test
  void activeGaugeFloorsAtZero() {
    // An unbalanced unregister (a HAPI bug, but we shouldn't crash or go
    // negative — Prometheus gauges are conventionally non-negative for
    // count-of-things metrics, and a negative value would mislead alerting).
    CanonicalSubscription sub = restHookSubscription();
    interceptor.onSubscriptionUnregistered(sub);
    interceptor.onSubscriptionUnregistered(sub);

    assertThat(interceptor.activeCount()).isZero();
  }

  // -- delivery counters -----------------------------------------------------

  @Test
  void deliveryCounterIncrementsOnSuccess() {
    CanonicalSubscription sub = restHookSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    interceptor.onBeforeDelivery(sub, msg);
    interceptor.onDeliverySuccess(sub, msg);

    Counter counter =
        registry
            .find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_TOTAL)
            .tag("outcome", "success")
            .tag("channel_type", "RESTHOOK")
            .counter();
    assertThat(counter).isNotNull();
    assertThat(counter.count()).isEqualTo(1.0);
  }

  @Test
  void deliveryCounterIncrementsOnFailure() {
    CanonicalSubscription sub = restHookSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    interceptor.onBeforeDelivery(sub, msg);
    interceptor.onDeliveryFailed(sub, msg, new RuntimeException("simulated 500"));

    Counter counter =
        registry
            .find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_TOTAL)
            .tag("outcome", "failure")
            .tag("channel_type", "RESTHOOK")
            .counter();
    assertThat(counter).isNotNull();
    assertThat(counter.count()).isEqualTo(1.0);
  }

  @Test
  void deliveryTimerRecordsDuration() {
    CanonicalSubscription sub = restHookSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    interceptor.onBeforeDelivery(sub, msg);
    // Small sleep so System.nanoTime delta is observable. Micrometer will
    // record whatever we observe (down to nanos); even a no-op would still
    // record a positive count, but >0 duration is more honest.
    sleepMillis(5);
    interceptor.onDeliverySuccess(sub, msg);

    Timer timer =
        registry
            .find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_DURATION)
            .tag("outcome", "success")
            .tag("channel_type", "RESTHOOK")
            .timer();
    assertThat(timer).isNotNull();
    assertThat(timer.count()).isEqualTo(1L);
    assertThat(timer.totalTime(java.util.concurrent.TimeUnit.MILLISECONDS)).isGreaterThan(0.0);
  }

  @Test
  void deliveryWithoutBeforeStillCountsButSkipsTimer() {
    // An out-of-band delivery path (e.g. a HAPI version that doesn't fire
    // BEFORE_DELIVERY in some failure mode). The counter must still
    // increment so operators see the attempt; the timer record is skipped
    // because we have no start nanos to compute against.
    CanonicalSubscription sub = restHookSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    // intentionally NO onBeforeDelivery call
    interceptor.onDeliverySuccess(sub, msg);

    Counter counter =
        registry
            .find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_TOTAL)
            .tag("outcome", "success")
            .counter();
    assertThat(counter).isNotNull();
    assertThat(counter.count()).isEqualTo(1.0);

    // Timer either absent or count==0 — both are acceptable; just no
    // sample on this delivery.
    Timer timer = registry.find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_DURATION).timer();
    if (timer != null) {
      assertThat(timer.count()).isZero();
    }
  }

  @Test
  void channelTypeFallbackForUnknown() {
    // Defensive: a CanonicalSubscription with no channel type set must not
    // NPE the counter builder; falls back to "unknown" so the metric is
    // still observable.
    CanonicalSubscription sub = new CanonicalSubscription();
    ResourceDeliveryMessage msg = new ResourceDeliveryMessage();
    interceptor.onBeforeDelivery(sub, msg);
    interceptor.onDeliverySuccess(sub, msg);

    Counter counter =
        registry
            .find(SubscriptionMetricsInterceptor.METRIC_DELIVERY_TOTAL)
            .tag("channel_type", "unknown")
            .counter();
    assertThat(counter).isNotNull();
    assertThat(counter.count()).isEqualTo(1.0);
  }

  // -- helpers ---------------------------------------------------------------

  private static CanonicalSubscription restHookSubscription() {
    CanonicalSubscription sub = new CanonicalSubscription();
    sub.setChannelType(CanonicalSubscriptionChannelType.RESTHOOK);
    return sub;
  }

  private static void sleepMillis(long ms) {
    try {
      Thread.sleep(ms);
    } catch (InterruptedException ex) {
      Thread.currentThread().interrupt();
    }
  }
}
