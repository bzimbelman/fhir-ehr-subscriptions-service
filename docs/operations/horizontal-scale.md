<!-- docs-lint:ignore-metric=fhir_subs_scheduler_dispatch_duration_seconds -->
<!-- docs-lint:ignore-metric=fhir_subs_hl7processor_queue_depth_gauge -->

# Horizontal Scale and Multi-Instance Deployment

Per [ADR 0002](../high-level-design/decisions/0002-single-instance-no-leader-election.md),
v1.0 ships single-instance — one binary process per deployment. This
document records what changes when an operator's volume demands more,
and what is already in place to support that.

The TL;DR: the **claim-loop primitive (`SELECT FOR UPDATE SKIP LOCKED`)**
is multi-worker safe inside one process and **multi-pod safe across
processes**. The pieces missing for multi-instance are operational, not
algorithmic.

## What's already multi-instance-safe

| Component | Mechanism | Source |
|---|---|---|
| HL7 message claim loop | `FOR UPDATE SKIP LOCKED` on `hl7_message_queue` | `internal/hl7processor/processor.go` |
| Topic-matcher claim loop | `FOR UPDATE SKIP LOCKED` on `resource_changes` | `internal/matcher/` |
| Submatcher claim loop | `FOR UPDATE SKIP LOCKED` on `ehr_events` | `internal/engine/submatcher/` |
| Scheduler claim loop | `FOR UPDATE SKIP LOCKED` on `deliveries` (B-31, S-8.1) | `internal/engine/scheduler/worker.go` |
| Migration runner | `pg_advisory_lock(0xFEEDFACE)` | `internal/infra/storage/migrate/migrate.go::migrationAdvisoryLockID` |
| Audit chain append | `pg_advisory_xact_lock` per append (B-34) | `internal/infra/observability/audit/pgstore.go` |
| Retention sweeper | `pg_advisory_lock(0x52455445_4E54494F)` per run (audit B-32) | `internal/infra/storage/retention/retention.go::retentionAdvisoryLockID` |

These are the load-bearing concurrency primitives. They were verified
by audit B-* and S-* fixes.

> **Partition maintenance:** the partition rotator
> (`internal/infra/storage/partition/partition.go`) does **not** take an
> advisory lock. Concurrent runs are safe because every DDL statement
> the rotator emits is an idempotent `CREATE TABLE IF NOT EXISTS`
> (forward-create) or a `DETACH PARTITION` whose target name is
> deterministic per month. A multi-pod race produces redundant
> idempotent DDL, not contention. The cost is paid in catalog locks,
> not advisory locks; the per-tick `LockTimeout` GUC bounds that cost.

## What's already in place for the data plane

- **Partitioned tables** for the high-volume rows: `resource_changes`
  and `ehr_events` are RANGE-partitioned by month. The package
  `internal/infra/storage/partition/` runs the rotator that creates
  upcoming partitions and drops aged ones (S-13.8 / S-13.9). The
  rotator is started by `storage.Start` with config from
  `storage.partitioning.*` (see
  `cmd/fhir-subs/config.go::StoragePartitioningConfig`).
- **Configurable retention windows** (`storage.retention.*` —
  `hl7_message_queue`, `deliveries`, `dead_letters`,
  plus `storage.partitioning.resource_changes_retention` and
  `ehr_events_retention`) so operators trade durability vs disk
  pressure without code changes. See
  `cmd/fhir-subs/config.go::StorageRetentionConfig` and
  `StoragePartitioningConfig`.
- **AfterConnect hooks** apply `statement_timeout`,
  `idle_in_transaction_session_timeout`, and `lock_timeout` on every
  connection
  (`internal/infra/storage/pool/pool.go::AfterConnect`, S-13.5) —
  sets a per-query budget that prevents one runaway query from
  wedging an entire pool.

## What needs operator attention to scale

### 1. Connection pooler (PgBouncer)

Each pod opens a `pgxpool` (default 25 connections per pool). Three
pods × five workers per pod easily exceeds Postgres's default
`max_connections=100`. Drop in PgBouncer in `transaction` mode:

```yaml
# Helm-style values; adapt to your platform
pgbouncer:
  poolMode: transaction
  maxClientConn: 1000
  defaultPoolSize: 50  # per database
  serverIdleTimeout: 600
```

Important: the service uses `LISTEN/NOTIFY`-free patterns and
`SET LOCAL`-free transactions, so `pool_mode=transaction` is safe.
Avoid `session` mode — it caps at one client per backend connection,
which defeats the pooler's purpose.

### 2. Read replicas (post-MVP)

The service performs **all** writes against the primary (claim loops
use `FOR UPDATE`, which requires the writer). Read replicas are
useful only for the API's read-heavy paths (`GET /Subscription`,
`GET /Subscription/{id}`, `GET /Subscription?...`, `$status`,
`$events`).

Today the service does not split reads from writes — every query
goes through `cfg.database.url`. Splitting requires:

- A second `pgxpool.Pool` for replicas (typed `ReadPool`).
- `internal/api/handlers/pg_stores.go` plumbed to use `ReadPool` for
  list / get / search; the write path (POST/PUT/DELETE) keeps the
  primary.
- Awareness in retention / partition maintenance that they MUST run
  against the primary.

This is a documented v1.0 follow-up. For most facility-scale
deployments the primary handles both read and write fine.

### 3. Sharding

`resource_changes` and `ehr_events` are the highest-volume tables.
If they exceed a single primary's write bandwidth (typically ~10k
inserts/s per pod, well past most facility deployments), sharding by
`adapter_id` or `topic_url` is the next step. The schema is
shard-friendly:

- `resource_changes` partition key (`created_month`) is independent
  of any candidate shard key.
- `ehr_events` carries `topic_url` and `correlation_id` — natural
  shard candidates.
- `deliveries` is per-subscription; sharding by `subscription_id`
  keeps the per-subscription ordering invariant intact.

A practical sharding layer wraps the `repos.*Repo` types with a
router that selects the shard pool by shard key. This is **not** in
the v1.0 scope — most adopters will not need it.

### 4. Worker pod sizing

The Helm chart should expose:

```yaml
replicaCount: 3   # baseline
autoscaling:
  enabled: false  # default; the claim-loop primitive scales linearly
                  # but enabling HPA on CPU is reasonable for spiky traffic
podDisruptionBudget:
  minAvailable: 2 # tolerate a single-node failure during maintenance
```

The scheduler's `DispatchConcurrency`
(`internal/engine/scheduler/worker.go::Config.DispatchConcurrency`,
S-8.1) caps in-flight deliveries per pod, so ramping replicas
linearly scales throughput without risking subscriber overload.

### 5. Network policies

For multi-pod deployments behind a Service, ensure NetworkPolicy
restricts pod-to-Postgres traffic to the actual pod selector — a
default-deny ingress on Postgres that allows only the service's
pods keeps a compromised sidecar from talking to the database.

## When to scale up

The production binary does not currently expose a queue-depth gauge
or a scheduler-dispatch duration histogram on the
`fhir_subs_*` registry; those are tracked as P1 observability
follow-ups. Until they ship, lean on Postgres-side and operator-
instrumented signals:

- p95 / p99 of HL7 ingest lag — measured by hand from
  `received_at` vs `processed_at` on `hl7_message_queue`:

  ```sql
  SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (processed_at - received_at)))
  FROM hl7_message_queue
  WHERE processed_at IS NOT NULL AND processed_at > now() - interval '5 minutes';
  ```

- `deliveries` queue depth as a count rather than a gauge:

  ```sql
  SELECT status, COUNT(*) FROM deliveries
  WHERE scheduled_at < now() GROUP BY status;
  ```

  A `pending` row count rising linearly across snapshots is the
  scale-up trigger.

- pgxpool's `acquire_wait_total` (operator-instrumented in the
  pool wrapper, not yet exported) — when wait counts climb, add
  PgBouncer or raise `pool_size`.

- Postgres CPU > 70% sustained across the daily peak (use the
  cluster-side dashboard).

- `rate(fhir_subs_dead_letters_total{reason="delivery_exhausted"}[5m])` —
  rising sustained means subscribers are failing; investigate
  before adding capacity.

Until these signals fire together, single-instance is operationally
simpler and the operational fixes (PgBouncer + AfterConnect timeouts)
deliver more ROI than horizontal scale.

## What this batch (P2.10) closed

- This document, capturing the recipe set.
- Confirmation that the existing partition rotator + claim-loop
  primitives + advisory-lock serialization (migration / retention /
  audit) are multi-pod safe; no code change needed for the baseline
  multi-instance deployment.
- Re-attribution of the partition rotator's safety mechanism: it
  uses idempotent DDL, not advisory locks. Earlier drafts of this
  runbook claimed `pg_advisory_xact_lock`; that was incorrect.

## What remains for v1.0

- Read-replica plumbing in the storage / handlers stack.
- Queue-depth + scheduler-dispatch metrics on the
  `fhir_subs_*` registry (observability follow-up).
- A bundled Helm chart with the values shown above (also tracked
  under P3.4).
- An ECR / Quay multi-arch image build (also tracked under P3.4).

These are tracked separately. P2.10 is closed once the recipe is
documented — the **algorithmic** support for multi-instance is
already shipping.
