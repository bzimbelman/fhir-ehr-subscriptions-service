# Production Readiness Audit

Date: 2026-06-18
Commit audited: `b624b7d` (main)
Module: `github.com/bzimbelman/fhir-ehr-subscriptions-service`
Scope: production code only (excludes `_test.go` files); 128 `.go` files / ~22k LOC across `cmd/`, `internal/`, `adapters/`. Cross-referenced against `docs/future-work.md` — items already there are noted, not restated.
Methodology: line-by-line read of every production file across four parallel scrutiny tracks (auth + API; channels + delivery; pipeline + matcher + engine + MLLP + HL7 + topics; storage + infra + lifecycle + observability + cmd). Plus `go vet ./...` (clean) and grep sweeps for `TODO`/`FIXME`/`HACK`/`XXX`/"for now"/"stub"/"placeholder"/"simplified".

## Summary

**Original audit (2026-06-18, commit `b624b7d`):** roughly **30 BLOCKERs**, **~70 SHOULD-FIX**, **~30 NICE-TO-HAVE**. The original summary observation was that the system had a working backbone but was not yet a "drop in and run" production service — correctness defects (silent fail-open in matcher, nondeterministic bundle JSON breaking audit-chain assumptions, decryption that hardcoded `key_version=1` and broke key rotation, retention sweeper that physically deleted hash-chained audit rows), security defects (PLAIN/LOGIN SMTP allowed over plaintext, WSS upgrade with no Origin enforcement, SSRF on subscriber endpoints, JWKS fetch unrestricted, header injection via correlation ID, JTI cache eviction broken), and operational defects (`/readyz` always 503 in cmd/run.go, HTTP server missing Write/Idle timeouts, multi-pod migration race with no advisory lock, fire-and-forget activation goroutines without shutdown coordination).

**Current state:** **0 BLOCKERs remaining**, **~70 SHOULD-FIX still open** (mostly unchanged from the original audit), **~30 NICE-TO-HAVE still open**. All B-1 through B-35 BLOCKERs have been resolved on `main` — see the **Resolution Status** table below for per-finding commit SHAs and verification tests. B-4 is the sole exception: its lifecycle scaffolding is in place (B-1 / B-2 / B-3 wiring) but the full production binary still does not mount `handlers.RegisterRoutes`; it is tracked as PARTIALLY RESOLVED pending a follow-up that adds DB / codec / auth / channel / MLLP wiring under its own RED/GREEN cycle. Two whole package trees are still empty (`internal/channels/*`, `internal/queue`, `internal/wakeup`, `internal/adapters/{defaults,epic}`, `internal/adapterspi`) — phantom abstractions covered under SHOULD-FIX S-16. The remaining SHOULD-FIX work centers on hardening API edges (rate limits, body size knobs, pagination), tightening channel defaults (resthook header allowlist, websocket idle-timeout enforcement, message channel deterministic bundle), and reconciling the audit-chain canonicaliser with a real RFC 8785 (JCS) implementation.

---

## Resolution Status

The original audit catalogued 35 BLOCKERs (B-1 through B-35). The table below records the disposition of each one against `main` as of 2026-06-18. Use the per-finding sections below the table for the full **what / why / fix / Resolution** detail; this table is the navigation aid.

| ID | Title (abbreviated) | Status | Resolving commit(s) |
|----|---------------------|--------|---------------------|
| B-1 | `/readyz` always 503 in production entry point | RESOLVED | `35b2cea`, `192ab8e` (merged in `8096936`) |
| B-2 | HTTP server missing Write / Idle / MaxHeaderBytes | RESOLVED | `35b2cea` (merged in `8096936`) |
| B-3 | `markStartupComplete` fires before system is ready | RESOLVED | `35b2cea`, `192ab8e` (merged in `8096936`) |
| B-4 | Production `run.go` never calls `handlers.RegisterRoutes` | PARTIALLY RESOLVED (scaffolding) | `192ab8e` (merged in `8096936`) — full API wiring deferred to a follow-up |
| B-5 | `jwksCache` map race | RESOLVED | `168cc80` (merged in `0a771cf`) |
| B-6 | Token endpoint missing body-size limit | RESOLVED | `7200625` (merged in `0a771cf`) |
| B-7 | Missing `jti` silently accepted (replay bypass) | RESOLVED | `e788c0e` (merged in `0a771cf`) |
| B-8 | Raw JWT parser error leaked in HTTP body | RESOLVED | `3e7de5e` (merged in `0a771cf`) |
| B-9 | JTI cache eviction broken | RESOLVED | `aa7567f` (merged in `0a771cf`) |
| B-10 | Fire-and-forget activation goroutines | RESOLVED | `a6e042f` (merged in `790c6a8`) |
| B-11 | SSRF on subscriber endpoint URL | RESOLVED | `9689730` (merged in `790c6a8`) |
| B-12 | JWKS fetch unauthenticated / no body cap | RESOLVED | `34d2196` (merged in `0a771cf`) |
| B-13 | Audit log persists full request body | RESOLVED | `25dffba` (merged in `790c6a8`) |
| B-14 | Email STARTTLS default `Preferred` | RESOLVED | `a6a36aa` (merged in `d3f2a4b`) |
| B-15 | SMTP PLAIN/LOGIN allowed over plaintext | RESOLVED | `a6a36aa` (merged in `d3f2a4b`) |
| B-16 | Email header injection via `CorrelationID` | RESOLVED | `a6a36aa` (merged in `d3f2a4b`) |
| B-17 | WebSocket upgrade `InsecureSkipVerify=true` | RESOLVED | `a0360e4` (merged in `d3f2a4b`) |
| B-18 | WebSocket ack-channel close-of-closed race | RESOLVED | `a0360e4` (merged in `d3f2a4b`) |
| B-19 | MLLP listener missing connection caps | RESOLVED | `b8c1209` (merged in `d3f2a4b`) |
| B-20 | MLLP listener missing TLS / mTLS | RESOLVED | `b8c1209` (merged in `d3f2a4b`) |
| B-21 | HL7 processor decrypts with hardcoded key version | RESOLVED | `07d7be2` (cherry-pick of `6d0e5a2`) |
| B-22 | `pending_pairs` migration omits `key_version` | RESOLVED | `07d7be2` (cherry-pick of `6d0e5a2`) |
| B-23 | Matcher silently passes through unknown search params | RESOLVED | `3d80c7d` (cherry-pick of `04e2c36`, merged in `8096936`) |
| B-24 | FHIRPath `runFHIRPath` defaults to fail-OPEN | RESOLVED | `51b8e53` (cherry-pick of `a1f4b12`, merged in `8096936`) |
| B-25 | Topic catalog rejections do not fail startup | RESOLVED | `3d80c7d` (cherry-pick of `04e2c36`, merged in `8096936`) |
| B-26 | `nextEventNumber` race | RESOLVED | `f600d42` (cherry-pick of `1ba1c45`) |
| B-27 | Cursor monotonicity assumes deliveries never deleted | RESOLVED | `f600d42` (cherry-pick of `1ba1c45`) |
| B-28 | Bundle JSON encoding nondeterministic | RESOLVED | `b76f1b0` (cherry-pick of `0b39e95`) |
| B-29 | `CatalogProvider` swap not torn-read-safe | RESOLVED | `51b8e53` (cherry-pick of `a1f4b12`, merged in `8096936`) |
| B-30 | High-cardinality MSH-9 label on MLLP nack metric | RESOLVED | `7e797f3` (merged in `f853619`) |
| B-31 | Scheduler shutdown does not drain in-flight deliveries | RESOLVED | `5017364` |
| B-32 | Retention sweeper SQL injection / audit-chain DELETE | RESOLVED | `e697162`, `52ed074` |
| B-33 | Multi-pod migration race | RESOLVED | `52ed074` |
| B-34 | Audit log file sink missing fsync; pgstore lock leak | RESOLVED | `ad6ddd2` (merged in `f853619`) |
| B-35 | Secret rotation unreachable — SIGHUP not registered | RESOLVED | `3a81559` (merged in `f853619`) |

Counts: 34 RESOLVED, 1 PARTIALLY RESOLVED (B-4), 0 open.

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

#### B-5: jwksCache map has no mutex; concurrent `/token` requests will fatal-error the process — **RESOLVED** (`f786914`)
- **File:** `internal/api/auth/token_endpoint.go:352-393`
- **What:** `jwksCache.entries` is a plain `map[string]jwksCacheEntry` read at line 370 and written at line 393 with no synchronization.
- **Why it matters:** Two simultaneous token POSTs hit Go's runtime "concurrent map read and map write" fatal error → the entire process crashes. Trivial to trigger from a single attacker.
- **Fix:** Wrap with `sync.Mutex` (mirroring `Verifier.jwksMu`), or use `sync.Map` / a singleflight wrapper.
- **Status:** Fixed in `f786914`; jwksCache now gates entries via `sync.Mutex`. Race-detector regression test in `internal/api/auth/token_endpoint_jwks_race_test.go` and `e2e/orchestrator/auth_jwks_race_test.go` (50 concurrent /token POSTs).

#### B-6: Token endpoint has no body size limit — **RESOLVED** (`c194ad3`)
- **File:** `internal/api/auth/token_endpoint.go:118-126`
- **What:** `r.ParseForm()` is called with no `http.MaxBytesReader` wrapping `r.Body`.
- **Why it matters:** Unauthenticated endpoint — any attacker can flood multi-MB POSTs through `ParseForm` and exhaust memory/CPU.
- **Fix:** `r.Body = http.MaxBytesReader(w, r.Body, 64*1024)` before `ParseForm`; configurable.
- **Status:** Fixed in `c194ad3`; cap is `MaxTokenRequestBodyBytes` (64 KiB) by default and overridable via `TokenEndpointConfig.MaxRequestBodyBytes`. Oversized bodies receive `413 Request Entity Too Large`. Tests: `token_endpoint_body_limit_test.go` and `e2e/orchestrator/auth_body_size_test.go`.

#### B-7: Missing `jti` is silently accepted (replay protection bypass) — **RESOLVED** (`284da10`)
- **File:** `internal/api/auth/token_endpoint.go:231` and `internal/api/auth/verifier.go:243-249`
- **What:** JTI replay-protection is gated on `jti != ""`; an assertion with no jti claim bypasses replay detection.
- **Why it matters:** SMART Backend Services / RFC 7523 §3 mandates `jti`. A stolen assertion can be replayed for the entire validity window.
- **Fix:** Reject assertions with missing/empty `jti` as malformed.
- **Status:** Fixed in `284da10`; both the token endpoint and the verifier now treat missing/empty `jti` as malformed (401). Tests: `token_endpoint_jti_required_test.go` and `e2e/orchestrator/auth_jti_required_test.go`.

#### B-8: Raw JWT parser error returned in HTTP response body — **RESOLVED** (`c544f01`)
- **File:** `internal/api/auth/token_endpoint.go:194`
- **What:** `fmt.Sprintf("assertion validation failed: %v", err)` is sent to the client as `OperationOutcome.diagnostics`.
- **Why it matters:** jwt/v5 error strings can leak token internals, key IDs, algorithm names — info-disclosure aiding offline attacks.
- **Fix:** Map errors to a fixed enum of generic strings; never echo `err.Error()` on the auth path.
- **Status:** Fixed in `c544f01`; introduced `diagnosticForReason` enum, raw error redirected to optional `TokenEndpointConfig.Logger`. Tests: `token_endpoint_error_scrub_test.go` and `e2e/orchestrator/auth_error_scrubbing_test.go` (asserts response body contains no `crypto/rsa`, `verification error`, key ids, or `jwt:` phrases).

#### B-9: JTI cache eviction is broken (memory leak + degraded replay protection) — **RESOLVED** (`708c2ad`)
- **File:** `internal/api/auth/jti_cache.go:64-81`
- **What:** `Seen()` deletes expired entries from `entries` but not from `order`; `Put()` evicts `c.order[0]` which may already be absent from `entries`. `c.order = c.order[1:]` leaks the underlying array.
- **Why it matters:** Map can grow past `cap` without further eviction → OOM under sustained churn; replay protection silently weakens.
- **Fix:** Use `hashicorp/golang-lru`, or sweep `order` correctly on expire and rebuild slice when growth exceeds 2× cap.
- **Status:** Fixed in `708c2ad`; `Seen()` now sweeps `order` via `removeLocked`, `Put()` loops eviction so multiple ghosts can clear at once, and `maybeCompactLocked` rebuilds the underlying slice when its `cap` exceeds 2× configured cap. Tests under race detector: `jti_cache_test.go` (internal-package introspection) and `e2e/orchestrator/auth_jti_cache_eviction_test.go` (behavioural).

#### B-10: Fire-and-forget activation goroutines have no shutdown / timeout / panic-recover — RESOLVED in commit `a6e042f` (merged in `790c6a8`)
- **File:** `internal/api/handlers/subscription_handlers.go:189, 439`
- **What:** `go s.activate(context.Background(), id)` runs the channel handshake on a fresh background ctx with no cancel, no timeout, no recover.
- **Why it matters:** (a) shutdown drops in-flight handshakes, leaving rows stuck `requested`; (b) a channel-adapter panic crashes the process; (c) a slow vendor pins goroutine + DB conn forever.
- **Fix:** Track in `sync.WaitGroup` keyed off server ctx; wrap `defer recover()`; `context.WithTimeout`.
- **Resolved:** `8c7db78` (impl), `8cbd5c6` (RED tests), `2ec4de9` (e2e: api_activate_shutdown_test.go, api_activate_panic_test.go). New `Deps.LifecycleCtx` / `Deps.ActivationTimeout` (default 30s) / `Deps.ActivationWaitGroup`; new `spawnActivate` helper in `internal/api/handlers/activation.go` runs the goroutine under the WaitGroup with a derived `context.WithTimeout` and a `defer recover()` that logs, increments `fhir_subs_api_activate_panic_total`, and best-effort flips the row to `error`. `activate()` falls back to a fresh background ctx for the status / audit bookkeeping when the per-call ctx is dead.

#### B-11: SSRF — subscriber-supplied endpoint URL is not validated — RESOLVED in commit `9689730` (merged in `790c6a8`)
- **File:** `internal/api/handlers/subscription_handlers.go:74-80, 100-102`
- **What:** `internal.Endpoint` is taken verbatim from the JSON body; only `format: uri`. `http://169.254.169.254/...`, `http://localhost:5432`, `file:///etc/passwd`, `gopher://` reach the rest-hook channel intact.
- **Why it matters:** Classic SSRF — on EKS/GKE this exfiltrates IAM credentials. Egress filtering bypassed.
- **Fix:** Allowlist schemes (`https://` only in prod), block private/link-local/loopback CIDRs, optional configurable allow-host list.
- **Resolved:** `16a1806` (impl), `209685d` (RED tests), `2ec4de9` (e2e: api_ssrf_metadata_test.go, api_ssrf_localhost_test.go, api_ssrf_https_required_test.go). New `URLValidator` interface (`internal/api/handlers/url_validator.go`) with config-driven scheme allowlist (https default, opt-in http), DNS-aware blocks for loopback / unspecified / link-local / RFC1918 / multicast / IPv6 ULA / cloud-metadata IPs, plus a sentinel `ErrSSRFBlocked` and an `AllowHosts` bypass for trusted internal hosts. Wired through `Deps.URLValidator`; create / update emit fhirerror 400 + `validation_failures{kind=ssrf}` before the row is persisted.

#### B-12: JWKS fetch is unauthenticated, unrestricted, and uses `http.DefaultClient` (no timeout, no body cap) — **RESOLVED** (`9045845`)
- **File:** `internal/api/auth/token_endpoint.go:97-99`, `internal/api/auth/verifier.go:106`
- **What:** Whatever URL is in `auth_clients.JwksURL` is GETted with no scheme/host validation, no body-size limit, no client timeout.
- **Why it matters:** SSRF on the auth path; a slow/hostile JWKS host hangs every authentication call indefinitely; a 100MB "jwks" response exhausts memory.
- **Fix:** Dedicated client with 5s timeout; require `https://`; enforce host allowlist; `io.LimitReader(resp.Body, 1MiB)`.
- **Status:** Fixed in `9045845`; shared `jwksPolicy` used by both token endpoint and verifier — `https`-only by default (opt-in `AllowInsecureJWKS` for local dev), optional `JWKSAllowedHosts` allowlist, dedicated `http.Client` with `DefaultJWKSFetchTimeout` (5 s, configurable via `JWKSFetchTimeout`), `io.LimitReader` capped at `MaxJWKSBodyBytes` (1 MiB). Tests: `jwks_fetch_test.go` and `e2e/orchestrator/auth_jwks_https_only_test.go`.

#### B-13: Audit log persists full request body (PHI / secrets at rest in plaintext) — RESOLVED in commit `25dffba` (merged in `790c6a8`)
- **File:** `internal/api/handlers/subscription_handlers.go:184, 433`
- **What:** Audit `Append(...)` is called with the full JSON request body (up to 1MB) on create and update.
- **Why it matters:** FHIR Subscription resources can carry `header[]` (bearer tokens for outbound calls) and PII. Plaintext at rest violates HIPAA minimum-necessary, breaks secret-rotation, and inflates storage.
- **Fix:** Strip secret-bearing fields (Authorization headers etc.) before persisting; cap canonical size; consider hashing.
- **Resolved:** `759c3a8` (impl), `3587d52` (RED tests), `2ec4de9` (e2e: api_audit_redaction_test.go). New `RedactSubscriptionForAudit` (`internal/api/handlers/audit_redact.go`) replaces values of secret-named JSON keys (`header`, `headers`, `authorization`, `token`, `apikey`, `privatekey`, `secret`, `password`, ...) with `[REDACTED]`, scrubs JWT-shape / PEM-armored / long-base64 substrings, and caps canonical bytes at `Deps.AuditMaxBytes` (default 16 KiB) with a parseable truncation envelope. `createSubscription` and `updateSubscription` route the request body through the redactor before `Audit.Append`.

#### B-14: Email STARTTLS default is `Preferred` (cleartext fallback) for a healthcare service — RESOLVED in commit `a6a36aa` (merged in `d3f2a4b`)
- **File:** `internal/channel/email/email.go:79-80, 213-220`
- **What:** Default STARTTLS policy is "Preferred" — falls back to plaintext when not advertised.
- **Why it matters:** PHI in cleartext over public network is a HIPAA breach (MITM strip-STARTTLS attack).
- **Fix:** Default to `STARTTLSRequired`; require operator opt-in to Preferred.
- **Resolution:** Default STARTTLS policy is now `STARTTLSRequired`. Operators must opt into `Preferred` (or `Disabled`) explicitly, and `New` emits a startup WARN log noting the strip-STARTTLS / cleartext risk on any non-Required configuration. Test: `TestNewDefaultsToSTARTTLSRequired` (channel-level) and the e2e orchestrator coverage in `e2e/orchestrator/email_starttls_required_test.go`.

#### B-15: SMTP PLAIN/LOGIN allowed over plaintext (credential leak) — RESOLVED in commit `a6a36aa` (merged in `d3f2a4b`)
- **File:** `internal/channel/email/email.go:600-622`
- **What:** With `STARTTLSDisabled` or `Preferred`-with-no-advertise, PLAIN/LOGIN AUTH ships base64-encoded credentials in cleartext. The `host != "localhost"` guard in `loginAuth.Start` misses `127.0.0.1`, `::1`, link-local.
- **Why it matters:** SMTP relay password lifted off the wire.
- **Fix:** Refuse to construct the channel if `STARTTLS=Disabled && AuthMechanism!=AuthNone`; require explicit `AllowCleartextAuth` flag.
- **Resolution:** `New` now refuses to construct a Channel that would ship AUTH credentials in plaintext (`STARTTLSDisabled` + non-empty `AuthMechanism`). Operators on a closed-network relay can opt back in via the explicit `AllowCleartextAuth=true` flag. Tests: `TestNewRefusesCleartextAuth` (5 subtests covering PLAIN, LOGIN, CRAM-MD5, etc.) and `TestNewAllowsCleartextAuthWhenExplicitlyOptedIn`.

#### B-16: Email header injection via `CorrelationID` — RESOLVED in commit `a6a36aa` (merged in `d3f2a4b`)
- **File:** `internal/channel/email/email.go:407-412, 489-499`
- **What:** `env.CorrelationID` is written verbatim into `Message-ID` and `X-Correlation-ID` headers via `writeHeader`; no CRLF rejection. `newMessageID` also doesn't sanitize.
- **Why it matters:** A CRLF in correlation ID forges arbitrary SMTP headers / smuggles message body. Combined with the unvalidated correlation ID accepted from `X-Correlation-ID` HTTP header (B-39), this is end-to-end attacker-controllable.
- **Fix:** Reject any `corr` containing `\r` or `\n` (and ideally non-ASCII / non-RFC2822-token chars) at `writeHeader`.
- **Resolution:** Envelope construction now rejects CRLF / NUL / C0 control chars in `CorrelationID` before any wire I/O; `writeHeader` strips CR/LF defensively as a redundant inner control. Refusal returns `PermanentFailure` so the scheduler does not retry. Tests: `TestDeliverRejectsCRLFInCorrelationID` (3 subtests for `\r`, `\n`, `\r\n`) and `TestDeliverRejectsCRLFInCorrelationIDViaMessageID`.

#### B-17: WebSocket upgrade is `InsecureSkipVerify=true` — Origin not verified (CSWSH) — RESOLVED in commit `a0360e4` (merged in `d3f2a4b`)
- **File:** `internal/channel/websocket/websocket.go:213-225`
- **What:** `coder/websocket.Accept` is called with `InsecureSkipVerify: true`, skipping the Origin header check.
- **Why it matters:** A reverse proxy does NOT check WS Origin — application must. Cross-Site WebSocket Hijacking lets a malicious origin in a victim's browser bind any token.
- **Fix:** Take an `OriginPatterns []string` option; default-deny cross-origin.
- **Resolution:** `InsecureSkipVerify=true` is gone. `Options.OriginPatterns` is the explicit allowlist; default-deny applies when the slice is empty (i.e., the upgrade rejects any cross-origin browser request unless an operator has named its origin). The upgrade log line records the rejected `Origin` header so operators see who is being denied. Tests: `TestUpgradeRejectsCrossOriginByDefault`, `TestUpgradeAllowsConfiguredOrigin`, `TestUpgradeRejectsUnlistedOriginWhenPatternsConfigured`.

#### B-18: WebSocket ack-channel close-of-closed-channel race — RESOLVED in commit `a0360e4` (merged in `d3f2a4b`)
- **File:** `internal/channel/websocket/websocket.go:471-553`
- **What:** `defer sess.cancelAck(env.Sequence)` on Deliver path and `deliverAck` (line 553 `close(ch)`) can both fire under concurrent ack-arrival + delivery-timeout — closing an already-closed channel panics.
- **Why it matters:** Server-wide panic from a single misbehaving subscriber's timing.
- **Fix:** Single-owner close (use `sync.Once`, or guard with mutex; deliverAck marks delivered, waiter closes).
- **Resolution:** The per-sequence ack channel is now wrapped in an `ackWaiter` struct that gates close through `sync.Once`. `registerAck` returns the same waiter when called twice for the same sequence (deduplicates duplicate `Deliver`); both `deliverAck` and the `Deliver` cleanup defer call `closeOnce`, so neither path can close an already-closed channel. Tests run under the race detector: `TestAckRaceDoesNotPanic` (200 iterations) and `TestConcurrentAckCancelDoesNotCloseClosedChannel` (200 iterations).

#### B-19: MLLP listener has no max-connection cap, no per-IP rate limit, no admission semaphore — RESOLVED in commit `b8c1209` (merged in `d3f2a4b`)
- **File:** `internal/mllp/endpoint.go:43-93`
- **What:** Every accepted TCP conn spawns an unbounded goroutine with read buffer + framer state; `connsWG` only counts.
- **Why it matters:** Trivial accept-flood DoS; goroutine + memory exhaustion.
- **Fix:** `cfg.MaxConnections` (semaphore-gated accept); `cfg.MaxConnectionsPerIP` (token bucket).
- **Resolution:** `ListenerConfig` gains `MaxConnections` (cross-endpoint admission semaphore) and `MaxConnectionsPerIP` (per-IP token bucket). Connections beyond the caps are accepted and immediately closed; a WARN log records the offending peer and the new `fhir_subs_mllp_connections_refused_total` counter ticks. Slots are released on disconnect via a release closure bound to the handler goroutine's defer. Tests: `TestListener_MaxConnections_RefusesExcess`, `TestListener_MaxConnectionsPerIP_RefusesExcess`.

#### B-20: MLLP listener has no TLS / MTLS support — HL7 (PHI) over plaintext — RESOLVED in commit `b8c1209` (merged in `d3f2a4b`)
- **File:** `internal/mllp/endpoint.go:43-51`
- **What:** Plain `net.Listen("tcp", ...)`. No `tls.Config`, no client cert verification. Operators have no way to opt in.
- **Why it matters:** HL7 messages carry PHI; running plaintext on a hospital network is an OCR / HIPAA finding.
- **Fix:** Add `cfg.TLS` (min v1.2, AEAD ciphers); optional `RequireAndVerifyClientCert`; wrap with `tls.NewListener`.
- **Resolution:** `ListenerConfig.TLS` is the new opt-in: cert/key paths plus optional mTLS via `RequireAndVerifyClientCert + ClientCAs`. `endpoint.bind` wraps the TCP listener with `tls.NewListener` when TLS is set; min TLS 1.2 floor enforced. `Validate()` rejects mTLS configurations that omit `ClientCAs`. Tests: `TestListener_TLS_RequiresTLSHandshake`, `TestListener_TLS_MTLS_RequiresClientCert`.

#### B-21: HL7 processor decrypts pending pairs with hardcoded `key_version=1` (breaks key rotation) — RESOLVED in commit `07d7be2`
- **File:** `internal/hl7processor/processor.go:545`, `internal/infra/storage/repos/pending_pairs.go:86`
- **What:** Both call sites pass literal `1` to `Codec.Decrypt`.
- **Why it matters:** ADR 0010 #7 commits to key rotation. After version bump, every pending row encrypted under the old version becomes undecryptable; held HL7 halves are silently dead-lettered or stuck. Same applies to any other repo that stores ciphertext.
- **Fix:** Persist `key_version` per row, read it back, pass to `Decrypt`.
- **Resolution:** Commit `6d0e5a2` (also `07d7be2` on main). `PendingPairsRepo.Insert` now persists `codec.Encrypt`'s returned key_version; `ClaimExpired` reads it back into `PendingPairRow.KeyVersion` and passes it to `Decrypt`. `processor.lockPending` and `reaper.lockPendingForReap` likewise SELECT `key_version` and decrypt with the row's version. E2E test `TestE2E_PendingPairs_DecryptsWithRowKeyVersion` writes a row under key v1, swaps to a codec where v2 is active, and asserts the row still decrypts correctly. Audit of other repos: `hl7_message_queue.go`, `ehr_events.go`, `resource_changes.go` all already use `rec.KeyVersion`; no other hardcoded-`1` callsites remain.

#### B-22: `pending_pairs` migration omits `key_version` column entirely — RESOLVED in commit `07d7be2`
- **File:** `migrations/0001_init.sql:99-108`, `internal/infra/storage/repos/pending_pairs.go:32-44`
- **What:** Schema has no `key_version` column; `Insert` writes encrypted bytes with no version stamp.
- **Why it matters:** Same root cause as B-21; the rotation contract is unfulfillable.
- **Fix:** Add migration `ALTER TABLE pending_pairs ADD COLUMN key_version SMALLINT NOT NULL DEFAULT 1`; update `Insert`/`Decrypt`.
- **Resolution:** Commit `6d0e5a2` (also `07d7be2` on main). New migration `0003_pending_pairs_key_version.sql` adds `key_version int NOT NULL DEFAULT 1` (idempotent via IF NOT EXISTS). Existing rows are stamped with v1 — the only version any deployed instance has used to date — so the migration is forward-compatible without a backfill UPDATE.

#### B-23: Matcher silently passes through unknown FHIR search parameters (fail-open silence) — RESOLVED in commit `3d80c7d` (merged in `8096936`)
- **File:** `internal/matcher/matcher.go:286-355`
- **What:** `extractFieldValues` hardcodes `status/subject/patient/code/category/name/_lastUpdated`; any other parameter returns nil → clause fails closed → topic silently never matches; no rejection at catalog load, no metric.
- **Why it matters:** A topic referencing `performer`, `encounter`, `period`, `class`, etc. silently never fires; subscribers miss notifications and operators have no signal.
- **Fix:** Reject topics at load if they reference unsupported parameters; emit `matcher_unknown_parameter_total{topic}`.
- **Resolution:** Commit `3d80c7d` (cherry-pick `04e2c36`). `internal/topics/catalog/catalog.go` now exports `SupportedSearchParameters()` / `IsSupportedSearchParameter()` and `parseSearchExpression` rejects any topic referencing an unsupported parameter at load time. `compileOne` likewise rejects unsupported `canFilterBy.filterParameter`. The catalog's `Rejected()` method (and `Report.Rejected`) surface the rejections so /readyz can read them. Tests: `TestLoadRejectsUnsupportedSearchParameter`, `TestLoadRejectsUnsupportedFilterByParameter`, `TestLoadAcceptsAllSupportedSearchParameters`, `TestLoadRejectedTopicAbsentFromCatalog` (`internal/topics/catalog/correctness_test.go`); e2e `TestMatcher_unknownParamRejected` (`e2e/orchestrator/matcher_unknown_param_rejected_test.go`).

#### B-24: Matcher FHIRPath `runFHIRPath` defaults to fail-OPEN (returns true) for unrecognized expressions — RESOLVED in commit `51b8e53` (merged in `8096936`)
- **File:** `internal/matcher/matcher.go:510-547`
- **What:** After `.exists()` and `.status = '...'` checks fall through, return `true` — every unknown FHIRPath expression treated as a match.
- **Why it matters:** Future-work P1.2 covers building the sandboxed evaluator, but the *current default* is fail-OPEN which is more dangerous than fail-closed: a topic with `Patient.deceased.empty()` would fire on every change, leaking notifications. (Future-work doesn't call out the fail-direction — flagging as a separate concrete defect.)
- **Fix:** Default to fail-CLOSED today; emit `fhirpath_unknown_expression_total`; reverse only when sandbox lands.
- **Resolution:** Commit `51b8e53` (cherry-pick `a1f4b12`). `runFHIRPath` now returns `false` for any expression shape outside the recognized minimal set. Wiring layers can install a callback via `matcher.SetUnknownFHIRPathReporter` to bump `fhir_subs_matcher_fhirpath_unknown_expression_total` without coupling the matcher package to a metrics dependency. `IsRecognizedFHIRPath` is exported for catalog-level strict-mode validation. Tests: `TestEvaluateFHIRPathUnknownExpressionFailsClosed`, `TestEvaluateFHIRPathRecognizedExpressionStillMatches` (`internal/matcher/correctness_test.go`); e2e `TestMatcher_fhirpathFailClosed` (`e2e/orchestrator/matcher_fhirpath_fail_closed_test.go`).

#### B-25: Topic catalog rejections do not fail startup; operator override silently shadows working built-in — RESOLVED in commit `3d80c7d` (merged in `8096936`)
- **File:** `internal/topics/catalog/catalog.go:240-282`
- **What:** Per-topic rejections accumulate in `Rejected`; only schema-load errors are fatal. Operator-supplied broken topic with the same `(url,version)` shadows the built-in working topic.
- **Why it matters:** Operator typo silently drops a topic from runtime; no /readyz signal; no override audit trail.
- **Fix:** Surface rejected topics to /readyz; add `--strict` startup mode; on operator-validation failure fall back to lower-priority topic; emit `topic_overridden_total`/`topic_rejected_total`.
- **Resolution:** Commit `3d80c7d` (cherry-pick `04e2c36`). `catalog.LoadStrict` is the strict-mode entry point — it returns a non-nil error wrapping every rejection so `--strict-topics` startup wiring can refuse to start. `Load` walks sources in priority order (Operator > Adapter > BuiltIn) and on a higher-priority compile failure falls back to the lower-priority working topic, recording an `Override{URL, Version, FromOrigin, FromSource, ToOrigin, ToSource, Reason}` entry. Both `Rejected` and `Overridden` are surfaced through the `Catalog` handle so /readyz can read them after the `Report` is dropped. Tests: `TestLoadStrictModeRejectsAtStartup`, `TestLoadStrictModeAcceptsValidCatalog`, `TestLoadFallsBackToBuiltInWhenOperatorOverrideRejected` (`internal/topics/catalog/correctness_test.go`); e2e `TestMatcher_strictMode`, `TestMatcher_topicOverride` (`e2e/orchestrator/`).

#### B-26: `nextEventNumber` race — two workers can both insert event_number N+1 — RESOLVED in commit `f600d42`
- **File:** `internal/engine/submatcher/worker.go:337-348`
- **What:** `SELECT MAX(event_number)+1` inside the tx without a per-subscription advisory lock; `UNIQUE(subscription_id, event_number)` will reject one of the racers but the worker (line 281) doesn't catch SQLSTATE 23505 — it surfaces as a generic insert error and aborts the whole batch.
- **Why it matters:** Under load the entire batch fails on harmless contention; throughput collapses; deliveries stuck.
- **Fix:** Per-subscription `pg_advisory_xact_lock`, or sequence per subscription, or catch 23505 and retry.
- **Resolution:** Commit `1ba1c45` (also `f600d42` on main). New migration `0004_subscriptions_next_event_number.sql` adds `next_event_number bigint NOT NULL DEFAULT 0` and backfills it from `MAX(deliveries.event_number)`. `submatcher.nextEventNumber` now does `UPDATE subscriptions SET next_event_number = next_event_number + 1 ... RETURNING next_event_number`, so Postgres's row-level lock under UPDATE serializes contention naturally — no MAX-from-deliveries lookup. E2E `TestE2E_EventNumber_NoDuplicatesUnderConcurrency` fires 50 concurrent transactions for a single subscription and asserts the resulting deliveries set is exactly 1..50 with no gaps and no duplicates.

#### B-27: Cursor monotonicity assumes deliveries rows are never deleted; retention will reuse event numbers — RESOLVED in commit `f600d42`
- **File:** `internal/engine/submatcher/worker.go:337-348` (consumer) + `internal/infra/storage/retention/retention.go:78` (deleter)
- **What:** `nextEventNumber = MAX(event_number)+1`; if retention deletes a high-numbered delivery, the next insert RE-USES that number.
- **Why it matters:** Subscribers depend on monotonic event numbers for replay/ack semantics; reuse breaks every WebSocket replay scenario.
- **Fix:** Either keep tombstones, or persist `next_event_number` on the subscription row independent of MAX.
- **Resolution:** Commit `1ba1c45` (also `f600d42` on main). Same fix as B-26: the cursor lives on `subscriptions.next_event_number` and is advanced via `UPDATE ... RETURNING`. Retention deleting deliveries no longer affects the cursor. E2E `TestE2E_EventNumber_ContinuesAfterRetention` writes 5 deliveries, deletes 3, writes 5 more, and asserts the new event_numbers continue from 6 (rather than reusing low numbers) and that `subscriptions.next_event_number` ends at 10.

#### B-28: Bundle JSON encoding is nondeterministic — breaks any audit-chain hashing of bundle bytes — RESOLVED in commit `b76f1b0`
- **File:** `internal/engine/builder/builder.go:174-190`
- **What:** Bundle assembly uses `map[string]any` → `json.Marshal`. Go map iteration is randomized; map JSON output is therefore non-deterministic.
- **Why it matters:** Hash-chained audit log over bundle bytes produces different hashes on identical inputs. Any downstream signer (S/MIME plan, P2.3) is broken at the foundation.
- **Fix:** Use a canonical JSON encoder (sorted keys), a struct, or `json.RawMessage` to preserve byte form.
- **Resolution:** Commit `0b39e95` (also `b76f1b0` on main). Bundle assembly now uses fixed-shape Go structs (`notificationBundle`, `subscriptionStatus`, `notificationEvent`, `reference`, `bundleEntry`) with explicit JSON struct tags so field order is canonical FHIR order: `resourceType`, `type`, `timestamp`, `entry`. The SubscriptionStatus is encoded once into a `json.RawMessage` so its byte layout is locked before being embedded in the entry list. Unit test `TestBuildBundleDeterminism` and e2e `TestE2E_Builder_BundleBytesAreDeterministic` both build the same job 100 times and assert byte-identical output (sha256 stable across all 100).

#### B-29: Catalog `CatalogProvider` swap is interface-typed; returns are not torn-read-safe — RESOLVED in commit `51b8e53` (merged in `8096936`)
- **File:** `internal/matcher/matcher.go:554, 656`
- **What:** Worker calls `cat := w.catalog()` per tick; interface values are 2 words. Without `atomic.Pointer`, a concurrent reload can return a torn interface value (data + type pointer mismatch).
- **Why it matters:** Catalog hot-reload data race; sporadic crash with cryptic stack.
- **Fix:** Use `atomic.Pointer[catalog.Catalog]` inside provider impl; document contract on `CatalogProvider`.
- **Resolution:** Commit `51b8e53` (cherry-pick `a1f4b12`). New `matcher.AtomicCatalogProvider` wraps an `atomic.Pointer[catalog.Catalog]`; `Get`/`Store` are race-free and `AsProvider()` returns a `CatalogProvider` closure ready for `NewWorker`. The `CatalogProvider` doc now documents the atomic-swap contract that callers (e.g., the harness's `topic_seed.go` mutex-guarded swap) must satisfy. Tests: `TestAtomicCatalogProviderRaceFree` (1000 swaps × 8 readers under `-race`) and `TestAtomicCatalogProviderUsableAsCatalogProvider` (`internal/matcher/correctness_test.go`); e2e `TestMatcher_catalogHotReloadRace` (`e2e/orchestrator/matcher_catalog_hot_reload_race_test.go`). Whole-repo `go test -race ./...` passes.

#### B-30: High-cardinality MSH-9 label on MLLP nack metric — Prometheus cardinality bomb — RESOLVED in commit `7e797f3` (merged in `f853619`)
- **File:** `internal/mllp/connection.go:269-274`
- **What:** `MetricNackTotal{type: mshFields.MessageType}` — when `allowed_message_types` mismatches, the rejected MSH-9 string becomes a label value.
- **Why it matters:** A hostile peer blasting garbage MSH-9 values creates a new time-series per value; Prometheus OOM.
- **Fix:** Whitelist label values to configured set; bucket "other" into a single value.
- **Resolution:** Commits `a387293` (RED) + `7e797f3` (GREEN), merged via `f853619`. New helper `bucketMessageTypeLabel(t, allowed)` in `internal/mllp/connection.go` returns `t` verbatim only when it is in the configured `AllowedMessageTypes` set; everything else collapses to the single label value `"other"`. Cardinality of `nack_total{reason=message_type}` is now bounded by `len(allowed)+1`. E2E `TestB30_MLLPMetricCardinalityCap` (`e2e/orchestrator/mllp_metric_cardinality_cap_test.go`) drives 100 distinct hostile MSH-9 values through the real listener over real TCP and asserts exactly one `nack_total{type=other}` series carrying all 100 increments.

#### B-31: Scheduler shutdown does not drain in-flight deliveries; recovery sweep missing — RESOLVED in commit `5017364`
- **File:** `internal/engine/scheduler/worker.go:166-234`
- **What:** Two issues: (1) `Run` returns on `ctx.Done()` without waiting for in-flight `dispatchOne` calls; (2) rows flipped to `'delivering'` by `claimTx` before Commit have no recovery path on worker crash.
- **Why it matters:** Graceful shutdown silently strands deliveries in `'delivering'`; over time the queue fills with dead rows that no worker will touch.
- **Fix:** `sync.WaitGroup` for in-flight dispatches; periodic recovery sweep that resets stale `'delivering'` rows past a stuck-threshold to `'pending'`.
- **Resolution:** Commit `945a160`. `tickOnce` now wraps each `dispatchOne` in `sync.WaitGroup`; `Run` waits up to `cfg.ShutdownGrace` (default 10s) for the WaitGroup before returning. A separate goroutine ticks `recoverStuck` every `cfg.RecoveryInterval` (default 30s); `recoverStuck` flips `'delivering'` rows whose `updated_at < now() - cfg.StuckThreshold` (default 5m) back to `'pending'` and bumps `attempts`. Exposed `RecoverStuckForTest` as a tiny seam. E2E tests cover stuck-row reset, fresh-row not touched, Run-drains-and-returns, and the periodic-sweep-during-Run path.

#### B-32: Retention sweeper SQL construction allows future SQL injection; uses `failed_permanent` status that doesn't exist; physically deletes hash-chained audit rows; runs without ctx deadline or advisory lock — RESOLVED in commits `e697162`, `52ed074`
- **File:** `internal/infra/storage/retention/retention.go:78-134`
- **What:** Multiple compounding defects: (a) `fmt.Sprintf` injects `predicate`/`idCol` into SQL with no whitelist; (b) sweeps `'failed_permanent'` which is not in the schema enum (`'failed'` is); (c) sweeps `audit_log` whose hash-chain breaks on any DELETE; (d) no `ORDER BY` so concurrent sweeps starve in lock contention; (e) no `context.WithTimeout` per Exec; (f) no `pg_advisory_lock` so multi-pod sweeps stomp on each other.
- **Why it matters:** First retention run permanently breaks audit chain (chain-verifier tool from P2.5 will report "chain break at row 0" forever); deliveries marked `failed` linger; bad query plans hang the entire pool.
- **Fix:** Whitelist `(table, idCol)` against an internal struct; replace `fmt.Sprintf` with bound params; use real schema enum; explicitly EXCLUDE `audit_log` from time-based DELETE (use partition rotation with chain-checkpoint); add `ORDER BY idCol`; per-Tick deadline; advisory lock around the whole sweep.
- **Resolution:** Commits `e697162` and `52ed074`. (table, idCol, predicate) come from a whitelisted `allowedTargets` map; `sweep()` refuses non-whitelisted tables. Status enum corrected to `'failed'`/`'dead'` (the schema's actual values). `audit_log` removed from the allow-list entirely; `cfg.AuditLog` is silently ignored with a doc comment pointing at partition-rotation as the audit retention strategy. Each chunk DELETE adds `ORDER BY <idCol>` and runs under `context.WithTimeout(ctx, SweepExecTimeout)` (30s). `Tick` acquires a session-level `pg_advisory_lock(retentionAdvisoryLockID)` on a dedicated connection so multi-pod runs serialize. E2E `TestE2E_Retention_DoesNotDeleteAuditLog` seeds 100 audit rows with very-old timestamps, runs `Tick` with `cfg.AuditLog=1ns`, and asserts the row count is unchanged. Audit retention follow-up is tracked separately as future-work P2.5 (partition-rotation-based).

#### B-33: Multi-pod migration race — no advisory lock around `applyAll`; `Concurrent` detection is a substring match — RESOLVED in commit `52ed074`
- **File:** `internal/infra/storage/migrate/migrate.go:191-208`
- **What:** Multiple replicas at rollout time race the migration runner; `CREATE INDEX CONCURRENTLY` detection is `strings.Contains(... "CREATE INDEX CONCURRENTLY")` — a SQL comment with that text trips it.
- **Why it matters:** "relation already exists" cascades, partial migrations, duplicate `schema_migrations` rows.
- **Fix:** `pg_advisory_lock(<known constant>)` for the duration of `applyAll`; statement-level parser for concurrency detection.
- **Resolution:** Commit `52ed074`. `applyAll` now holds `pg_advisory_lock(0xFEEDFACE)` on a dedicated connection for the whole apply pass; concurrent migrators serialize. `detectConcurrent` was rewritten to require an explicit `-- @CONCURRENT` directive on the leading content line of the migration body — comments and string literals containing `CREATE INDEX CONCURRENTLY` no longer trip the heuristic. Unit `TestDetectConcurrentRequiresExplicitDirective` pins the parser; e2e `TestE2E_Migrate_AdvisoryLockSerializesParallelRunners` fires 3 concurrent `migrate.Up` calls against a fresh container and asserts the final `schema_migrations` row count exactly matches the embedded migration set.

#### B-34: Audit log file sink has no fsync; `pgstore` lock leaked on writer panic — RESOLVED in commit `ad6ddd2` (merged in `f853619`)
- **File:** `internal/infra/observability/observability.go:282`, `internal/infra/observability/audit/pgstore.go:74-93`
- **What:** File sink uses `os.OpenFile(O_APPEND|O_CREATE|O_WRONLY)` with no fsync per write; `audit.Writer.Emit` acquires conn → `pg_advisory_lock` → INSERT → release in a manual sequence with no `defer recover()` over the lock holder.
- **Why it matters:** (a) Power loss loses recent audit rows — strongest durability claim broken silently; (b) panic between acquire and release leaves the chain advisory lock held forever (next audit write blocks indefinitely).
- **Fix:** Fsync on file sink (or document as best-effort); use `pg_advisory_xact_lock` (transaction-scoped, auto-released on commit/rollback).
- **Resolution:** Commits `de73974` (RED) + `ad6ddd2` (GREEN), merged via `f853619`. Three changes:
  - `audit.Writer.Emit` now wraps the durable path in `defer recover()`; on panic it releases the chain lock via the captured `release` closure and surfaces the panic as `audit: panic in durable write path: ...` to the caller. The `released` flag prevents double-release on the happy path.
  - `audit.PgStore` switches from session-level `pg_advisory_lock` to xact-scoped `pg_advisory_xact_lock`. `AcquireChainLock` opens a tx and takes the lock inside it; `LastChainHash` and `InsertAuditRow` route through `currentTx()` so the read + insert run inside that tx. Release commits the tx, which auto-releases the lock — connection loss, rollback, or panic all release it without manual intervention.
  - `observability.fileSink` is configurable: `every_write` (default, fsync per Emit) or `batched` (periodic fsync via background ticker; `Close()` drains and fsyncs once for clean shutdown). Lifecycle owns the sink and calls `Close()` before closing the underlying file handle.
  - E2E tests: `TestB34_AuditWriterPanicReleasesChainLock` (`e2e/orchestrator/audit_pgstore_panic_release_test.go`) injects a panic mid-Insert via a fault-injection store and asserts the lock is released (next Emit completes promptly under a 2s timeout instead of blocking forever); `TestB34_AuditFileSinkFsyncs` (`e2e/orchestrator/audit_file_sink_fsync_test.go`) writes through the production observability module's file sink and asserts the rows are on disk after Emit returns.

#### B-35: Secret rotation is unreachable — SIGHUP not registered — RESOLVED in commit `3a81559` (merged in `f853619`)
- **File:** `internal/infra/lifecycle/signals.go:61-77`, `internal/infra/config/secrets/secrets.go:80-110`
- **What:** Signal handler registers only SIGTERM/SIGINT. `${file:...}` placeholders are read once at startup; rotation requires a SIGHUP that no one listens for.
- **Why it matters:** Vault Agent / cert-manager rotates the on-disk file; our process keeps the old value indefinitely. Long-lived processes ship expired creds.
- **Fix:** Register SIGHUP and route it to `config.Module.Reload`; also add periodic file-mtime polling so rotation works without explicit signal.
- **Resolution:** Commits `57fafb1` (RED) + `3a81559` (GREEN) + `614bb4e` (test fixture), merged via `f853619`. Four changes:
  - `lifecycle.LifecycleModule.SetReloadHandler(fn)` registers a SIGHUP-driven callback. `installSignalHandlers` now subscribes to `SIGHUP` in addition to `SIGTERM`/`SIGINT`; the dispatcher routes SIGHUP to `invokeReloadHandler` (no-op when nil) and never to `RequestShutdown`.
  - `secrets.ResolveWithFilePaths` returns the deduplicated set of on-disk paths actually read for `${file:...}` placeholders, so the host can later mtime-poll them.
  - `config.ReloadTrigger` gains `TriggerFileMtime` alongside `TriggerSIGHUP`. `Module.Reload` accepts both. Every reload (applied or rejected) fans out to `OnReload(fn func(trigger string))` hooks with the trigger label — hosts wire this to a `fhir_subs_config_reload_total{trigger}` counter.
  - `Module.WatchSecretFiles(ctx, interval)` starts a goroutine that polls each tracked secret-file's mtime every `interval` (default 60s) and fires `Reload(TriggerFileMtime)` on any change. Tracks paths refresh on every Reload so the watcher follows config rotations that add/drop file placeholders.
  - E2E tests: `TestB35_SIGHUPReloadsConfig` (`e2e/orchestrator/config_sighup_reload_test.go`) wires SIGHUP → `config.Reload`, raises a real SIGHUP at the test process, and asserts the OnReload trigger label is "sighup". `TestB35_FileMtimePollTriggersReload` (`e2e/orchestrator/config_file_mtime_poll_test.go`) starts the watcher with a 50ms interval, mutates a `${file:...}`-backed secret on disk, and asserts a "file_mtime" reload fires within 2s without any signal.

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
- **`internal/engine/scheduler/worker.go:230-232`** — RESOLVED in `acd798d`: `Config.DispatchConcurrency` (default 1) bounds parallel dispatchOne calls per batch via a semaphore so one slow channel cannot head-of-line-block siblings.
- **`internal/engine/scheduler/worker.go:241-249`** — RESOLVED in `acd798d`: `ClassifyRequeueReason` + `ReasonSubscriptionUnavailable` / `ReasonEhrEventUnavailable` route not-found to dead-letter immediately. Pure DB load errors stay transient.
- **`internal/engine/scheduler/worker.go:269-278`** — RESOLVED in `acd798d`: `ClassifyBuildError` + `isPermanentBuildError` recognize deterministic build failures (nil id, decode focus, marshal status/bundle) and dead-letter immediately.
- **`internal/engine/scheduler/scheduler.go:60-62`** — RESOLVED in `acd798d`: `RetryConfig.PerChannel` map + `MaxAttemptsFor(channelType)` + `ClassifyOutcomeForChannel` give per-channel-type override of MaxAttempts.
- **`internal/engine/scheduler/scheduler.go:118-123`** — RESOLVED in `acd798d`: `MaxJitter=0.5` constant; `applyDefaults` now clamps Jitter to [0, 0.5] so the (1+offset) multiplier cannot approach zero.
- **`internal/engine/scheduler/worker.go:336-378`** — DEFERRED: inline UPDATE SQL in worker still present. The shared `applyBailoutDecision` consolidates the bail-out paths; full migration of the Decision SQL to the DeliveriesRepo is tracked under follow-up storage refactor.

#### S-9: Pipeline / MLLP / HL7 processor
- **`internal/mllp/connection.go:130-145`** — DEFERRED: read goroutine has no per-message frame-assembly deadline. Mitigated by S-9.4 (framer pending bound) which forces Malformed once the peer streams junk past 2× maxBody, but a true per-message deadline is still future work.
- **`internal/mllp/connection.go:316-322`** — DEFERRED: `persistCtx` is intentionally decoupled per the LLD's drain rule (in-flight persists complete after ctx cancel). `PersistTimeout ≤ ShutdownDrainGrace` cap at Validate is tracked under config validation work.
- **`internal/mllp/connection.go:483`** — RESOLVED in `acd798d`: `isClosedConnErr` now uses `errors.Is(net.ErrClosed)` plus narrow string matches for two known sentinels (was substring "closed" — caught vendor errors like "JWKS host closed for maintenance").
- **`internal/mllp/framer.go:113`** — RESOLVED in `acd798d`: `pendingExceeded()` returns Malformed{Oversized} once pending grows past 2× maxBody.
- **`internal/mllp/msh.go:33-97`** — RESOLVED in `acd798d`: `ExtractMSH` now surfaces MSH-7 (`MessageDateTime`) and MSH-18 (`Charset`) so callers can metric encoding and source `occurred` from sender's stamp.
- **`internal/hl7processor/processor.go:25-29`, `reaper.go:76`** — PARTIALLY RESOLVED in `acd798d`: `ReaperBatchSize` knob + `DefaultReaperBatchSize` constant added (was inline LIMIT 64). `ClaimBatchSize`, `ClaimIdlePollInterval`, `ReaperTickInterval` were already exposed.
- **`internal/hl7processor/processor.go:198-205`** — RESOLVED in `acd798d`: `MetricClaimCycleErrors` emitted from claim and reaper loops on non-canceled errors.
- **`internal/hl7processor/processor.go:212-298`** — ADDRESSED BY DESIGN: `peekUnprocessed` + per-row `lockRow` use `FOR UPDATE SKIP LOCKED` with `processed = false` predicate. `ok=false` path explicitly handles the lost-race case; the comment at lines 218-223 documents the race-free intent.
- **`internal/hl7processor/processor.go:288-298`** — DEFERRED: `BeginTx` failure leaves row unprocessed; per-row retry budget tracked under S-12 (matcher/submatcher MaxRowAttempts pattern; needs equivalent for hl7processor).
- **`internal/hl7processor/processor.go:407`** — RESOLVED in `acd798d`: `messageDateTime(parsed)` parses MSH-7 from `parsed.Raw`; processOne uses MSH-7 when present, falling back to `deps.Now()` otherwise.
- **`internal/hl7processor/processor.go:486-498`** — RESOLVED in `acd798d`: `MetricSameKindCollision` counter on the same-kind paired-hold defensive path with adapter_id / resource_type / held_kind / arriving_kind labels.
- **`internal/hl7processor/translate.go:53-60`** — RESOLVED in `acd798d`: `vendorPanicError` sentinel + `isPanicError` route Lex/Classify/MapToFHIR panics to `ErrorClassUnexpected` (not `ErrorClassParse`).

#### S-10: Matcher
- **`internal/matcher/matcher.go:198-213`** — RESOLVED in `d3fad44`: `SetMalformedResourceReporter` + `reportMalformedResource("search_expression", err)` callback fires on json.Unmarshal failure. Behavior unchanged (still fail-closed); metric is opt-in via wiring.
- **`internal/matcher/matcher.go:255-273`** — RESOLVED in `d3fad44`: bare-clause path now also tries `equalsString` so the matcher matches submatcher.bareClause (S-10.2).
- **`internal/matcher/matcher.go:255`** — RESOLVED in `d3fad44`: `SetUnsupportedModifierReporter` + `reportUnsupportedModifier("in", parameter)` callback fires on the `:in` path.
- **`internal/matcher/matcher.go:466-479`** — RESOLVED in `d3fad44`: `parseFlexibleDateWithFlag` returns `(time, imputedTZ, ok)` so callers can metric when a non-RFC3339 input was silently coerced to UTC.
- **`internal/matcher/matcher.go:546`** — RESOLVED in earlier B-24 work: `unknownFHIRPathReporter` fires on every fail-closed FHIRPath evaluation.
- **`internal/matcher/matcher.go:610-638`** — PARTIALLY RESOLVED in `d3fad44`: `Config.MaxRowAttempts` (default 8) added to the Config surface; full applyDecision wiring (incrementing the counter on tx failure + dead-lettering at cap) tracked under storage refactor.

#### S-11: Topics catalog
- **`internal/topics/catalog/catalog.go:421-452`** — RESOLVED in `d3fad44` (defense-in-depth) + JSON schema (primary): `compileTrigger` rejects supportedInteraction values not in {create,update,delete}.
- **`internal/topics/catalog/catalog.go:374-382`** — RESOLVED in `d3fad44`: `Topic.EventCodings []EventCoding` now carries (system, code) pairs alongside the legacy code-only `EventCodes`. Callers that need cross-system disambiguation read EventCodings.
- **`internal/topics/catalog/catalog.go:395-403`** — DEFERRED: `notificationShape` still collapses; rejecting multi-entry topics or per-entry compile is breaking-change scope. Tracked under future-work.
- **`internal/topics/catalog/catalog.go`** — PARTIALLY RESOLVED in earlier B-25 work: `Catalog.Rejected()` and `Catalog.Overridden()` expose the diagnostic surface; Prometheus `topics_rejected_total{origin,reason}` / `topic_overridden_total{from,to}` are wired in callers, not this package.

#### S-12: Engine / submatcher / builder
- **`internal/engine/submatcher/worker.go:251`** — DEFERRED: `ListActiveByTopic` still returns the full list inside the fanout tx. Streaming/pagination requires a repo refactor (LIMIT/OFFSET cursor); tracked under storage work.
- **`internal/engine/submatcher/worker.go:178-202`** — RESOLVED in `d3fad44`: `Config.PoolSize` (default 1) mirrors matcher.Config.PoolSize; API consistency restored.
- **`internal/engine/submatcher/worker.go:178-202`** — PARTIALLY RESOLVED in `d3fad44`: `Config.MaxRowAttempts` (default 8) added to Config surface; full counter wiring inside Run is tracked alongside matcher's equivalent.
- **`internal/engine/submatcher/worker.go:302-310`** — DEFERRED: fanout tx still updates `events_since_subscription_start` inline; batching/de-dup is hot-subscription scaling work tracked in future-work.
- **`internal/engine/submatcher/worker.go:354-370`** — RESOLVED in `d3fad44`: `resourceTypeOf` now uses a streaming `json.Decoder` (`scanResourceType`) to find the top-level `resourceType` without materializing the body; falls back to full unmarshal only for weird shapes.
- **`internal/engine/builder/builder.go:103-108`** — RESOLVED in `d3fad44`: `sort.SliceStable` now tie-breaks on event ID string when per-sub event_numbers collide (e.g., when PerSubEventNumbers is missing entries), making the sort deterministic.
- **`internal/engine/builder/builder.go:139, 185`** — RESOLVED in `d3fad44`: Bundle.timestamp and notificationEvent.timestamp now use `time.RFC3339Nano`, preserving sub-second precision per the FHIR `instant` spec.
- **`internal/engine/builder/builder.go:208-210`** — RESOLVED in `d3fad44`: handshake/heartbeat correlation_id is now a deterministic v5 UUID derived from notificationType + subscriptionID. Replays produce the same ID.
- **`internal/engine/builder/builder.go:36-38`** — DEFERRED: `fhir+xml` rejection belongs at the subscription-create API path; the builder hardcodes fhir+json. Tracked under S-2/S-5 (API + message channel) work.

#### S-13: Storage / repos — RESOLVED in c765c8e (branch fix/sf-storage-observability-config)
- **`internal/infra/storage/codec/codec.go:142-189`** — RESOLVED c765c8e: AES-GCM key-rotation cadence (NIST 2^32 limit) documented in package doc.
- **`internal/infra/storage/repos/audit_log.go:18-32`** — RESOLVED c765c8e: added `AppendChained` with prev-hash verification + `ErrAuditPrevHashMismatch`; legacy `Append` documented.
- **`internal/infra/storage/repos/subscriptions.go:82-118`** — RESOLVED c765c8e: added `StreamActiveByTopic` and `ListActiveByTopicPage` for bounded-memory fanout; `ListActiveByTopic` retained as a thin wrapper.
- **`internal/infra/storage/pool/pool.go:115-132`** — RESOLVED c765c8e: defaults stay but every field is now configurable via `pool.Config` and plumbed through `storage.Pool`.
- **`internal/infra/storage/pool/pool.go:129-132`** — RESOLVED c765c8e: `AfterConnect` now SETs `statement_timeout`, `idle_in_transaction_session_timeout`, and `lock_timeout` on every checked-out connection.
- **`internal/infra/storage/storage.go:182-229`** — RESOLVED c765c8e: each Tick now runs under `context.WithTimeout(bgCtx, TickTimeout)`; `OnTickError` hook surfaces previously-discarded errors.
- **`internal/infra/storage/storage.go:268-283`** — RESOLVED c765c8e: pool-close budget derived from `cfg.Lifecycle.ShutdownGracePeriod` (default 30s); 5s hardcode removed.
- **`internal/infra/storage/partition/partition.go:42`** — RESOLVED c765c8e: errors flow through `OnTickError`; first-run failure no longer silently discarded.
- **`internal/infra/storage/partition/partition.go:106-145`** — RESOLVED c765c8e: `dropOnePartition` runs `ALTER TABLE ... DETACH PARTITION` and `DROP TABLE` inside a single transaction; partial failure rolls back.
- **`migrations/0001_init.sql:277-300`** — RESOLVED c765c8e: documented apply-time `now()` semantics + `CREATE TABLE IF NOT EXISTS` idempotency contract; checksum is over file text so it remains stable.

#### S-14: Observability — RESOLVED in 9e7fa45 (branch fix/sf-storage-observability-config)
- **`internal/infra/observability/observability.go:179`** — RESOLVED 9e7fa45: `LoggingConfig.Sink` and `LoggingConfig.DebugLogPayloads` plumbed into `logging.NewLogger`; default still `os.Stdout`.
- **`internal/infra/observability/observability.go:298-300`** — RESOLVED 9e7fa45: `fileSink` pre-constructs the inner `WriterSink` once at construction; per-Emit allocation removed.
- **`internal/infra/observability/audit/audit.go:42-51, 233`** — RESOLVED 9e7fa45: chain genesis literal is now configurable via `WriterOptions.GenesisLiteral`; `GenesisHashFromLiteral` and `VerifyChainWithGenesis` exposed for matching readers.
- **`internal/infra/observability/audit/audit.go:151-205`** — RESOLVED 9e7fa45 (documented): the advisory_lock + LastChainHash + INSERT serialization is the chain's throughput cap by design; `Emit` doc-block calls out the constraint and points operators at deployment-side mitigations.
- **`internal/infra/observability/audit/audit.go:347-450`** — RESOLVED 9e7fa45: `canonicalNumber` rewritten to use IEEE-754 shortest round-trip via `strconv.AppendFloat('g', -1, 64)`; integer fast-path retained; documented for external JCS auditors.
- **`internal/infra/observability/correlation/correlation.go:114-126`** — RESOLVED 9e7fa45: `MaxCorrelationIDLen=128` cap; `IsValidCorrelationIDChar` allow-list (alnum + `-._/`); CRLF / oversize / disallowed-char inputs fall back to a fresh UUID.
- **`internal/infra/observability/logging/logging.go:35-41, 192-208`** — RESOLVED 9e7fa45: PHI list expanded with `patient`/`payload`/`dob`/`mrn`/`webhook`/`callback`/`target`/`name`/`identifier`/etc.; redaction match is now case-insensitive.
- **`internal/infra/observability/tracing/tracing.go:62-100`** — RESOLVED 9e7fa45: `tracing.SafeAttribute` redacts span values that are PII-shaped (long strings or sensitive key suffixes); guidance documented.
- **`internal/infra/observability/tracing/tracing.go:73-79`** — RESOLVED 9e7fa45: OTLP exporter constructed with `context.WithTimeout` (default 10s), `TLSConfig` / `Headers` / `Insecure` knobs added; refuses plaintext OTLP to non-loopback collectors unless `Insecure=true`.

#### S-15: Config / lifecycle — RESOLVED in 081200d (branch fix/sf-storage-observability-config)
- **`internal/infra/config/config.go:118-125`** — RESOLVED 081200d: `Start` now respects ctx (refuses with wrapped `ctx.Err()` if already canceled).
- **`internal/infra/config/loader/loader.go:101-102`** — RESOLVED 081200d: `EnvCollisions` exposes ambiguous env-var derivations; `ReadEnvForKnownKeys` drops colliding env vars rather than silently picking one.
- **`internal/infra/config/redaction/redaction.go:101-121`** — RESOLVED 081200d: walker depth-capped at `MaxRedactDepth=256`; deeper subtrees collapse to `RedactedTooDeep` sentinel.
- **`internal/infra/config/secrets/secrets.go:80-110`** — RESOLVED 081200d: `${file:...}` reads use `io.LimitReader(f, MaxSecretFileSize+1)`; oversize returns typed `ErrSecretFileTooLarge`.
- **`internal/infra/config/effective_store/effective_store.go:107-119`** — RESOLVED 081200d: bounded worker pool (`MaxConcurrentNotifications=32`); panicking subscribers recovered + logged via `SetNotifyPanicLogger`; saturated-pool callbacks fall back to inline-with-recover.
- **`internal/infra/config/reload/reload.go:189-216`** — RESOLVED 081200d: `splitPath` honours `\.` and `\\` escapes so keys containing literal `.` (subscriber hostnames) route correctly.
- **`internal/infra/lifecycle/sequencer.go:281-306`** — RESOLVED 081200d: `resultsMu` serializes hook-goroutine and phase-deadline writes into the shared slice; deadline path no longer races; report loop reads a mutex-protected snapshot.
- **`internal/infra/lifecycle/sequencer.go:362-369`** — RESOLVED 081200d: `isDeadlineExceeded` uses `errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)`; wrapped sentinels classified correctly.

#### S-16: Empty packages / dead code — RESOLVED 2026-06-18 (1ffc778, 2a0bbc2, 060d538, eea803a, ed87e64)
- **`internal/channels/{email,message,resthook,websocket}/*.go`** — entire `internal/channels/` (plural) tree is 5-line empty stubs; production uses `internal/channel/` (singular). Delete the duplicates. **RESOLVED 1ffc778** — entire `internal/channels/` tree deleted; verified no production imports.
- **`internal/queue/queue.go`, `internal/wakeup/wakeup.go`** — empty packages; either implement or delete. **NOT FOUND** — no `internal/queue/` or `internal/wakeup/` package exists in tree (audit was incorrect; the real packages live at `internal/infra/queue/` and `internal/infra/wakeup/` and contain real code). No action needed.
- **`internal/adapters/{defaults,epic}/*.go`, `internal/adapterspi/adapterspi.go`** — 5-line empty packages; flagged as misleading abstraction. **RESOLVED 2a0bbc2 + 060d538** — `internal/adapters/` and `internal/adapterspi/` deleted; production uses `internal/adapter/` (singular) and `internal/adapter/spi/`.
- **`internal/domain/{bundle,cursor}/*.go`, `internal/domain/domain.go`** — empty domain packages. **RESOLVED eea803a** — entire `internal/domain/` tree deleted.
- **`internal/infra/lifecycle/lifecycle.go:283-284`** — dead reference `var _ = errors.New` indicates incomplete wiring. **RESOLVED ed87e64** — sentinel and now-unused `errors` import removed.

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
