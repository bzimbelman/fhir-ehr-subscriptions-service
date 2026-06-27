# Log schema — `schema_version 1.0`

> Status: canonical reference (ticket #397). Builds on #388, which introduced
> the JSON layout and `schema_version` field. This doc is the contract:
> downstream agents and log-parsing automations rely on it to know which
> fields are safe to depend on and which are not.

The interface-engine and the hapi-auth JAR both emit JSON-formatted log
records in the layout defined here. One record per line in production (so
log aggregators like Loki, Elastic, Datadog can parse them directly);
pretty-printed when the Spring profile is `dev`.

## Why a single schema

Tracing one HL7 message through the system used to require `grep`-ing across
four log streams in four different formats. After ticket #388, every log
line is JSON with a `correlation_id` field that's the same value at every
hop, so one

```bash
kubectl logs -n epic387-test deploy/ep387-interface-engine \
  | jq -c 'select(.correlation_id == "<the-id>")'
```

returns the full pipeline trace for that one message. The hapi pod returns
matching records for the HAPI-side of the conversation by the same id.

But "same JSON shape today" is not the same promise as "same JSON shape
tomorrow." Without an explicit stability contract, an agent or downstream
parser that consumes our logs has no way to know which fields are safe to
depend on. This document is that contract.

---

## Versioning policy

Every record carries a `schema_version` string of the form `MAJOR.MINOR`.
The current version is **`1.0`**.

### When to bump

| Change                                                                    | Bump            | Example                                                                                              |
|---------------------------------------------------------------------------|-----------------|------------------------------------------------------------------------------------------------------|
| Remove a REQUIRED or OPTIONAL field                                       | **MAJOR**       | Dropping `thread_name` — would break parsers depending on it                                         |
| Rename a REQUIRED or OPTIONAL field                                       | **MAJOR**       | `correlation_id` → `trace_id`                                                                        |
| Change the type of a REQUIRED or OPTIONAL field                           | **MAJOR**       | `correlation_id` from string-UUID to integer                                                         |
| Change the semantic meaning of an existing field                          | **MAJOR**       | `level` from log level (ERROR/WARN/...) to numeric severity                                          |
| Add a new field (any tier)                                                | **MINOR**       | Adding `tenant_id` as OPTIONAL                                                                       |
| Promote an EXPERIMENTAL field to OPTIONAL or REQUIRED                     | **MINOR**       | EXPERIMENTAL `outbox_lag_ms` becomes OPTIONAL after a release                                        |
| Remove or change an EXPERIMENTAL field                                    | **no bump**     | EXPERIMENTAL is explicitly unstable; see tier definitions                                            |
| Add new enum values to an open-set string field documented as freeform    | **no bump**     | Adding `event=replay_scheduled` to the open-set `message` tag conventions                            |
| Fix typos / rewordings in human-readable `message` content                | **no bump**     | `message` is documented as not stable line-for-line                                                  |
| Adjust log levels (`INFO` → `DEBUG` for a noisy line)                     | **no bump**     | The `level` FIELD's promise is unchanged; the per-call-site value isn't a schema commitment          |
| Bug-fix in how a field is rendered (timezone normalization, escaping)     | **no bump**     | Field's presence and meaning don't change                                                            |

### Major-version deprecation cycle

A MAJOR bump (removing or renaming a REQUIRED/OPTIONAL field) requires a
deprecation cycle:

1. One minor release adds the new field alongside the old one and marks the
   old field deprecated in this doc.
2. The next major release removes the deprecated field and bumps
   `schema_version` from `1.x` to `2.0`.

Major bumps are coordinated with downstream log-aggregator dashboards and
agent integrations — they aren't unilateral.

---

## Stability tiers

Every field in the matrix below carries one of three tiers.

### REQUIRED

Present on every JSON log record this service emits. Removing or renaming
requires a MAJOR version bump and a deprecation cycle. Agents and parsers
MAY assume the field exists on every record without a defensive null check.

### OPTIONAL

Present when applicable. Some OPTIONAL fields are conditional: the doc says
exactly when. Example: `correlation_id` is OPTIONAL at the schema level
(startup banners don't have one) but REQUIRED on per-message logs (see the
notes column). Removing an OPTIONAL field still requires a MAJOR bump.

Agents and parsers MUST tolerate the field being absent. The doc names the
conditions under which it appears.

### EXPERIMENTAL

Present but explicitly unstable. May change, be renamed, change type, or
disappear without a version bump. New fields land here first while we
shake out their shape; once we're sure, a MINOR bump promotes them to
OPTIONAL or REQUIRED.

Agents and parsers SHOULD NOT take a hard dependency on EXPERIMENTAL
fields. If they do, they own the breakage.

---

## Field-stability matrix

| Field            | Type                              | Tier         | Since | Notes                                                                                                                            |
|------------------|-----------------------------------|--------------|-------|----------------------------------------------------------------------------------------------------------------------------------|
| `@timestamp`     | ISO 8601 string (UTC, ms-prec.)   | REQUIRED     | 1.0   | Set by Logback's `LogstashEncoder`. Always UTC. Fractional seconds to millisecond precision.                                     |
| `level`          | enum string                       | REQUIRED     | 1.0   | One of `TRACE` / `DEBUG` / `INFO` / `WARN` / `ERROR`. Enum is closed; new levels would be a MAJOR bump.                          |
| `logger_name`    | string                            | REQUIRED     | 1.0   | Java FQCN of the SLF4J logger (e.g. `com.bzonfhir.subscriptionservice.interfaceengine.routes.IngestRoutes`). Format may evolve as packages reshape; identity as a string is stable. |
| `message`        | string                            | REQUIRED     | 1.0   | Human-readable, SLF4J `{}` placeholders resolved. Text content is **not** stable — agents must not regex-match on `message` for state. Use structured fields or `mdc.*` instead. |
| `thread_name`    | string                            | OPTIONAL     | 1.0   | JVM thread name. Useful for debugging Camel / Tomcat thread interactions. Absent only on the rare records where Logback can't determine a thread. |
| `correlation_id` | UUID string                       | OPTIONAL     | 1.0   | Server-assigned UUID per inbound message/request. **REQUIRED on per-message log lines** (set by the servlet filter, Camel route processor, or worker `processOne` entry). Omitted on logs outside any request scope (startup banners, scheduled-task boilerplate). |
| `schema_version` | string `MAJOR.MINOR`              | REQUIRED     | 1.0   | The version of this matrix the record conforms to. Currently `"1.0"`. Pinned via Logback `customFields`, so it appears on every record without polluting the MDC. |
| `stack_trace`    | string                            | OPTIONAL     | 1.0   | Present whenever an SLF4J call passes an exception (e.g. `log.error("...", ex)`). One string with newlines preserved. Default formatter trims uninformative reflection frames. |
| `mdc.*`          | object (free-form string→string)  | OPTIONAL     | 1.0   | Free-form MDC values lifted as top-level fields. Documented EXPERIMENTAL extension point — individual MDC keys are not part of the stability contract unless explicitly added to this matrix. New per-key entries land here first, then graduate via a MINOR bump. |
| `@version`       | string                            | OPTIONAL     | 1.0   | Emitted by `LogstashEncoder` (Logstash event-format version, currently `"1"`). Framework-default; we don't take a hard commitment on its presence if we swap encoders. Agents should ignore unless they're specifically interoperating with a Logstash pipeline. |
| `level_value`    | integer                           | OPTIONAL     | 1.0   | Numeric SLF4J level (`INFO=20000`, `WARN=30000`, `ERROR=40000`). Emitted by `LogstashEncoder` for downstream filtering. Use `level` for queries; `level_value` is a convenience field whose presence is tied to the encoder.            |

### Field naming conventions for future additions

When a new field is added (under the MINOR-bump rules above):

- snake_case (`tenant_id`, `subscription_id`, `outbox_lag_ms`)
- Suffix `_id` for identifiers, `_ms` / `_seconds` for durations, `_at` for
  ISO 8601 timestamps
- Booleans named for the affirmative (`is_replay`, not `is_not_original`)
- Enums documented inline with their closed set

These conventions aren't load-bearing for v1.0 (the v1.0 field set is what
it is), but new fields are expected to follow them.

---

## Worked examples

The three records below are illustrative of the shape an agent will see on
the wire. They are themselves valid JSON; the doc-parser test (see
`scripts/observability/test-doc-parses.sh`) verifies they parse cleanly.

### Startup log (no correlation_id, no stack_trace)

```json
{
  "@timestamp": "2026-06-26T17:41:55.012Z",
  "level": "INFO",
  "logger_name": "com.bzonfhir.subscriptionservice.interfaceengine.InterfaceEngineApplication",
  "message": "Started InterfaceEngineApplication in 6.214 seconds (process running for 6.821)",
  "thread_name": "main",
  "schema_version": "1.0"
}
```

### Per-message processing log (correlation_id REQUIRED)

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

### Exception log (stack_trace present)

```json
{
  "@timestamp": "2026-06-26T17:42:13.001Z",
  "level": "WARN",
  "logger_name": "com.bzonfhir.subscriptionservice.interfaceengine.worker.IngestedMessageWorker",
  "message": "event=retry_scheduled message_id=42 source_system=EPIC source_id=MSG00001 attempt=2 next_attempt_at=2026-06-26T17:42:43Z",
  "thread_name": "scheduling-1",
  "correlation_id": "0192f6c1-9d7a-7e3b-9c1e-66d8a0c3f1e0",
  "schema_version": "1.0",
  "stack_trace": "java.io.IOException: matchbox unreachable\n\tat com.bzonfhir.subscriptionservice.interfaceengine.transform.MatchboxClient.transform(MatchboxClient.kt:88)\n\t..."
}
```

---

## Open-set conventions inside `message`

The doc explicitly says `message` text is not stable, but the team has a
loose convention worth documenting (so operators and agents can still grep
it usefully — at their own risk):

- Worker lifecycle events lead with `event=<verb>` — `event=dlq`,
  `event=retry_scheduled`, `event=transformed`, `event=delivered`.
- Identifiers follow as `key=value` tokens: `message_id=42`,
  `source_system=EPIC`, `source_id=MSG00001`.
- These tokens are best-effort; new event types may appear at any time
  without a version bump. Agents that want structured access to these
  values should request them be lifted into typed fields (MINOR bump).

---

## How to add a new field

1. Set the value on the SLF4J MDC at the appropriate scope (filter,
   processor, worker entry).
2. Add `<includeMdcKeyName>your_field</includeMdcKeyName>` to both
   `interface-engine/src/main/resources/logback-spring.xml` and
   `hapi/auth/src/main/resources/logback-spring-subsvc.xml`.
3. Add a row to the field-stability matrix above. Mark the new field
   EXPERIMENTAL by default unless there's a strong reason to land it as
   OPTIONAL or REQUIRED on day one.
4. Bump `schema_version` by one MINOR (`1.x` → `1.(x+1)`). Update both the
   logback configs and this doc.
5. Update `JsonLogLayoutTest` (and the HAPI-side test) to assert the new
   field is present where expected.

---

## `X-Correlation-Id` HTTP header

The header is the wire form of `correlation_id`. Both services:

- Read it on every inbound request; if missing or malformed, generate a UUID v4.
- Echo it on the response so the caller can correlate.
- Forward it on every outbound request (interface-engine → matchbox,
  interface-engine → HAPI, HAPI → REST-hook subscriber).

For inbound HL7 v2 MLLP messages, the protocol has no equivalent header — the
interface-engine generates a UUID at receive time and persists it on the row.

---

## CI gate

Schema stability is enforced (or will be — implementation is deferred) by a
CI gate described in [`schema-stability-contract.md`](./schema-stability-contract.md).
The gate parses this doc into a structured representation, captures every
log record produced by the test suite, and fails the build if any record is
missing a REQUIRED field or contains a field marked as removed.

The placeholder is stubbed at
`scripts/observability/check-log-schema.sh`; the real implementation is a
follow-up ticket.

---

## See also

- [Ticket #388](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/388) — original implementation
- [Ticket #397](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/397) — schema-stability contract (this doc)
- [`metric-catalog.md`](./metric-catalog.md) — sister contract for Prometheus metrics
- [`schema-stability-contract.md`](./schema-stability-contract.md) — what the future CI gate will enforce
- `interface-engine/src/main/kotlin/.../observability/CorrelationId.kt` — header + MDC constants
- `hapi/auth/src/main/java/.../observability/CorrelationIdInterceptor.java` — HAPI-side propagation
