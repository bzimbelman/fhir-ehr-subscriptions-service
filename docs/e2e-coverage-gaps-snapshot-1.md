# E2E Coverage Gaps — Snapshot 1

**Repo:** `/Users/bzimbelman/cz/fhir-subscriptions-foss`
**Module:** `github.com/bzimbelman/fhir-ehr-subscriptions-service`
**Date:** 2026-06-19
**Scope:** Static inventory only. Only tests under `e2e/orchestrator/` were considered (no `e2e/admin-ui/` exists). "Covered" = an e2e test drives the production binary (or close to it) end-to-end through the capability. Tests under `internal/**/...` and harness-only tests that wire their own pipeline / API server (rather than launching `cmd/fhir-subs`) are tagged "partial" — they exercise the *component*, not the wiring.

---

## 1. Capability Inventory

Sources read: `cmd/fhir-subs/wiring.go`, `cmd/fhir-subs/run.go`, `cmd/fhir-subs/probes.go`, `cmd/fhir-subs/config.go`, `internal/api/handlers/router.go`, `internal/api/handlers/admin.go`, `internal/api/handlers/subscription_handlers.go`, `internal/adapter/spi/interfaces.go`, `internal/channel/channel.go`, `internal/channel/{email,message,resthook,websocket}/*`, `internal/mllp/*`, `internal/engine/*`, `internal/infra/observability/audit/*`, `internal/infra/storage/{retention,migrate}/*`.

| # | Capability | Source(s) |
|---|---|---|
| C1 | Production binary boots from YAML, applies env/CLI overrides, validates config | `cmd/fhir-subs/{main,run,config}.go` |
| C2 | Lifecycle module: probes (`/healthz`, `/readyz`, `/startup`), sequenced shutdown phases, signal dispatch | `internal/infra/lifecycle`, `cmd/fhir-subs/run.go` |
| C3 | DB pool open + ping with bounded timeout, fail-fast on unreachable DB | `cmd/fhir-subs/wiring.go:80-106` |
| C4 | Schema migration runs at startup (advisory-locked) | `internal/infra/storage/migrate`, `wiring.go:108-114` |
| C5 | Codec key bundle parse + active-key resolution | `wiring.go:535-560` (buildCodec) |
| C6 | Adapter registry: register, version-pin load, build SPI implementations | `internal/adapter/registry`, `wiring.go:124-146` |
| C7 | SPI: `Hl7MessageProcessor` (Lex / Classify / MapToFHIR / Validate / cancel-replace hooks) | `internal/adapter/spi/interfaces.go:104-166` |
| C8 | SPI: `FhirScanRunner` (ScanPlan / RunScan / ContentHash / Normalize) | `interfaces.go:185-216` |
| C9 | SPI: `VendorAPIClient` (Consume / Translate change feed) | `interfaces.go:241-244` |
| C10 | SPI: `HydrationService` (on-demand FHIR ref fetch + cache TTL) | `interfaces.go:257-271` |
| C11 | SPI: `EhrAdapter.OnStart` / `OnShutdown` lifecycle hooks | `interfaces.go:281-318` |
| C12 | SPI: `AdapterStateStore` per-adapter scoped KV | `interfaces.go:59-70` |
| C13 | SPI: `ResourceChangeSink` framework write surface | `interfaces.go:75-77` |
| C14 | Auth verifier: SMART JWT verify, JWKS cache, JTI replay cache, clock skew | `internal/api/auth`, `wiring.go:565-613` |
| C15 | Token endpoint (`POST /token`): client-credentials, issued-secret HMAC | `wiring.go:232-234`, `internal/api/auth` |
| C16 | URL validator: SSRF guard (HTTPS-required, localhost block, metadata-IP block, allow-http opt-in) | `internal/api/handlers/url_validator.go`, `wiring.go:181-183` |
| C17 | Per-channel `ChannelActivator` for `rest-hook` (real handshake bundle), `websocket` (no-op default), `email` (no-op default) | `wiring.go:184-200`, `cmd/fhir-subs/activators.go` |
| C18 | Activation goroutine: bounded timeout, panic-recovered, drained on shutdown via `ActivationWaitGroup` | `wiring.go:226-228`, handler activation path |
| C19 | API: POST `/Subscription` (create) — body cap, schema validate, audit, rate-limit, activation kickoff | `subscription_handlers.go:221-432`, `router.go:323-324` |
| C20 | API: GET `/Subscription` search with `_count` pagination + `next` link | `subscription_handlers.go:487-599`, `router.go:325` |
| C21 | API: GET `/Subscription/{id}` read | `subscription_handlers.go:452-486`, `router.go:326` |
| C22 | API: PUT `/Subscription/{id}` update | `subscription_handlers.go:600-780`, `router.go:327` |
| C23 | API: DELETE `/Subscription/{id}` | `subscription_handlers.go:781-814`, `router.go:328` |
| C24 | API: GET `/Subscription/{id}/$status` (single) | `subscription_handlers.go:815-845`, `router.go:329` |
| C25 | API: GET `/Subscription/$status` (bulk, capped IDs) | `subscription_handlers.go:846-886`, `router.go:330` |
| C26 | API: GET `/Subscription/{id}/$events` (replay with bounded page + `next` link) | `subscription_handlers.go:887-991`, `router.go:331` |
| C27 | API: POST `/Subscription/{id}/$get-ws-binding-token` (rate-limited) | `subscription_handlers.go:992-1060`, `router.go:332` |
| C28 | API: GET `/SubscriptionTopic` search | `subscription_handlers.go:1061-1088`, `router.go:335` |
| C29 | API: GET `/SubscriptionTopic/{id}` read | `subscription_handlers.go:1089-1119`, `router.go:336` |
| C30 | API: GET `/metadata` CapabilityStatement (authenticated) | `subscription_handlers.go:1120-1196`, `router.go:337` |
| C31 | API: public `/metadata` (unauthenticated, FHIR conformance probe) | `router.go:354-363` |
| C32 | API: catch-all 404 / 405 OperationOutcome behind auth | `router.go:340-345` |
| C33 | Admin surface (`/admin/topics`, `/admin/subscriptions`, `/admin/dead_letters`) behind shared-secret bearer | `internal/api/handlers/admin.go` |
| C34 | Per-client rate limiter on POST `/Subscription` | `auth.ClientRateLimiter`, `router.go:324` |
| C35 | Per-client rate limiter on `$get-ws-binding-token` | `router.go:332` |
| C36 | Audit log: subscription create/update/delete, redaction, hash-chain, file sink fsync | `internal/infra/observability/audit/*` |
| C37 | MLLP listener: TCP framing, ACK/NACK, persist queue row | `internal/mllp/{listener,framer,ack,persister}.go` |
| C38 | MLLP TLS / mTLS | `internal/mllp/config.go:14-32`, `internal/mllp/listener.go` |
| C39 | MLLP PROXY-protocol-v2 | `internal/mllp/proxyproto.go` |
| C40 | MLLP admission: max connections, max-per-IP, persist-NACK-then-drop | `internal/mllp/listener.go:18-150` |
| C41 | MLLP frame assembly timeout, ack write timeout, idle close | `internal/mllp/config.go:106-122` |
| C42 | Pipeline: HL7 processor (claim / classify / pair-cancel-replace / dead-letter) | `internal/hl7processor`, `wiring.go:264-284` |
| C43 | Pipeline: matcher (ResourceChange → ehr_event via topic catalog) | `internal/matcher`, `wiring.go:312-316` |
| C44 | Pipeline: submatcher (ehr_event → per-active-subscription delivery rows) | `internal/engine/submatcher`, `wiring.go:323-327` |
| C45 | Pipeline: scheduler (delivery claim → channel → retry curve / dead-letter) | `internal/engine/scheduler`, `wiring.go:334-351` |
| C46 | Pipeline: notification builder (deterministic bundle bytes, sub-second precision) | `internal/engine/builder` |
| C47 | Pipeline: heartbeat worker (subscription-status heartbeat) | `internal/engine/heartbeat` (NOT wired in cmd) |
| C48 | Pipeline: scheduler recovery sweep (resets stuck `delivering` rows) | `internal/engine/scheduler` |
| C49 | Channel: rest-hook (HTTP POST, retry-after, classifier, header denylist, body excerpt opt-out, oversized bundle reject, FD leak guard) | `internal/channel/resthook` |
| C50 | Channel: websocket (origin check, max sessions, replay cap, ack race, binding-token consume) | `internal/channel/websocket` |
| C51 | Channel: email (STARTTLS-required, STARTTLS-fallback metric, cleartext-auth-blocked, CRLF reject, S/MIME) | `internal/channel/email` |
| C52 | Channel: FHIR messaging (`message`) — outer Bundle wrap, deterministic bytes | `internal/channel/message` |
| C53 | Topic catalog: load JSON files from `topics.catalog_dir` at startup | `internal/topics/catalog`, `wiring.go:292-307` |
| C54 | Topic catalog: SIGHUP reload, mtime-poll reload | `wiring.go:374-398`, `internal/infra/config/reload` |
| C55 | Matcher: FHIRPath fail-closed, strict mode, unknown-param reject, topic override | `internal/matcher` |
| C56 | Storage: deliveries/events/dead_letters/audit_log/pending_pairs repos | `internal/infra/storage/repos` |
| C57 | Storage: codec encrypt/decrypt with row key version | `internal/infra/storage/codec` |
| C58 | Storage: pending-pairs reaper (cancel/replace window) | `internal/hl7processor/reaper` |
| C59 | Storage: retention worker (does not delete audit_log) | `internal/infra/storage/retention` (NOT wired in cmd) |
| C60 | Storage: migration advisory-lock serializes parallel runners | `internal/infra/storage/migrate` |
| C61 | Storage: event-number sequence (no duplicates under concurrency, continues after retention) | `repos.NewEhrEventsRepo` |
| C62 | Observability: structured logger with PHI redaction, correlation IDs, OTEL tracing, Prom metrics | `internal/infra/observability/{logging,correlation,tracing,metrics}` (only logging is wired in cmd) |
| C63 | Server HTTP: read/write/idle timeouts, max header bytes, force-close on grace exhaustion | `cmd/fhir-subs/run.go:135-229` |
| C64 | Server HTTP TLS (cert/key file) | `cmd/fhir-subs/config.go:232-236` (config-only — TLS NOT wired in run.go) |
| C65 | Graceful shutdown: phased (StopAccepting → DrainInFlight → CloseConnections), bounded by grace period, MLLP drain, pipeline drain, activation drain, DB close | `wiring.go:439-501`, `cmd/fhir-subs/run.go` |

**Total capabilities enumerated: 65**

---

## 2. Coverage Table

Each row maps a capability to the e2e test(s) that exercise it. "Prod-binary" = test launches `cmd/fhir-subs` via `startProdBinary` (the gold standard). "Harness" = test uses `e2e/harness` to wire its own pipeline/API server (component-level, NOT prod wiring). "Partial" = covered but limited (e.g., auth disabled).

| # | Capability | Coverage | Test(s) / Note |
|---|---|---|---|
| C1 | Binary boot from YAML | Prod-binary | `prod_binary_serves_subscription_api_test.go`, `prod_binary_processes_hl7_message_test.go` |
| C2 | Lifecycle probes | Prod-binary (partial) | `/healthz`/`/readyz` polled in `prod_binary_*` tests; `/startup` not asserted |
| C3 | DB ping fail-fast | Prod-binary | `prod_binary_db_unreachable_test.go` |
| C4 | Schema migration | Harness | `storage_migration_race_test.go` (uses harness DB; advisory lock asserted but not via cmd binary) |
| C5 | Codec key bundle | Indirect | covered transitively by `prod_binary_*` tests; no targeted bad-key/missing-active test |
| C6 | Adapter registry | Indirect | only the `default` adapter is exercised; no test for version pin mismatch, unknown id, or registry failure mode |
| C7 | SPI Hl7MessageProcessor | Harness (partial) | `cancel_and_replace_hl7_test.go` (uses default adapter pass-through); **`TestScenario_AdaptHL7ToFHIR` is `t.Skip`** |
| C8 | SPI FhirScanRunner | **GAP** | `TestScenario_FHIRScanRunnerEmitsResourceChange` and `TestScenario_cancel_and_replace_scan` both `t.Skip` |
| C9 | SPI VendorAPIClient | **GAP** | `TestScenario_VendorChangeFeedEmitsResourceChange` is `t.Skip` |
| C10 | SPI HydrationService | **GAP** | no e2e test |
| C11 | EhrAdapter OnStart/OnShutdown | **GAP** | no e2e asserts the hooks fire on the prod binary |
| C12 | AdapterStateStore | **GAP** | no e2e test |
| C13 | ResourceChangeSink | Indirect | exercised by harness pipeline; not isolated |
| C14 | Auth verifier (SMART JWT) | Harness | `auth_*_test.go` (token endpoint, jti, jwks, rate-limit, error scrubbing); **never run via prod binary**: `prod_binary_serves_subscription_api_test.go` deliberately sets `audience=""` to skip auth |
| C15 | Token endpoint | Harness | `auth_body_size_test.go`, `auth_error_scrubbing_test.go`, `auth_jti_*`, `auth_jwks_*`, `auth_rate_limit_test.go`, `auth_revocation_test.go` |
| C16 | URL validator (SSRF) | Harness | `api_ssrf_https_required_test.go`, `api_ssrf_localhost_test.go`, `api_ssrf_metadata_test.go` |
| C17 | Channel activator (rest-hook real / others no-op) | Harness | `api_activate_panic_test.go` (panic), `api_activate_shutdown_test.go` (drain); rest-hook activator's actual handshake POST has no isolated test |
| C18 | Activation goroutine bounding | Harness | `api_activate_shutdown_test.go` (asserts WG drain); panic recovery: `api_activate_panic_test.go` |
| C19 | POST /Subscription (create) | Harness + Prod-binary (partial) | many harness tests; prod binary only proves "non-404 returned" with auth disabled |
| C20 | GET /Subscription search + pagination | Harness | `api_search_pagination_test.go` |
| C21 | GET /Subscription/{id} | Harness (indirect) | exercised by helpers; no dedicated e2e |
| C22 | PUT /Subscription/{id} | Harness | `api_s12_9_fhirxml_reject_test.go` (UPDATE branch) |
| C23 | DELETE /Subscription/{id} | **GAP** | no e2e test |
| C24 | GET $status (single) | Harness (indirect) | helpers exercise this; **`TestScenario_SubscriptionStatusOperation` is `t.Skip`** |
| C25 | GET $status (bulk capped) | Harness | `api_s2_fixes_test.go::TestE2E_S2_StatusBulkCapped` |
| C26 | GET $events (replay + next link) | Harness | `api_search_pagination_test.go::TestE2E_S2_EventsReplayBundleLink`, `events_replay_test.go`, `api_s2_fixes_test.go::TestE2E_S2_EventsRejectsInvalidSinceParam` |
| C27 | POST $get-ws-binding-token | Harness (indirect) | `wss_delivery_and_reconnect_test.go` uses ws-binding-token via helper; no rate-limit-targeted test |
| C28 | GET /SubscriptionTopic search | **GAP** | no e2e test |
| C29 | GET /SubscriptionTopic/{id} | **GAP** | no e2e test |
| C30 | GET /metadata (auth) | Harness (partial) | `api_s2_fixes_test.go::TestE2E_S2_MetadataInstantUsesZForm`; prod-binary metadata test only checks "shaped like FHIR" |
| C31 | Public /metadata (unauth) | **GAP** | `RegisterPublicRoutes` is **not called by `wiring.go`** — wiring gap, no e2e |
| C32 | Catch-all 404/405 behind auth | **GAP** | no e2e test |
| C33 | Admin surface | **GAP** | `RegisterAdminRoutes` is **NOT called by `wiring.go`** — wiring gap; no e2e against the prod binary asserts the admin surface exists or is gated by token |
| C34 | Rate-limit POST /Subscription | Harness | `api_per_client_rate_limit_test.go` |
| C35 | Rate-limit $get-ws-binding-token | **GAP** | no e2e test |
| C36 | Audit log redaction + chain | Harness/Stub | `api_audit_redaction_test.go` (harness API); `audit_pgstore_panic_release_test.go` uses an in-memory fake `Store`; `audit_file_sink_fsync_test.go` exercises the file sink directly. **`TestScenario_AuditChainIsValid` is `t.Skip`.** No e2e proves the prod binary writes a valid hash-chained audit row. |
| C37 | MLLP framing + ACK + persist | Prod-binary | `prod_binary_processes_hl7_message_test.go`; harness: `smoke_listener_ack_scenario_test.go`, `smoke_persist_scenario_test.go`, `mllp_failure_modes_test.go`, `real_listener_test.go` |
| C38 | MLLP TLS / mTLS | Component-only | `mllp_tls_test.go`, `mllp_mtls_test.go` directly construct `mllp.Listener` with TLS. **Prod binary cannot configure TLS** — `cmd/fhir-subs/config.go::MLLPConfig` has no TLS fields. Wiring gap. |
| C39 | MLLP PROXY-protocol-v2 | **GAP** | `MLLPListener.ProxyProtocolV2` is plumbed into config + wiring, but no e2e drives a prod binary configured with it |
| C40 | MLLP admission caps | Component-only | `mllp_max_connections_test.go`, `mllp_failure_modes_test.go` (Per-IP, MaxConnections, NackThenDrop) — all bypass prod binary |
| C41 | MLLP frame timeouts | Component-only | `mllp_failure_modes_test.go::TestE2E_MLLP_Slowloris_AcceptsButDropsAfterIdle`, `mllp_should_fix_test.go` |
| C42 | HL7 processor pipeline stage | Prod-binary | `prod_binary_processes_hl7_message_test.go` (default adapter); harness: `cancel_and_replace_hl7_test.go`, `pipeline_correctness_test.go::TestScenario_Pipeline_AdapterPanicDeadLetters_OtherMessagesFlow` |
| C43 | Matcher stage | Harness | `matcher_*_test.go` (5 tests), `pipeline_correctness_test.go` |
| C44 | Submatcher stage | Harness | `subscription_filter_drop_test.go`, `pipeline_correctness_test.go` |
| C45 | Scheduler stage | Harness | `scheduler_should_fix_test.go`, `engine_scheduler_drain_test.go`, `engine_scheduler_recovery_test.go` |
| C46 | Builder determinism | Harness | `engine_bundle_determinism_test.go`, `builder_should_fix_test.go` |
| C47 | Heartbeat worker | **GAP** | `internal/engine/heartbeat` exists but is **not wired** in `cmd/fhir-subs/wiring.go`. No e2e test. |
| C48 | Scheduler recovery sweep | Harness | `engine_scheduler_recovery_test.go`, `engine_scheduler_drain_test.go::TestE2E_Scheduler_RecoverySweepRunsPeriodically` |
| C49 | Rest-hook channel | Harness | `channels_resthook_failure_modes_test.go` (9 tests), `channels_resthook_hardening_test.go` (3 tests), `single_event_to_resthook_test.go` |
| C50 | WebSocket channel | Harness | `channels_websocket_*` (3 tests), `wss_delivery_and_reconnect_test.go`. **NOTE:** websocket channel is **not registered in prod binary** (`wiring.go` only registers `rest-hook` activator + websocket no-op). The actual `websocket.Channel` is exercised by harness only. |
| C51 | Email channel | Harness | `channels_email_*` (4 tests), `email_v1_smtp_test.go`. **Email channel is also not registered in prod binary** — wiring gap. |
| C52 | FHIR messaging channel | **GAP** | `internal/channel/message` exists with hardening tests at `channels_message_hardening_test.go` (these construct the channel directly, not even via harness pipeline). **Not wired anywhere in prod or harness.** |
| C53 | Topic catalog load at startup | Prod-binary | `prod_binary_topics_catalog_d1_test.go::TestE2E_ProdBinary_D1_TopicsLoadedFromCatalogDir` |
| C54 | SIGHUP / mtime-poll catalog reload | Prod-binary | `TestE2E_ProdBinary_D1_SIGHUPReloadsCatalog`; harness: `config_sighup_reload_test.go`, `config_file_mtime_poll_test.go`, `matcher_catalog_hot_reload_race_test.go` |
| C55 | Matcher correctness | Harness | `matcher_*_test.go` (5 tests) |
| C56 | Repos | Indirect | covered transitively |
| C57 | Codec key version | Harness | `storage_pending_pairs_key_version_test.go` |
| C58 | Pending-pairs reaper | **GAP** | `TestScenario_CancelAndReplaceWindowExpires` is `t.Skip` |
| C59 | Retention worker | **GAP** | `internal/infra/storage/retention` is **not wired** in `cmd/fhir-subs/wiring.go`. `storage_retention_no_audit_log_delete_test.go` calls `retention.Run` directly on the harness DB. |
| C60 | Migration advisory lock | Harness | `storage_migration_race_test.go` |
| C61 | Event-number sequence | Harness | `engine_event_number_race_test.go` |
| C62 | Observability (metrics, tracing, audit) | **GAP** | only `logging` is wired in `run.go`. Tracing, metrics, and audit emitters are not started by `cmd/fhir-subs`. The PG audit `Store` is wired into handlers but the audit `Writer` is not constructed in `wiring.go`. No e2e proves traces are emitted, metrics are scraped, or audit rows are written via the prod binary. |
| C63 | HTTP server timeouts | Indirect | `prod_binary_*` tests boot the server; no targeted slowloris test against the prod binary |
| C64 | Server HTTP TLS | **GAP** | `TLSConfig` struct exists in cmd/config; the `*http.Server` in `run.go` calls `srv.Serve(listener)` with no TLS — the cert/key fields are unused. Wiring gap. No test covers it. |
| C65 | Graceful shutdown phases | Prod-binary | `prod_binary_graceful_shutdown_test.go`; harness: `graceful_shutdown_test.go`, `restart_recovery_test.go`, `engine_scheduler_drain_test.go::TestE2E_Scheduler_RunDrainsThenReturns` |

**Coverage tally (production-binary level only):**

- Prod-binary covered: **8** (C1, C2 partial, C3, C37, C42, C53, C54, C65)
- Harness-only covered (proves component, not prod wiring): **35**
- Skip stubs: **11** (`skipped_scenarios_test.go`'s 11 t.Skip functions)
- True gaps (no e2e at all): **22**

---

## 3. Stubs and Bypasses

Tests that *appear* to cover capabilities but don't actually drive production code:

| Test | What it does | What it doesn't do |
|---|---|---|
| `skipped_scenarios_test.go` (11 tests) | All `t.Skip` with the message naming the missing component | Nothing. They are merge-gate placeholders. Notable skips: `cancel_and_replace_scan`, `AdaptHL7ToFHIR`, `FHIRScanRunnerEmitsResourceChange`, `VendorChangeFeedEmitsResourceChange`, `TopicMatcherFanout`, `CancelAndReplaceWindowExpires`, `DeliveryRetryThenSuccess`, `DeliveryDeadLetterAfterMax`, `SubscriptionStatusOperation`, `AuditChainIsValid` |
| `prod_binary_serves_subscription_api_test.go` | Boots prod binary, sends `POST /Subscription` | Sets `AuthAudience=""` to bypass the auth middleware entirely. Asserts only "response is not 404 and shaped like FHIR" — does not verify the row was persisted, the activation goroutine fired, or the audit row was written. |
| `audit_pgstore_panic_release_test.go` | Tests Writer panic-recovery | Uses an in-memory fake `Store` with a `sync.Mutex` chain — does not exercise the real `pgstore` advisory lock or the prod binary's audit writer (which isn't wired in `cmd/fhir-subs` at all). |
| `audit_file_sink_fsync_test.go` | Tests file sink fsync | Exercises only the file-sink module, not the integrated audit writer in a running binary. |
| `mllp_tls_test.go`, `mllp_mtls_test.go` | TLS / mTLS rejection | Construct `mllp.Listener` directly. The prod binary's `cmd/fhir-subs/config.go::MLLPConfig` does not even have TLS fields, so no operator can configure MLLP-TLS via YAML today. |
| `mllp_max_connections_test.go`, `mllp_failure_modes_test.go` (admission tests) | Per-IP / Max / NackThenDrop / Slowloris | Bypass prod binary; construct listener directly. The wired `buildMLLPListener` does pass `MaxConnections` and `MaxConnectionsPerIP` but no e2e proves end-to-end. |
| `channels_message_hardening_test.go` | Asserts deterministic bytes, RFC3339Nano, content-type validation | Builds `message.Channel` directly. The `message` channel is **not registered in any scheduler registry** — neither prod nor harness — so it has no path to actual delivery in any test. |
| `channels_email_*_test.go` | STARTTLS / cleartext / CRLF behavior | Drive the email channel's `Deliver()` directly via the harness pipeline; the prod binary does not register the email channel in its scheduler `chReg`, so a production deployment cannot deliver email today. |
| `channels_websocket_*_test.go` | Origin / max sessions / replay cap / ack race | Drive the websocket channel directly; production scheduler has no `websocket` channel registered (only no-op activator). |
| `engine_bundle_determinism_test.go` | Determinism of bundle bytes | Calls `requireHarness` only to assert harness availability, then constructs the builder directly without driving end-to-end. |
| `helpers_test.go` (3 tests) | "TestHelpers_*" | Tests of the harness helpers themselves, not the SUT. |
| `docker_gate_test.go` (3 tests) | Tests the test-harness's docker gate | Tests the gate, not the SUT. |
| `dump_state_test.go` | Diagnostic dump helpers | Not a SUT test. |

---

## 4. Top 10 Highest-Leverage Gaps

Capabilities that, if covered with a real prod-binary e2e, would catch the most regressions. Ranked by blast radius × probability-of-regression × current absence of any prod-binary signal.

1. **C33 Admin surface (`/admin/*`)** — `RegisterAdminRoutes` is defined but **not called by `cmd/fhir-subs/wiring.go`**. Operators have no admin endpoints today. Pending tasks `#277-#279` look like they're tackling this; an e2e is the proof. A boot+`curl /admin/topics` test would have caught the missing wire-up immediately.

2. **C62 Observability (audit chain, metrics, tracing)** — `wiring.go` wires `handlers.NewPgAuditStore(pool)` into `Deps.Audit` but never constructs an `audit.Writer`, never starts the metrics scraper, never starts the tracer. Pending task `#274-#276` matches. A "boot binary, create subscription, query `audit_log` and assert chain hash + actor + action" e2e is the load-bearing missing test. Currently the only audit e2e uses an in-memory fake store.

3. **C14 Auth verifier on the production binary** — every auth e2e is harness-level, and the one prod-binary API test deliberately disables auth (`audience=""`). This is the difference between "the code works" and "the binary enforces it." A regression that disables auth wiring would not be caught.

4. **C38 / C64 TLS on the production binary (MLLP and HTTP)** — neither MLLP-TLS nor HTTP-TLS is plumbed end-to-end. `cmd/fhir-subs/config.go::MLLPConfig` has no TLS fields; `run.go` calls `srv.Serve` not `srv.ServeTLS`. Component-level TLS tests pass, but a production deployment cannot enable TLS via config. This is a HIPAA / OCR exposure if it ships.

5. **C49 / C50 / C51 / C52 Channels in production scheduler** — only `rest-hook` is registered in `chReg` (`wiring.go:177-178`). WebSocket, email, and FHIR-messaging channels exist and have hardening tests, but the prod binary cannot deliver via any of them. An e2e that boots the binary, creates a websocket subscription, and asserts a delivery would expose this as a missing feature, not a passing component test.

6. **C36 End-to-end audit chain through the prod binary** — covered as part of C62 above, but worth its own line. `TestScenario_AuditChainIsValid` is `t.Skip`. The chain-hash invariant is the audit story's core value; without an e2e, a regression in `Writer`, `Store`, or `Sink` ordering goes unnoticed.

7. **C19 + activation handshake (rest-hook real handshake POST)** — `wiring.go` constructs a real `restHookActivator` that POSTs a synthetic FHIR R5 handshake bundle to the subscriber and only flips `status=active` on 2xx. No e2e exercises this path against the prod binary — the activation panic / shutdown tests use the harness API server with stub activators.

8. **C47 Heartbeat worker** — `internal/engine/heartbeat` exists but is **not wired in `wiring.go`**. Subscribers depending on `subscription-status: heartbeat` notifications get nothing today. An e2e that creates a Subscription with `heartbeatPeriod=5s` and expects a heartbeat bundle within 10s would catch the missing wire-up.

9. **C59 Retention worker** — `internal/infra/storage/retention` exists but is **not wired**. The harness retention test calls `retention.Run` directly. Eventually `ehr_events` and `deliveries` tables grow unbounded.

10. **C8 / C9 / C10 SPI surfaces beyond HL7 (FhirScanRunner, VendorAPIClient, HydrationService)** — `wiring.go:138-146` calls `loadedAdapter.BuildHl7Processor(...)` only. The other three Build methods on `EhrAdapter` are never called. The default adapter probably no-ops them, but no test asserts the absence is intentional or proves a custom adapter could plug in. Three skip stubs (`FHIRScanRunnerEmitsResourceChange`, `VendorChangeFeedEmitsResourceChange`, `cancel_and_replace_scan`) are placeholders for this.

---

## 5. Recommended New E2E Tests

For each gap, a proposed file path and the assertion that proves the wiring. All should boot the prod binary via `startProdBinary` (or a thin extension) — that's the whole point.

### High priority (catches wiring gaps that exist today)

1. **`e2e/orchestrator/prod_binary_admin_surface_test.go`** — Boot prod binary with `AdminToken="…"` (after `cmd/fhir-subs/wiring.go` is updated to call `handlers.RegisterAdminRoutes`). Assert: `GET /admin/topics` with bearer token returns 200 + `items[]`; with no/bad token returns 401; `GET /admin/dead_letters?limit=5` honors the cap; `GET /admin/subscriptions?clientId=foo` requires the param.

2. **`e2e/orchestrator/prod_binary_audit_chain_test.go`** — Boot prod binary, create a subscription via the API, then SQL-read `audit_log` and verify `(actor_kind, action, outcome)` matches and the chain hash links to the previous row. Crash-restart the binary mid-create and verify the chain is still walkable. Catches the missing audit Writer wiring as well as `pgstore` advisory-lock regressions.

3. **`e2e/orchestrator/prod_binary_smart_auth_required_test.go`** — Boot prod binary with `AuthAudience` non-empty (real verifier). Assert: unauthenticated `POST /Subscription` → 401; valid SMART JWT → 201; expired/audience-mismatched → 401. This is the "C19 with auth on" test.

4. **`e2e/orchestrator/prod_binary_resthook_handshake_test.go`** — Boot prod binary, point a Subscription at a `mocksub` HTTP server that returns 2xx for the handshake. Assert subscription transitions `requested → active` after the handshake POST. Then fail the handshake (mocksub returns 4xx) and assert it stays `requested` and an audit row is written.

5. **`e2e/orchestrator/prod_binary_observability_test.go`** — Boot prod binary, scrape `/metrics` (after metrics endpoint is wired), assert `subscription_created_total`, `mllp_messages_persisted_total`, `delivery_attempts_total` exist with sensible labels. Assert at least one trace span is emitted for a request (probably via OTLP test exporter).

### Medium priority (covers shipped features that lack prod-binary signal)

6. **`e2e/orchestrator/prod_binary_websocket_delivery_test.go`** — Once `websocket` channel is registered in `chReg`. Subscribe via WS, send HL7 over MLLP, assert binding-token consume + notification frame.

7. **`e2e/orchestrator/prod_binary_email_delivery_test.go`** — Once `email` channel is registered in `chReg`. Use a STARTTLS test relay; assert the delivery row marks `delivered`.

8. **`e2e/orchestrator/prod_binary_mllp_tls_test.go`** — Once `MLLPConfig` carries TLS fields. Boot binary with TLS server cert, dial plaintext → reject; dial with TLS → success.

9. **`e2e/orchestrator/prod_binary_http_tls_test.go`** — Once `run.go` calls `srv.ServeTLS` when `cfg.Server.HTTP.TLS.CertFile` is set. Assert plaintext rejected, HTTPS accepted.

10. **`e2e/orchestrator/prod_binary_heartbeat_test.go`** — Once `heartbeat.Worker` is wired. Create a Subscription with `heartbeatPeriod=2s`; mocksub asserts a heartbeat-typed bundle within 5s.

11. **`e2e/orchestrator/prod_binary_retention_test.go`** — Once retention worker is wired. Insert old rows directly, advance test clock or wait, assert retention deletes from `ehr_events`/`deliveries` but **not** `audit_log`.

12. **`e2e/orchestrator/prod_binary_subscription_topic_api_test.go`** — `GET /SubscriptionTopic` and `GET /SubscriptionTopic/{id}` against prod binary with the catalog dir loaded.

13. **`e2e/orchestrator/prod_binary_capability_statement_test.go`** — `GET /metadata` against prod binary. Assert `software.version`, `fhirVersion`, the SMART security extension lists `tokenEndpointURL` and `JWKSURL` from config.

14. **`e2e/orchestrator/prod_binary_status_operations_test.go`** — `GET /Subscription/{id}/$status` against prod binary; unblock `TestScenario_SubscriptionStatusOperation` and convert it to a real assertion.

### Lower priority (nice-to-have / edge cases)

15. **`e2e/orchestrator/prod_binary_delete_subscription_test.go`** — DELETE happy path + 404.

16. **`e2e/orchestrator/prod_binary_proxy_protocol_v2_test.go`** — Configure `ProxyProtocolV2: true` on a listener; assert a non-PROXY peer is rejected and a PROXY-v2-prefixed peer is accepted.

17. **`e2e/orchestrator/prod_binary_ws_binding_token_rate_limit_test.go`** — Saturate the per-client rate limiter on `$get-ws-binding-token`.

18. **`e2e/orchestrator/prod_binary_404_405_test.go`** — `GET /unknown` → 404 OperationOutcome; `PATCH /Subscription/{id}` → 405.

19. **`e2e/orchestrator/prod_binary_pending_pairs_reaper_test.go`** — Replace `TestScenario_CancelAndReplaceWindowExpires` skip with a real test driving HL7 cancel-without-replacement through the prod binary.

20. **`e2e/orchestrator/prod_binary_adapter_version_pin_test.go`** — Configure `adapter.version_pin` mismatching the installed adapter; binary should fail to start with a clear error.

---

## 6. Summary Numbers

- Capabilities enumerated: **65**
- Production-binary covered: **8** (~12%)
- Harness/component covered: **35** (~54%)
- Stubs / `t.Skip` placeholders: **11**
- True gaps (no e2e at all): **22** (~34%)

The headline finding is that **production-binary coverage is shallow**: the e2e suite proves component correctness but rarely proves the components are actually wired together by `cmd/fhir-subs`. Several capabilities (`websocket`/`email` channels, `heartbeat`, `retention`, MLLP-TLS, HTTP-TLS, `RegisterAdminRoutes`, `RegisterPublicRoutes`, audit `Writer`, observability metrics & tracing) appear well-tested at the component level but are **not wired into the production binary at all** — a category of bug the current e2e suite cannot catch.
