# Production Readiness Audit

Date: 2026-06-18
Commit audited: `b624b7d` (main)
Module: `github.com/bzimbelman/fhir-ehr-subscriptions-service`
Scope: production code only (excludes `_test.go` files); 128 `.go` files / ~22k LOC across `cmd/`, `internal/`, `adapters/`. Cross-referenced against `docs/future-work.md` — items already there are noted, not restated.
Methodology: line-by-line read of every production file across four parallel scrutiny tracks (auth + API; channels + delivery; pipeline + matcher + engine + MLLP + HL7 + topics; storage + infra + lifecycle + observability + cmd). Plus `go vet ./...` (clean) and grep sweeps for `TODO`/`FIXME`/`HACK`/`XXX`/"for now"/"stub"/"placeholder"/"simplified".

## Summary

Roughly **30 BLOCKERs**, **~70 SHOULD-FIX**, **~30 NICE-TO-HAVE**. The system has a working backbone but is not yet a "drop in and run" production service — there are correctness defects (silent fail-open in matcher, nondeterministic bundle JSON breaking audit-chain assumptions, decryption that hardcodes `key_version=1` and breaks key rotation, retention sweeper that physically deletes hash-chained audit rows), security defects (PLAIN/LOGIN SMTP allowed over plaintext, WSS upgrade with no Origin enforcement, SSRF on subscriber endpoints, JWKS fetch unrestricted, header injection via correlation ID, JTI cache eviction broken), and operational defects (`/readyz` always 503 in cmd/run.go, HTTP server missing Write/Idle timeouts, multi-pod migration race with no advisory lock, fire-and-forget activation goroutines without shutdown coordination). Two whole package trees are empty (`internal/channels/*`, `internal/queue`, `internal/wakeup`, `internal/adapters/{defaults,epic}`, `internal/adapterspi`) — phantom abstractions that will mislead future contributors. Top concerns for an initial-run deployment: liveness/readiness wiring; key-version persistence in `pending_pairs`; deterministic JCS for the audit chain; default-deny instead of default-allow on subscriber-supplied headers and channel transports; resource caps on every accept-loop and read path.

---

## Findings

### BLOCKER

#### B-1: `/readyz` always returns 503 in production entry point — RESOLVED in commits 35b2cea, 192ab8e
- **File:** `cmd/fhir-subs/probes.go:59-75`, `cmd/fhir-subs/run.go:69-72`
- **What:** `makeReadyz` is hardcoded to write `unready: ["all_components"]`; the probe handler is never replaced by the real `lifecycle.Module.Probes()` output.
- **Why it matters:** k8s will mark every pod NotReady forever; no traffic ever flows in a real cluster.
- **Fix:** Wire the lifecycle module's readiness aggregator into the probe mux before `markStartupComplete()`.
- **Resolution:** `cmd/fhir-subs/run.go` now constructs `lifecycle.LifecycleModule` and mounts `Probes().Readyz` on the HTTP mux. With no readiness checks registered the aggregator returns 200 ready; per-component checks (DB pool, etc.) plug in via `lcMod.RegisterReadiness(...)` when B-4's storage wiring lands. The hardcoded `failed=["all_components"]` 503 path is gone — the legacy `probeMux/makeReadyz/makeHealthz/makeStartup` helpers were removed entirely.

#### B-2: HTTP server has no `WriteTimeout` / `IdleTimeout` / `MaxHeaderBytes` — RESOLVED in commit 35b2cea
- **File:** `cmd/fhir-subs/run.go:69-72`
- **What:** Only `ReadHeaderTimeout` is set on the production `http.Server` that hosts every API and probe route.
- **Why it matters:** Slowloris-style write-side hangs / unbounded idle conns; trivial DoS against the only HTTP listener.
- **Fix:** Mirror the `lifecycle/probe_server.go` pattern (Write/Idle/MaxHeaderBytes); make values configurable.
- **Resolution:** `HTTPConfig` gains `read_header_timeout`, `read_timeout`, `write_timeout`, `idle_timeout`, `max_header_bytes` (YAML keys, also `--set server.http.*`). Defaults `5s/30s/30s/120s/1MiB` are applied via `applyTimeoutDefaults` from both `loadConfig` and `runWithHooks` so every code path gets the safe values.

#### B-3: `markStartupComplete()` fires before the system is actually ready — RESOLVED in commits 35b2cea, 192ab8e
- **File:** `cmd/fhir-subs/run.go:50-89`
- **What:** Liveness `/healthz` flips to OK as soon as the listener binds; DB, migrations, observability, lifecycle modules don't gate it.
- **Why it matters:** A stuck pod that never finished startup will be considered live forever — k8s will not restart it.
- **Fix:** Defer `markStartupComplete` until lifecycle Start returns success across all modules.
- **Resolution:** `runWithHooks` now calls `lcMod.MarkStartupComplete()` only after `lifecycle.Start` succeeds AND the listener has bound. The shutdown path also routes through the lifecycle sequencer: `srv.Shutdown` is registered as a `PhaseCloseConnections` hook so the Phase-1 ProbeObserveWindow elapses (k8s observes `/readyz=503 shutting_down`) BEFORE the listener stops accepting. When B-4's storage/handlers/pipeline wiring lands, every module registers before this gate.

#### B-4: Production `cmd/fhir-subs/run.go` never calls `handlers.RegisterRoutes` — the API is wired only in tests — PARTIALLY RESOLVED (scaffolding) in commit 192ab8e; full wiring deferred
- **File:** `cmd/fhir-subs/run.go` (whole file)
- **What:** The HTTP server in run.go serves only the probe mux; `handlers.RegisterRoutes` (the real subscription API) is invoked only from tests / e2e harness.
- **Why it matters:** Today the binary literally does not serve the FHIR subscription endpoints. A "real-world deployment" is impossible.
- **Fix:** Construct the full router (subscriptions, $get-ws-binding-token, $events, $status, /metadata) inside run.go and mount it; gate behind auth + observability middleware.
- **Status:** The new `buildHTTPMux` is the single seam where the lifecycle probes + `/metadata` are mounted today and where `handlers.RegisterRoutes` will plug in once the production binary gains config knobs for the database URL, codec key provider, auth issuer/audience/JWKS, channel constructors, and MLLP listener. The full wiring is intentionally deferred from this branch because each new dependency adds blast radius (DB connection failure modes, key rotation surface, channel TLS) that deserves its own RED/GREEN cycle on top of the lifecycle gate the B-1/B-2/B-3 fix already gives operators. The probe-only binary is now safe to deploy as a "known-not-yet-serving-API" stage; the same binary will gain the API + pipeline workers in the follow-up B-4-full commit.

#### B-5: jwksCache map has no mutex; concurrent `/token` requests will fatal-error the process
- **File:** `internal/api/auth/token_endpoint.go:352-393`
- **What:** `jwksCache.entries` is a plain `map[string]jwksCacheEntry` read at line 370 and written at line 393 with no synchronization.
- **Why it matters:** Two simultaneous token POSTs hit Go's runtime "concurrent map read and map write" fatal error → the entire process crashes. Trivial to trigger from a single attacker.
- **Fix:** Wrap with `sync.Mutex` (mirroring `Verifier.jwksMu`), or use `sync.Map` / a singleflight wrapper.

#### B-6: Token endpoint has no body size limit
- **File:** `internal/api/auth/token_endpoint.go:118-126`
- **What:** `r.ParseForm()` is called with no `http.MaxBytesReader` wrapping `r.Body`.
- **Why it matters:** Unauthenticated endpoint — any attacker can flood multi-MB POSTs through `ParseForm` and exhaust memory/CPU.
- **Fix:** `r.Body = http.MaxBytesReader(w, r.Body, 64*1024)` before `ParseForm`; configurable.

#### B-7: Missing `jti` is silently accepted (replay protection bypass)
- **File:** `internal/api/auth/token_endpoint.go:231` and `internal/api/auth/verifier.go:243-249`
- **What:** JTI replay-protection is gated on `jti != ""`; an assertion with no jti claim bypasses replay detection.
- **Why it matters:** SMART Backend Services / RFC 7523 §3 mandates `jti`. A stolen assertion can be replayed for the entire validity window.
- **Fix:** Reject assertions with missing/empty `jti` as malformed.

#### B-8: Raw JWT parser error returned in HTTP response body
- **File:** `internal/api/auth/token_endpoint.go:194`
- **What:** `fmt.Sprintf("assertion validation failed: %v", err)` is sent to the client as `OperationOutcome.diagnostics`.
- **Why it matters:** jwt/v5 error strings can leak token internals, key IDs, algorithm names — info-disclosure aiding offline attacks.
- **Fix:** Map errors to a fixed enum of generic strings; never echo `err.Error()` on the auth path.

#### B-9: JTI cache eviction is broken (memory leak + degraded replay protection)
- **File:** `internal/api/auth/jti_cache.go:64-81`
- **What:** `Seen()` deletes expired entries from `entries` but not from `order`; `Put()` evicts `c.order[0]` which may already be absent from `entries`. `c.order = c.order[1:]` leaks the underlying array.
- **Why it matters:** Map can grow past `cap` without further eviction → OOM under sustained churn; replay protection silently weakens.
- **Fix:** Use `hashicorp/golang-lru`, or sweep `order` correctly on expire and rebuild slice when growth exceeds 2× cap.

#### B-10: Fire-and-forget activation goroutines have no shutdown / timeout / panic-recover
- **File:** `internal/api/handlers/subscription_handlers.go:189, 439`
- **What:** `go s.activate(context.Background(), id)` runs the channel handshake on a fresh background ctx with no cancel, no timeout, no recover.
- **Why it matters:** (a) shutdown drops in-flight handshakes, leaving rows stuck `requested`; (b) a channel-adapter panic crashes the process; (c) a slow vendor pins goroutine + DB conn forever.
- **Fix:** Track in `sync.WaitGroup` keyed off server ctx; wrap `defer recover()`; `context.WithTimeout`.

#### B-11: SSRF — subscriber-supplied endpoint URL is not validated
- **File:** `internal/api/handlers/subscription_handlers.go:74-80, 100-102`
- **What:** `internal.Endpoint` is taken verbatim from the JSON body; only `format: uri`. `http://169.254.169.254/...`, `http://localhost:5432`, `file:///etc/passwd`, `gopher://` reach the rest-hook channel intact.
- **Why it matters:** Classic SSRF — on EKS/GKE this exfiltrates IAM credentials. Egress filtering bypassed.
- **Fix:** Allowlist schemes (`https://` only in prod), block private/link-local/loopback CIDRs, optional configurable allow-host list.

#### B-12: JWKS fetch is unauthenticated, unrestricted, and uses `http.DefaultClient` (no timeout, no body cap)
- **File:** `internal/api/auth/token_endpoint.go:97-99`, `internal/api/auth/verifier.go:106`
- **What:** Whatever URL is in `auth_clients.JwksURL` is GETted with no scheme/host validation, no body-size limit, no client timeout.
- **Why it matters:** SSRF on the auth path; a slow/hostile JWKS host hangs every authentication call indefinitely; a 100MB "jwks" response exhausts memory.
- **Fix:** Dedicated client with 5s timeout; require `https://`; enforce host allowlist; `io.LimitReader(resp.Body, 1MiB)`.

#### B-13: Audit log persists full request body (PHI / secrets at rest in plaintext)
- **File:** `internal/api/handlers/subscription_handlers.go:184, 433`
- **What:** Audit `Append(...)` is called with the full JSON request body (up to 1MB) on create and update.
- **Why it matters:** FHIR Subscription resources can carry `header[]` (bearer tokens for outbound calls) and PII. Plaintext at rest violates HIPAA minimum-necessary, breaks secret-rotation, and inflates storage.
- **Fix:** Strip secret-bearing fields (Authorization headers etc.) before persisting; cap canonical size; consider hashing.

#### B-14: Email STARTTLS default is `Preferred` (cleartext fallback) for a healthcare service
- **File:** `internal/channel/email/email.go:79-80, 213-220`
- **What:** Default STARTTLS policy is "Preferred" — falls back to plaintext when not advertised.
- **Why it matters:** PHI in cleartext over public network is a HIPAA breach (MITM strip-STARTTLS attack).
- **Fix:** Default to `STARTTLSRequired`; require operator opt-in to Preferred.

#### B-15: SMTP PLAIN/LOGIN allowed over plaintext (credential leak)
- **File:** `internal/channel/email/email.go:600-622`
- **What:** With `STARTTLSDisabled` or `Preferred`-with-no-advertise, PLAIN/LOGIN AUTH ships base64-encoded credentials in cleartext. The `host != "localhost"` guard in `loginAuth.Start` misses `127.0.0.1`, `::1`, link-local.
- **Why it matters:** SMTP relay password lifted off the wire.
- **Fix:** Refuse to construct the channel if `STARTTLS=Disabled && AuthMechanism!=AuthNone`; require explicit `AllowCleartextAuth` flag.

#### B-16: Email header injection via `CorrelationID`
- **File:** `internal/channel/email/email.go:407-412, 489-499`
- **What:** `env.CorrelationID` is written verbatim into `Message-ID` and `X-Correlation-ID` headers via `writeHeader`; no CRLF rejection. `newMessageID` also doesn't sanitize.
- **Why it matters:** A CRLF in correlation ID forges arbitrary SMTP headers / smuggles message body. Combined with the unvalidated correlation ID accepted from `X-Correlation-ID` HTTP header (B-39), this is end-to-end attacker-controllable.
- **Fix:** Reject any `corr` containing `\r` or `\n` (and ideally non-ASCII / non-RFC2822-token chars) at `writeHeader`.

#### B-17: WebSocket upgrade is `InsecureSkipVerify=true` — Origin not verified (CSWSH)
- **File:** `internal/channel/websocket/websocket.go:213-225`
- **What:** `coder/websocket.Accept` is called with `InsecureSkipVerify: true`, skipping the Origin header check.
- **Why it matters:** A reverse proxy does NOT check WS Origin — application must. Cross-Site WebSocket Hijacking lets a malicious origin in a victim's browser bind any token.
- **Fix:** Take an `OriginPatterns []string` option; default-deny cross-origin.

#### B-18: WebSocket ack-channel close-of-closed-channel race
- **File:** `internal/channel/websocket/websocket.go:471-553`
- **What:** `defer sess.cancelAck(env.Sequence)` on Deliver path and `deliverAck` (line 553 `close(ch)`) can both fire under concurrent ack-arrival + delivery-timeout — closing an already-closed channel panics.
- **Why it matters:** Server-wide panic from a single misbehaving subscriber's timing.
- **Fix:** Single-owner close (use `sync.Once`, or guard with mutex; deliverAck marks delivered, waiter closes).

#### B-19: MLLP listener has no max-connection cap, no per-IP rate limit, no admission semaphore
- **File:** `internal/mllp/endpoint.go:43-93`
- **What:** Every accepted TCP conn spawns an unbounded goroutine with read buffer + framer state; `connsWG` only counts.
- **Why it matters:** Trivial accept-flood DoS; goroutine + memory exhaustion.
- **Fix:** `cfg.MaxConnections` (semaphore-gated accept); `cfg.MaxConnectionsPerIP` (token bucket).

#### B-20: MLLP listener has no TLS / MTLS support — HL7 (PHI) over plaintext
- **File:** `internal/mllp/endpoint.go:43-51`
- **What:** Plain `net.Listen("tcp", ...)`. No `tls.Config`, no client cert verification. Operators have no way to opt in.
- **Why it matters:** HL7 messages carry PHI; running plaintext on a hospital network is an OCR / HIPAA finding.
- **Fix:** Add `cfg.TLS` (min v1.2, AEAD ciphers); optional `RequireAndVerifyClientCert`; wrap with `tls.NewListener`.

#### B-21: HL7 processor decrypts pending pairs with hardcoded `key_version=1` (breaks key rotation)
- **File:** `internal/hl7processor/processor.go:545`, `internal/infra/storage/repos/pending_pairs.go:86`
- **What:** Both call sites pass literal `1` to `Codec.Decrypt`.
- **Why it matters:** ADR 0010 #7 commits to key rotation. After version bump, every pending row encrypted under the old version becomes undecryptable; held HL7 halves are silently dead-lettered or stuck. Same applies to any other repo that stores ciphertext.
- **Fix:** Persist `key_version` per row, read it back, pass to `Decrypt`.

#### B-22: `pending_pairs` migration omits `key_version` column entirely
- **File:** `migrations/0001_init.sql:99-108`, `internal/infra/storage/repos/pending_pairs.go:32-44`
- **What:** Schema has no `key_version` column; `Insert` writes encrypted bytes with no version stamp.
- **Why it matters:** Same root cause as B-21; the rotation contract is unfulfillable.
- **Fix:** Add migration `ALTER TABLE pending_pairs ADD COLUMN key_version SMALLINT NOT NULL DEFAULT 1`; update `Insert`/`Decrypt`.

#### B-23: Matcher silently passes through unknown FHIR search parameters (fail-open silence) — RESOLVED
- **File:** `internal/matcher/matcher.go:286-355`
- **What:** `extractFieldValues` hardcodes `status/subject/patient/code/category/name/_lastUpdated`; any other parameter returns nil → clause fails closed → topic silently never matches; no rejection at catalog load, no metric.
- **Why it matters:** A topic referencing `performer`, `encounter`, `period`, `class`, etc. silently never fires; subscribers miss notifications and operators have no signal.
- **Fix:** Reject topics at load if they reference unsupported parameters; emit `matcher_unknown_parameter_total{topic}`.
- **Resolution:** Commit `3d80c7d` (cherry-pick `04e2c36`). `internal/topics/catalog/catalog.go` now exports `SupportedSearchParameters()` / `IsSupportedSearchParameter()` and `parseSearchExpression` rejects any topic referencing an unsupported parameter at load time. `compileOne` likewise rejects unsupported `canFilterBy.filterParameter`. The catalog's `Rejected()` method (and `Report.Rejected`) surface the rejections so /readyz can read them. Tests: `TestLoadRejectsUnsupportedSearchParameter`, `TestLoadRejectsUnsupportedFilterByParameter`, `TestLoadAcceptsAllSupportedSearchParameters`, `TestLoadRejectedTopicAbsentFromCatalog` (`internal/topics/catalog/correctness_test.go`); e2e `TestMatcher_unknownParamRejected` (`e2e/orchestrator/matcher_unknown_param_rejected_test.go`).

#### B-24: Matcher FHIRPath `runFHIRPath` defaults to fail-OPEN (returns true) for unrecognized expressions — RESOLVED
- **File:** `internal/matcher/matcher.go:510-547`
- **What:** After `.exists()` and `.status = '...'` checks fall through, return `true` — every unknown FHIRPath expression treated as a match.
- **Why it matters:** Future-work P1.2 covers building the sandboxed evaluator, but the *current default* is fail-OPEN which is more dangerous than fail-closed: a topic with `Patient.deceased.empty()` would fire on every change, leaking notifications. (Future-work doesn't call out the fail-direction — flagging as a separate concrete defect.)
- **Fix:** Default to fail-CLOSED today; emit `fhirpath_unknown_expression_total`; reverse only when sandbox lands.
- **Resolution:** Commit `51b8e53` (cherry-pick `a1f4b12`). `runFHIRPath` now returns `false` for any expression shape outside the recognized minimal set. Wiring layers can install a callback via `matcher.SetUnknownFHIRPathReporter` to bump `fhir_subs_matcher_fhirpath_unknown_expression_total` without coupling the matcher package to a metrics dependency. `IsRecognizedFHIRPath` is exported for catalog-level strict-mode validation. Tests: `TestEvaluateFHIRPathUnknownExpressionFailsClosed`, `TestEvaluateFHIRPathRecognizedExpressionStillMatches` (`internal/matcher/correctness_test.go`); e2e `TestMatcher_fhirpathFailClosed` (`e2e/orchestrator/matcher_fhirpath_fail_closed_test.go`).

#### B-25: Topic catalog rejections do not fail startup; operator override silently shadows working built-in — RESOLVED
- **File:** `internal/topics/catalog/catalog.go:240-282`
- **What:** Per-topic rejections accumulate in `Rejected`; only schema-load errors are fatal. Operator-supplied broken topic with the same `(url,version)` shadows the built-in working topic.
- **Why it matters:** Operator typo silently drops a topic from runtime; no /readyz signal; no override audit trail.
- **Fix:** Surface rejected topics to /readyz; add `--strict` startup mode; on operator-validation failure fall back to lower-priority topic; emit `topic_overridden_total`/`topic_rejected_total`.
- **Resolution:** Commit `3d80c7d` (cherry-pick `04e2c36`). `catalog.LoadStrict` is the strict-mode entry point — it returns a non-nil error wrapping every rejection so `--strict-topics` startup wiring can refuse to start. `Load` walks sources in priority order (Operator > Adapter > BuiltIn) and on a higher-priority compile failure falls back to the lower-priority working topic, recording an `Override{URL, Version, FromOrigin, FromSource, ToOrigin, ToSource, Reason}` entry. Both `Rejected` and `Overridden` are surfaced through the `Catalog` handle so /readyz can read them after the `Report` is dropped. Tests: `TestLoadStrictModeRejectsAtStartup`, `TestLoadStrictModeAcceptsValidCatalog`, `TestLoadFallsBackToBuiltInWhenOperatorOverrideRejected` (`internal/topics/catalog/correctness_test.go`); e2e `TestMatcher_strictMode`, `TestMatcher_topicOverride` (`e2e/orchestrator/`).

#### B-26: `nextEventNumber` race — two workers can both insert event_number N+1
- **File:** `internal/engine/submatcher/worker.go:337-348`
- **What:** `SELECT MAX(event_number)+1` inside the tx without a per-subscription advisory lock; `UNIQUE(subscription_id, event_number)` will reject one of the racers but the worker (line 281) doesn't catch SQLSTATE 23505 — it surfaces as a generic insert error and aborts the whole batch.
- **Why it matters:** Under load the entire batch fails on harmless contention; throughput collapses; deliveries stuck.
- **Fix:** Per-subscription `pg_advisory_xact_lock`, or sequence per subscription, or catch 23505 and retry.

#### B-27: Cursor monotonicity assumes deliveries rows are never deleted; retention will reuse event numbers
- **File:** `internal/engine/submatcher/worker.go:337-348` (consumer) + `internal/infra/storage/retention/retention.go:78` (deleter)
- **What:** `nextEventNumber = MAX(event_number)+1`; if retention deletes a high-numbered delivery, the next insert RE-USES that number.
- **Why it matters:** Subscribers depend on monotonic event numbers for replay/ack semantics; reuse breaks every WebSocket replay scenario.
- **Fix:** Either keep tombstones, or persist `next_event_number` on the subscription row independent of MAX.

#### B-28: Bundle JSON encoding is nondeterministic — breaks any audit-chain hashing of bundle bytes
- **File:** `internal/engine/builder/builder.go:174-190`
- **What:** Bundle assembly uses `map[string]any` → `json.Marshal`. Go map iteration is randomized; map JSON output is therefore non-deterministic.
- **Why it matters:** Hash-chained audit log over bundle bytes produces different hashes on identical inputs. Any downstream signer (S/MIME plan, P2.3) is broken at the foundation.
- **Fix:** Use a canonical JSON encoder (sorted keys), a struct, or `json.RawMessage` to preserve byte form.

#### B-29: Catalog `CatalogProvider` swap is interface-typed; returns are not torn-read-safe — RESOLVED
- **File:** `internal/matcher/matcher.go:554, 656`
- **What:** Worker calls `cat := w.catalog()` per tick; interface values are 2 words. Without `atomic.Pointer`, a concurrent reload can return a torn interface value (data + type pointer mismatch).
- **Why it matters:** Catalog hot-reload data race; sporadic crash with cryptic stack.
- **Fix:** Use `atomic.Pointer[catalog.Catalog]` inside provider impl; document contract on `CatalogProvider`.
- **Resolution:** Commit `51b8e53` (cherry-pick `a1f4b12`). New `matcher.AtomicCatalogProvider` wraps an `atomic.Pointer[catalog.Catalog]`; `Get`/`Store` are race-free and `AsProvider()` returns a `CatalogProvider` closure ready for `NewWorker`. The `CatalogProvider` doc now documents the atomic-swap contract that callers (e.g., the harness's `topic_seed.go` mutex-guarded swap) must satisfy. Tests: `TestAtomicCatalogProviderRaceFree` (1000 swaps × 8 readers under `-race`) and `TestAtomicCatalogProviderUsableAsCatalogProvider` (`internal/matcher/correctness_test.go`); e2e `TestMatcher_catalogHotReloadRace` (`e2e/orchestrator/matcher_catalog_hot_reload_race_test.go`). Whole-repo `go test -race ./...` passes.

#### B-30: High-cardinality MSH-9 label on MLLP nack metric — Prometheus cardinality bomb
- **File:** `internal/mllp/connection.go:269-274`
- **What:** `MetricNackTotal{type: mshFields.MessageType}` — when `allowed_message_types` mismatches, the rejected MSH-9 string becomes a label value.
- **Why it matters:** A hostile peer blasting garbage MSH-9 values creates a new time-series per value; Prometheus OOM.
- **Fix:** Whitelist label values to configured set; bucket "other" into a single value.

#### B-31: Scheduler shutdown does not drain in-flight deliveries; recovery sweep missing
- **File:** `internal/engine/scheduler/worker.go:166-234`
- **What:** Two issues: (1) `Run` returns on `ctx.Done()` without waiting for in-flight `dispatchOne` calls; (2) rows flipped to `'delivering'` by `claimTx` before Commit have no recovery path on worker crash.
- **Why it matters:** Graceful shutdown silently strands deliveries in `'delivering'`; over time the queue fills with dead rows that no worker will touch.
- **Fix:** `sync.WaitGroup` for in-flight dispatches; periodic recovery sweep that resets stale `'delivering'` rows past a stuck-threshold to `'pending'`.

#### B-32: Retention sweeper SQL construction allows future SQL injection; uses `failed_permanent` status that doesn't exist; physically deletes hash-chained audit rows; runs without ctx deadline or advisory lock
- **File:** `internal/infra/storage/retention/retention.go:78-134`
- **What:** Multiple compounding defects: (a) `fmt.Sprintf` injects `predicate`/`idCol` into SQL with no whitelist; (b) sweeps `'failed_permanent'` which is not in the schema enum (`'failed'` is); (c) sweeps `audit_log` whose hash-chain breaks on any DELETE; (d) no `ORDER BY` so concurrent sweeps starve in lock contention; (e) no `context.WithTimeout` per Exec; (f) no `pg_advisory_lock` so multi-pod sweeps stomp on each other.
- **Why it matters:** First retention run permanently breaks audit chain (chain-verifier tool from P2.5 will report "chain break at row 0" forever); deliveries marked `failed` linger; bad query plans hang the entire pool.
- **Fix:** Whitelist `(table, idCol)` against an internal struct; replace `fmt.Sprintf` with bound params; use real schema enum; explicitly EXCLUDE `audit_log` from time-based DELETE (use partition rotation with chain-checkpoint); add `ORDER BY idCol`; per-Tick deadline; advisory lock around the whole sweep.

#### B-33: Multi-pod migration race — no advisory lock around `applyAll`; `Concurrent` detection is a substring match
- **File:** `internal/infra/storage/migrate/migrate.go:191-208`
- **What:** Multiple replicas at rollout time race the migration runner; `CREATE INDEX CONCURRENTLY` detection is `strings.Contains(... "CREATE INDEX CONCURRENTLY")` — a SQL comment with that text trips it.
- **Why it matters:** "relation already exists" cascades, partial migrations, duplicate `schema_migrations` rows.
- **Fix:** `pg_advisory_lock(<known constant>)` for the duration of `applyAll`; statement-level parser for concurrency detection.

#### B-34: Audit log file sink has no fsync; `pgstore` lock leaked on writer panic
- **File:** `internal/infra/observability/observability.go:282`, `internal/infra/observability/audit/pgstore.go:74-93`
- **What:** File sink uses `os.OpenFile(O_APPEND|O_CREATE|O_WRONLY)` with no fsync per write; `audit.Writer.Emit` acquires conn → `pg_advisory_lock` → INSERT → release in a manual sequence with no `defer recover()` over the lock holder.
- **Why it matters:** (a) Power loss loses recent audit rows — strongest durability claim broken silently; (b) panic between acquire and release leaves the chain advisory lock held forever (next audit write blocks indefinitely).
- **Fix:** Fsync on file sink (or document as best-effort); use `pg_advisory_xact_lock` (transaction-scoped, auto-released on commit/rollback).

#### B-35: Secret rotation is unreachable — SIGHUP not registered
- **File:** `internal/infra/lifecycle/signals.go:61-77`, `internal/infra/config/secrets/secrets.go:80-110`
- **What:** Signal handler registers only SIGTERM/SIGINT. `${file:...}` placeholders are read once at startup; rotation requires a SIGHUP that no one listens for.
- **Why it matters:** Vault Agent / cert-manager rotates the on-disk file; our process keeps the old value indefinitely. Long-lived processes ship expired creds.
- **Fix:** Register SIGHUP and route it to `config.Module.Reload`; also add periodic file-mtime polling so rotation works without explicit signal.

---

### SHOULD-FIX

#### S-1: HTTP server / probe handler misuses
- **`cmd/fhir-subs/main.go:138`** — `applySets` errors print raw error including `--set key=val` value verbatim; redact RHS before formatting.
- **`cmd/fhir-subs/main.go:152`** — `signal.NotifyContext` registers SIGTERM/SIGINT only (parallels B-35).
- **`cmd/fhir-subs/main.go:109-160`** — no top-level `defer recover()` over realMain; a panic in startup or in the HTTP serve goroutine crashes without a structured log/correlation-id.
- **`cmd/fhir-subs/run.go:50`** — JSON logger writes to caller-supplied `io.Writer` (stderr in main); observability logger from `internal/infra/observability` is bypassed entirely.
- **`cmd/fhir-subs/run.go:117-122`** — `srv.Close()` error after `srv.Shutdown` failure is dropped.
- **`cmd/fhir-subs/run.go:131-133`** — shutdown wait is `ShutdownGracePeriod + 2s`; magic 2s slack.
- **`cmd/fhir-subs/probes.go:91-108`** — `/metadata` returns OperationOutcome stub (already future-work P1.7, but stub also locked behind auth — see S-2).
- **`cmd/fhir-subs/config.go:75`, `defaults.go:22`, `metrics.go`** — defaults bind `0.0.0.0:<port>` with no loopback opt-in path; document and consider `127.0.0.1` default with explicit prod opt-in.

#### S-2: API / handlers
- **`internal/api/handlers/router.go:107-109`** — `/metadata` is mounted inside auth middleware; FHIR conformance probes hit it unauthenticated.
- **`internal/api/handlers/subscription_handlers.go:104, 384`** — body size limit hardcoded `1<<20`; no shared config knob.
- **`internal/api/handlers/subscription_handlers.go:113, 391, 397`** — schema-validation error from json-schema library returned verbatim to client; cap length and stabilize wording.
- **`internal/api/handlers/subscription_handlers.go:140-152`** — If-None-Exist evaluates O(N) `ListByClient` per POST; push predicate into SQL.
- **`internal/api/handlers/subscription_handlers.go:170-174`** — channel registry plain-map read across goroutines; document immutability or guard with `sync.RWMutex`.
- **`internal/api/handlers/subscription_handlers.go:198, 375-382`** — ETag is the resource id (not a version) and `If-Match` accepts unquoted form; lost-update cannot be detected.
- **`internal/api/handlers/subscription_handlers.go:205-227`** — six `_ = err` swallows in `activate`; no logger; DB/audit/channel failures invisible.
- **`internal/api/handlers/subscription_handlers.go:316-346`** — `searchSubscriptions` has no pagination.
- **`internal/api/handlers/subscription_handlers.go:551, 629-635, 642`** — magic timestamp format `"2006-01-02T15:04:05Z07:00"` repeated 5×; emits `+00:00` not `Z` (FHIR `instant` non-spec for many consumers).
- **`internal/api/handlers/subscription_handlers.go:558-589`** — `$status` bulk has no cap on `id` query params; sequential GetByID per id.
- **`internal/api/handlers/subscription_handlers.go:617-618`** — `since, _ := strconv.ParseInt(...)` discards parse errors; should 400.
- **`internal/api/handlers/subscription_handlers.go:786-805`** — `buildSubscriptionStatus` uses `context.Background()` for DB read; deadline propagation defeated.
- **`internal/api/handlers/subscription_handlers.go:898-899`** — `fhirVersion` hardcoded `"5.0.0"`.
- **`internal/api/handlers/pg_stores.go:43-188`** — no per-query deadline; one slow query starves the pool.
- **`internal/api/handlers/pg_stores.go:159-188`** — `$events` replay capped at hardcoded `LIMIT 1000` with no client signal of truncation.
- **`internal/api/handlers/pg_stores.go:251-263`** — `Hash: []byte{0}` placeholder; production path silently has no hash-chain integrity.
- **`internal/api/handlers/tracing.go:32, 44`** — unvalidated `X-Correlation-ID` reflected into spans / outbound headers; should require UUIDv4 format.
- **`internal/api/metrics/metrics.go:181-183`** — `/metrics` has no auth; handler trusts network layer alone.
- **`internal/api/metrics/metrics.go:208, 217-227`** — `routePattern` falls back to `r.URL.Path` for unmatched routes; cardinality explosion from scanners.
- **`internal/api/metrics/metrics.go:237-241, 310-327`** — histograms have no bucket-count cap; cardinality validator only catches `subscription_id` and `peer_addr`; `endpoint`, `topic_url`, `client_id`, `correlation_id`, `actor_id` unguarded.

#### S-3: Auth
- **`internal/api/auth/token_endpoint.go:106-108`** — 60s `ClockSkew` default is generous; widens replay window. Configurable; document <30s prod recommendation.
- **`internal/api/auth/token_endpoint.go:289-294`** — no rate limit on token endpoint; bursts of bogus assertions DoS the auth path.
- **`internal/api/handlers/subscription_handlers.go`** (POST /Subscription, $get-ws-binding-token) — no per-client rate limit on subscription creates / WS binding token mints.
- **`internal/api/auth/token_endpoint.go:232`** — `exp, _ := claimToTime(...)` discards parse error; on zero-time `Put`, JTI replay protection silently disabled for that token.
- **`internal/api/principal.go:22-28`** — `HasScope` is O(n); minor.

#### S-4: Channels — rest-hook
- **`internal/channel/resthook/resthook.go:144-161`** — default `*http.Client` has no `Timeout`; only context deadline protects calls. Set `c.http.Timeout`.
- **`internal/channel/resthook/resthook.go:148-161`** — `MaxIdleConnsPerHost`/`MaxConnsPerHost` hardcoded; no `TLSClientConfig` knob; no min-version pin.
- **`internal/channel/resthook/resthook.go:213-217`** — no enforced max bundle size; `payload=full-resource` with embedded base64 sends MB per attempt × retries.
- **`internal/channel/resthook/resthook.go:285-291`** — `allowSubscriberHeader` is default-permit; allowlist lookup is dead code; subscribers can forge `X-Internal-Trust`, `X-Auth-User`, etc.
- **`internal/channel/resthook/resthook.go:368-374`** — NXDOMAIN classified as `PermanentFailure`; transient DNS conditions get dead-lettered immediately.
- **`internal/channel/resthook/resthook.go:434-441`** — `readBodyExcerpt` reads up to 256B of subscriber 4xx response into `out.Reason` which is logged; PHI may leak via redaction-bypass.

#### S-5: Channels — message
- **`internal/channel/message/message.go:152-175`** — same default-client-no-Timeout as resthook.
- **`internal/channel/message/message.go:264-266`** — non-`fhir+json` content type fails at delivery time as PermanentFailure rather than being rejected at subscription create.
- **`internal/channel/message/message.go:319`** — `Bundle.timestamp` uses `time.RFC3339` (second precision); FHIR `instant` expects sub-second.
- **`internal/channel/message/message.go:359-374`** — same default-permit allowlist semantics as resthook.

#### S-6: Channels — email
- **`internal/channel/email/email.go:548-555`** — uses `dialer.Deadline` from ctx but never sets `dialer.Timeout`; stdlib `Deadline` is absolute time so OK in practice but fragile.
- **`internal/channel/email/email.go:574-595`** — no metric when STARTTLS-Preferred fallback to plaintext occurs; operator has no compliance signal.
- **`internal/channel/email/email.go:707-735`** — `smtpErrorCode` parses error strings via custom byte loop with dead `smtpErr` interface lines; use `errors.As(err, new(*textproto.Error))`.
- **`internal/channel/email/email.go:298-300, 563-565`** — `Close()` errors silently swallowed.

#### S-7: Channels — websocket
- **`internal/channel/websocket/websocket.go:113-129`** — `sessions` map has no upper bound, no per-client cap.
- **`internal/channel/websocket/websocket.go:213-228`** — no body-size limit / read-header-timeout on the upgrade handler; slowloris on handshake.
- **`internal/channel/websocket/websocket.go:236`** — `conn.SetReadLimit` never called; default 32KB inbound limit conflicts with `Options.MaxFrameBytes` (8MB) used outbound.
- **`internal/channel/websocket/websocket.go:233-247`** — bind-read timeout hardcoded 10s.
- **`internal/channel/websocket/websocket.go:391-408`** — pingLoop uses `context.Background()` parent; no Channel-level ctx; goroutine-leak window on hung Reads.
- **`internal/channel/websocket/websocket.go:399`** — single ping uses full `idleTimeout` (5min default); should use a short write timeout, separately track last-read for idle detection.
- **`internal/channel/websocket/websocket.go:360-389`** — readLoop has no idle-timeout enforcement; documented "5min idle" is unimplemented.
- **`internal/channel/websocket/websocket.go:331-348`** — replay loop materializes the entire `ReplaySince` slice; client requesting replay-from-zero on a million-event subscription triggers OOM.
- **`internal/channel/websocket/websocket.go:486-500`** — `Close` doesn't `WaitGroup`-join per-session goroutines; non-deterministic shutdown.

#### S-8: Scheduler
- **`internal/engine/scheduler/worker.go:230-232`** — batch dispatched serially in one goroutine; one slow channel call (30s) blocks 63 others.
- **`internal/engine/scheduler/worker.go:241-249`** — `subs.GetByID` / `loadEhrEvent` returning `nil` (not-found) is permanent; treated as transient; burns 8 retries on hopeless deliveries.
- **`internal/engine/scheduler/worker.go:269-278`** — Build errors classified transient by default; deterministic build failures (missing topic, malformed resource) waste retry budget.
- **`internal/engine/scheduler/scheduler.go:60-62`** — `MaxAttempts=8` hardcoded with no per-channel-type override.
- **`internal/engine/scheduler/scheduler.go:118-123`** — jitter math: at `Jitter≥1.0` (validated cap is `<1.0` but non-negative), the multiplier can become negative → backoff floor; document `Jitter ∈ [0, 0.5]`.
- **`internal/engine/scheduler/worker.go:336-378`** — raw inline UPDATE SQL in worker; schema migrations changing column names break the worker silently.

#### S-9: Pipeline / MLLP / HL7 processor
- **`internal/mllp/connection.go:130-145`** — read goroutine has no per-message frame-assembly deadline; slowloris keeps a connection open indefinitely.
- **`internal/mllp/connection.go:316-322`** — `persistCtx` decoupled from ctx; cap `PersistTimeout ≤ ShutdownDrainGrace` at Validate.
- **`internal/mllp/connection.go:483`** — `isClosedConnErr` uses substring match on `"closed"`; use `errors.Is(net.ErrClosed)`.
- **`internal/mllp/framer.go:113`** — `pending` slice can grow unbounded between Next() calls in `stateOpen`.
- **`internal/mllp/msh.go:33-97`** — `ExtractMSH` ignores MSH-18 charset; non-ASCII content corrupts patient name comparison and ack echoes.
- **`internal/hl7processor/processor.go:25-29`, `reaper.go:76`** — `ClaimBatchSize=16`, `IdlePoll=1s`, `ReaperTick=5s`, reaper `LIMIT 64` hardcoded.
- **`internal/hl7processor/processor.go:198-205`** — claim-cycle errors logged but not metric'd.
- **`internal/hl7processor/processor.go:212-298`** — race window between `peekUnprocessed` and per-row `lockRow` produces "lost-race" outcomes conflated with `OutcomeRolledBack`.
- **`internal/hl7processor/processor.go:288-298`** — `BeginTx` failure leaves row unprocessed forever; no per-row retry budget.
- **`internal/hl7processor/processor.go:407`** — `occurred = p.deps.Now()`; should source from MSH-7.
- **`internal/hl7processor/processor.go:486-498`** — same-kind paired hold logged but no metric.
- **`internal/hl7processor/translate.go:53-60`** — vendor lex panic mapped to `ErrorClassParse` instead of `ErrorClassUnexpected`.

#### S-10: Matcher
- **`internal/matcher/matcher.go:198-213`** — silent `false` on `json.Unmarshal` error; no metric.
- **`internal/matcher/matcher.go:255-273`** — bare-clause equality chain doesn't include `equalsString`; submatcher does — semantic divergence.
- **`internal/matcher/matcher.go:255`** — `:in` modifier silent fail-closed; no metric.
- **`internal/matcher/matcher.go:466-479`** — `parseFlexibleDate` silently treats non-RFC3339 dates as UTC; comparator answers wrong near boundaries.
- **`internal/matcher/matcher.go:546`** — comment promises "operator metric should flag unknown FHIRPath"; no metric emitted.
- **`internal/matcher/matcher.go:610-638`** — `Worker.Run` retries forever on err; no per-row attempts cap; poison row pins worker.

#### S-11: Topics catalog
- **`internal/topics/catalog/catalog.go:421-452`** — `compileTrigger` doesn't validate `SupportedInteraction` values; typos silently never match.
- **`internal/topics/catalog/catalog.go:374-382`** — eventTrigger compile drops `system`, indexes by `code` only; cross-system code collision possible.
- **`internal/topics/catalog/catalog.go:395-403`** — `notificationShape` collapses to one shape but appends includes from all entries; reject multi-entry topics or per-entry compile.
- **`internal/topics/catalog/catalog.go`** — no `topics_rejected_total{origin,reason}` / `topic_overridden_total{from,to}` metrics.

#### S-12: Engine / submatcher / builder
- **`internal/engine/submatcher/worker.go:251`** — `ListActiveByTopic` inside fanout tx; no pagination.
- **`internal/engine/submatcher/worker.go:178-202`** — single goroutine per Worker with no `PoolSize` knob; matcher exposes one, submatcher does not — API inconsistency.
- **`internal/engine/submatcher/worker.go:178-202`** — same poison-row no-budget retry as matcher.
- **`internal/engine/submatcher/worker.go:302-310`** — fanout tx updates `subscriptions.events_since_subscription_start`; popular subscription becomes hotspot under contention.
- **`internal/engine/submatcher/worker.go:354-370`** — `resourceTypeOf` full-unmarshals every event resource; use streaming decoder.
- **`internal/engine/builder/builder.go:103-108`** — `sort.SliceStable` on `events` by `perSubEv` lookup; missing IDs silently sort to 0.
- **`internal/engine/builder/builder.go:139, 185`** — `time.RFC3339` (no Nano) drops sub-second precision in event/Bundle timestamps.
- **`internal/engine/builder/builder.go:208-210`** — handshake/heartbeat correlation_id is fresh `uuid.NewString()`; non-deterministic; replays produce different IDs.
- **`internal/engine/builder/builder.go:36-38`** — `fhir+xml` deferred; no rejection at subscription create — silent dead-letter at delivery time.

#### S-13: Storage / repos
- **`internal/infra/storage/codec/codec.go:142-189`** — AES-GCM with 96-bit random nonces; document key-rotation cadence (NIST 2^32 message limit).
- **`internal/infra/storage/repos/audit_log.go:18-32`** — `AuditLogRepo.Append` accepts arbitrary `PrevHash` from caller; defense-in-depth missing.
- **`internal/infra/storage/repos/subscriptions.go:82-118`** — `ListActiveByTopic` returns all active subs in a slice; no streaming/pagination.
- **`internal/infra/storage/pool/pool.go:115-132`** — `MinConnections=4`, `MaxConnections=16` hardcoded; `pool_size` is the only YAML knob.
- **`internal/infra/storage/pool/pool.go:129-132`** — `AfterConnect` only sets `statement_timeout`; no `idle_in_transaction_session_timeout` or `lock_timeout`.
- **`internal/infra/storage/storage.go:182-229`** — sweepers run on `context.Background()`; no per-Tick deadline.
- **`internal/infra/storage/storage.go:268-283`** — pool-close hardcoded 5s budget; should derive from `cfg.Lifecycle.ShutdownGracePeriod`.
- **`internal/infra/storage/partition/partition.go:42`** — `_ = Tick(...)` first run discards error; subsequent ticks too.
- **`internal/infra/storage/partition/partition.go:106-145`** — `dropOlderThan` does DETACH then DROP without a tx wrapper; orphan partitions on crash.
- **`migrations/0001_init.sql:277-300`** — bootstrap migration calls `now()` at apply time; checksum drift on re-apply.

#### S-14: Observability
- **`internal/infra/observability/observability.go:179`** — logger sink hardcoded `os.Stdout`; `Options.DebugLogPayloads` never plumbed.
- **`internal/infra/observability/observability.go:298-300`** — `fileSink.Emit` constructs a fresh writer-sink per call; allocation churn.
- **`internal/infra/observability/audit/audit.go:42-51, 233`** — chain genesis is a hardcoded ASCII literal; persist in `audit_chain_meta` row instead.
- **`internal/infra/observability/audit/audit.go:151-205`** — every Emit serializes through one advisory lock + DB round-trip; throughput cap.
- **`internal/infra/observability/audit/audit.go:347-450`** — claims RFC 8785 (JCS) but `canonicalNumber` is partial; external auditors using a real JCS lib will compute different chain hashes.
- **`internal/infra/observability/correlation/correlation.go:114-126`** — accepts `X-Correlation-ID` verbatim; no length/format check; log-injection vector.
- **`internal/infra/observability/logging/logging.go:35-41, 192-208`** — PHI redaction blacklist is exact-case-sensitive on a hardcoded short field-name list; misses `Resource`/`payload`/`patient`/`dob`/`mrn`/`webhook`/`callback`/`target`.
- **`internal/infra/observability/tracing/tracing.go:62-100`** — span attributes can carry user data; no PII filter wrapper.
- **`internal/infra/observability/tracing/tracing.go:73-79`** — OTLP exporter constructed with `context.Background()`; no per-call timeout, no mTLS/auth.

#### S-15: Config / lifecycle
- **`internal/infra/config/config.go:118-125`** — `Start` takes ctx but ignores it; file reads have no deadline.
- **`internal/infra/config/loader/loader.go:101-102`** — env-var derivation `path → AUTH_TRUSTED_ISSUERS_0_JWKS_URL` collides on underscore-vs-dot variants.
- **`internal/infra/config/redaction/redaction.go:101-121`** — recursive walker has no depth cap; deep YAML stack-overflows.
- **`internal/infra/config/secrets/secrets.go:80-110`** — file-secret reader has no size cap; `${file:/dev/zero}` OOMs.
- **`internal/infra/config/effective_store/effective_store.go:107-119`** — every Publish notifies subscribers in a fresh goroutine per callback; bounded pool needed; no panic recover.
- **`internal/infra/config/reload/reload.go:189-216`** — `splitPath` parses dotted paths with no escape; literal `.` in keys silently broken.
- **`internal/infra/lifecycle/sequencer.go:281-306`** — phase deadline timeout reads `results[i].name` without per-slot synchronization; race on shutdown.
- **`internal/infra/lifecycle/sequencer.go:362-369`** — `isDeadlineExceeded` uses `==` not `errors.Is`; wrapped sentinels misclassified.

#### S-16: Empty packages / dead code
- **`internal/channels/{email,message,resthook,websocket}/*.go`** — entire `internal/channels/` (plural) tree is 5-line empty stubs; production uses `internal/channel/` (singular). Delete the duplicates.
- **`internal/queue/queue.go`, `internal/wakeup/wakeup.go`** — empty packages; either implement or delete.
- **`internal/adapters/{defaults,epic}/*.go`, `internal/adapterspi/adapterspi.go`** — 5-line empty packages; flagged as misleading abstraction.
- **`internal/domain/{bundle,cursor}/*.go`, `internal/domain/domain.go`** — empty domain packages.
- **`internal/infra/lifecycle/lifecycle.go:283-284`** — dead reference `var _ = errors.New` indicates incomplete wiring.

---

### NICE-TO-HAVE

#### N-1: Polish
- **`internal/api/auth/principal.go:22-28`** — `HasScope` is O(n); cache as `map[string]struct{}`.
- **`internal/api/handlers/subscription_handlers.go:480-490`** — `equalJSON` swallows unmarshal errors and treats malformed JSON as equal.
- **`internal/api/handlers/subscription_handlers.go:680-686`** — `crypto/rand.Read` failure increments only HTTP 500 counter; add a `rand_failures_total` metric.
- **`internal/api/handlers/router.go:111-117`** — `NotFound`/`MethodNotAllowed` rely on upstream auth wiring; mount auth as `chi.Middleware` for compile-time guarantee.
- **`internal/channel/email/email.go:392`** — `subject := c.cfg.SubjectTemplate` — when template substitution lands, sanitize each substituted value for CRLF.
- **`internal/channel/resthook/resthook.go:421-431`** — `formatTraceparent` doesn't validate hex; non-hex correlation IDs produce invalid trace-id silently.
- **`internal/channel/websocket/websocket.go:381-388`** — ack handling doesn't validate that eventNumber is in the sent-set.
- **`internal/channel/websocket/websocket.go:418-484`** — Deliver / Close race: Deliver writes to a closing conn; document or refcount per-session.
- **`internal/channel/message/message.go:263-323`** — `wrapInMessageBundle` re-marshals via `map[string]any`; non-deterministic byte output (downstream signing concern).
- **`internal/engine/scheduler/scheduler.go:107-114`** — `ComputeBackoff` doubling loop is O(log Max/Initial); cap iteration at 64 explicitly.
- **`internal/engine/scheduler/worker.go:289-299`** — channel setup-error retries forever; consider classify-as-permanent on Nth consecutive same-subscription occurrence.
- **`internal/hl7processor/processor.go:425-468`** — pending_kind enum reuses `create` to mean "held replacement"; rename or split.
- **`internal/hl7processor/processor.go:614-635`** — `adapter_id × resource_type × change_kind` 3-way label cross-product; cap `adapter_id` cardinality.
- **`internal/hl7processor/translate.go`** — no charset normalization at translate; document contract that adapters must transcode pre-translate.
- **`internal/matcher/matcher.go:572-588`** — backoff state has no `matcher_backoff_seconds` gauge.
- **`internal/matcher/matcher.go:670-679`** — `committed=true` variable name lies about the rollback path.
- **`internal/topics/catalog/catalog.go:240-282`** — document immutability contract on Catalog; rename providers to make swap-pointer-on-reload explicit.
- **`internal/topics/catalog/catalog.go:415-417`** — `RawJSON` in-memory copy of every topic body; lazy-load from disk for large catalogs.
- **`internal/topics/catalog/catalog.go:271-282`** — emit structured override-log when operator topic shadows built-in.
- **`internal/mllp/connection.go:119-145`** — `readBuf` 8192 hardcoded; per-read `append([]byte(nil), buf...)` allocates per Read; pool buffers.
- **`internal/mllp/connection.go:296`** — body double-copy (framer already returns fresh).
- **`internal/mllp/connection.go:430-435`** — write deadline 2s hardcoded.
- **`internal/mllp/framer.go:225-236`** — `scanEndPair` is O(n) per Append; track scan offset.
- **`internal/mllp/listener.go:148-174`** — `time.After` (timer leak) on shutdown.
- **`internal/mllp/endpoint.go:90`** — no PROXY protocol v2; behind LB every conn appears from same IP.
- **`internal/infra/lifecycle/sequencer.go:285-306`** — `time.After` timer leaks on early phase return.
- **`internal/infra/observability/audit/audit.go:264-274`** — `bytesEqual` reinvented; use `bytes.Equal`.
- **`internal/infra/observability/audit/audit.go:298-339`** — sink failure has no buffer/queue; document fail-open.
- **`internal/infra/observability/audit/audit.go:57-65`** — `Event.Payload` taken by reference; defensive copy at Emit entry.
- **`internal/infra/observability/audit/pgstore.go:43-44`** — advisory lock id is FNV truncation; reserve a documented integer constant.
- **`internal/infra/storage/codec/codec.go`** — envelope format hardcoded `0x01`; add envelope-version log on Decrypt mismatch.
- **`internal/infra/storage/repos/deliveries.go:53-95`** — no tiebreaker in `ORDER BY next_attempt_at ASC`; non-deterministic claim ordering.
- **`internal/infra/storage/repos/subscription_topics.go:60-87`** — `ListByStatus` has no LIMIT.
- **`internal/infra/storage/claim/claim.go:40`** — substring match for "FOR UPDATE"/"SKIP LOCKED" can be tricked by SQL comments.
- **`internal/infra/storage/partition/partition.go:33-65`** — `Run` captures `cfg.RunInterval` once; ignores reload changes.

---

## Audit notes / methodology

- **Tools run:** `go vet ./...` (clean); recursive `grep -rn` for `TODO|FIXME|HACK|XXX|for now|stub|placeholder|simplified|temporarily` (see results in transcript). `golangci-lint` not run — would surface mechanical issues but the goal here is judgment.
- **Files focused on most:** every `.go` file under `cmd/`, `internal/`, `adapters/` excluding `_test.go` (128 files / ~22k LOC).
- **Cross-checked against `docs/future-work.md`** — items already documented (P1.2 FHIRPath sandbox, P1.3 :in valuesets, P1.4 ICU folding, P1.5 matcher metrics, P1.7 CapabilityStatement, P1.8 hydration, P1.9 WSS Sec-WebSocket-Protocol, P1.11 WSS bind-token hashing, P1.12 dead-letters runbook, P2.3 S/MIME, P2.4 R4B/R5 wire, P2.5 audit verifier CLI, P2.6 heartbeats, P2.7 auth recheck, P2.8 OTel exporter, P2.10 multi-instance) are NOT restated.
- **What I didn't deeply review:**
  - The `e2e/`, `testdata/`, `docs/` trees (out of scope per the task).
  - Adapter-SPI conformance crate (P3.1) and the Epic-specific adapter (`internal/adapters/epic/epic.go` is empty so there was nothing to review).
  - The full set of FHIRPath expression shapes the catalog accepts — I sampled the production parser; the corpus tests would deserve their own pass.
  - Production performance tests / benchmarks — no benchmark suite was reviewed; some claims about hot-path allocations are inferred.
  - Cryptographic primitive selection (AES-GCM vs ChaCha20-Poly1305) — the selection is reasonable but key-rotation cadence vs message volume is a SLO conversation.

The strongest two impressions: (1) the system is structurally complete but has many "trust your callers" gaps that fall apart the moment a misbehaving subscriber, hostile peer, or operator typo lands — defaults need to flip from permissive to strict before this is safe to deploy unattended; (2) the audit-chain commitment is at odds with the retention sweeper, the bundle JSON encoder, and the `pgstore` lock-leak path — all three need to be reconciled before the audit module's promise is real.
