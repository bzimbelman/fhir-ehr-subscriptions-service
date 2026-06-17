# ADR 0001: Postgres is the only supported datastore

**Status.** Accepted.

**Reader's prerequisites.** Read [../domains/storage.md](../domains/storage.md) and `../../architecture.md` (section "Datastore").

## Context

The system needs durable state for: HL7 message inbox, vendor-neutral resource changes, topic-matched events, deliveries, dead letters, adapter state (scan snapshots, change-feed cursors), subscription registry, topic catalog, auth clients, and audit log. Throughput is moderate — a busy facility might see tens of HL7 messages per second sustained.

Three options were considered:

- A single SQL backend.
- Multiple SQL backends (SQLite for small deployments, Postgres for medium, MySQL for compatibility).
- A SQL backend plus an external message broker (Kafka, NATS) for the inter-stage queues.

Postgres covers every functional requirement: ACID transactions for the transactional outbox between stages, partitioning for `resource_changes` and `ehr_events`, `SELECT FOR UPDATE SKIP LOCKED` for safe per-row claims across worker fibers, JSONB for FHIR resource bodies, and standard tooling for backups, replication, and monitoring.

Supporting multiple SQL backends would mean per-dialect SQL, per-dialect migration tooling, and per-dialect quirk handling (SQLite has no SKIP LOCKED, MySQL handles JSON differently, etc.). The architecture's "operational simplicity" constraint argues against carrying that maintenance burden for a project at this stage.

Adding an external broker would replace one operational dependency (Postgres) with two (Postgres + broker) for no gain — Postgres's `SELECT FOR UPDATE SKIP LOCKED` plus in-memory wakeup signals already provides the queue semantics the pipeline needs, and the architecture explicitly chose this model ("durability lives in the table, coordination does not").

## Decision

**Postgres is the only supported datastore.** No SQLite, no MySQL, no other SQL backend in v1. No external message broker.

The transactional outbox between stages uses Postgres's `SELECT FOR UPDATE SKIP LOCKED` semantics. In-memory wakeup signals are a latency optimization on top; the durable rows are the source of truth.

## Consequences

### Positive

- **One dialect.** Schema definitions, migrations, queries, and operational documentation are written once. No conditional code paths for backend-specific behavior.
- **Mature ecosystem.** Postgres has well-understood backup, replication, point-in-time recovery, and observability tooling. Operators do not need a project-specific operational playbook beyond the standard Postgres one.
- **Right primitives.** ACID transactions for the outbox, `SKIP LOCKED` for safe claims, partitioning for high-volume tables, JSONB for FHIR resource bodies. Every feature the pipeline relies on is a first-class Postgres feature.
- **No additional infrastructure dependency.** A deployment is the container plus Postgres. No broker, no separate stream-processing system.

### Negative

- **Deployments without Postgres available cannot run this server.** Operators that have only MySQL or only Oracle can't drop the server in. They must stand up Postgres. For a single-tenant facility deployment this is a one-time cost, but it is non-zero.
- **Heavy write load on `resource_changes` and `ehr_events`** is funneled through one Postgres instance. We accept this — the architecture's "operational simplicity" constraint says one instance plus catch-up-from-durable-rows is acceptable for the workload. A future deployment that needs higher throughput can use Postgres read replicas, partitioning, or sharding strategies; nothing in the schema design precludes them.
- **No SQLite means no zero-dependency local dev story** out of the box. Local development uses Postgres in a dev container.

### Neutral

- **Future expansion is possible.** Adding a second backend (e.g., MySQL) later is a deliberate project-wide decision, not a v1 oversight. The repository abstractions are written against Postgres semantics; a hypothetical future MySQL adapter would need to map equivalent semantics, and the project would commit to the maintenance burden at that time.
- **Encryption at rest** is a deployment-policy decision (whole-database vs. column-level — see [../domains/storage.md](../domains/storage.md#encryption-at-rest)). Postgres supports both.
- **The choice of Postgres is independent of the choice of implementation language.** The language decision is recorded separately; the storage decision is "Postgres only" regardless.
