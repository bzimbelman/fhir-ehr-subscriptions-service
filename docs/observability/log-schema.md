# Log schema — `schema_version 1.0`

> Status: ticket #388. The full stability promise + CI gate lives in #397.

The interface-engine and the hapi-auth JAR both emit JSON-formatted log records
in the layout defined here. One record per line in production (so log
aggregators like Loki, Elastic, Datadog can parse them directly); pretty-printed
when the Spring profile is `dev`.

## Why a single schema

Today, tracing one HL7 message through the system requires `grep`-ing across
four different log streams in four different formats. After ticket #388, every
log line is JSON with a `correlation_id` field that's the same value at every
hop, so one

```bash
kubectl logs -n epic387-test deploy/ep387-interface-engine \
  | jq -c 'select(.correlation_id == "<the-id>")'
```

returns the full pipeline trace for that one message. The hapi pod returns
matching records for the HAPI-side of the conversation by the same id.

## Field table

| Field            | Required | Description                                                                                              |
|------------------|----------|----------------------------------------------------------------------------------------------------------|
| `@timestamp`     | yes      | ISO 8601 UTC. Set by Logback's `LogstashEncoder`.                                                        |
| `level`          | yes      | `ERROR` / `WARN` / `INFO` / `DEBUG` / `TRACE`.                                                           |
| `logger_name`    | yes      | Fully qualified class name of the logger.                                                                |
| `message`        | yes      | The formatted log message (SLF4J `{}` placeholders resolved).                                            |
| `thread_name`    | no       | JVM thread name emitting the record. Useful for debugging worker / Camel / Tomcat thread interactions.   |
| `correlation_id` | yes\*    | Server-assigned UUID per inbound message/request. Required on per-message log lines; optional on startup/shutdown logs (no message in scope). |
| `schema_version` | yes      | Currently `"1.0"`. Bumped per the rules below.                                                           |
| `stack_trace`    | yes\*\*  | Present whenever a log call carries an exception. One string with newlines preserved.                    |
| `mdc.*`          | no       | Any additional MDC keys set by future call sites (e.g. `tenant_id`, `subscription_id`).                  |

`*` Required on log lines emitted under a request/message scope — set by the
servlet filter, Camel route processor, or worker `processOne` entry. Optional
on lines emitted outside any scope (startup/shutdown banners, scheduled-task
boilerplate).

`**` Required only when the SLF4J call passed an exception (`log.error("...",
ex)`); otherwise omitted.

## Example record

Production layout (single line for clarity, shown wrapped here):

```json
{
  "@timestamp": "2026-06-26T17:42:12.345Z",
  "level": "INFO",
  "logger_name": "com.bzonfhir.subscriptionservice.interfaceengine.routes.IngestRoutes",
  "message": "received id=42 type=ADT_A04 controlId=MSG00001 sourceSystem=EPIC",
  "thread_name": "Camel (camel-1) thread #1 - MllpTcpServer[2575]",
  "correlation_id": "0192f6c1-9d7a-7e3b-9c1e-66d8a0c3f1e0",
  "schema_version": "1.0"
}
```

With an exception:

```json
{
  "@timestamp": "2026-06-26T17:42:13.001Z",
  "level": "WARN",
  "logger_name": "com.bzonfhir.subscriptionservice.interfaceengine.worker.IngestedMessageWorker",
  "message": "event=retry_scheduled message_id=42 source_system=EPIC source_id=MSG00001 ...",
  "thread_name": "scheduling-1",
  "correlation_id": "0192f6c1-9d7a-7e3b-9c1e-66d8a0c3f1e0",
  "schema_version": "1.0",
  "stack_trace": "java.io.IOException: matchbox unreachable\n  at ..."
}
```

## Versioning

Semver. The version applies to the FIELD SET, not the LOG MESSAGES — message
text can change at any time without bumping the version, but adding or
removing FIELDS follows these rules:

- **Patch / no bump** — fixing a bug in how a field is rendered (timezone
  normalization, escaping). The field's presence and meaning don't change.
- **Minor bump** (`1.x → 1.(x+1)`) — adding a new optional field. Existing
  consumers continue to work; new consumers can read the new field.
- **Major bump** (`1.x → 2.0`) — removing or renaming an existing field, or
  changing the semantic meaning of an existing field. Coordinated with
  downstream log-aggregator dashboards.

Ticket #397 will add a CI gate that diffs the field schema against the
appender output to prevent accidental bumps.

## How to add a new field

1. Set the value on the SLF4J MDC at the appropriate scope (filter, processor,
   worker entry).
2. Add `<includeMdcKeyName>your_field</includeMdcKeyName>` to both
   `interface-engine/src/main/resources/logback-spring.xml` and
   `hapi/auth/src/main/resources/logback-spring-subsvc.xml`.
3. Add a row to the table above.
4. Bump `schema_version` per the rules above.

## `X-Correlation-Id` HTTP header

The header is the wire form of `correlation_id`. Both services:

- Read it on every inbound request; if missing or malformed, generate a UUID v4.
- Echo it on the response so the caller can correlate.
- Forward it on every outbound request (interface-engine → matchbox,
  interface-engine → HAPI, HAPI → REST-hook subscriber).

For inbound HL7 v2 MLLP messages, the protocol has no equivalent header — the
interface-engine generates a UUID at receive time and persists it on the row.

## See also

- [Ticket #388](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/388) — implementation
- [Ticket #397](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/397) — schema-stability promise + CI gate
- `interface-engine/src/main/kotlin/.../observability/CorrelationId.kt` — header + MDC constants
- `hapi/auth/src/main/java/.../observability/CorrelationIdInterceptor.java` — HAPI-side propagation
