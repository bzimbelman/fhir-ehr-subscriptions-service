# Storage

**Purpose.** The Postgres tables that make the system durable, the writers and readers of each table, retention defaults, indexes, the transactional outbox pattern, and migration discipline.

**Reader's prerequisites.** Read [../overview.md](../overview.md) and `../../architecture.md` (sections "Storage Schema" and "Concurrency inside the service"). The wire-level row contracts between stages are in [../contracts/internal-tables.md](../contracts/internal-tables.md). The decision to support Postgres only is recorded in [decisions/0001-postgres-only.md](../decisions/0001-postgres-only.md).

## Postgres only

Postgres is the only supported datastore. No SQLite. No MySQL. No external Kafka, Redis, NATS, RabbitMQ. State is in Postgres; in-process queues are in-memory wakeups whose correctness is the table contents, not the wakeup. See [decisions/0001-postgres-only.md](../decisions/0001-postgres-only.md).

## The table set

Eleven tables. Each row below maps to a table, identifies its writer, its consumer, its retention default, and the indexes the workload requires.

| Table | Writer | Consumer | Retention default | Notes |
|---|---|---|---|---|
| `hl7_message_queue` | [MLLP Listener](mllp-listener.md) | HL7 Message Processor (in [adapter](ehr-adapter.md)) | 7d for processed rows; unprocessed kept indefinitely | Append-only inbox. The durability boundary of the EHR side. |
| `resource_changes` | [Adapter](ehr-adapter.md) sub-components | [Topic Matcher](topic-matcher.md) | 30d | Vendor-neutral FHIR-shaped change log. Append-only; sequence-numbered; partitioned by month. |
| `ehr_events` | [Topic Matcher](topic-matcher.md) | [Subscriptions Engine](subscriptions-engine.md) | 30d | One row per `(resource_change × matching topic)`. Append-only; sequence-numbered; partitioned by month. Drives `$events` replay. |
| `deliveries` | [Subscriptions Engine](subscriptions-engine.md) | Channel modules ([channels](channels.md)) | 90d | One row per `(inbound_event × matching subscription)`; status, attempts, next_attempt_at. |
| `dead_letters` | Engine + adapter | Operators (read-only) | 180d | Notifications that exhausted retries; HL7 messages that failed translation/validation. |
| `pending_pairs` | [Adapter](ehr-adapter.md) HL7 Message Processor | Same | While the hold window is open + a small grace; expired rows reaped | Cancel-and-replace correlation state. **Separate table from `adapter_state`** — survives restart so the held cancellation rejoins the correlation window after recovery. See row spec in [../contracts/internal-tables.md#pending_pairs](../contracts/internal-tables.md#pending_pairs) and [../decisions/0005-cancel-and-replace-in-adapter.md](../decisions/0005-cancel-and-replace-in-adapter.md). |
| `adapter_state` | [Adapter](ehr-adapter.md) sub-components | Same | While referenced | KV store for scan snapshots, change-feed cursors, last-seen tokens. **Does not** hold cancel-and-replace pending pairs — those live in `pending_pairs`. |
| `subscriptions` | [Subscriptions API](subscriptions-api.md) | Engine, API | While `status != 'off'` then 90d | Subscription registry. Includes `eventsSinceSubscriptionStart`, current status, channel config. |
| `subscription_topics` | [Topics](topics.md) loader | All | While referenced | SubscriptionTopic resources, versioned canonicals. Old versions retained until no subscription references them. |
| `auth_clients` | Config loader (file → DB on SIGHUP) | API auth | Forever (until removed) | Registered subscriber clients, public-key JWKS URLs, scopes. Sourced from `auth.client_registry` in the config file. |
| `audit_log` | Everywhere | Operators (read-only) | 7y | Append-only audit trail. |

### `pending_pairs` row shape

Owned by the HL7 Message Processor (and any other adapter sub-component that participates in cancel-and-replace correlation). The full row contract is in [../contracts/internal-tables.md#pending_pairs](../contracts/internal-tables.md#pending_pairs); the headline columns are:

- `correlation_key` (PK) — vendor-specific identifier that links the cancel and the replacement (HL7 ORC-2/ORC-3, Epic placeholder ID, Cerner correlation_id, etc.).
- `pending_resource` — the FHIR resource of whichever half arrived first (typically the cancellation), serialized.
- `pending_kind` — `delete` or `create` (the half currently held).
- `source_message_id` — back-reference into `hl7_message_queue` so the source row is held un-processed until the pair resolves.
- `expires_at` — the hold-window deadline. A reaper sweep flushes expired rows as a plain `delete` (lone cancellation) or plain `create` (lone replacement) — see [decisions/0005](../decisions/0005-cancel-and-replace-in-adapter.md).
- `created_at` — for monitoring.

Retention values are configurable; the defaults above are conservative for HIPAA-aligned deployments. Operators must review against their HIPAA / regional obligations.

## Indexing strategy

Indexes the workload absolutely needs. The architecture lists these and the storage domain owns the migrations.

- `deliveries(status, next_attempt_at)` — the delivery scheduler's main pull query: "give me pending deliveries whose `next_attempt_at` is in the past." Without this index, every scheduler tick scans the table.
- `ehr_events(sequence)` — `$events` replay reads by sequence; the engine reads by sequence. Sequence is monotonic.
- `resource_changes(sequence)` — the Topic Matcher reads by sequence.
- `hl7_message_queue(processed, received_at)` — the HL7 Message Processor's claim query: "the oldest unprocessed row."
- `subscriptions(topic_id, status)` — the engine's Stage 3 pull: "active subscriptions on this topic."
- `subscription_topics(url, version)` — unique. The catalog's primary lookup.
- `deliveries(subscription_id, event_number)` — unique. Outbound idempotency: each `(subscription_id, eventNumber)` is delivered at most once.
- `resource_changes(adapter_id, correlation_id)` — unique. Inbound idempotency: a duplicate ingest of the same source event is a no-op.
- `audit_log(occurred_at)` — operator queries by time range.
- `pending_pairs(expires_at)` — the reaper sweep finds expired rows in O(log n).

Additional indexes will appear as the search-parameter set on `Subscription` and `SubscriptionTopic` is fully implemented (the spec-required search parameters listed in [subscriptions-api.md](subscriptions-api.md#search-parameters)).

## Partitioning

Two tables are partitioned by month: `resource_changes` and `ehr_events`. Both are append-only, sequence-numbered, and high-volume relative to the rest of the schema. Monthly partitions:

- bound the size of any single index;
- allow retention to be enforced by detaching and dropping old partitions in O(seconds) rather than O(retention) `DELETE` statements;
- keep autovacuum cheap.

Maintenance:

- A scheduled task (driven by the lifecycle module on a daily cron) creates the next month's partitions ahead of time so a write at the partition boundary never fails.
- The same task drops partitions older than the configured retention.
- Operators can pause auto-drop via configuration; some deployments keep more history than the default. If pausing, operators must monitor disk separately.

`hl7_message_queue` and `deliveries` are not partitioned in v1 — they are smaller and processed-row cleanup is straightforward. If volume justifies, partitioning these is a low-risk follow-on.

## The transactional outbox pattern

Every stage handoff is a transactional outbox: the same transaction that **marks input row N processed** also **inserts output rows derived from N**. This is the architecture's core durability invariant.

Concretely:

- HL7 Message Processor: `UPDATE hl7_message_queue SET processed = true` and `INSERT INTO resource_changes ...` in **one transaction**.
- Topic Matcher: `UPDATE resource_changes SET processed = true` and one or more `INSERT INTO ehr_events ...` in **one transaction**.
- Subscriptions Engine (Stage 3): `UPDATE ehr_events SET claimed = ...` and one or more `INSERT INTO deliveries ...` in **one transaction**.
- Channel module + delivery scheduler: `UPDATE deliveries SET status = 'delivered', delivered_at = now()` after a confirmed wire-level delivery.

This guarantees:

- A crash mid-stage cannot leave the system in a state where the input was consumed but the output was not produced (or vice versa).
- A worker that crashes after marking input processed but before sending the wakeup signal does NOT lose work — the next worker re-claims by reading the table; wakeups are a latency optimization, not a correctness mechanism.
- `SELECT FOR UPDATE SKIP LOCKED` lets multiple worker fibers in the same process claim distinct rows without coordination.

There is no separate outbox publisher process and no external message broker. The outbox **is** the input table of the next stage. Postgres is the broker.

## In-process queue and wakeup signals

The architecture's `infra/queue` module is a Postgres-backed in-process job queue: workers consume from in-memory channels; durability lives in the table; coordination does not. The architecture's `infra/wakeup` module is the in-memory signal bus that lets one stage wake the next one immediately after a commit (the dashed arrows in the canonical sequence diagram).

This is a latency optimization. Correctness is the table contents:

- If a wakeup is lost (a brief stall, a restart between commit and signal), the downstream component finds the row on its next periodic read of its input table.
- If a worker crashes mid-stage, its row's `FOR UPDATE` lock is released by Postgres on connection close; another worker re-claims via `SKIP LOCKED`.
- If the entire process crashes, the next incarnation reads its input tables on startup and resumes.

Storage is the seam, in-process channels are an in-memory speed-up, and there is no external broker.

## Retention details

Retention is per-table and per-deployment. Defaults from `../../architecture.md`'s configuration example:

- `hl7_message_queue: 7d` — processed rows only. Unprocessed rows are kept until consumed (no auto-drop of unprocessed work).
- `resource_changes: 30d` — bounds how far back the topic matcher could re-evaluate (it doesn't re-evaluate; this is just the change-log retention).
- `ehr_events: 30d` — bounds the `$events` replay window. Subscribers that need a longer catch-up window need a longer retention here. Storage cost grows roughly linearly.
- `deliveries: 90d` — operators querying delivery history.
- `dead_letters: 180d` — long enough to investigate failures.
- `audit_log: 7y` — HIPAA-aligned default for a clinical event store.

Retention is enforced by:

- **Partitioned tables** — drop monthly partitions older than the window.
- **Non-partitioned tables** — a daily cleanup job runs `DELETE WHERE created_at < now() - retention`. The job batches in chunks small enough not to bloat WAL or hold long locks. (`hl7_message_queue` filters by `processed = true` first.)

Retention shorter than the default is permitted but operationally meaningful: shortening `ehr_events` narrows `$events`, shortening `audit_log` may violate compliance obligations.

## Encryption at rest

All DB columns containing FHIR resource bodies or HL7 v2 message bodies are encrypted at the storage layer. Two acceptable mechanisms:

- **Whole-database encryption** at the Postgres / disk level (TDE on RDS, LUKS, etc.). The simplest deployment.
- **Column-level encryption** of payload columns (`resource_changes.resource`, `ehr_events.resource`, `hl7_message_queue.body`, `dead_letters.payload`). The configuration domain holds the encryption key reference (`storage.encryption.at_rest_key`).

The choice is a deployment policy. The schema is shaped so column-level encryption can be added or removed without changing application code; the application sees plaintext bodies and the storage layer handles the rest.

Audit log integrity (append-only-ness) is enforced by the application: the `audit_log` table has `INSERT`-only application credentials and the code never issues `UPDATE` or `DELETE` against it. Operators with elevated DB access can still tamper; that is mitigated by deployment policy (separation of duties), not by application code.

## Schema migrations

The project ships migrations in a forward-compatible **expand-then-contract** discipline:

1. **Expand.** Add a new column / index / table. The new release writes both old and new shapes (or new only) but reads handle both. The previous release handles the new schema gracefully — typically by ignoring the new column.
2. **Migrate.** Backfill data into the new shape. Run online; never block writes.
3. **Contract.** Drop the old shape in a later release once nothing reads it.

Each migration ships as a SQL file in the project's migration tool's format (numbered, idempotent, reversible where possible). Migrations run on startup before the service goes ready (the startup probe waits for migrations to complete; the lifecycle domain owns the sequencing).

Two rules:

- **Migrations never block on application traffic.** No long-running schema changes that take exclusive locks. Index creation uses `CREATE INDEX CONCURRENTLY`; column drops happen in a later release after readers have been removed.
- **Forward compatibility per release.** A release N+1 must run against a database migrated by release N (so a partial rollout works). A release N must tolerate a database migrated by N+1 for a single release cycle (so a rollback works).

Schema changes that violate either rule require a planned maintenance window. The default expectation is no maintenance windows for schema changes.

## Storage volume rough sizing

Not a hard rule, just a back-of-envelope so operators understand the retention/storage trade-off.

For a deployment receiving roughly 10 HL7 messages per second sustained:

- `hl7_message_queue`: 10/s × 86400/day × 7d = 6M processed rows + 1KB body each ≈ 6 GB before processed cleanup is dropped.
- `resource_changes`: 10/s × 86400 × 30 = 26M rows × ~5 KB FHIR resource ≈ 130 GB at 30d retention.
- `ehr_events`: depends on topic match rate. If 30% match: 8M rows × ~5KB ≈ 40 GB.
- `deliveries`: depends on subscriber count. 10 subscribers × 8M ≈ 80M rows × ~1KB ≈ 80 GB at 90d retention.
- `audit_log`: dwarfed by clinical data even at 7y.

These are rough. Real workloads vary by message size (HL7 OBX-heavy ORUs are large), match rate, and subscriber count. The point is that the dominant storage is `resource_changes` and `deliveries`, and retention of those two drives most of the disk footprint. Partitioning makes their cleanup cheap.

## What this domain does NOT do

- **It does not produce or consume rows.** Producing and consuming is the job of the named writers and readers in the table above. Storage owns the schema, the indexes, the migrations, and retention.
- **It does not implement the queue protocol.** `SELECT FOR UPDATE SKIP LOCKED` semantics are owned by each stage's worker. Storage just provides the table.
- **It does not own encryption keys.** Keys live in the [configuration](configuration.md) domain (and ultimately in the operator's secret store).
- **It does not own the audit policy.** Auditable events are decided by each stage; storage just guarantees `audit_log` is append-only.
- **It does not own the FHIR data shape.** The shape of `resource_changes.resource` and `ehr_events.resource` is the FHIR resource per the [version strategy](../decisions/0004-fhir-version-strategy.md) — owned by the engine and adapter domains. Storage stores it.
