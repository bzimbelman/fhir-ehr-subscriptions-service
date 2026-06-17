# 0008 — Resolved Design Questions

**Status:** Accepted

## Context

The Low-Level Design pass surfaced 17 design questions where the architecture and HLD were silent or implicit. They were collected during the LLD authoring round and reviewed against the FHIR Subscriptions spec, the architecture's existing commitments, and prior decisions in this directory.

This ADR locks each one in so the LLDs and any future implementation doc reference a single authoritative answer instead of re-litigating.

## Decision

| # | Topic | Decision |
|---|---|---|
| 1 | Vendor webhook ingress (host-provided HTTP receiver for vendors that push) | **Deferred to a future release.** v1 ships without a webhook ingress path. Adapters whose vendors push via webhook are not supported in v1. Add when an adapter actually needs it. |
| 2 | Cancel-and-replace `correlation_id` on the merged `update` row | **The held half's `correlation_id`.** It is the durable anchor — already committed to `pending_pairs` and to its source `hl7_message_queue` row — so retries of the merge transaction stay idempotent. |
| 3 | Cancel-and-replace recognition at FHIR scan time | **Implement.** The FHIR Scan Runner correlates pairs the same way the HL7 Message Processor does (cancelled resource alongside its replacement linked via `basedOn` / `replaces`) and emits one consolidated `update` row. Behavior is identical across HL7 and scan paths so subscribers see the same wire shape regardless of which path detected the change. |
| 4 | Adapter manifest declares `capabilities[X] = false` but `build_X()` returns non-null | **Fatal startup error.** The host validates the manifest against build_* returns and refuses to start. Adapters lying about their own capability is not silently tolerated. |
| 5 | Failed adapter sub-component supervisors | **No in-process auto-restart.** Supervisor failure crashes the process; the container orchestrator's restart policy is the supervisor of last resort. This matches the broader "no leader election, no replica coordination" model — adding in-process supervision recreates the complexity removed elsewhere. |
| 6 | Partition key for `resource_changes` and `ehr_events` | **`created_month`** (server-side insert time). Predictable, every partition fills cleanly, and retention drops cleanly with `DETACH PARTITION + DROP TABLE`. Late-arriving rows still land in the current month's partition. `occurred_at` queries are still indexed for `$events` replay even though they aren't partition-aligned. |
| 7 | Cancel-and-replace ordering: replacement arrives before cancellation | **Symmetric hold.** `pending_pairs` accepts both `pending_kind = delete` (held cancellation, default case) and `pending_kind = create` (held replacement). The merged `update` row is identical regardless of which half arrived first. Subscribers see the same wire shape no matter what order the EHR sent. |
| 8 | Cancel-and-replace pair recognition across two MLLP listener endpoints | **Same-endpoint only.** A pair is recognized only when both halves arrived on the same listener endpoint. Cross-endpoint correlation is not attempted; if a vendor splits halves across feeds, both emit as plain `delete` / `create` and a metric records the unmatched pair. The architecture already requires one message type per endpoint, so cross-endpoint pairing should not occur in well-configured deployments. |
| 9 | Admin API for runtime mutation (subscribers, topic catalog, log level, retry parameters) | **Drop the admin API.** The four operations originally proposed for admin are all config changes. Operators manage the deployment via: config file + SIGHUP for the reloadable subset; container restart for everything else; health/readiness probes for orchestrator interaction; metrics / traces / logs / audit log for read-only observability. The FHIR Subscriptions spec does not define admin endpoints; we do not invent them. |
| 10 | Prometheus metric prefix | **`fhir_subs_`** (e.g., `fhir_subs_deliveries_total`). Searchable, unambiguous, matches the architecture's earlier convention. The shorter `fsf_` prefix that appeared in some LLD drafts is rejected. |
| 11 | Audit-log default sink | **`stdout`.** Container deployments — including the project's Kubernetes target — collect logs from stdout. `file` remains a documented alternative for non-containerized deployments. |
| 12 | `$events` replay: filter against current `Subscription.filterBy` or filter as it was at original delivery | **Current `filterBy`.** Spec is ambiguous; we choose the simpler and more commonly-expected semantics: the subscriber gets events that match what they're currently subscribed to. Documented explicitly so subscribers do not expect time-travel replay. |
| 13 | `MessageHeader.eventCoding` for the `message` channel | **Spec-aligned.** The `system` is `http://terminology.hl7.org/CodeSystem/subscription-notification-type` (the same code system used for `SubscriptionStatus.type` in R5), and the `code` is the matching notification-type code (`event-notification` / `heartbeat` / `handshake` / `query-status` / `query-event`). No project-private code system is invented. |
| 14 | `$events` `?content=` parameter override | **Honor it, gated on scopes.** Spec is ambiguous; permissive default. The override is only honored if the calling subscriber's scopes still permit the requested content level (the spec-mandated delivery-time scope check applies to `$events` replays the same as live notifications). |
| 15 | Bulk `$status` partial failure | **Searchset Bundle with OperationOutcome entries.** Each requested subscription becomes one Bundle entry: a `SubscriptionStatus` on success, an `OperationOutcome` on failure. FHIR-idiomatic, matches the convention used elsewhere for bulk operations. |
| 16 | Conditional update (`PUT /Subscription/?criteria=`) | **Not in v1.** FHIR-defined but adds non-trivial complexity (resolving criteria, handling 0/1/many matches). Documented as a known gap. |
| 17 | WebSocket channel reconnect handling | **Same retry/backoff/error-state policy as every other channel.** WSS is not special. The channel detects disconnection and returns `TransientFailure` with a backoff hint; the delivery scheduler applies the standard exponential-backoff-with-jitter retry curve and transitions the subscription to `error` after max attempts per the spec. There is no WebSocket-specific reconnect grace, no binding-token-TTL coupling, no separate timeout. |

## Consequences

**Positive.**
- The LLD set has a single authoritative answer for every previously-open question. Implementers do not have to re-derive these.
- Several decisions reduce v1 scope: no admin API (#9), no webhook ingress (#1), no conditional update (#16). Each is a real complexity savings.
- Cancel-and-replace handling is now consistent across HL7 and FHIR scan paths (#3, #7), so subscribers see uniform wire shape regardless of detection path.
- WebSocket handling stops being a special case (#17). One retry policy serves every channel.
- All spec-ambiguous decisions (#12, #14, #15, #13) are documented so the project's choice is clear at conformance-test time.

**Negative.**
- Vendors whose change feed is webhook-only have no v1 path (#1). The first such adapter will need to design and ship the webhook receiver — likely host-provided to mirror the MLLP listener pattern.
- "No admin API" (#9) means the only way to add a subscriber client or topic at runtime is config-file-then-SIGHUP. Operators expecting a REST surface for these operations will not find one.
- Symmetric cancel-and-replace hold (#7) doubles the size of `pending_pairs` in the worst case (held creates and held deletes both possible). Still small; not a real problem.
- `created_month` partitioning (#6) means an `$events` query for "events that occurred last month" but were ingested this month reads two partitions. Acceptable; the indexed `occurred_at` column makes the query correct, just not partition-aligned.

**Neutral.**
- The previously-flagged audit-log default change from `file` to `stdout` (#11) is purely a default-config change; operators can override.
- The metric-prefix decision (#10) is naming hygiene; mechanically inconsequential.

## Touched documents

This ADR drives targeted text changes in:

- `architecture.md` — references to a runtime admin API (drop them); WSS-specific reconnect language (drop it); audit-log default sink (`stdout`).
- `high-level-design/domains/configuration.md` — admin-API references; reloadable surface stays via SIGHUP only.
- `high-level-design/domains/observability.md` — metric prefix.
- `high-level-design/domains/lifecycle.md` — already aligned.
- `low-level-design/*.md` — every "Open question" that this ADR closes is updated to point at this ADR with the resolved answer; every default sink / metric prefix value is updated to match.
