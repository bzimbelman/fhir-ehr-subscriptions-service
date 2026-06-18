# Future Work

This document tracks work that is deliberately deferred from the current release. Items are grouped by priority. Each item names the source artifact (LLD, ADR, e2e scenario, code comment) so a contributor can pick it up without rediscovering the context.

The current release ships the FHIR Subscriptions API surface, the seven-stage pipeline (MLLP → HL7 message processor → topic matcher → submatcher → builder → scheduler → channel), four notification channels (rest-hook, websocket, email, message), the adapter SPI with a reference passthrough adapter, full storage + lifecycle + config + observability infrastructure, and 12 of 13 LLD-defined e2e scenarios passing end-to-end through the real pipeline against testcontainers Postgres.

What it does **not** ship is documented below.

---

## Priority 1 — Required before a real-world deployment

These items must land before any production deployment. They are not optional polish; the system has correctness, security, or operational gaps without them.

### 1.1 Adapter framework supervisors (host-side scaffolding)

**Source:** [docs/low-level-design/adapter-spi-framework.md](low-level-design/adapter-spi-framework.md) §1, §3 ("Supervisors")

**Status:** Adapter SPI surface (interfaces, types, registry, reference adapter) is on main. The host-side supervisors that *call into* adapter sub-components are not built.

**What's missing:**

- `Hl7MessageProcessorSupervisor` — wraps an adapter's `Hl7MessageProcessor`; today the `internal/hl7processor/` worker calls the SPI directly, but the LLD specifies a supervisor with restart-on-panic, per-adapter metrics labels, and concurrent-instance fanout
- `FhirScanRunnerSupervisor` — needed to run scheduled snapshot diffs against vendor FHIR endpoints; emits to `resource_changes` table. **No production worker invokes `ScanPlan`/`RunScan` and emits to `resource_changes`** (`internal/adapter/spi/interfaces.go:185-187`)
- `VendorAPIClientSupervisor` — for adapters whose vendors push via change feed
- `LifecycleCoordinator` — the framework-level start/stop coordinator over all adapter sub-components
- `AdapterContext` construction — secrets resolution, Pg state store wiring, transactional sink wiring (today these are stubbed in tests with manual `AdapterContext{}` literals)
- `manifest_validator` — JSON Schema compile of `config_schema`, contributed-topic URL collision detection, capability-vs-builder cross-check (LLD §8 "stateful manifest validations" — explicitly deferred per the adapter-spi merge verifier)

**Why this is P1:** without supervisors, a panic in an adapter callback crashes the worker process. Restart-on-panic + isolated metrics per adapter are operational table stakes.

### 1.2 FHIRPath sandboxed evaluator

**Source:** [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md) §6 "FHIRPath sandbox"; ADR 0006

**Status:** `internal/matcher/matcher.go::runFHIRPath` is a minimal pattern-matching gate covering only `<R>.<field>.exists()` and `<R>.status = '<v>'` shapes (the forms the built-in topics use). Any other FHIRPath expression passes through silently as `true`.

**What's missing:**

- A sandboxed FHIRPath evaluator with:
  - Per-evaluation wall-clock timeout (default 100ms per ADR 0006)
  - Node-traversal limit
  - Deny-list for non-deterministic functions
  - `now()` and `today()` stamped at evaluation start; nothing else with side effects
- Catalog rejection of uncompilable FHIRPath at load time (today the catalog accepts any non-empty string)
- Per-topic metric for FHIRPath evaluation timeouts and runtime errors

**Why this is P1:** silent pass-through on unknown expressions means a topic with a non-trivial `fhirPathCriteria` would fire on every event. That's a security and correctness defect — subscribers would receive notifications they shouldn't see.

### 1.3 `:in` modifier ValueSet expansion in topic matcher

**Source:** [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md), ADR 0006

**Status:** `internal/matcher/matcher.go` returns `false` for any `:in` clause (fail-closed). LLD requires fail-loud rejection at catalog-load time.

**What's missing:**

- ValueSet loader (pre-loaded into the topic catalog per ADR 0006)
- Catalog rejection of `:in` expressions referencing unknown ValueSets
- ValueSet expansion at topic compile time, not at every event evaluation

**Why this is P1:** topics with `:in` filters compile but never fire. Operators get no warning and silently miss notifications.

### 1.4 ICU root-locale folding for all string equality

**Source:** ADR 0010 #4, [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md)

**Status:** `internal/matcher/` applies ICU folding only on `:contains`. ADR 0010 #4 mandates folding on **all** string comparisons including default `=`.

**What's missing:**

- Apply `foldICURoot` to both LHS and RHS in `evaluateClause` before the equality check
- Same for the topic catalog's filter normalization
- Tests covering accented characters in `subject:identifier` filters

**Why this is P1:** spec-compliant FHIR search-parameter matching is locale-insensitive. A subscription with `subject:identifier=Müller` would silently miss events for `mUller` today.

### 1.5 Topic Matcher metrics

**Source:** [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md) §9, ADR 0008 #10

**Status:** Zero metrics emitted from `internal/matcher/`. LLD specifies a full set with prefix `fhir_subs_matcher_*`.

**What's missing:**

- `fhir_subs_matcher_resource_changes_claimed_total{outcome}` — outcome ∈ {processed, deferred, error}
- `fhir_subs_matcher_topics_evaluated_total{topic_id}`
- `fhir_subs_matcher_topic_match_total{topic_id}`
- `fhir_subs_matcher_fhirpath_timeouts_total{topic_id}`
- `fhir_subs_matcher_evaluate_duration_seconds{topic_id}` (histogram)
- `fhir_subs_matcher_ehr_events_emitted_total`

**Why this is P1:** without these, operators have no visibility into matching throughput, slow topics, or fhirpath timeouts. Production triage is impossible.

### 1.6 Admin API or operator surface

**Source:** ADR 0008 #9 explicitly says "no admin API in v1"; deferred but operationally required

**Status:** Per ADR 0008, no admin API ships in v1. This is correct for the spec scope but creates real operational gaps.

**What's missing (for a real deployment):**

- A way to list topics from the catalog, see which are active, and reload after edit
- A way to force-fail a stuck delivery for manual replay
- A way to re-run reconciliation for a stuck cancel/replace pair past its window
- A way to audit which subscriptions a given client owns (today require direct DB access)

**Why this is P1 for production (P2 for spec):** without an operator surface, every operational incident requires a DBA. That's not viable for a 24/7 service.

### 1.7 CapabilityStatement implementation

**Source:** `cmd/fhir-subs/probes.go:91-103` ("makeMetadata returns a stub OperationOutcome saying the service is starting"); subscriptions-api.md §3

**Status:** `/metadata` returns a stub OperationOutcome saying "service is starting; CapabilityStatement not yet available." This is a placeholder.

**What's missing:**

- A real `CapabilityStatement` reflecting:
  - All endpoints in the router
  - The supported FHIR version(s) — R4B Backport IG and R5 native
  - The supported channel types
  - The `$status`, `$events`, `$get-ws-binding-token` operations
  - The OAuth/SMART Backend Services token endpoint and JWKS

**Why this is P1:** spec conformance requires `/metadata` to return a real CapabilityStatement. Subscribers use it for capability discovery; without it, third-party tools and conformance suites fail.

### 1.8 Hydration (`_include` / `_revinclude`)

**Source:** [docs/low-level-design/subscriptions-engine.md](low-level-design/subscriptions-engine.md) §4 "Notification Bundle assembly"; `internal/engine/builder/builder.go:23` ("application/fhir+json in v1; XML is deferred")

**Status:** Builder produces the focus resource(s) only. Per `notification_shape_hint`, the spec allows the server to include related resources via `_include` / `_revinclude`. Today's builder doesn't.

**What's missing:**

- Adapter `HydrationService.Fetch(ctx, ref)` actual implementation in real adapters (the default returns `ErrHydrationUnsupported` for every call — see `adapters/default/default.go:143`)
- Builder integration: when `notification_shape_hint` declares includes, builder calls `HydrationService.Fetch` for each `_include` reference and adds the resource to the Bundle
- Cache layer for hydration (per LLD; default 60s TTL)
- Failure mode: hydration error degrades to focus-only Bundle with a warning in `OperationOutcome` extension

**Why this is P1:** subscribers expecting includes (most clinical decision support apps do) get incomplete Bundles. They can't action the notification without an extra round-trip.

### 1.9 WSS bind transport — Sec-WebSocket-Protocol header

**Source:** WSS verifier finding (PASS-WITH-CAVEATS); [docs/low-level-design/channels.md](low-level-design/channels.md) §4.2

**Status:** `internal/channel/websocket/` accepts the bind token via an in-band JSON message. The LLD pseudo-code (and the FHIR R4B/R5 Backport WebSocket profile most subscribers will write to) specifies the `Sec-WebSocket-Protocol` header form.

**What's missing:**

- Accept bind via `Sec-WebSocket-Protocol: fhirsubscriptions/v1.0+<token>` (or whichever token-encoding the spec settles on; review the latest IG)
- Keep the in-band JSON path for backward compatibility OR remove and document the breaking change

**Why this is P1:** subscribers written against the spec will fail to bind. We're not interoperable until this matches the spec.

### 1.10 Adapter manifest config_schema validation

**Source:** [docs/low-level-design/adapter-spi-framework.md](low-level-design/adapter-spi-framework.md) §8

**Status:** Stateful manifest validations are deferred per the adapter-spi merge: JSON Schema compile of `config_schema`, contributed-topic URL collision detection, capability-vs-builder cross-check.

**What's missing:**

- At adapter registration, compile the manifest's `config_schema` (JSON Schema) and validate the operator-supplied adapter config against it; reject startup on mismatch
- Detect collisions where two registered adapters declare the same contributed topic URL
- Cross-check declared capabilities against the actual builder methods (e.g., `Capabilities.HydrationService=true` must imply non-nil `BuildHydrationService` return)

**Why this is P1:** without this, a malformed adapter manifest fails at runtime instead of startup, with an opaque error message far from the root cause.

### 1.11 Authn/Authz hardening of the WSS bind token storage

**Source:** Discovered post-merge; pending fix on `fix/wss-bind-token-hashing` (in flight)

**Status:** Storage stores `sha256(token)` but the channel sends cleartext to `Consume`, so production binds never match. The e2e harness papers over it with a sha256 wrapper. Fix in progress.

**What's missing:**

- Move sha256 hashing into `WsBindingTokensRepo` (Insert and Consume both hash internally with cleartext input)
- Remove inline sha256 from API handler
- Remove the harness workaround
- Verify e2e wss scenario still passes once the workaround is gone

**Why this is P1:** WSS subscriptions are completely non-functional in production today.

### 1.12 Dead-letter operational runbook

**Source:** [docs/low-level-design/storage.md](low-level-design/storage.md) (`dead_letters` table); ADR 0001

**Status:** Schema and write path exist. No documented process for what to do with rows in the table.

**What's missing:**

- Operator runbook: how to inspect dead_letters, how to requeue, how to mark resolved
- Optional: a CLI subcommand on `fhir-subs` (e.g., `fhir-subs dead-letters list|replay|forget`) — though admin operations are scope-bounded per ADR 0008
- Metric: `fhir_subs_dead_letters_total{reason}` for alerting

**Why this is P1:** dead_letters silently accumulate without process. Subscribers don't know they're missing notifications.

---

## Priority 2 — Important for a v1.0 release

These items are not strictly required to deploy, but they materially limit the system's usefulness or make on-call painful.

### 2.1 FHIR Scan Runner adapter framework worker

**Source:** [docs/low-level-design/e2e-harness.md](low-level-design/e2e-harness.md) (`cancel_and_replace_scan` scenario); `internal/adapter/spi/interfaces.go:185-187`

**Status:** `FhirScanRunner` SPI interface exists. No production worker invokes `ScanPlan`/`RunScan` and emits to `resource_changes`. The 13th e2e scenario (`cancel_and_replace_scan`) is the documented DEFERRED scenario waiting on this.

**What's missing:**

- Production worker that:
  - Reads adapter-supplied scan plans (which FHIR endpoints to poll, how often, what resource types)
  - Diffs successive snapshots to produce `resource_changes` rows
  - Handles pagination, etag caching, conditional GET
  - Schedules per-plan with jittered intervals
- Unblocks the `cancel_and_replace_scan` scenario

**Why this is P2:** vendors that don't push HL7 v2 (e.g., FHIR-native EHRs that publish a Subscription endpoint but don't implement Subscriptions) are not supported until this lands. With it, a polling adapter becomes a generic option for any vendor.

### 2.2 Vendor API Client framework worker

**Source:** [docs/low-level-design/adapter-spi-framework.md](low-level-design/adapter-spi-framework.md), `internal/adapter/spi/interfaces.go`; e2e scenario `TestScenario_VendorChangeFeedEmitsResourceChange` (skipped)

**Status:** SPI exists. No worker invokes it.

**What's missing:**

- Production worker that:
  - Polls or streams from a vendor's proprietary API per `VendorAPIClient.Stream()` or equivalent
  - Translates vendor-shaped messages into `resource_changes` rows
  - Implements backoff, reconnect, dead-letter on persistent failure

**Why this is P2:** unblocks adapters for vendors whose change feed isn't HL7 v2 or FHIR REST polling — Athena, NextGen, Cerner Code, etc.

### 2.3 Email channel S/MIME + Direct SMTP support

**Source:** ADR 0010 #5 ("v1 ships SMTP-only; S/MIME and Direct deferred to v2"); [docs/low-level-design/channels.md](low-level-design/channels.md) §"Email channel"

**Status:** v1 ships clear-SMTP only with STARTTLS. Real healthcare deployments often require S/MIME signing or Direct SMTP for HIPAA-compliant clinical messaging.

**What's missing:**

- S/MIME signing/encryption layer atop the existing SMTP path
- Direct SMTP profile (DTAAP-compliant message encoding, Direct-trust-bundle validation)
- Configurable S/MIME cert/key resolution from the secret store
- Tests against a fake Direct receiver

**Why this is P2:** without these, the email channel is unusable for HIPAA-compliant clinical workflows in the US.

### 2.4 R4B/R5 wire negotiation completeness

**Source:** [docs/high-level-design/decisions/0004-fhir-version-strategy.md](high-level-design/decisions/0004-fhir-version-strategy.md), `internal/api/versionshim/`

**Status:** `Negotiate(acceptHeader)` returns either R4B or R5 from `Accept` header parsing. The full conversion of `Subscription` and `SubscriptionTopic` resources between R4B Backport IG shape and R5 native shape is **not** implemented in the version shim.

**What's missing:**

- R4B → R5 conversion for `Subscription` (criteria URL parsing, channel.endpoint location differences)
- R5 → R4B serialization on read paths
- `Bundle` of type `subscription-notification` shape differences between R4B and R5
- Tests with golden inputs from the spec's official examples

**Why this is P2:** today the system only fully serves R5-native subscribers. R4B subscribers can negotiate to R4B but the response shape is the R5-native form — not the spec's R4B Backport IG form. This breaks conformance for R4B clients.

### 2.5 Audit chain verifier CLI

**Source:** [docs/low-level-design/observability.md](low-level-design/observability.md) §"Audit log"; e2e scenario `TestScenario_AuditChainIsValid` (skipped)

**Status:** Audit log writes to Postgres `audit_log` with hash-chained JCS-canonicalized rows. No tool to verify the chain integrity post-hoc.

**What's missing:**

- `fhir-subs audit verify [--from <ts>] [--to <ts>]` subcommand that walks the chain and reports any break
- Per-row JCS re-canonicalization to confirm the stored hash matches
- Optional: out-of-band signature on the chain head for tamper-evident export

**Why this is P2:** a hash-chained audit log without a verifier is a bookkeeping exercise. Auditors need a way to prove the chain is intact.

### 2.6 Heartbeats and handshakes

**Source:** [docs/low-level-design/subscriptions-engine.md](low-level-design/subscriptions-engine.md) §6

**Status:** Builder fully wires Bundle assembly for handshake / heartbeat / query-status / query-event Bundles. The scheduler's claim loop only handles `event-notification` deliveries — it does not emit heartbeats on idle subscriptions or send handshake notifications on subscription state transitions.

**What's missing:**

- Heartbeat timer wheel (per LLD: configurable per-subscription `heartbeatPeriod`, default off)
- State-machine for subscription transitions (`requested` → `active` → `error` → `off`) that emits handshake Notifications via the configured channel
- Tests covering the four notification types (event, handshake, heartbeat, query-status, query-event)

**Why this is P2:** subscribers can't tell if a quiet subscription is healthy or broken. Heartbeats are part of the spec's reliability story.

### 2.7 Auth re-check at delivery prep

**Source:** [docs/low-level-design/subscriptions-engine.md](low-level-design/subscriptions-engine.md) §3 "submatcher"; engine merge LLD ambiguity #5

**Status:** `internal/engine/submatcher/` has a `FanoutAuthRevoked` decision in the public API but the worker doesn't drive it because the project has no `AuthValidator.Recheck()` SPI yet. The hook is in place; the integration is deferred.

**What's missing:**

- Define the `AuthValidator.Recheck(ctx, subscriptionID) (Active, error)` SPI in `internal/api/auth/`
- Wire submatcher to call it before fanout for each candidate subscription
- Cache rechecks (subscription-level TTL) to avoid hammering the auth path
- Tests: revoked subscription stops receiving deliveries within configured TTL

**Why this is P2:** subscriptions whose owning client is revoked continue to receive notifications until the next manual delete. That's a confidentiality risk.

### 2.8 OpenTelemetry trace export configuration

**Source:** [docs/low-level-design/observability.md](low-level-design/observability.md)

**Status:** OTel tracing is wired throughout (correlation IDs, spans on all major operations). The exporter is configurable but defaults to no-op (`stdout` for development). Production deployments need OTLP/HTTP, OTLP/gRPC, Jaeger, etc.

**What's missing:**

- Configuration schema for the OTel exporter (endpoint, headers, sampling rate, TLS)
- Documented deployment recipes for Datadog, Honeycomb, Jaeger
- A "start with traces, end with traces" smoke test in deployments

**Why this is P2:** without trace export, distributed tracing is local-only. Cross-service debugging across the EHR adapter → bridge → subscriber path is invisible.

### 2.9 Webhook ingress (vendor push)

**Source:** ADR 0008 #1 ("Deferred to a future release. v1 ships without a webhook ingress path.")

**Status:** Explicitly out of scope for v1.

**What's missing:**

- Host-provided HTTP receiver that vendors can POST to
- Per-vendor authentication (HMAC, mTLS, shared secret rotation)
- Mapping from inbound HTTP body to `hl7_message_queue` or directly to `resource_changes`

**Why this is P2:** unblocks adapters for any vendor that pushes via webhook (Cerner Code, Epic SmartLite, etc.).

### 2.10 Multi-instance / horizontal scale

**Source:** [docs/high-level-design/decisions/0001-postgres-only.md](high-level-design/decisions/0001-postgres-only.md), [docs/high-level-design/decisions/0002-single-instance-no-leader-election.md](high-level-design/decisions/0002-single-instance-no-leader-election.md)

**Status:** v1 is explicitly single-instance per ADR 0002. The `SELECT FOR UPDATE SKIP LOCKED` claim primitive supports multi-worker within one process; multi-instance needs more.

**What's missing (when scale demands it):**

- Postgres replica + connection pooler (PgBouncer) configuration
- Partitioning strategy for `resource_changes` and `ehr_events` (the schema already supports monthly partitions; add a partition rotator)
- Optional sharding strategy for very-high-throughput deployments

**Why this is P2:** until volume demands it, single-instance is operationally simpler. Most facility-scale deployments will not need this.

---

## Priority 3 — Polish and quality-of-life

Nice-to-haves that improve developer/operator experience but don't block production.

### 3.1 Adapter authoring guide

**Source:** Talk slide deck (presentation.md) "Next Steps"; community ask #1

**Status:** No comprehensive guide. The SPI is documented; there's no walkthrough of "build an adapter for vendor X from scratch."

**What's missing:**

- Step-by-step tutorial: scaffold the package, declare the manifest, implement the four sub-components, run the conformance harness
- Example adapter for a common vendor (Epic, Athena, NextGen)
- Conformance crate (LLD §9.2) — a packaged test suite that any adapter author can run against their adapter

### 3.2 More EHR adapters

**Source:** Talk slide deck "Call for Help from the Community"

**What's missing:**

- Epic adapter (Z-segment extensions, Interconnect API, FHIR profile quirks)
- Cerner / Oracle Health adapter
- Athena adapter
- NextGen adapter
- MEDITECH adapter
- Allscripts adapter
- Direct SMTP adapter (for facilities that only expose Direct)

### 3.3 Repository unused code cleanup

**Source:** WSS verifier note, e2e wireup post-merge survey

**What's missing:**

- `WsBindingTokensRepo.Get` and `Delete` are now unused on main (the API uses `Insert` and the channel uses `Consume`). Either keep with tests or remove.
- `cmd/fhir-subs/probes.go` `/metadata` stub will be obsolete once the real CapabilityStatement lands (P1.7)
- `internal/api/handlers/handlers_test.go:701` has `t.Skip("compile-time placeholder only")` — verify this is still needed once P1.7 lands

### 3.4 Container / Helm packaging

**What's missing:**

- Multi-arch Dockerfile (linux/amd64, linux/arm64)
- Helm chart with sensible defaults for Postgres dependency, secret management (External Secrets Operator integration), HPA, PDB, NetworkPolicy
- Reference Postgres operator setup (CrunchyData / Zalando)
- Migration runner sidecar / init container

### 3.5 Documentation site

**What's missing:**

- Hosted docs (mkdocs / docusaurus) with the LLD/ADR set rendered with cross-references
- Deployment guide for AWS, GCP, Azure, on-prem K8s
- Chat / community link expansion (chat.fhir.org channel name; GitHub Discussions enabled)

### 3.6 CI/CD

**What's missing:**

- GitHub Actions: build, test (unit + integration + e2e), lint, gofmt, race detector across all PRs
- Codecov / coverage reporting
- Release automation (semver tags, binary artifacts, container image push)
- Conformance test nightly run against the spec's reference subscriber

---

## Priority 4 — Speculative / spec-evolution dependent

These wait on external developments.

### 4.1 FHIR R6 support

**Source:** [docs/high-level-design/decisions/0004-fhir-version-strategy.md](high-level-design/decisions/0004-fhir-version-strategy.md)

**Status:** R6 is in first full ballot. Project tracks it but does not implement against ballot drafts.

**What changes when R6 publishes:** new wire shape variants for the version shim; potentially new channel types or new optional Bundle entries; spec compliance review.

### 4.2 Spec extensions

**Source:** ADR 0007 (Spec-bounded scope)

**Status:** Project does not add private extensions. If a feature is genuinely useful but outside the spec, the path is: support on the subscriber side, support via a custom channel, or propose to HL7 for inclusion in a future spec version.

---

## How to pick this up

Each item names its source artifact. Before starting:

1. Read the named LLD/ADR/scenario file
2. Check if a related issue exists on GitHub
3. Open a worktree off main, follow the project's TDD-mandatory operating procedure
4. Submit a PR with the work + tests + doc updates

Items in **Priority 1** are the right place for a contributor who wants production impact. Items in **Priority 2** are the right place for someone who wants to expand the system's reach. Items in **Priority 3** are the right place for someone who wants to ship adapter or operator experience.
