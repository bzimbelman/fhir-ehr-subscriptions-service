# Metric catalog — `schema_version 1.0`

> Status: contract reference (ticket #397). The implementation of the actual
> Prometheus endpoints lives in ticket #389, landing in parallel with this
> doc. This catalog is the **contract**: it defines what we promise about
> metric names, labels, and stability. The actual emitted metrics may differ
> slightly when #389 lands; a follow-up commit reconciles the two.

Both services expose a Prometheus exposition-format endpoint:

- `interface-engine`: `/actuator/prometheus` (Spring Boot Actuator + Micrometer)
- `hapi`: `/actuator/prometheus` (Spring Boot Actuator on the HAPI servlet)

Every metric documented here carries the same stability guarantees as the
log schema: REQUIRED, OPTIONAL, or EXPERIMENTAL. The version
(`schema_version: 1.0`) is the contract version, not a Prometheus-level
label.

---

## Versioning policy

Identical in spirit to the log schema. See
[`log-schema.md`](./log-schema.md#versioning-policy) for the full rationale.
Summary:

| Change                                                                     | Bump            |
|----------------------------------------------------------------------------|-----------------|
| Remove a REQUIRED or OPTIONAL metric                                       | **MAJOR**       |
| Rename a REQUIRED or OPTIONAL metric                                       | **MAJOR**       |
| Change the type of a metric (counter → gauge, gauge → histogram)           | **MAJOR**       |
| Remove or rename a label on a REQUIRED metric                              | **MAJOR**       |
| Add a new metric (any tier)                                                | **MINOR**       |
| Add a new label value to an open-set label                                 | **no bump**     |
| Promote an EXPERIMENTAL metric to OPTIONAL or REQUIRED                     | **MINOR**       |
| Remove or change an EXPERIMENTAL metric                                    | **no bump**     |
| Adjust histogram buckets (Prometheus tolerates this; queries still work)   | **no bump**     |

A MAJOR bump triggers the same deprecation cycle as logs: one minor release
emits both the old and new metric side by side, the next major drops the
old one.

---

## Naming convention

Every metric name follows:

```
<service>_<component>_<noun>_<unit>
```

- **service**: `interface_engine` or `fhir_subscription` (the HAPI side
  names itself for the FHIR concept it measures, not for "hapi", because
  the deployment shape may change).
- **component**: optional segment when one service has multiple
  subsystems (e.g. `interface_engine_transform_*`, `interface_engine_dlq_*`).
- **noun**: what is being counted/measured (`messages`, `transitions`,
  `requests`).
- **unit**: required suffix that names the Prometheus convention:

| Suffix      | Type        | Notes                                                                                |
|-------------|-------------|--------------------------------------------------------------------------------------|
| `_total`    | counter     | Prometheus convention: monotonic counters end in `_total`. ALWAYS.                   |
| `_seconds`  | histogram   | Wall-clock durations. Use seconds (not ms) for consistency with the OTel spec.       |
| `_bytes`    | histogram or gauge | Sizes in bytes (not KB/MB). Document the dimension inline.                    |
| (none)      | gauge       | Instantaneous values: queue depth, active connection count, current row count.       |

Unitless gauges (e.g. `fhir_subscription_active` — a count of resources)
omit a unit suffix. They MUST still be unambiguous from the name.

### Label conventions

- snake_case, lowercase
- Closed enum values only — no free-form strings, no IDs, no patient
  attributes (see "no PII" below)
- Each label is named for the dimension it slices: `status`, `outcome`,
  `source_protocol`, `source_system`, `reason`

---

## PII and cardinality rules

These rules are **non-negotiable**. Violations are blocked by code review
and will be caught by the future CI gate.

### No PII in labels — ever

The following are NEVER acceptable as label values:

- Patient names, medical record numbers (MRNs), DOBs, addresses
- Raw HL7 v2 message contents or FHIR resource bodies
- Authentication tokens, secrets, API keys
- Free-form `message` text or anything originating from external input
- IP addresses of patient devices

This includes labels indirectly derived from PII (e.g. a hash of an MRN —
still trivially reversible across a small population).

If a metric needs to slice by patient-scoped data, it doesn't belong in
Prometheus. It belongs in the admin API (ticket #392 / #396) or the
durable message store, both of which carry the appropriate access controls.

### Bounded cardinality

Every label MUST have a documented, bounded value set. The maximum
acceptable cardinality is documented per metric (see the catalog below).
A metric with N labels has cardinality up to the product of their
individual cardinalities — design accordingly.

- `status`, `outcome`, `reason` — closed enum strings, < 10 values
- `source_protocol` — closed enum: `MLLP`, `HTTP`, `REST_HOOK`,
  `WEBSOCKET` (and future named additions, no free-form)
- `source_system` — closed enum populated from deployment config (the set
  of EHRs/labs the operator has provisioned). Document a hard cap of 100
  values per deployment; if a deployment exceeds that, the metric stops
  being labeled by `source_system`.

Anything tenant-scoped or message-scoped (e.g. `tenant_id`,
`subscription_id`, `message_id`) is **forbidden** as a label. Those
identifiers belong on traces and structured logs, not on counters.

---

## Metric catalog

The fields:

- **Name**: full Prometheus metric name
- **Type**: counter / gauge / histogram
- **Labels**: documented dimensions
- **Tier**: REQUIRED / OPTIONAL / EXPERIMENTAL
- **Since**: schema version the metric appeared in
- **Cardinality**: max expected series count per deployment

### Interface engine — ingestion

| Name                                          | Type      | Labels                                            | Tier         | Since | Cardinality cap        |
|-----------------------------------------------|-----------|---------------------------------------------------|--------------|-------|------------------------|
| `interface_engine_ingested_messages_total`    | counter   | `status`, `source_protocol`, `source_system`      | REQUIRED     | 1.0   | ~1000 (status × protocol × system) |
| `interface_engine_transform_duration_seconds` | histogram | `outcome`                                         | REQUIRED     | 1.0   | ~5 (outcome enum)      |
| `interface_engine_hapi_post_duration_seconds` | histogram | `outcome`                                         | REQUIRED     | 1.0   | ~5                     |
| `interface_engine_dlq_transitions_total`      | counter   | `source_protocol`, `reason`                       | REQUIRED     | 1.0   | ~50 (protocol × reason)|
| `interface_engine_dlq_current_size`           | gauge     | (none)                                            | REQUIRED     | 1.0   | 1                      |

**`status` enum** (for `ingested_messages_total`): `RECEIVED`,
`TRANSFORMING`, `DELIVERED`, `RETRYING`, `DEAD_LETTER`. Matches the
durable-store state machine.

**`outcome` enum** (for the `_duration_seconds` histograms): `success`,
`failure`, `timeout`. New outcomes can be added without a MAJOR bump (open
enum is documented here).

**`reason` enum** (for `dlq_transitions_total`): `MAX_ATTEMPTS_EXCEEDED`,
`UNRECOVERABLE_ERROR`, `MANUAL_PURGE`. Closed enum; expansion requires a
MINOR bump.

### HAPI — FHIR subscriptions

| Name                                          | Type      | Labels      | Tier         | Since | Cardinality cap        |
|-----------------------------------------------|-----------|-------------|--------------|-------|------------------------|
| `fhir_subscription_active`                    | gauge     | (none)      | REQUIRED     | 1.0   | 1                      |
| `fhir_subscription_delivery_total`            | counter   | `outcome`   | REQUIRED     | 1.0   | ~5                     |
| `fhir_subscription_delivery_duration_seconds` | histogram | `outcome`   | REQUIRED     | 1.0   | ~5                     |

### Default metrics inherited from Spring Boot / Micrometer

Spring Boot's `actuator` + Micrometer's `prometheus` registry produce a
large set of default metrics: JVM (`jvm_memory_used_bytes`,
`jvm_gc_pause_seconds`), HTTP server (`http_server_requests_seconds`),
Tomcat thread pool, Camel route timings, JDBC pool stats, etc.

These metrics are **OPTIONAL** under our schema — present today, may change
shape if we upgrade Micrometer or swap registries. Agents and dashboards
that depend on them MUST treat them as best-effort. We don't take a hard
commitment on Spring/Micrometer-emitted metrics' stability because we don't
control their naming.

The two services may choose to expose Spring's defaults or not on a future
release without that being a MAJOR bump on the contract.

### EXPERIMENTAL — landing here first

(none today — placeholder section for future fields)

Metrics added in MINOR releases that we're still validating land here.
They MAY change shape or disappear without notice. Once we're confident in
the design, a follow-up MINOR bump promotes them to OPTIONAL or REQUIRED
and the row moves to the relevant section above.

---

## What NOT to query

Examples of queries that look reasonable but are explicitly unsupported:

- **Per-message lookup by id**: `interface_engine_ingested_messages_total{message_id="..."}` — message IDs are not labels and never will be (cardinality + PII risk). Use the admin API.
- **By tenant**: `interface_engine_ingested_messages_total{tenant_id="..."}` — `tenant_id` is not a label (cardinality). Tenant slicing belongs in admin API queries or, if needed at metric grain, in a separately scoped registry per tenant (a future epic, not on the v1.0 contract).
- **Sum across services by `service` label**: services don't expose a self-identifying label. Use Prometheus's `job` / `instance` from the scrape config instead.
- **Patient outcomes**: nothing per-patient is exposed in metrics. By design.

---

## CI gate

Cross-checking the live `/actuator/prometheus` output against this catalog
is the future job of the CI gate described in
[`schema-stability-contract.md`](./schema-stability-contract.md). The gate
will:

1. Parse this catalog's metric tables into a structured representation.
2. Boot the service in a Testcontainers harness.
3. Hit `/actuator/prometheus`, collect every metric line.
4. Assert every REQUIRED metric is present with the documented labels.
5. Assert no metric on the exposition has been marked as removed.
6. Fail the build if either check fails.

Implementation is a follow-up ticket. The placeholder script lives at
`scripts/observability/check-log-schema.sh` (it covers both logs and
metrics, despite the file name — split if it gets unwieldy).

---

## See also

- [Ticket #389](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/389) — Prometheus endpoint implementation (in parallel)
- [Ticket #397](https://op.bzonfhir.com/openproject/projects/subscription-service/work_packages/397) — this contract
- [`log-schema.md`](./log-schema.md) — sister contract for JSON logs
- [`schema-stability-contract.md`](./schema-stability-contract.md) — what the future CI gate will enforce
- [Prometheus naming best practices](https://prometheus.io/docs/practices/naming/) — the conventions this catalog follows
