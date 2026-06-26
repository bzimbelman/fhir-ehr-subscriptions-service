package com.bzonfhir.subscriptionservice.interfaceengine.observability

import io.micrometer.core.instrument.MeterRegistry
import org.slf4j.LoggerFactory
import org.springframework.beans.factory.annotation.Value
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.scheduling.annotation.Scheduled
import org.springframework.stereotype.Component
import java.util.concurrent.atomic.AtomicLong

/**
 * Gauge for `interface_engine_dlq_current_size` (Epic #387, ticket #389).
 *
 * Polls Postgres at a configurable interval and exposes the current count of
 * `ingested_messages` rows in `status=DEAD_LETTER` as a Prometheus gauge.
 *
 * ## Why a polled gauge instead of a callback gauge?
 *
 * Micrometer offers two shapes for gauges:
 *
 *   A) Callback: `meterRegistry.gauge(name, supplier)` — Micrometer invokes
 *      the supplier on every Prometheus scrape (typically 30s in our chart).
 *      Simpler, no scheduler needed.
 *
 *   B) Polled: a @Scheduled method writes to an [AtomicLong] held by the
 *      gauge; Prometheus reads the AtomicLong at scrape time.
 *
 * We picked B. Reasons:
 *
 *   - The callback shape runs the SQL on the Prometheus scrape thread, which
 *     means one slow query starves the whole `/actuator/prometheus`
 *     response. With multiple scrapers (federation, Grafana) that compound.
 *   - Decoupling poll interval from scrape interval lets us tune them
 *     independently — typical setup: 30s scrape, 30s poll. If DLQ load
 *     spikes we can drop the poll to 5s without changing the scrape
 *     contract operators see.
 *   - The polled count IS stale by definition between polls, but the DLQ
 *     gauge is a low-frequency alerting signal (e.g. "alert if
 *     dlq_current_size > 100 for 5m") — 30s of staleness is well below
 *     the 5-minute window most alerts use.
 *
 * ## Why not a `count` SQL on @PostConstruct only?
 *
 * The gauge must REFLECT changes to the DLQ — a row added by the worker
 * needs to show up in the metric on the next scrape. Static init won't do
 * that.
 *
 * ## Gated by `subscription-service.observability.metrics.enabled`
 *
 * Default ON. Operators in a non-Prometheus environment can flip it off
 * to skip the scheduled SELECT. Same toggle should disable the rest of
 * the metrics scheduler family if we add more.
 */
@Component
@ConditionalOnProperty(
    prefix = "subscription-service.observability.metrics",
    name = ["enabled"],
    havingValue = "true",
    matchIfMissing = true,
)
class DlqSizeGauge(
    private val jdbc: JdbcTemplate,
    meterRegistry: MeterRegistry,
    @Value("\${subscription-service.observability.metrics.dlq-poll-ms:30000}")
    private val pollIntervalMs: Long,
) {

    private val log = LoggerFactory.getLogger(DlqSizeGauge::class.java)

    /**
     * The AtomicLong holding the latest poll value. Wrapped by the gauge
     * registered below; Prometheus reads it directly at scrape time, so
     * the read path is allocation-free and lock-free.
     */
    private val currentSize: AtomicLong = AtomicLong(0)

    init {
        // Register the gauge ONCE at bean construction. Micrometer's
        // `gauge` helper holds a weak reference to the source object, but
        // passing an AtomicLong (which the component itself owns) keeps
        // the gauge alive for the lifetime of the bean — i.e. the
        // lifetime of the app.
        meterRegistry.gauge(
            InterfaceEngineMetrics.METRIC_DLQ_CURRENT_SIZE,
            currentSize,
        ) { it.get().toDouble() }
    }

    /**
     * Poll Postgres for the current DLQ size. Runs at a fixed delay so
     * one slow query doesn't get re-overlapped by the next tick.
     *
     * The query is intentionally trivial: `COUNT(*) WHERE status =
     * 'DEAD_LETTER'`. With the standard idx on `status` this is a single
     * index scan even on multi-million-row tables.
     *
     * Errors are logged and swallowed — a DB blip mustn't crash the
     * scheduler thread; the next tick will retry. The gauge keeps its
     * last good value, which is the right behavior for a monitoring
     * metric (better stale-but-monotonic than NaN).
     */
    @Scheduled(
        fixedDelayString = "\${subscription-service.observability.metrics.dlq-poll-ms:30000}",
        initialDelayString = "\${subscription-service.observability.metrics.dlq-poll-initial-ms:1000}",
    )
    fun poll() {
        try {
            val count = jdbc.queryForObject(
                "SELECT count(*) FROM ingested_messages WHERE status = 'DEAD_LETTER'",
                Long::class.java,
            ) ?: 0L
            currentSize.set(count)
            log.debug("dlq_current_size poll: {}", count)
        } catch (ex: Exception) {
            // Don't propagate — preserve the last good value.
            log.warn("dlq_current_size poll failed: {}", ex.message)
        }
    }

    /**
     * Test hook — let a test force a poll synchronously rather than
     * sleeping for the next scheduled tick.
     */
    internal fun pollNow() = poll()

    /** Test hook — read the latest polled value without going through Prometheus. */
    internal fun currentValue(): Long = currentSize.get()
}
