package com.bzonfhir.subscriptionservice.observability;

import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import ca.uhn.fhir.interceptor.api.Hook;
import ca.uhn.fhir.interceptor.api.Interceptor;
import ca.uhn.fhir.interceptor.api.Pointcut;
import ca.uhn.fhir.jpa.subscription.model.CanonicalSubscription;
import ca.uhn.fhir.jpa.subscription.model.ResourceDeliveryMessage;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;

/**
 * HAPI interceptor that publishes subscription-side metrics to the Prometheus
 * scrape endpoint (Epic #387, ticket #389).
 *
 * <p>Exposes three series:
 *
 * <ul>
 *   <li>{@code hapi_subscription_active} (gauge) — current count of active
 *       Subscription resources, maintained by the {@link
 *       Pointcut#SUBSCRIPTION_AFTER_ACTIVE_SUBSCRIPTION_REGISTERED} and
 *       {@link Pointcut#SUBSCRIPTION_AFTER_ACTIVE_SUBSCRIPTION_UNREGISTERED}
 *       hooks. A push-style counter is preferable to a polled
 *       {@code SELECT count(*)} because HAPI's subscription registry IS the
 *       source of truth — polling the DAO would round-trip through JPA,
 *       which loses to a simple long increment by every measure.
 *   <li>{@code hapi_subscription_delivery_total} (counter, labels:
 *       {@code outcome=success|failure}, {@code channel_type=...}) —
 *       increments once per delivery attempt at the matching
 *       {@code SUBSCRIPTION_AFTER_REST_HOOK_DELIVERY} (success) or
 *       {@code SUBSCRIPTION_AFTER_DELIVERY_FAILED} (failure) pointcut.
 *   <li>{@code hapi_subscription_delivery_duration_seconds} (timer) — wall-
 *       clock time observed by the after-delivery hook. HAPI doesn't pass
 *       us a pre-computed duration; we estimate it from the moment of the
 *       BEFORE_DELIVERY hook (the closest hook to the start of the work).
 * </ul>
 *
 * <h2>Labels: bounded by design</h2>
 *
 * <p>The metric labels are deliberately tiny:
 *
 * <ul>
 *   <li>{@code outcome} is the two-value enum {@code success|failure}.
 *   <li>{@code channel_type} is HAPI's {@link
 *       ca.uhn.fhir.jpa.subscription.model.CanonicalSubscriptionChannelType}
 *       enum — roughly a half-dozen values in practice.
 * </ul>
 *
 * <p>NOT used as labels:
 *
 * <ul>
 *   <li>Subscription id (one time-series per resource → millions in a busy
 *       multi-tenant cluster).
 *   <li>Endpoint URL (every customer's webhook host is unique).
 *   <li>Error message text (every transient failure mints a new error
 *       string).
 *   <li>Patient/resource ids that the delivery touched (PII risk).
 * </ul>
 *
 * <p>Operators who need per-subscription detail use the matching
 * Subscription resource and {@code /actuator/health}-style endpoints, not
 * a metric label.
 *
 * <h2>Threading note</h2>
 *
 * <p>HAPI's REST-hook deliverer fires the after-delivery hooks from a pool
 * of message-bus consumer threads — typically 2-4 in a default setup. The
 * Micrometer counter / timer types are lock-free / thread-safe, so we
 * register one of each at construction time and the hooks call into them
 * directly with no synchronization.
 *
 * <p>The gauge value is a plain {@link AtomicLong}. {@link MeterRegistry#gauge}
 * binds it via a callback that reads the AtomicLong on every scrape — no
 * stale-value problem because the registered increments / decrements are
 * what mutate it.
 */
@Interceptor
public class SubscriptionMetricsInterceptor {

  /** Names exposed in the Prometheus scrape — referenced by tests. */
  public static final String METRIC_ACTIVE = "hapi.subscription.active";

  public static final String METRIC_DELIVERY_TOTAL = "hapi.subscription.delivery";

  public static final String METRIC_DELIVERY_DURATION = "hapi.subscription.delivery.duration";

  /** SLF4J MDC key the BEFORE-delivery hook stashes its nanoTime under. */
  static final String DELIVERY_START_NANO_KEY = "hapi.subscription.delivery.start.nano";

  private static final Logger log = LoggerFactory.getLogger(SubscriptionMetricsInterceptor.class);

  private final MeterRegistry meterRegistry;

  /**
   * Current active-subscription count. {@link AtomicLong} so the meter
   * registry callback reads it without locking on every scrape.
   *
   * <p>Maintained by the
   * {@link Pointcut#SUBSCRIPTION_AFTER_ACTIVE_SUBSCRIPTION_REGISTERED} /
   * {@code _UNREGISTERED} hooks. HAPI fires these for the lifecycle of every
   * Subscription resource that reaches {@code status=active}, so the count
   * always reflects HAPI's own "what is the registry serving" answer.
   */
  private final AtomicLong activeSubscriptions = new AtomicLong(0);

  public SubscriptionMetricsInterceptor(MeterRegistry meterRegistry) {
    this.meterRegistry = meterRegistry;
    // Register the gauge once at construction. Micrometer's `gauge` helper
    // keeps a weak reference to the source object, but the source here is
    // our owned AtomicLong field — its lifetime matches this bean's, which
    // matches the JVM's.
    meterRegistry.gauge(METRIC_ACTIVE, activeSubscriptions, AtomicLong::doubleValue);
    log.info("SubscriptionMetricsInterceptor initialized; metrics registered on {}",
        meterRegistry.getClass().getSimpleName());
  }

  // -- active gauge ---------------------------------------------------------

  /**
   * Increment the active-subscription counter when HAPI activates one.
   * The CanonicalSubscription argument is unused — we don't add per-id
   * labels (see class javadoc on cardinality) — but HAPI still passes it
   * so we accept it.
   */
  @Hook(Pointcut.SUBSCRIPTION_AFTER_ACTIVE_SUBSCRIPTION_REGISTERED)
  public void onSubscriptionRegistered(CanonicalSubscription subscription) {
    long now = activeSubscriptions.incrementAndGet();
    if (log.isDebugEnabled()) {
      log.debug("subscription registered; active={}", now);
    }
  }

  /**
   * Decrement on de-activation. {@link AtomicLong} handles concurrent
   * register/unregister calls safely; we never let the gauge go negative
   * by clamping at zero, which would only ever fire on a bug in HAPI
   * itself (an unregister with no matching register).
   */
  @Hook(Pointcut.SUBSCRIPTION_AFTER_ACTIVE_SUBSCRIPTION_UNREGISTERED)
  public void onSubscriptionUnregistered(CanonicalSubscription subscription) {
    long updated = activeSubscriptions.updateAndGet(v -> Math.max(0L, v - 1));
    if (log.isDebugEnabled()) {
      log.debug("subscription unregistered; active={}", updated);
    }
  }

  // -- delivery timing ------------------------------------------------------

  /**
   * Stamp a start-of-delivery marker on the {@link ResourceDeliveryMessage}'s
   * attributes so the matching AFTER hook can compute a duration.
   *
   * <p>We don't use MDC because HAPI's delivery threads aren't the request
   * threads, so the per-request MDC values aren't on them. The delivery
   * message itself IS what flows from before-hook to after-hook on the
   * same thread, so it's the natural place to stash the start time.
   */
  @Hook(Pointcut.SUBSCRIPTION_BEFORE_DELIVERY)
  public void onBeforeDelivery(
      CanonicalSubscription subscription, ResourceDeliveryMessage deliveryMessage) {
    deliveryMessage.setAttribute(DELIVERY_START_NANO_KEY, Long.toString(System.nanoTime()));
  }

  /**
   * Record a successful delivery. The {@code outcome=success} counter
   * ticks and the timer records the wall-clock duration from the
   * before-delivery marker.
   */
  @Hook(Pointcut.SUBSCRIPTION_AFTER_REST_HOOK_DELIVERY)
  public void onDeliverySuccess(
      CanonicalSubscription subscription, ResourceDeliveryMessage deliveryMessage) {
    recordDelivery(subscription, deliveryMessage, "success");
  }

  /**
   * Record a failed delivery. HAPI's failure hook is wider than just rest-
   * hook — it also fires for message channel and email channel deliveries —
   * which is what we want: one counter covering every failed attempt.
   *
   * <p>Note the {@code SUBSCRIPTION_AFTER_DELIVERY_FAILED} signature takes
   * an {@code Exception} arg too, which we accept and ignore (HAPI requires
   * the parameter order to match its declaration; the exception itself is
   * not a label — error text is high-cardinality and PII-prone).
   */
  @Hook(Pointcut.SUBSCRIPTION_AFTER_DELIVERY_FAILED)
  public void onDeliveryFailed(
      CanonicalSubscription subscription,
      ResourceDeliveryMessage deliveryMessage,
      Exception exception) {
    recordDelivery(subscription, deliveryMessage, "failure");
  }

  private void recordDelivery(
      CanonicalSubscription subscription, ResourceDeliveryMessage deliveryMessage, String outcome) {
    String channelType = readChannelType(subscription);

    Counter.builder(METRIC_DELIVERY_TOTAL)
        .description("Count of HAPI Subscription delivery attempts by outcome")
        .tag("outcome", outcome)
        .tag("channel_type", channelType)
        .register(meterRegistry)
        .increment();

    Long startNanos = readStartNanos(deliveryMessage);
    if (startNanos != null) {
      long elapsedNanos = System.nanoTime() - startNanos;
      Timer.builder(METRIC_DELIVERY_DURATION)
          .description("HAPI Subscription delivery wall-clock duration")
          .tag("outcome", outcome)
          .tag("channel_type", channelType)
          .register(meterRegistry)
          .record(elapsedNanos, TimeUnit.NANOSECONDS);
    }
  }

  /**
   * Pull the channel type off the CanonicalSubscription enum, falling back
   * to {@code "unknown"} so a missing value never causes a NPE in the
   * counter builder. Package-private so the test can verify the mapping.
   */
  static String readChannelType(CanonicalSubscription subscription) {
    if (subscription == null || subscription.getChannelType() == null) {
      return "unknown";
    }
    return subscription.getChannelType().name();
  }

  /**
   * Read the start-of-delivery nano timestamp stashed by the before hook.
   * Returns null if the BEFORE hook didn't fire for some reason (e.g. an
   * out-of-band delivery path that skips it) — we then skip the timer
   * record but still count the outcome.
   */
  private static Long readStartNanos(ResourceDeliveryMessage deliveryMessage) {
    if (deliveryMessage == null) {
      return null;
    }
    String raw = deliveryMessage.getAttribute(DELIVERY_START_NANO_KEY).orElse(null);
    if (raw == null || raw.isBlank()) {
      return null;
    }
    try {
      return Long.parseLong(raw);
    } catch (NumberFormatException ex) {
      return null;
    }
  }

  // -- test hooks -----------------------------------------------------------

  /** Test hook — direct read of the active-subscription counter. */
  long activeCount() {
    return activeSubscriptions.get();
  }
}
