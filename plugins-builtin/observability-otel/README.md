# observability-otel — built-in plugin

> Ticket #433, Epic #425.
> Re-expresses the existing OTel + Prometheus + correlation-id wiring as a
> built-in [`ObservabilityEnricher`](../../plugins-spi/src/main/kotlin/com/bzonfhir/subscriptionservice/spi/ObservabilityEnricher.kt)
> implementation.

## What this module owns

| Concern                                                                          | Owner                  |
|----------------------------------------------------------------------------------|------------------------|
| "What log fields should every JSON record have?"                                 | **this plugin**        |
| "What Prometheus labels should each metric series carry?"                        | **this plugin**        |
| OTel SDK init, span helpers                                                      | `interface-engine`     |
| Correlation-id servlet filter / MDC propagation                                  | `interface-engine`     |
| Logback JSON encoder + `customFields` `schema_version` literal                   | `interface-engine`     |
| `/actuator/prometheus` endpoint exposure                                         | `interface-engine`     |
| Scheduled DLQ-size poller                                                        | `interface-engine`     |

The plugin SAYS WHAT to log/label. The infrastructure does the actual
logging and exposing. Cleanly splitting the two means a third-party
plugin can swap or extend the "what" half without touching transport.

## Standard log-field set

Emitted via `enrichLogFields(ctx)` — see [`StandardLogFields.kt`](src/main/kotlin/com/bzonfhir/subscriptionservice/plugins/observabilityotel/StandardLogFields.kt):

| Field             | Tier per #397    | Source                                                       |
|-------------------|------------------|--------------------------------------------------------------|
| `schema_version`  | REQUIRED         | constant `"1.0"`                                             |
| `correlation_id`  | OPTIONAL/REQUIRED| `ObservabilityContext.correlationId`                         |
| `trace_id`        | OPTIONAL         | `ObservabilityContext.attributes["trace_id"]`                |
| `span_id`         | OPTIONAL         | `ObservabilityContext.attributes["span_id"]`                 |
| `source_protocol` | OPTIONAL         | `ObservabilityContext.attributes["source_protocol"]`         |
| `source_system`   | OPTIONAL         | `ObservabilityContext.attributes["source_system"]`           |
| `message_type`    | OPTIONAL         | `ObservabilityContext.attributes["message_type"]`            |

OPTIONAL fields are omitted (not emitted as empty string) when their
source value is blank — so a downstream parser can do an `if (record.containsKey("trace_id"))` check without defensive empty-string handling.

## Standard metric-label catalog

Emitted via `enrichMetricLabels(metricName, ctx)` — see [`StandardMetricLabels.kt`](src/main/kotlin/com/bzonfhir/subscriptionservice/plugins/observabilityotel/StandardMetricLabels.kt).
Mirrors the table in [`docs/observability/metric-catalog.md`](../../docs/observability/metric-catalog.md):

| Metric series                                       | Labels                                                       |
|-----------------------------------------------------|--------------------------------------------------------------|
| `interface_engine.ingested_messages`                | `source_protocol`, `source_system`, `message_type`, `status` |
| `interface_engine.transform.duration`               | `outcome`                                                    |
| `interface_engine.hapi_post.duration`               | `outcome`                                                    |
| `interface_engine.dlq_transitions`                  | `source_protocol`, `reason`                                  |
| `interface_engine.dlq_current_size`                 | (none — single time-series)                                  |
| `interface_engine.received_to_delivered`            | (none — single time-series)                                  |

Unknown metric names → empty map. The runtime emits the metric without
plugin-added labels (same shape as before this plugin existed).

## Schema-version stability

`schema_version = "1.0"`. Bumping this is a contract break per
[`docs/observability/log-schema.md`](../../docs/observability/log-schema.md);
coordinate with downstream log-aggregator dashboards and run the
deprecation cycle. The Logback config (`interface-engine/src/main/resources/logback-spring.xml`)
also pins `schema_version="1.0"` via `customFields`; both copies must
agree, and `StandardLogFieldsTest` holds an assertion on the constant.

## Hot-path discipline

Both enrichment methods fire on every log line / every metric
increment. Implementations are pure map construction — no I/O, no
regex, no string formatting. If a future field needs an expensive
lookup, pre-compute at plugin construction time.

## Building

```
./gradlew :plugins-builtin:observability-otel:build
```

The module depends only on `:plugins-spi` and the Kotlin stdlib. Spring
is `compileOnly`; the host (`interface-engine`) supplies it at
runtime.
