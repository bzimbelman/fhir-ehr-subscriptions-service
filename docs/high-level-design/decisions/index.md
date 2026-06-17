# Decisions Index

**Purpose.** A single page that lists the design decisions captured across the HLD set, both standalone ADRs and decisions documented inside other documents. Each entry: title, one-line summary, the trade-off, and where the decision is recorded.

**Reader's prerequisites.** None. This is a wayfinding document. Follow each entry's link for context.

## Standalone ADRs

| ADR | Title | One-line summary | Trade-off |
|---|---|---|---|
| [0001](0001-postgres-only.md) | Postgres is the only supported datastore | One SQL backend, no SQLite, no MySQL, no external broker. | Operational simplicity vs. lock-in to one DB engine. |
| [0002](0002-single-instance-no-leader-election.md) | One container, one process, no leader election | Single-tenant deployment, no replica coordination. | Operational simplicity, no coordination bugs vs. vertical scaling only and restart-as-recovery latency. |
| [0003](0003-mllp-listener-vendor-neutral.md) | MLLP listener is host-provided and vendor-neutral | The TCP listener does not parse HL7; vendor adapters do. | One well-tested listener vs. each adapter implementing its own. |
| [0004](0004-fhir-version-strategy.md) | FHIR version strategy | R5-shaped internal model, R4B Backport on the wire as primary, R5 native supported. | Multi-version subscriber support vs. version-shim maintenance. |
| [0005](0005-cancel-and-replace-in-adapter.md) | Cancel-and-replace correlation lives in the adapter | Adapter merges cancel-and-replace pairs into a single `update` row. | Vendor knowledge stays in adapter; subscribers see ordinary updates. Hold-window latency on unpaired cancellations. |
| [0006](0006-no-cql-no-regex.md) | Subscription matching uses only spec-defined languages | FHIR search-parameter expressions and FHIRPath. No CQL, no regex, no DSL. | Spec compliance and small evaluator vs. expressiveness for some clinical patterns. |
| [0007](0007-spec-bounded-scope.md) | We stay inside the FHIR Subscriptions spec boundary | No project-private wire-shape, auth, channel, or matching extensions. | Subscriber portability and small codebase vs. some real-world feature asks declined. |
| [0008](0008-resolved-design-questions.md) | Resolutions for 17 design questions surfaced during LLD authoring | Locks in answers for cancel-and-replace correlation_id, partition key, admin API removal, WSS retry handling, MessageHeader.eventCoding, etc. | Closes ambiguities before implementation; reduces v1 scope by removing the admin API and webhook ingress. |
| [0009](0009-language-choice.md) | Implementation language: Go | Single statically-linked Go 1.22+ binary in a distroless container; locked library shortlist for Postgres, HTTP, WSS, JWT, FHIR/HL7, observability. | Container size and contributor barrier vs. hand-rolling subsets of HL7v2 / FHIR / FHIRPath. |
| [0010](0010-implementation-defaults.md) | Nine implementation defaults: correlation_id (UUIDv4), event_number scope, JCS canonicalization, ICU root locale, email v1 SMTP-only, ValueSet directory, audit log table+sink, base R5 profile validation, Apache 2.0 license. | Pins every routine engineering choice the LLDs left ambiguous. | None of these is FHIR-spec-mandated; we accept the project-default cost in exchange for unambiguous contracts before implementation. |

## Decisions documented inside other HLD documents

These are decisions that have architectural consequences but were captured directly in a domain or contract doc rather than a standalone ADR. Each entry points at the section that records the decision.

| Decision | One-line summary | Recorded in |
|---|---|---|
| Pipeline is five stages with Postgres handoffs | Translate → topic match → fanout → build → send. Each stage's output is the next stage's input table. | [../overview.md](../overview.md), `../../architecture.md` ("Pipeline from EHR change to subscriber notification"). |
| Transactional outbox between stages | Each stage's output INSERT is in the same transaction as its input UPDATE-processed. | [../domains/storage.md](../domains/storage.md#the-transactional-outbox-pattern), [../contracts/internal-tables.md](../contracts/internal-tables.md). |
| `SELECT FOR UPDATE SKIP LOCKED` for stage workers | Worker fibers claim distinct rows; durability is the table; coordination is intra-process. | `../../architecture.md` ("Concurrency inside the service"), [../domains/storage.md](../domains/storage.md#in-process-queue-and-wakeup-signals). |
| In-memory wakeup signals are a latency optimization, not a correctness mechanism | If a wakeup is lost, the next consumer finds the row by reading the table. | `../../architecture.md` ("End-to-end sequence"), [../domains/storage.md](../domains/storage.md#in-process-queue-and-wakeup-signals). |
| Persistence-then-ACK on the MLLP listener | Listener does not ACK the EHR until the row is committed. | [../domains/mllp-listener.md](../domains/mllp-listener.md#persistence-then-ack-durability). |
| Multiple MLLP listener endpoints per deployment | One bind+port per HL7 message type, all writing to the same `hl7_message_queue`. | [../domains/mllp-listener.md](../domains/mllp-listener.md#multiple-listener-endpoints-per-deployment). |
| Adapter SPI is base-class framework, not a flat trait | Each sub-component ships a base class with REQUIRED and OPTIONAL overrides; the framework owns cross-cutting concerns. | [../contracts/adapter-spi.md](../contracts/adapter-spi.md), `../../architecture.md` ("The contract — base classes and overrides"). |
| Hydration is the only synchronous in-memory call across the EHR/Subscriptions boundary | Everything else crosses through Postgres rows. | `../../architecture.md` ("End-to-end sequence" notes), [../domains/subscriptions-engine.md](../domains/subscriptions-engine.md#hydration), [../domains/ehr-adapter.md](../domains/ehr-adapter.md#hydration-service). |
| FHIR scans use snapshot-and-diff, not `_lastUpdated` | Most EHR FHIR APIs do not honor `_lastUpdated` accurately. | `../../architecture.md` ("FHIR Scan Runner"), [../domains/ehr-adapter.md](../domains/ehr-adapter.md#fhir-scan-runner). |
| Vendor naming policy | Adapter IDs are vendor-versioned where vendor releases meaningfully differ (`epic`, `epic-2026-11`, etc.). | [../domains/ehr-adapter.md](../domains/ehr-adapter.md#vendor-naming-policy). |
| `adapters/default` is the conformance reference | Generic v2-to-FHIR plus standards-compliant FHIR scan; every vendor adapter passes the same conformance suite. | [../domains/ehr-adapter.md](../domains/ehr-adapter.md#reference-adapter--adaptersdefault). |
| Channel SPI returns `DeliveryOutcome`; engine owns retry policy | Channels do not implement their own queues. | [../contracts/channel-spi.md](../contracts/channel-spi.md), [../domains/channels.md](../domains/channels.md). |
| Custom channels are first-class via the spec's extensible binding | A custom channel registers its own Coding and is listed in `CapabilityStatement`. | [../contracts/channel-spi.md](../contracts/channel-spi.md), [../domains/channels.md](../domains/channels.md#custom-channels). |
| `Subscription.maxCount` caps **events**, not Bundle entries | Included Patient / Practitioner resources do not count toward the cap. | [../contracts/notification-bundle.md](../contracts/notification-bundle.md#batching--subscriptionmaxcount), [../domains/subscriptions-engine.md](../domains/subscriptions-engine.md#batching--subscriptionmaxcount). |
| `maxBatchWait` flushes a partial batch on a timer | Without it, a low-traffic subscription would wait indefinitely for a second event. | [../domains/subscriptions-engine.md](../domains/subscriptions-engine.md#batching--subscriptionmaxcount). |
| `eventsSinceSubscriptionStart` advances only on confirmed delivery | Pending or failed deliveries do not advance the cursor. | [../domains/subscriptions-engine.md](../domains/subscriptions-engine.md#delivery-scheduler), [../contracts/internal-tables.md](../contracts/internal-tables.md#deliveries). |
| `$events` retention default is 30 days | Operators configure longer retention if subscribers need a longer catch-up window. | [../domains/storage.md](../domains/storage.md#retention-details), `../../architecture.md` ("Configuration"). |
| Liveness must not depend on Postgres | A DB outage should not cause a container restart loop. | [../domains/lifecycle.md](../domains/lifecycle.md#healthz--liveness). |
| Graceful shutdown drains in-flight work, no NACKs during drain | Force-exit on grace-period expiry; recovery from durable rows on next start. | [../domains/lifecycle.md](../domains/lifecycle.md#graceful-shutdown). |
| Authentication is SMART on FHIR Backend Services only | mTLS-only and OAuth client-credentials alternatives were removed during review. | [../domains/subscriptions-api.md](../domains/subscriptions-api.md#authentication), [../contracts/subscriber-api.md](../contracts/subscriber-api.md#authentication). |
| Authorization scopes are checked at create time AND delivery time | Per spec: re-check on every notification preparation. | [../domains/subscriptions-api.md](../domains/subscriptions-api.md#authorization), [../contracts/subscriber-api.md](../contracts/subscriber-api.md#authorization-scopes). |
| Topic catalog has three sources with conflict resolution | Operator > adapter > built-in. | [../domains/topics.md](../domains/topics.md#conflict-resolution). |
| Topic versioning is canonical URL plus version | Old versions are retained until no subscription references them. | [../domains/topics.md](../domains/topics.md#versioning). |
| HL7 ships no canonical SubscriptionTopic library | The project ships its own starter set as built-in topics. | [../domains/topics.md](../domains/topics.md#what-hl7-does-and-does-not-ship), `../../architecture.md` ("Open Questions"). |
| FHIRPath evaluator is sandboxed | Wall-clock timeout, node-traversal limit, no I/O, deny-list for non-deterministic functions. | [../domains/topic-matcher.md](../domains/topic-matcher.md#fhirpath-sandboxing), [0006](0006-no-cql-no-regex.md). |
| Configuration is layered, with secret placeholders | CLI > env > file > defaults. Secrets via `${env:VAR}` / `${file:/path}`. No runtime admin API. | [../domains/configuration.md](../domains/configuration.md), [0008](0008-resolved-design-questions.md). |
| Hot reload via SIGHUP only | Topic catalog, client registry, log level, retry parameters. Everything else requires a restart. | [../domains/configuration.md](../domains/configuration.md#hot-reload), [0008](0008-resolved-design-questions.md). |
| Encryption at rest is required for FHIR / HL7 payload columns | Whole-database or column-level; deployment policy. | [../domains/storage.md](../domains/storage.md#encryption-at-rest). |
| Schema migrations are forward-compatible expand-then-contract | A release N+1 must run against a database migrated by N. | [../domains/storage.md](../domains/storage.md#schema-migrations), [../contracts/internal-tables.md](../contracts/internal-tables.md#versioning-policy). |
| Conformance bar is external | Inferno and Touchstone, not project-internal claims. | [../domains/subscriptions-api.md](../domains/subscriptions-api.md#conformance-testing). |
| `resource_changes` and `ehr_events` are partitioned by month | Bounded index size, cheap retention via partition drop. | [../domains/storage.md](../domains/storage.md#partitioning). |
| Audit log retention default is 7 years | HIPAA-aligned default; operators must review against their obligations. | [../domains/storage.md](../domains/storage.md#retention-details), `../../architecture.md` ("Privacy and PHI handling"). |

## How to add a decision

If a future change introduces an architecturally consequential decision:

- **Standalone ADR** if the decision deserves its own page (a tradeoff with named alternatives, broad impact across multiple domains, or future maintainers will benefit from the full Context / Decision / Consequences narrative). Number sequentially: `0008-...`, `0009-...`. Update this index.
- **Inside a domain or contract doc** if the decision is local to that domain (an evaluation rule, a retention default, a manifest field). Add an entry to the table above pointing at the section that records it.

Either way, the decision is recorded once. This index is the single page that finds it.
