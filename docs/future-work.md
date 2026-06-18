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

### 1.4 ICU root-locale folding for all string equality — RESOLVED

**Source:** ADR 0010 #4, [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md)

**Status:** RESOLVED on `fix/p14-icu-folding-equality` (commit `8d4bbf9`). Both `internal/matcher/matcher.go` and `internal/engine/submatcher/submatcher.go` now route every string equality through a `foldEqual(a, b) = foldICURoot(a) == foldICURoot(b)` helper. Affected functions in each file: `equalsToken`, `equalsReference`, `equalsString`, `matchIdentifier`. The `:not` and bare-`=` clause paths inherit folding via these helpers; `:contains` was already folded. As a side fix, the package-level `transform.Chain` was replaced with a `sync.Pool` of fresh chains because `transform.Chain` is stateful and not goroutine-safe — the previous shared chain was a latent panic risk under concurrent fold calls.

**Notes:**

- `:exact` is intentionally case-sensitive per FHIR spec; the matcher's switch never accepts it (falls through to default → returns false), so no change needed.
- ValueSet `:in` (P1.3) is unrelated and tracked separately.
- The catalog itself stores authored string values verbatim; folding is applied at compare time, not at load.

### 1.5 Topic Matcher metrics — RESOLVED

**Source:** [docs/low-level-design/topic-matcher.md](low-level-design/topic-matcher.md) §9, ADR 0008 #10

**Status:** RESOLVED on `feat/future-work-p2-batch` (P2 batch). `internal/matcher/matcher.go` exposes a `MetricsEmitter` interface and `SetMetricsEmitter` hook (mirroring the existing reporter pattern). `internal/infra/observability/observability.go::Start` registers the counters and installs an emitter that forwards. Worker fires `ResourceChangeClaimed{outcome}` on every claim; `Evaluate` fires `TopicEvaluated`/`TopicMatch`/`EvaluateDuration` per candidate topic; the FHIRPath fail-closed reporter is wired to `FHIRPathTimeout`; ehr_events insertion fires `EhrEventEmitted`.

**What landed:**

- `fhir_subs_matcher_resource_changes_claimed_total{outcome}` — outcome ∈ {processed, deferred, error}
- `fhir_subs_matcher_topics_evaluated_total{topic_id}`
- `fhir_subs_matcher_topic_match_total{topic_id}`
- `fhir_subs_matcher_fhirpath_timeouts_total{topic_id}`
- `fhir_subs_matcher_evaluate_duration_seconds{topic_id}` (histogram)
- `fhir_subs_matcher_ehr_events_emitted_total`

Note: per the cardinality validator (S-2.20) `topic_url` is forbidden as a metric label; the implementation uses the canonical URL string in the `topic_id` label slot, which is bounded by the active topic catalog (typically O(10s) topics).

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

**Source:** `cmd/fhir-subs/probes.go:91-103` (legacy stub, retained as fallback); subscriptions-api.md §3

**Status:** PARTIAL. The real `CapabilityStatement` is implemented at `internal/api/handlers/subscription_handlers.go::buildCapabilityStatement` (around line 1014) and mounted via `RegisterPublicRoutes` at `internal/api/handlers/router.go:251`. The body advertises the FHIR version, software/implementation metadata, the `Subscription` and `SubscriptionTopic` resources with all CRUD interactions, the `$status` / `$events` / `$get-ws-binding-token` operations, the supported channel codes, the active topic URLs, and a SMART-on-FHIR security service coding. The legacy stub in `cmd/fhir-subs/probes.go` remains only as a fallback if handlers aren't wired.

**What's still missing:**

- Multi-version `fhirVersion`: today the body emits a single `Deps.FHIRVersion` value; the LLD requires the response to reflect both R4B Backport IG and R5 native when both are negotiable. (The unmerged `feat/future-work-p1-batch` commit `033f942` enriches this surface.)
- OAuth / SMART Backend Services discovery: `security.service` declares `SMART-on-FHIR` as the code, but the response carries no extension advertising the token endpoint URL or the JWKS URL. Subscribers that rely on `CapabilityStatement.rest.security.extension[oauth-uris]` cannot discover the endpoints today.
- R4B Backport profile/`instantiates` reference: the body does not declare the `instantiates` URI for the Subscriptions R5 Backport IG.
- Conformance-suite checks against the spec's reference profile have not been run.

**Why this is P1:** spec conformance requires `/metadata` to return a CapabilityStatement that advertises the OAuth discovery URIs and the supported FHIR versions. Without these, conformance suites and third-party SMART clients fail capability discovery.

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

**Source:** Discovered post-merge; landed on `fix/wss-bind-token-hashing` (merged via `b624b7d`)

**Status:** RESOLVED (`b624b7d`, commit `7b5b7c2`). `internal/infra/storage/repos/ws_binding_tokens.go` carries an internal `hashToken(s) = sha256(s)` helper (line 22) that is applied on every `Insert` (line 63) and every `Consume` (line 74), so callers pass cleartext and the on-disk column stores `sha256(cleartext)`. The inline sha256 wrappers in the API handler and the e2e harness have been removed; e2e WSS scenarios pass against the production code path.

**What landed:**

- `hashToken` applied at the storage boundary on Insert, Consume, expiry check, and lookup
- Inline sha256 removed from the API handler and the e2e harness
- e2e wss scenario passes end-to-end with cleartext passed through the channel

**Why this is P1:** WSS subscriptions were completely non-functional in production before this; binds now match because both the writer and the reader hash with the same boundary.

### 1.12 Dead-letter operational runbook

**Source:** [docs/low-level-design/storage.md](low-level-design/storage.md) (`dead_letters` table); ADR 0001

**Status:** PARTIAL. The metric is registered and emitted on origin/main: `MetricDeadLettersTotal = "fhir_subs_hl7processor_dead_letters_total"` is declared at `internal/hl7processor/metrics.go:34` and incremented at `internal/hl7processor/processor.go:777` per dead-letter insert. The operational runbook (inspect / requeue / forget by `reason`) and the optional CLI subcommand are still pending — both are queued on the unmerged `feat/future-work-p1-batch` branch (commit `6ac7051`).

**What landed:**

- `fhir_subs_hl7processor_dead_letters_total` counter registered and emitted from the HL7 processor

**What's still missing:**

- Operator runbook (`docs/operations/dead-letters-runbook.md`): how to inspect dead_letters, how to requeue, how to mark resolved, by `reason` value
- Optional CLI subcommand on `fhir-subs` (e.g., `fhir-subs dead-letters list|replay|forget`) — admin operations are scope-bounded per ADR 0008 and are tracked under P1.6
- Top-level `fhir_subs_dead_letters_total{reason}` rollup counter wired through `repos.SetDeadLetterReporter` and `metrics.Inventory` (the hl7processor-scoped metric exists; the cross-pipeline rollup with bounded `reason` labels is not yet registered)

**Why this is P1:** the metric alone lets operators alert on dead_letter rate, but without a runbook on-call has no procedure to drain or triage rows when the alert fires.

---

## Priority 2 — Important for a v1.0 release

These items are not strictly required to deploy, but they materially limit the system's usefulness or make on-call painful.

### 2.1 FHIR Scan Runner adapter framework worker — PARTIAL (MVP)

**Source:** [docs/low-level-design/e2e-harness.md](low-level-design/e2e-harness.md) (`cancel_and_replace_scan` scenario); `internal/adapter/spi/interfaces.go:185-187`

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch, MVP). `internal/adapter/scanrunner/` ships a production worker that drives the `FhirScanRunner` SPI: at startup it reads `ScanPlan()`, runs each target on its declared `Cadence` via a per-target ticker, and writes one `resource_changes` row per yielded resource. Successive scans of the same `(resourceType, id)` are gated by `ContentHash` — same hash → no row. First-sighting → `create`; different hash → `update`. The hash cache is in-memory and cold on restart (re-emits every resource as `create` after a process restart).

**What landed:**

- `internal/adapter/scanrunner/scanrunner.go`: `Worker` + `RowSink` SPI + `NewRepoSink` adapter
- Per-target ticker driven by `ScanTarget.Cadence`; immediate-on-startup tick
- ContentHash dedup with in-memory cache
- `TickOne` exported test seam
- Unit tests: first-sighting create, dedup on identical content, update on content change, empty-plan idle, New validation

**What's still pending (post-MVP):**

- Persistent ContentHash store (so the cache survives restart)
- etag / Last-Modified / conditional GET
- Supervisor restart-on-panic with per-adapter labels (LLD framework §3 "Supervisors")
- DELETE detection (the SPI's current `ScanIterator` does not surface tombstones)
- Production wiring into `buildProductionRuntime` (one Worker per registered adapter that declares `FhirScanRunner` capability) — the package is import-ready
- e2e scenario `cancel_and_replace_scan` activation

### 2.2 Vendor API Client framework worker — PARTIAL (MVP)

**Source:** [docs/low-level-design/adapter-spi-framework.md](low-level-design/adapter-spi-framework.md), `internal/adapter/spi/interfaces.go`; e2e scenario `TestScenario_VendorChangeFeedEmitsResourceChange` (skipped)

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch, MVP). `internal/adapter/vendorclient/` ships a production worker that drives the `VendorAPIClient` SPI: it hands the adapter an `EventSink`, calls `Consume(ctx, sink, cursor)`, and on each `sink.Push(record)` translates the record via `VendorAPIClient.Translate` and persists a `resource_changes` row. Cursor advances after a successful insert; on a `Consume` error the worker retries with exponential backoff (1s → 60s default). The cursor is in-memory only — on restart, `Consume` is called with `Options.InitialCursor`.

**What landed:**

- `internal/adapter/vendorclient/vendorclient.go`: `Worker`, `RowSink` SPI, `NewRepoSink` adapter, `eventSink` private impl
- Backoff loop in `Run` with configurable initial/max
- `PushOne` test seam
- Unit tests: PushOne translates+persists+advances cursor, translate error suppresses persistence and cursor advance, Run drives Consume to completion on ctx cancel, New validation

**What's still pending (post-MVP):**

- Persistent cursor store (LLD's `adapter_state` table) so cursor survives restart
- Supervisor restart-on-panic with per-adapter labels
- Dead-letter on persistent translate / insert failure (today errors propagate; production would benefit from a per-record dead-letter sink)
- Production wiring into `buildProductionRuntime` (one Worker per adapter that declares `VendorAPIClient` capability) — the package is import-ready
- e2e scenario `TestScenario_VendorChangeFeedEmitsResourceChange` activation

### 2.3 Email channel S/MIME + Direct SMTP support — PARTIAL (MVP — extension point only)

**Source:** ADR 0010 #5 ("v1 ships SMTP-only; S/MIME and Direct deferred to v2"); [docs/low-level-design/channels.md](low-level-design/channels.md) §"Email channel"

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch, MVP extension-point only). The email channel now accepts `Mode=ModeSMIME` when an operator-supplied `Config.Signer` (the new `email.Signer` SPI) is wired. The SPI has a single method, `Sign(message []byte) ([]byte, error)`, that returns a `multipart/signed` envelope wrapping the unsigned MIME bytes; the channel calls it after `buildMIME` and before SMTP DATA.

This MVP intentionally ships **no production signer**. A real S/MIME signer requires careful PKCS#7 SignedData encoding, certificate-store integration, and trust-bundle validation that is outside the scope of this batch (and benefits from a third-party library audit). Operators that need S/MIME today implement `Signer` themselves (e.g., wrapping `github.com/digitorus/pkcs7`) and inject it via Config; the channel handles routing.

**What landed:**

- `internal/channel/email/smime.go`: `Signer` SPI, `ErrSignerRequired`, `applySMIMESignature` hook
- `email.New` accepts `ModeSMIME` when `Config.Signer != nil`; rejects with `ErrSignerRequired` otherwise
- `Channel.Deliver` runs `applySMIMESignature` after `buildMIME` when `Mode=ModeSMIME`
- Unit tests: ModeSMIME requires Signer, ModeSMIME accepts with Signer, ModeDirect still rejected, unknown mode rejected

**What's still pending (post-MVP):**

- A bundled production `Signer` implementation (PKCS#7 SignedData with hardware-token + file-based PKCS#12 keystores)
- `ModeDirect` (DTAAP-compliant message encoding, Direct-trust-bundle validation, MDN handling) — currently still rejected at `New`
- Encryption (S/MIME enveloped data) on top of signing
- Tests against a fake Direct receiver

**Why this is P2:** the structural extension point unblocks operators that have an existing S/MIME stack to integrate. A bundled production signer + Direct support is a v1.0 follow-up.

### 2.4 R4B/R5 wire negotiation completeness — PARTIAL (MVP)

**Source:** [docs/high-level-design/decisions/0004-fhir-version-strategy.md](high-level-design/decisions/0004-fhir-version-strategy.md), `internal/api/versionshim/`

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch, MVP). The version shim now provides `RenderSubscriptionR4B(r5Body)` that converts the R5-native Subscription into the R4B Backport IG shape: `topic → criteria`, `channelType.code → channel.type`, `endpoint → channel.endpoint`, `content → channel.payload`, `header` flattens from `[{name,value}]` objects to `"Name: Value"` strings, `heartbeatPeriod`/`timeout` move to `channel`, `filterBy` lifts to `_criteria.extension` as the Backport criteria-filter. `internal/api/handlers/subscription_model.go::renderSubscriptionForAccept` plumbs the converter to all read paths (read, search, create-response, update-response). When `Accept: application/fhir+json; fhirVersion=4.0` is negotiated, R4B subscribers now receive the Backport shape.

**What landed:**

- `versionshim.RenderSubscriptionR4B` Subscription R5→R4B converter
- `handlers.renderSubscriptionForAccept` negotiation+render helper
- Read, search, create-response, and update-response all honor `Accept` header
- Unit tests for the converter (top-level mapping, header flattening, filterBy lift, non-Subscription passthrough, malformed JSON)

**What's still pending (post-MVP):**

- `SubscriptionTopic` resource R5↔R4B conversion on the read path. Today the topic-catalog endpoint emits R5 native regardless of negotiation.
- `Bundle` of type `subscription-notification` R4B serialization on the channel-delivery path. The notification builder emits R5 native; R4B subscribers receive R5-shaped bundles.
- Inverse conversion (R4B→R5) on the write path. Today `parseInternalFromBody` accepts both wire shapes loosely; a stricter R4B-only path with a Backport schema would let writers be more idiomatic.
- Conformance suite tests against the spec's official R4B golden examples.

**Why this is P2:** the MVP closes the most-requested gap: R4B subscribers negotiating `fhirVersion=4.0` now see the spec-shaped Subscription resource on read paths. SubscriptionTopic and Bundle conversions remain — they are tracked here as the v1.0 follow-up.

### 2.5 Audit chain verifier CLI — RESOLVED

**Source:** [docs/low-level-design/observability.md](low-level-design/observability.md) §"Audit log"

**Status:** RESOLVED on `feat/future-work-p2-batch` (P2 batch). The `fhir-subs` binary now dispatches a top-level `audit` subcommand. `fhir-subs audit verify [--config PATH] [--from RFC3339] [--to RFC3339]` walks the audit_log chain end-to-end, recomputes each row's chain_hash from the JCS-canonicalized event bytes, and reports per-row mismatches. The audit package gains `VerifyChainReport(ctx, store, opts) (VerifyResult, error)` — a structured walker that returns `RowsSeen`, `HeadHash` (hex of the most recent chain_hash, suitable for out-of-band export as a chain checkpoint), and a `Breaks` slice filtered to the optional `--from`/`--to` window. The exit code is 0 on a clean chain, 1 on any reported break, 2 on flag-parsing problems.

**What landed:**

- `audit.VerifyChainReport` and `audit.VerifyResult` / `audit.VerifyBreak` / `audit.VerifyOptions` in `internal/infra/observability/audit/audit.go`
- `cmd/fhir-subs/audit_cli.go` with the `audit verify` flag set
- `cmd/fhir-subs/main.go::realMain` first-arg subcommand dispatch
- Unit tests for the walker (clean chain, chain_hash mutation, time-window break filtering)
- Unit tests for the CLI flag parser (RFC3339 parsing, inverted-window rejection, --help, unknown-verb routing)

**Notes:**

- An out-of-band signature on the chain head is not implemented in this MVP. The verifier exposes the chain head as a hex string in `VerifyResult.HeadHash`; a follow-up can wrap that into a signed tamper-evident export tooling.

### 2.6 Heartbeats and handshakes — PARTIAL (heartbeat scheduler MVP)

**Source:** [docs/low-level-design/subscriptions-engine.md](low-level-design/subscriptions-engine.md) §6; D-2 (rest-hook handshake) RESOLVED in `3d0945f`.

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch). `internal/engine/heartbeat/` ships a heartbeat scheduler `Worker` that periodically scans for active subscriptions with `heartbeat_period > 0` and an `updated_at` older than the period, and enqueues a heartbeat-bundle delivery for each via the operator-supplied `Querier` SPI. The builder already produces the correct heartbeat Bundle shape (verified by existing tests); the scheduler dispatches the synthetic delivery like any other.

The companion gaps (subscription state machine, websocket activator, email activator) remain. The MVP closes the most-asked piece — "subscribers can't tell if a quiet subscription is healthy" — by giving operators a path to enable heartbeats on a deployment with the existing channel transports.

**What landed:**

- `internal/engine/heartbeat/heartbeat.go`: `Worker`, `Options`, `Querier` SPI, `Candidate`, `TickOnce` test seam
- Per-tick scan with bounded `CandidateLimit` and configurable `TickInterval` (default 30s)
- Per-row error logged-and-skipped so a single bad row does not stop heartbeats for the rest
- Unit tests: tick enqueues one heartbeat per due subscription, per-row enqueue error skipped, candidates query error propagates, immediate-on-startup tick

**What's still pending (post-MVP):**

- Postgres-backed `Querier` implementation (`SQL: SELECT id FROM subscriptions WHERE status='active' AND heartbeat_period > 0 AND updated_at < now() - heartbeat_period * interval '1 second' LIMIT $1`); the SPI is defined; the production wiring is intentionally deferred so operators do not auto-emit heartbeats before the production schema migration adds an explicit `last_delivery_at` column for a more accurate check
- State-machine for subscription transitions (`requested` → `active` → `error` → `off`) that emits handshake Notifications. Today the activation handshake fires on rest-hook only (D-2)
- Real `websocket` activator (token-issued path; client binds via `$get-ws-binding-token`)
- Real `email` activator (relay-side AUTH semantics)
- Per-subscription jitter to avoid thundering-herd

**Why this is P2:** the structural worker is in place. Wiring + the state-machine companion pieces are v1.0 follow-up.

### 2.7 Auth re-check at delivery prep — RESOLVED (MVP)

**Source:** [docs/low-level-design/subscriptions-engine.md](low-level-design/subscriptions-engine.md) §3 "submatcher"; engine merge LLD ambiguity #5

**Status:** RESOLVED on `feat/future-work-p2-batch` (P2 batch, MVP). `internal/api/auth/recheck.go` defines the `Rechecker` SPI: `Recheck(ctx, clientID, subscriptionID) (RecheckStatus, error)`. A `CachedRechecker` wraps any implementation with a subscription-level TTL cache; the cache stores both Active and Revoked outcomes (operators can call `Invalidate` on a revocation signal to force a re-fetch). `AlwaysActiveRechecker` is the default for deployments without a revocation surface. The submatcher worker exposes `WithAuthRechecker(r)` and `WithStateUpdater(u)` Options; on `Recheck → Revoked` the worker swaps the decision to `FanoutAuthRevoked`, suppresses the deliveries insert, and (if a state updater is wired) transitions the subscription to `status='error'` atomically with the absence of the row.

**What landed:**

- `internal/api/auth/recheck.go`: `Rechecker` SPI, `CachedRechecker` wrapper with `Invalidate`, `AlwaysActiveRechecker` default
- `internal/engine/submatcher/worker.go`: `AuthRechecker` local interface, `SubscriptionStateUpdater` SPI, `WithAuthRechecker`/`WithStateUpdater` Options, fanout-time integration with fail-open on transient errors
- Unit tests for the cached recheck SPI: cache hits within TTL, refresh after expiry, explicit `Invalidate`, fail-open on inner error, TTL=0 bypass
- Integration tests for the worker: revoked subscription gets no delivery row + state updater is called; transient recheck error fails open and the delivery is written

**What's still pending (post-MVP):**

- Production wiring of a real `Rechecker` against the `auth_clients` table (today the production wiring still installs `AlwaysActiveRechecker`); a follow-up branch can add a Postgres-backed implementation that reads `auth_clients.active` (or equivalent) per subscription's owning `client_id`
- A `SubscriptionsRepo`-backed `MarkErrorRevoked` for `WithStateUpdater` (the SPI is defined; the implementation is not wired yet — the worker handles a nil updater gracefully)
- Per-recheck metrics (cached-hit-rate, recheck-call latency)

**Why this is P2:** subscriptions whose owning client is revoked continue to receive notifications until the next manual delete. The MVP closes the structural gap (the SPI + the worker hook); a real auth-store integration is the operational follow-up.

### 2.8 OpenTelemetry trace export configuration — RESOLVED

**Source:** [docs/low-level-design/observability.md](low-level-design/observability.md)

**Status:** RESOLVED. The OTLP exporter configuration surface landed under S-14 #9 (`9e7fa45`): `internal/infra/observability/tracing/tracing.go` exposes `ExporterTimeout` (line 56), `TLSConfig` (line 61), `Headers` (line 63), and an `Insecure` toggle, all routed into the OTLP HTTP exporter. Deployment recipes for Datadog, Honeycomb, Jaeger, and Grafana Tempo, plus a smoke-test snippet, are documented at [docs/operations/otel-exporter-recipes.md](operations/otel-exporter-recipes.md).

**What landed:**

- Configuration schema knobs (timeout, TLS, headers, insecure) wired through `tracing.Options`
- Plumbed end-to-end so `observability.Start` honors operator-supplied OTLP transport settings
- Recipe docs for the four most common back-ends (P2 batch)

### 2.9 Webhook ingress (vendor push) — PARTIAL (MVP)

**Source:** ADR 0008 #1 ("Deferred to a future release. v1 ships without a webhook ingress path.")

**Status:** PARTIAL on `feat/future-work-p2-batch` (P2 batch, MVP). `internal/webhook/` ships a chi-mountable HTTP receiver that vendors POST to at `/webhooks/{adapter}`. The receiver validates the request via HMAC-SHA256 (shared per-adapter secret) compared in constant time against the `X-Hub-Signature-256` header (the convention GitHub, Stripe, and Cerner Code share), optionally enforces an `X-Webhook-Timestamp` skew window to prevent replay, parses the JSON body into the canonical `ResourceChange` shape, and persists a row through `repos.ResourceChangesRepo` so the matcher worker picks it up on next tick. 1 MiB body cap, exit codes: 202 on accept, 401 on signature/timestamp failure, 404 on unknown adapter, 400 on malformed body, 503 when storage is not wired.

**What landed:**

- `internal/webhook/webhook.go`: `Handler`, `Deps`, `SecretResolver`/`SecretMap`, HMAC-SHA256 verify, optional timestamp-skew enforcement, body cap, JSON parse, ResourceChange insert
- Unit tests: missing signature, unknown adapter, wrong secret, unsupported scheme, stale timestamp, malformed JSON, no-repo-wired

**What's still pending (post-MVP):**

- Wiring into the production HTTP server (`buildProductionRuntime`) and config schema for per-adapter `webhook_secret`. The package is import-ready; the wiring layer is intentionally not changed in this batch so the receiver does not auto-mount before operators have configured a secret.
- mTLS-only authentication (alternate to HMAC for vendors that prefer client-cert)
- Vendor-specific shape adapters (translate inbound non-FHIR webhook → FHIR resource). Today the receiver requires the vendor to push a FHIR-shaped body.
- Per-vendor secret rotation with overlapping windows
- Backpressure / dead-letter on persistent insert failure

**Why this is P2:** the structural ingress is in place. Wiring + per-vendor mapping happens per adapter as the integration list grows.

### 2.10 Multi-instance / horizontal scale — RESOLVED (recipe + algorithmic support already shipping)

**Source:** [docs/high-level-design/decisions/0001-postgres-only.md](high-level-design/decisions/0001-postgres-only.md), [docs/high-level-design/decisions/0002-single-instance-no-leader-election.md](high-level-design/decisions/0002-single-instance-no-leader-election.md)

**Status:** RESOLVED on `feat/future-work-p2-batch` (P2 batch). The algorithmic primitives needed for multi-pod deployment are already shipping on origin/main: `SELECT FOR UPDATE SKIP LOCKED` claim loops in every worker (HL7 processor, matcher, submatcher, scheduler), `pg_advisory_lock(0xFEEDFACE)` migration runner serialization (B-33), `pg_advisory_xact_lock` audit-chain serialization (B-34), partition rotator for `resource_changes`/`ehr_events` (`internal/infra/storage/partition/`, S-13.8/9), and per-connection `statement_timeout`/`lock_timeout` (S-13.5). The remaining work was documentation: capturing the operational recipes for PgBouncer, replica plumbing, sharding, and pod sizing.

The recipe is documented at [docs/operations/horizontal-scale.md](operations/horizontal-scale.md). It records what's already multi-pod safe, what needs operator attention (PgBouncer in transaction mode, NetworkPolicy on Postgres, replica counts, PDB), and what is genuinely deferred to a v1.0 follow-up (read-replica plumbing in the API handlers, sharding wrapper around `repos.*`).

**What landed:**

- `docs/operations/horizontal-scale.md`: the operator recipe (8 sections, 130 lines)
- Cross-reference back to the audit and S-* fixes that demonstrate multi-pod safety

**What's still pending (genuine v1.0 follow-up):**

- Read-replica plumbing (split `ReadPool` from primary `Pool`, wire `internal/api/handlers/pg_stores.go` to use replicas for list/get/search). Tracked here for v1.0.
- Bundled Helm chart with the values shown in the recipe (also tracked under P3.4)
- Sharding wrapper around `repos.*` — only needed past ~10k inserts/s, well past most facility-scale deployments

**Why this is P2 (now closed):** the scaling primitives are in place; operators can deploy multi-pod today using the documented recipe. The deeper work (replicas, sharding) is genuinely v1.0 follow-up and is appropriately scoped.

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

### 3.3 Repository unused code cleanup — RESOLVED (story/76-repo-cleanup)

**Source:** WSS verifier note, e2e wireup post-merge survey

**Resolution:**

- `WsBindingTokensRepo.Get` and `WsBindingTokensRepo.Delete` deleted from `internal/infra/storage/repos/ws_binding_tokens.go`. The API path uses `Insert` only and the WSS bind path uses `Consume`; nothing called these methods. Their unit test (`TestWsBindingTokensInsertAndDelete`) has been replaced with `TestWsBindingTokensInsert` covering Insert + the storage-boundary hash invariant.
- `cmd/fhir-subs/probes.go` `/metadata` stub kept as-is. P1.7 ships the real CapabilityStatement only when the production runtime is wired (DB pool + auth + topics catalog). The stub is still actively mounted in probe-only mode (`run.go:267-270`, `if prod == nil || prod.router == nil`) and exercised by `cmd/fhir-subs/probes_test.go`, `api_routes_test.go`, and `integration_test.go`. It's not dead.
- `internal/api/handlers/handlers_test.go` placeholder `TestPipelineErrors` (gated on `errors.Is(io.EOF, io.EOF)` so the body never executes) removed.

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
