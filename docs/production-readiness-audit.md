# Production Readiness Audit

Date: 2026-06-18
Commit audited: `b624b7d` (main)
Module: `github.com/bzimbelman/fhir-ehr-subscriptions-service`
Scope: production code only (excludes `_test.go` files); 128 `.go` files / ~22k LOC across `cmd/`, `internal/`, `adapters/`. Cross-referenced against `docs/future-work.md` — items already there are noted, not restated.
Methodology: line-by-line read of every production file across four parallel scrutiny tracks (auth + API; channels + delivery; pipeline + matcher + engine + MLLP + HL7 + topics; storage + infra + lifecycle + observability + cmd). Plus `go vet ./...` (clean) and grep sweeps for `TODO`/`FIXME`/`HACK`/`XXX`/"for now"/"stub"/"placeholder"/"simplified".

## Summary

**Original audit (2026-06-18, commit `b624b7d`):** roughly **30 BLOCKERs**, **~70 SHOULD-FIX**, **~30 NICE-TO-HAVE**. The original summary observation was that the system had a working backbone but was not yet a "drop in and run" production service — correctness defects (silent fail-open in matcher, nondeterministic bundle JSON breaking audit-chain assumptions, decryption that hardcoded `key_version=1` and broke key rotation, retention sweeper that physically deleted hash-chained audit rows), security defects (PLAIN/LOGIN SMTP allowed over plaintext, WSS upgrade with no Origin enforcement, SSRF on subscriber endpoints, JWKS fetch unrestricted, header injection via correlation ID, JTI cache eviction broken), and operational defects (`/readyz` always 503 in cmd/run.go, HTTP server missing Write/Idle timeouts, multi-pod migration race with no advisory lock, fire-and-forget activation goroutines without shutdown coordination).

**Current state:** **0 BLOCKERs remaining**, **108 SHOULD-FIX RESOLVED + 4 PARTIAL + 13 DEFERRED (125 total enumerated sub-items)**, **33 NICE-TO-HAVE RESOLVED + 2 DEFERRED + 4 OPEN (39 total: 35 N-1 polish items + 4 D-* items discovered during B-4 wiring)**. All B-1 through B-35 BLOCKERs have been resolved on `main`. B-4 has now been brought through to RESOLVED on the `fix/b4-full-production-wiring` branch: the production binary's `cmd/fhir-subs/run.go` constructs a real DB pool (with migrations), AES-GCM codec from a versioned key bundle, SMART Backend Services verifier + token endpoint, `handlers.RegisterRoutes` on a chi router, MLLP TCP listener with persist-then-ACK, and the four-stage pipeline (HL7 processor, matcher, submatcher, scheduler). All four phases of the lifecycle sequencer now have production hooks: stop_accepting (MLLP listener), drain_in_flight (pipeline + activation WaitGroup), close_connections (DB pool). The S-* SHOULD-FIX cohort (S-1 cmd hardening; S-2 API handler knobs; S-3/S-4/S-5 auth/rest-hook/message channel hardening; S-6 email STARTTLS metrics; S-7 websocket bounded-resources; S-8 scheduler classify-and-bail; S-9/S-10/S-11/S-12 pipeline / matcher / topics / engine; S-13/S-14/S-15 storage / observability / config; S-16 phantom-package cleanup) all landed across merges `d027dab`, `1078e41`, `83365e6`, `c1c6b32`, `7d4e7b4`, `6aaf6e5`. The N-1 polish batch landed in `fix/nice-to-have-batch` (commits `c1ff9eb`, `6649d9b`, `2c8e258`, `79921ca`, `ffe847b`). Remaining open work is the 13 DEFERRED SHOULD-FIX sub-bullets (cross-package storage refactors, audit-chain canonicaliser RFC 8785 reconciliation, fhir+xml rejection at API boundary), 2 DEFERRED N-1 items (chi.Middleware-typed Auth wiring, PROXY protocol v2 in MLLP), and the 4 D-* findings discovered during B-4 wiring (empty topic catalog, placeholder activator, pgxpool diagnostic latency, typed adapter error).

---

## Resolution Status

Every finding from the audit (BLOCKER, SHOULD-FIX sub-bullet, NICE-TO-HAVE polish item, and the four D-* findings discovered during B-4 wiring) is rolled up into the table below. Section bodies further down carry the original What/Why/Fix/Resolution prose for reference; this table is the single-page summary of "what's done, what remains." The section is encoded in the ID prefix (`B-*` BLOCKER, `S-*.X` SHOULD-FIX sub-bullet, `N-1.X` NICE-TO-HAVE polish, `D-*` discovered during B-4 wiring).

**Counts:**
- **BLOCKERs (B-*):** 35 total — 35 RESOLVED, 0 OPEN, 0 DEFERRED
- **SHOULD-FIX (S-*):** 125 total — 107 RESOLVED, 4 PARTIAL, 0 OPEN, 14 DEFERRED
- **NICE-TO-HAVE (N-* / D-*):** 39 total — 32 RESOLVED, 0 PARTIAL, 4 OPEN, 3 DEFERRED
- **TOTAL:** 199 findings — 174 RESOLVED, 4 PARTIAL, 4 OPEN, 17 DEFERRED (87% closed)

| ID | Title | Status | Commit/Branch | Notes |
|----|-------|--------|---------------|-------|
| B-1 | `/readyz` always 503 in production entry point | RESOLVED | `35b2cea`, `192ab8e` | merged in `8096936` |
| B-2 | HTTP server missing Write / Idle / MaxHeaderBytes | RESOLVED | `35b2cea` | merged in `8096936` |
| B-3 | `markStartupComplete` fires before system is ready | RESOLVED | `35b2cea`, `192ab8e` | merged in `8096936` |
| B-4 | Production `run.go` never calls `handlers.RegisterRoutes` | RESOLVED | `192ab8e` + `2e28aa8`, `c1e5b41`, `76f92bb`, `743cddc`, `0a5e3bc`, `b6004e7` | scaffolding merged in `8096936`; full DB / codec / auth / handlers / MLLP / pipeline wiring on `fix/b4-full-production-wiring` |
| B-5 | `jwksCache` map race | RESOLVED | `168cc80` | merged in `0a771cf` |
| B-6 | Token endpoint missing body-size limit | RESOLVED | `7200625` | merged in `0a771cf` |
| B-7 | Missing `jti` silently accepted (replay bypass) | RESOLVED | `e788c0e` | merged in `0a771cf` |
| B-8 | Raw JWT parser error leaked in HTTP body | RESOLVED | `3e7de5e` | merged in `0a771cf` |
| B-9 | JTI cache eviction broken | RESOLVED | `aa7567f` | merged in `0a771cf` |
| B-10 | Fire-and-forget activation goroutines | RESOLVED | `a6e042f` | merged in `790c6a8` |
| B-11 | SSRF on subscriber endpoint URL | RESOLVED | `9689730` | merged in `790c6a8` |
| B-12 | JWKS fetch unauthenticated / no body cap | RESOLVED | `34d2196` | merged in `0a771cf` |
| B-13 | Audit log persists full request body | RESOLVED | `25dffba` | merged in `790c6a8` |
| B-14 | Email STARTTLS default `Preferred` | RESOLVED | `a6a36aa` | merged in `d3f2a4b` |
| B-15 | SMTP PLAIN/LOGIN allowed over plaintext | RESOLVED | `a6a36aa` | merged in `d3f2a4b` |
| B-16 | Email header injection via `CorrelationID` | RESOLVED | `a6a36aa` | merged in `d3f2a4b` |
| B-17 | WebSocket upgrade `InsecureSkipVerify=true` | RESOLVED | `a0360e4` | merged in `d3f2a4b` |
| B-18 | WebSocket ack-channel close-of-closed race | RESOLVED | `a0360e4` | merged in `d3f2a4b` |
| B-19 | MLLP listener missing connection caps | RESOLVED | `b8c1209` | merged in `d3f2a4b` |
| B-20 | MLLP listener missing TLS / mTLS | RESOLVED | `b8c1209` | merged in `d3f2a4b` |
| B-21 | HL7 processor decrypts with hardcoded key version | RESOLVED | `07d7be2` | cherry-pick of `6d0e5a2` |
| B-22 | `pending_pairs` migration omits `key_version` | RESOLVED | `07d7be2` | cherry-pick of `6d0e5a2` |
| B-23 | Matcher silently passes through unknown search params | RESOLVED | `3d80c7d` | cherry-pick of `04e2c36`, merged in `8096936` |
| B-24 | FHIRPath `runFHIRPath` defaults to fail-OPEN | RESOLVED | `51b8e53` | cherry-pick of `a1f4b12`, merged in `8096936` |
| B-25 | Topic catalog rejections do not fail startup | RESOLVED | `3d80c7d` | cherry-pick of `04e2c36`, merged in `8096936` |
| B-26 | `nextEventNumber` race | RESOLVED | `f600d42` | cherry-pick of `1ba1c45` |
| B-27 | Cursor monotonicity assumes deliveries never deleted | RESOLVED | `f600d42` | cherry-pick of `1ba1c45` |
| B-28 | Bundle JSON encoding nondeterministic | RESOLVED | `b76f1b0` | cherry-pick of `0b39e95` |
| B-29 | `CatalogProvider` swap not torn-read-safe | RESOLVED | `51b8e53` | cherry-pick of `a1f4b12`, merged in `8096936` |
| B-30 | High-cardinality MSH-9 label on MLLP nack metric | RESOLVED | `7e797f3` | merged in `f853619` |
| B-31 | Scheduler shutdown does not drain in-flight deliveries | RESOLVED | `5017364` | branch-merge |
| B-32 | Retention sweeper SQL injection / audit-chain DELETE | RESOLVED | `e697162`, `52ed074` | |
| B-33 | Multi-pod migration race | RESOLVED | `52ed074` | |
| B-34 | Audit log file sink missing fsync; pgstore lock leak | RESOLVED | `ad6ddd2` | merged in `f853619` |
| B-35 | Secret rotation unreachable — SIGHUP not registered | RESOLVED | `3a81559` | merged in `f853619` |
| S-1.1 | `applySets` errors print raw RHS — redact | RESOLVED | `47e6916` | |
| S-1.2 | `signal.NotifyContext` SIGTERM/SIGINT only | RESOLVED | `3a81559` | already-done at audit time; lifecycle dispatcher handles SIGHUP separately |
| S-1.3 | No top-level `defer recover()` over `realMain` | RESOLVED | `ccdc7a1` | |
| S-1.4 | JSON logger bypasses observability logger | RESOLVED | `35d57e1` | |
| S-1.5 | `srv.Close()` error after `srv.Shutdown` failure dropped | RESOLVED | `dbc8356` | |
| S-1.6 | Magic 2s slack on shutdown wait | RESOLVED | — | already-done; magic 2s removed by earlier B-1/B-3 wiring |
| S-1.7 | `/metadata` should mount production CapabilityStatement | RESOLVED | `a9f96f7` | new `RegisterPublicRoutes`; cmd stub remains as fallback |
| S-1.8 | Default `0.0.0.0` bind, no loopback opt-in | RESOLVED | `6807f87` | warn-log on wildcard+insecure; fix is in `cmd/fhir-subs/config.go` (default) and `cmd/fhir-subs/run.go:161-167` (warn) — original `defaults.go`/`metrics.go` pointers were stale |
| S-2.1 | `/metadata` mounted inside auth middleware | RESOLVED | `a9f96f7` | |
| S-2.2 | Body size limit hardcoded `1<<20` | RESOLVED | `4743ce7` | new `Deps.MaxBodyBytes` |
| S-2.3 | json-schema error returned verbatim | RESOLVED | `4743ce7` | new `Deps.MaxSchemaErrorBytes` |
| S-2.4 | If-None-Exist O(N) `ListByClient` | DEFERRED | — | requires `SubscriptionsStore` interface change |
| S-2.5 | Channel registry plain-map race | RESOLVED | `caa68a4` | doc'd as immutable post-`RegisterRoutes` |
| S-2.6 | ETag is id, not version; `If-Match` unquoted | RESOLVED | `4743ce7` | requires `W/"<id>"` |
| S-2.7 | Six `_ = err` swallows in `activate` | RESOLVED | `a9f96f7` | new `Deps.Logger` (nil-safe) |
| S-2.8 | `searchSubscriptions` no pagination | DEFERRED | — | requires repo interface growth |
| S-2.9 | Magic timestamp format repeated 5× | RESOLVED | `4743ce7` | single `instantFormat` constant |
| S-2.10 | `since, _ := strconv.ParseInt(...)` discards err | RESOLVED | `4743ce7` | new `parseEventNumberParam` |
| S-2.11 | `$status` bulk no cap on id params | RESOLVED | `4743ce7` | new `Deps.MaxStatusBulkIDs` (256) |
| S-2.12 | `buildSubscriptionStatus` uses `context.Background()` | RESOLVED | `a9f96f7` | threaded `r.Context()` |
| S-2.13 | `fhirVersion` hardcoded `"5.0.0"` | RESOLVED | `4743ce7` | sourced from `Deps.FHIRVersion` |
| S-2.14 | `pg_stores` no per-query deadline | DEFERRED | — | needs B-4 wiring fully landed |
| S-2.15 | `$events` replay hardcoded `LIMIT 1000` | DEFERRED | — | follows S-2.8 pagination |
| S-2.16 | `Hash: []byte{0}` placeholder; no chain integrity | DEFERRED | — | replaced by observability/audit hash-chained store under B-4 |
| S-2.17 | Unvalidated `X-Correlation-ID` reflected | RESOLVED | `a9f96f7` | drops non-UUID values |
| S-2.18 | `/metrics` has no auth | RESOLVED | `a9f96f7` | new `metrics.AuthGuard(bearer)` |
| S-2.19 | `routePattern` falls back to `r.URL.Path` | RESOLVED | `a9f96f7` | constant `<unmatched>` |
| S-2.20 | Histogram bucket-count cap; cardinality validator narrow | RESOLVED | `caa68a4` | validator at `internal/infra/observability/metrics/metrics.go:309-338` rejects endpoint, topic_url, client_id, correlation_id, actor_id (audit's `internal/api/metrics/metrics.go` pointer was stale) |
| S-3.1 | 60s `ClockSkew` default too generous | RESOLVED | `a2318e9` | lowered to 30s |
| S-3.2 | No rate limit on token endpoint | RESOLVED | `a2318e9` | per-source-IP token bucket |
| S-3.3 | No per-client rate limit on subscription create / WS bind-token | DEFERRED | — | exported `RateLimit` primitive for handler MR |
| S-3.4 | `exp, _ := claimToTime(...)` discards err | RESOLVED | `a2318e9` | fail-closed; 401 malformed |
| S-3.5 | `HasScope` is O(n) | RESOLVED | `a2318e9` | sync.Once-guarded set |
| S-4.1 | rest-hook default `*http.Client` no `Timeout` | RESOLVED | `a2318e9` | `Timeout=RequestTimeout` |
| S-4.2 | `MaxIdleConnsPerHost`/`MaxConnsPerHost` hardcoded | RESOLVED | `a2318e9` | exposed via Options; TLS 1.3 default |
| S-4.3 | No max bundle size | RESOLVED | `a2318e9` | `Options.MaxBundleBytes` (8 MiB) |
| S-4.4 | `allowSubscriberHeader` default-permit | RESOLVED | `a2318e9` | deny-list expansion |
| S-4.5 | NXDOMAIN classified `PermanentFailure` | RESOLVED | `a2318e9` | DNS errors all transient |
| S-4.6 | `readBodyExcerpt` may leak PHI in `out.Reason` | RESOLVED | `a2318e9` | opt-in default OFF |
| S-5.1 | message channel default-client no Timeout | RESOLVED | `a2318e9` | mirror of S-4.1 |
| S-5.2 | non-`fhir+json` fails at delivery time | RESOLVED | `a2318e9` | `ValidateContentType` exposed |
| S-5.3 | `Bundle.timestamp` uses `time.RFC3339` | RESOLVED | `a2318e9` | `RFC3339Nano` |
| S-5.4 | message channel default-permit allowlist | RESOLVED | `a2318e9` | mirror of S-4.4 |
| S-6.1 | `dialer.Timeout` not set | RESOLVED | `972dda7` | bound to `RequestTimeout` |
| S-6.2 | No metric on STARTTLS-Preferred fallback | RESOLVED | `972dda7` | new `MetricSTARTTLSOutcomeTotal` |
| S-6.3 | `smtpErrorCode` custom byte-loop parser | RESOLVED | `972dda7` | switched to `errors.As(*textproto.Error)` |
| S-6.4 | `Close()` errors silently swallowed | RESOLVED | `972dda7` | logged via `slog.WarnContext` |
| S-7.1 | `sessions` map unbounded | RESOLVED | `4775357` | `Options.MaxSessions` / `MaxSessionsPerClient` |
| S-7.2 | No upgrade body-size / read-header timeout | RESOLVED | `4775357` | `ConfigureServer` + 4 KiB bind read limit |
| S-7.3 | `conn.SetReadLimit` never called | RESOLVED | `4775357` | tightens to `MaxFrameBytes` after bind |
| S-7.4 | bind-read timeout hardcoded 10s | RESOLVED | `4775357` | `Options.BindTimeout` |
| S-7.5 | pingLoop uses `context.Background()` parent | RESOLVED | `4775357` | bound to channel ctx via `New()` |
| S-7.6 | Single ping uses full `idleTimeout` | RESOLVED | `4775357` | `Options.PingWriteTimeout` (10s) |
| S-7.7 | readLoop has no idle-timeout enforcement | RESOLVED | `4775357` | per-session `lastReadAtNS` polled by pingLoop |
| S-7.8 | Replay loop materializes full slice | RESOLVED | `4775357` | `Options.MaxReplayEvents` (10k) + `replay-truncated` frame |
| S-7.9 | `Close` doesn't WaitGroup-join goroutines | RESOLVED | `4775357` | per-session `c.wg` |
| S-8.1 | Single in-flight `dispatchOne` per batch | RESOLVED | `acd798d` | `Config.DispatchConcurrency` |
| S-8.2 | Not-found errors not dead-lettered | RESOLVED | `acd798d` | `ClassifyRequeueReason` + sentinel reasons |
| S-8.3 | Permanent build errors retried | RESOLVED | `acd798d` | `isPermanentBuildError` |
| S-8.4 | `MaxAttempts` not per-channel-type | RESOLVED | `acd798d` | `RetryConfig.PerChannel` |
| S-8.5 | Jitter uncapped | RESOLVED | `acd798d` | `MaxJitter=0.5` clamp |
| S-8.6 | Inline UPDATE SQL in worker | DEFERRED | — | full migration to DeliveriesRepo tracked under storage refactor |
| S-9.1 | MLLP read goroutine no per-message frame deadline | DEFERRED | — | mitigated by S-9.4; true per-message deadline future work |
| S-9.2 | `persistCtx` decoupled — `PersistTimeout` cap missing | DEFERRED | — | tracked under config validation |
| S-9.3 | `isClosedConnErr` substring-matches "closed" | RESOLVED | `acd798d` | `errors.Is(net.ErrClosed)` |
| S-9.4 | Framer no pending-byte bound | RESOLVED | `acd798d` | `pendingExceeded()` returns Malformed{Oversized} at 2× maxBody |
| S-9.5 | `ExtractMSH` doesn't surface MSH-7/MSH-18 | RESOLVED | `acd798d` | new `MessageDateTime`, `Charset` |
| S-9.6 | `ReaperBatchSize`, claim/idle knobs | PARTIAL | `acd798d` | `ReaperBatchSize` added; others were already exposed |
| S-9.7 | No `MetricClaimCycleErrors` emission | RESOLVED | `acd798d` | emitted from claim and reaper loops |
| S-9.8 | `peekUnprocessed` lost-race window | RESOLVED | — | addressed-by-design: `FOR UPDATE SKIP LOCKED` + `processed=false` predicate |
| S-9.9 | `BeginTx` failure leaves row unprocessed | DEFERRED | — | per-row retry budget tracked under S-12 pattern |
| S-9.10 | MSH-7 not used for occurred timestamp | RESOLVED | `acd798d` | `messageDateTime(parsed)` |
| S-9.11 | Same-kind paired-hold collision metric missing | RESOLVED | `acd798d` | `MetricSameKindCollision` |
| S-9.12 | Translate panics misclassified as `ErrorClassParse` | RESOLVED | `acd798d` | `vendorPanicError` sentinel + `isPanicError` |
| S-10.1 | Matcher json.Unmarshal error not metricised | RESOLVED | `d3fad44` | `SetMalformedResourceReporter` |
| S-10.2 | Bare-clause path inconsistent with submatcher | RESOLVED | `d3fad44` | `equalsString` parity |
| S-10.3 | `:in` path no metric on unsupported modifier | RESOLVED | `d3fad44` | `SetUnsupportedModifierReporter` |
| S-10.4 | Flexible-date silent UTC coercion | RESOLVED | `d3fad44` | `parseFlexibleDateWithFlag` returns imputedTZ flag |
| S-10.5 | FHIRPath fail-closed metric | RESOLVED | (B-24 work) | `unknownFHIRPathReporter` |
| S-10.6 | `MaxRowAttempts` knob + counter | PARTIAL | `d3fad44` | knob added; counter wiring tracked under storage refactor |
| S-11.1 | `compileTrigger` missing `supportedInteraction` enum check | RESOLVED | `d3fad44` | defense-in-depth + JSON schema |
| S-11.2 | `Topic.EventCodings` missing system+code pair | RESOLVED | `d3fad44` | new `EventCoding` slice |
| S-11.3 | `notificationShape` collapses multi-entry | RESOLVED | story/54 | catalog `Load` rejects multi-entry topics with clear error |
| S-11.4 | Topic catalog Prometheus metrics | PARTIAL | (B-25 work) | `Rejected()`/`Overridden()` exposed; metric wiring in callers |
| S-12.1 | `ListActiveByTopic` materializes full list | DEFERRED | — | streaming requires repo refactor |
| S-12.2 | submatcher `PoolSize` knob missing | RESOLVED | `d3fad44` | `Config.PoolSize` |
| S-12.3 | submatcher `MaxRowAttempts` | PARTIAL | `d3fad44` | knob added; counter wiring pending |
| S-12.4 | Fanout tx inline `events_since_subscription_start` UPDATE | DEFERRED | — | hot-subscription scaling work |
| S-12.5 | `resourceTypeOf` materializes full body | RESOLVED | `d3fad44` | streaming `json.Decoder` |
| S-12.6 | Builder sort non-deterministic on tie | RESOLVED | `d3fad44` | `sort.SliceStable` + ID tiebreaker |
| S-12.7 | Bundle/notificationEvent timestamps `RFC3339` | RESOLVED | `d3fad44` | `RFC3339Nano` |
| S-12.8 | Handshake/heartbeat correlation_id non-deterministic | RESOLVED | `d3fad44` | deterministic v5 UUID |
| S-12.9 | `fhir+xml` rejection at builder, not API | DEFERRED | — | belongs at subscription-create API path |
| S-13.1 | AES-GCM rotation cadence undocumented | RESOLVED | `c765c8e` | NIST 2^32 limit doc'd |
| S-13.2 | Audit log no chained-append + prev-hash verify | RESOLVED | `c765c8e` | `AppendChained` + `ErrAuditPrevHashMismatch` |
| S-13.3 | `ListActiveByTopic` no streaming/page variant | RESOLVED | `c765c8e` | `StreamActiveByTopic`, `ListActiveByTopicPage` |
| S-13.4 | pgxpool defaults non-configurable | RESOLVED | `c765c8e` | `pool.Config` plumbed through |
| S-13.5 | `AfterConnect` no statement_timeout / lock_timeout | RESOLVED | `c765c8e` | sets statement_timeout, idle_in_tx_session_timeout, lock_timeout |
| S-13.6 | Storage tick has no per-tick deadline | RESOLVED | `c765c8e` | `TickTimeout` + `OnTickError` |
| S-13.7 | Pool-close budget hardcoded 5s | RESOLVED | `c765c8e` | derived from `cfg.Lifecycle.ShutdownGracePeriod` |
| S-13.8 | Partition first-run failure silent | RESOLVED | `c765c8e` | flows through `OnTickError` |
| S-13.9 | `dropOnePartition` not transactional | RESOLVED | `c765c8e` | DETACH + DROP in single tx |
| S-13.10 | Migrations apply-time `now()` undocumented | RESOLVED | `c765c8e` | doc'd; checksum is over file text |
| S-14.1 | `LoggingConfig.Sink` / `DebugLogPayloads` not plumbed | RESOLVED | `9e7fa45` | passed into `logging.NewLogger` |
| S-14.2 | `fileSink` per-Emit alloc | RESOLVED | `9e7fa45` | inner `WriterSink` pre-constructed |
| S-14.3 | Audit chain genesis literal hardcoded | RESOLVED | `9e7fa45` | `WriterOptions.GenesisLiteral` |
| S-14.4 | Audit Emit advisory_lock + LastChainHash + INSERT throughput cap | RESOLVED | `9e7fa45` | doc'd as design constraint |
| S-14.5 | `canonicalNumber` non-IEEE754 round-trip | RESOLVED | `9e7fa45` | `strconv.AppendFloat('g', -1, 64)` |
| S-14.6 | Correlation ID no length cap / charset | RESOLVED | `9e7fa45` | `MaxCorrelationIDLen=128`, allow-list |
| S-14.7 | Logging PHI list narrow / case-sensitive | RESOLVED | `9e7fa45` | expanded list, case-insensitive |
| S-14.8 | tracing span values not redacted | RESOLVED | `9e7fa45` | `tracing.SafeAttribute` |
| S-14.9 | OTLP exporter no timeout / TLS / Insecure knob | RESOLVED | `9e7fa45` | timeout (10s), TLSConfig, Headers, Insecure |
| S-15.1 | `config.Start` ignores ctx | RESOLVED | `081200d` | refuses with wrapped `ctx.Err()` |
| S-15.2 | `loader` env-var collisions silent | RESOLVED | `081200d` | `EnvCollisions` exposes ambiguity |
| S-15.3 | redaction walker has no depth cap | RESOLVED | `081200d` | `MaxRedactDepth=256` |
| S-15.4 | `${file:...}` reads have no size cap | RESOLVED | `081200d` | `io.LimitReader` + `ErrSecretFileTooLarge` |
| S-15.5 | effective_store has no bounded notification pool | RESOLVED | `081200d` | `MaxConcurrentNotifications=32` + recover |
| S-15.6 | reload `splitPath` doesn't honour `\.` escape | RESOLVED | `081200d` | escapes for `\.` and `\\` |
| S-15.7 | sequencer `resultsMu` race on deadline path | RESOLVED | `081200d` | mutex-serialized writes |
| S-15.8 | `isDeadlineExceeded` bare comparison | RESOLVED | `081200d` | `errors.Is` against DeadlineExceeded + Canceled |
| S-16.1 | Empty `internal/channels/` (plural) duplicate tree | RESOLVED | `1ffc778` | tree deleted |
| S-16.2 | Empty `internal/queue/`, `internal/wakeup/` packages | RESOLVED | — | audit was incorrect — no such packages exist; real ones live at `internal/infra/queue/` and `internal/infra/wakeup/` |
| S-16.3 | Empty `internal/adapters/`, `internal/adapterspi/` trees | RESOLVED | `2a0bbc2`, `060d538` | deleted; production uses singular `internal/adapter/` |
| S-16.4 | Empty `internal/domain/` packages | RESOLVED | `eea803a` | tree deleted |
| S-16.5 | `internal/infra/lifecycle/lifecycle.go` dead `_ = errors.New` | RESOLVED | `ed87e64` | sentinel and unused import removed |
| N-1.1 | `HasScope` is O(n); cache as `map[string]struct{}` | RESOLVED | `a2318e9` | already addressed under S-3.5 at audit time |
| N-1.2 | `equalJSON` swallows unmarshal errors | RESOLVED | `6649d9b` | falls back to `bytes.Equal` on parse failure; test `TestN1_EqualJSONMalformedFallsBackToBytes` |
| N-1.3 | `crypto/rand.Read` failure no `rand_failures_total` | RESOLVED | `6649d9b` | new `RandFailureRecorder` interface + `Metrics.RandFailuresTotal` |
| N-1.4 | `NotFound`/`MethodNotAllowed` rely on upstream auth wiring | DEFERRED | — | Reclassified — chi.Middleware-typed Auth wiring touches every wiring caller |
| N-1.5 | email Subject CRLF strip | RESOLVED | `2c8e258` | flows through `stripCRLF` before write; test `TestN1_BuildMIMEStripsCRLFFromSubject` |
| N-1.6 | `formatTraceparent` doesn't validate hex | RESOLVED | `2c8e258` | drops non-hex bytes pre-pad; test `TestN1_FormatTraceparentEmitsHexOnly` |
| N-1.7 | WS ack handling no eventNumber-in-sent-set check | RESOLVED | `79921ca` | `deliverAck` returns sent-set hit; emits `MetricUnknownAckTotal` on rogue acks |
| N-1.8 | WS Deliver / Close race documentation | RESOLVED | `79921ca` | `Deliver` doc-block now spells out concurrency contract |
| N-1.9 | message channel `wrapInMessageBundle` non-deterministic | RESOLVED | story/59 | Typed structs (`outerBundle`, `messageHeader` family) + `json.RawMessage` inner-entry passthrough + injectable `Options.Clock`/`Options.NewID`; ADR 0011 records the JCS-vs-typed-struct trade-off; tests `TestMessageBundleDeterminism*` (100 wraps byte-identical) + e2e `TestE2E_Message_BundleBytesDeterministic` |
| N-1.10 | `ComputeBackoff` doubling-loop iteration cap | RESOLVED | `79921ca` | `maxBackoffDoublingSteps = 64` constant; test `TestN1_ComputeBackoffIterationCap` |
| N-1.11 | Channel setup-error retries forever | RESOLVED | `ffe847b` | already retried under `RetryConfig.MaxAttempts` budget; doc-block updated. Reclassified follow-up: classify-as-permanent on N consecutive setup errors |
| N-1.12 | `pending_kind` enum reuses `create` for "held replacement" | RESOLVED | `ffe847b` | known schema constraint; rename is a migration outside polish scope |
| N-1.13 | adapter_id × resource_type × change_kind 3-way label cardinality | RESOLVED | `ffe847b` | cardinality contract documented in `metrics.go`; all three are deployment-bound closed sets |
| N-1.14 | translate has no charset normalization contract | RESOLVED | `ffe847b` | adapters MUST transcode to UTF-8 before calling translate |
| N-1.15 | matcher backoff has no `matcher_backoff_seconds` gauge | RESOLVED | `ffe847b` | new `SetBackoffReporter` optional gauge; test `TestN1_SetBackoffReporterStoresAndUnsetsCallback` |
| N-1.16 | matcher `committed=true` lies about rollback path | RESOLVED | `ffe847b` | renamed `committed` → `txDone` |
| N-1.17 | Catalog immutability contract undocumented | RESOLVED | `ffe847b` | Catalog immutability + RawJSON lifetime contracts spelled out in type doc |
| N-1.18 | Catalog `RawJSON` in-memory copy of every body | RESOLVED | `ffe847b` | RawJSON lifetime documented as intentional; lazy-load is follow-up |
| N-1.19 | Override-shadow has no structured log | RESOLVED | `ffe847b` | new `Override.LogFields()` returns structured map |
| N-1.20 | MLLP `readBuf` 8192 hardcoded; per-Read alloc | RESOLVED | `79921ca`, `ffe847b` | `ListenerConfig.ReadBufBytes` knob (default 8192) |
| N-1.21 | MLLP body double-copy | RESOLVED | `ffe847b` | `Body: append([]byte(nil), body...)` removed |
| N-1.22 | MLLP write deadline 2s hardcoded | RESOLVED | `79921ca` | `ListenerConfig.AckWriteTimeout` (default 2s) |
| N-1.23 | `scanEndPair` is O(n) per Append | RESOLVED | `ffe847b` | `closedScanned` offset + windowed `scanEndPairRange` helper |
| N-1.24 | MLLP listener `time.After` timer leaks on shutdown | RESOLVED | `79921ca` | `time.NewTimer + defer Stop` |
| N-1.25 | MLLP no PROXY protocol v2 | DEFERRED | — | Reclassified — requires new dependency (proxyproto) and config surface |
| N-1.26 | sequencer `time.After` leaks on early phase return | RESOLVED | `ffe847b` | probeWindow path now uses `time.NewTimer + Stop` |
| N-1.27 | `bytesEqual` reinvented; use `bytes.Equal` | RESOLVED | `c1ff9eb` | helper deleted; switched to `bytes.Equal` |
| N-1.28 | Audit sink failure has no buffer/queue | RESOLVED | `c1ff9eb` | `Writer.Emit` doc-block spells out fail-open sink semantics; durable row is the source of truth |
| N-1.29 | `Event.Payload` taken by reference; no defensive copy | RESOLVED | `c1ff9eb` | `Writer.Emit` takes shallow copy at entry; test `TestN1_EmitDefensiveCopiesPayload` |
| N-1.30 | pgstore advisory lock id is FNV truncation | RESOLVED | `c1ff9eb` | `AuditChainAdvisoryLockID` int64 constant published; FNV-1a runtime hash removed |
| N-1.31 | codec envelope format hardcoded `0x01` | RESOLVED | `ffe847b` | envelope format byte `0x01` documented; Decrypt error already includes observed format byte |
| N-1.32 | deliveries `ORDER BY next_attempt_at` no tiebreaker | RESOLVED | `79921ca` | `ORDER BY next_attempt_at ASC, id ASC` |
| N-1.33 | `subscription_topics.ListByStatus` no LIMIT | RESOLVED | `79921ca` | `DefaultListByStatusCap = 1000`; new `ListByStatusPage(limit, offset)` |
| N-1.34 | claim FOR UPDATE / SKIP LOCKED substring match fragile | RESOLVED | `79921ca` | `stripSQLCommentsAndStrings` before substring match |
| N-1.35 | partition `Run` ignores reload changes | RESOLVED | `ffe847b` | re-reads `cfg.RunInterval` and `cfg.TickTimeout` every iteration |
| D-1 | Production binary loads an empty catalog | RESOLVED | `3d0945f` | `topics.catalog_dir` config block walks operator JSON files at startup; SIGHUP-driven hot reload via `lcMod.SetReloadHandler` swaps the `AtomicCatalogProvider`. |
| D-2 | rest-hook channel handshake is a placeholder | RESOLVED | `3d0945f` | `restHookActivator` POSTs a synthetic FHIR R5 handshake Bundle to the subscriber endpoint; 2xx → `HandshakeSucceeded`, anything else (non-2xx, dial error, timeout, scheme rejection) → `HandshakeFailed`. |
| D-3 | pgxpool startup ping retries past `pingCtx` in some paths | RESOLVED | `3d0945f` | `buildPoolConfig` calls `pgxpool.ParseConfig` and overrides `ConnConfig.ConnectTimeout` (default 5s) so per-attempt dials honor the operator-supplied bound. |
| D-4 | Adapter version pin error path uses generic Go error | RESOLVED | `3d0945f` | `formatRunError` switches on `*registry.UnknownAdapterError` and emits a structured operator-facing line carrying the requested id and bundled list. |

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

#### B-4: Production `cmd/fhir-subs/run.go` never calls `handlers.RegisterRoutes` — RESOLVED in commits 192ab8e (scaffolding) + 2e28aa8 / c1e5b41 / 76f92bb / 743cddc / 0a5e3bc / b6004e7 (full wiring)
- **File:** `cmd/fhir-subs/run.go`, new `cmd/fhir-subs/wiring.go`
- **What:** The HTTP server in run.go served only the probe mux; `handlers.RegisterRoutes` (the real subscription API) was invoked only from tests / e2e harness.
- **Why it matters:** The binary literally did not serve the FHIR subscription endpoints. A "real-world deployment" was impossible.
- **Fix:** Constructed the full router (subscriptions, $get-ws-binding-token, $events, $status, /metadata) inside run.go and mount it; gate behind auth + observability middleware.
- **Resolution:** Lands across two pieces. The scaffolding (192ab8e) replaced the hardcoded probe mux with the lifecycle-module-driven probe handlers. The B-4-full wiring (this branch) introduces `cmd/fhir-subs/wiring.go::buildProductionRuntime` which:
  1. Opens a `pgxpool.Pool` against `database.url` and runs the embedded migrations under a 60s budget (fails-loud on either step).
  2. Builds an AES-GCM `codec.Codec` from the configured `codec.keys` versioned bundle, requiring `codec.active_key_version`.
  3. Loads the configured adapter via `internal/adapter/registry` (default reference adapter is registered in-tree).
  4. Constructs the full SMART Backend Services `auth.Verifier` and `auth.TokenEndpoint` against the `auth_clients` repo when `auth.audience` is set; auth disabled (probe-only mode) when `auth.audience=""`.
  5. Builds the `internal/channel/resthook` channel and registers it under the scheduler's `ChannelRegistry`.
  6. Constructs `handlers.Deps` against pg-backed stores (subscriptions, topics, events, deliveries, ws-tokens, audit) and calls `handlers.RegisterRoutes` on a chi router; mounts the verifier middleware when auth is enabled.
  7. When `mllp.listeners[]` is configured, starts `internal/mllp.Listener` against a pgx-backed `Persister` that goes through `repos.Hl7MessageQueueRepo` (so raw_body lands encrypted under the same codec).
  8. Launches the four pipeline workers (`hl7processor`, `matcher`, `submatcher`, `scheduler`) with config-driven `claim_batch_size` and `idle_poll_interval` per stage.
  9. Registers shutdown hooks against the lifecycle module: `mllp.stop_accepting` (PhaseStopAccepting), `pipeline.drain` and `api.activations.drain` (PhaseDrainInFlight), and `database.close` (PhaseCloseConnections).
  10. Registers a `database` readiness check that pings the pool with a 2s budget on every `/readyz` probe.
  Probe-only fallback (no `database.url`) keeps the legacy `/metadata` stub so the existing CMD smoke tests continue to function.
- **E2E coverage:** Four new `e2e/orchestrator/prod_binary_*_test.go` scenarios prove the wiring against a real Postgres testcontainer and the production binary (built by `go build` and launched via `exec.Command`):
  - `prod_binary_serves_subscription_api_test.go` — POST /Subscription/ against the binary, assert it does NOT return 404 (i.e., the route is mounted; without an auth token the handler returns 401 with an OperationOutcome — that is the expected post-wiring shape).
  - `prod_binary_processes_hl7_message_test.go` — drives an HL7 v2 message through the binary's MLLP listener; asserts hl7_message_queue, resource_changes, ehr_events flow. Catalog-driven matching is not exercised here (the binary's catalog is empty by default; topic loading is a follow-up).
  - `prod_binary_db_unreachable_test.go` — points the binary at an unreachable Postgres; asserts the binary fails and never binds the listener.
  - `prod_binary_graceful_shutdown_test.go` — SIGTERM mid-flight; asserts /readyz reports `shutting_down`.
- **Discovered during wiring:** see the `## Discovered during B-4 full wiring` section below for the new findings.

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

#### S-1: HTTP server / probe handler misuses — RESOLVED in merges `d027dab` / `8096936`
- **S-1.1 RESOLVED (47e6916)** `cmd/fhir-subs/main.go:138` — `applySets` errors print raw error including `--set key=val` value verbatim; redact RHS before formatting.
- **S-1.2 ALREADY DONE (3a81559)** `cmd/fhir-subs/main.go:152` — `signal.NotifyContext` registers SIGTERM/SIGINT only (parallels B-35). Lifecycle module's signal dispatcher (B-35) handles SIGHUP separately; cmd-level NotifyContext correctly does NOT cancel ctx on SIGHUP.
- **S-1.3 RESOLVED (ccdc7a1)** `cmd/fhir-subs/main.go:109-160` — no top-level `defer recover()` over realMain; a panic in startup or in the HTTP serve goroutine crashes without a structured log/correlation-id.
- **S-1.4 RESOLVED (35d57e1)** `cmd/fhir-subs/run.go:50` — JSON logger writes to caller-supplied `io.Writer` (stderr in main); observability logger from `internal/infra/observability` is bypassed entirely.
- **S-1.5 RESOLVED (dbc8356)** `cmd/fhir-subs/run.go:117-122` — `srv.Close()` error after `srv.Shutdown` failure is dropped.
- **S-1.6 ALREADY DONE** `cmd/fhir-subs/run.go:131-133` — shutdown wait is `ShutdownGracePeriod + 2s`; magic 2s slack. Removed by an earlier B-1 / B-3 wiring commit; current code uses just `ShutdownGracePeriod`.
- **S-1.7 RESOLVED (a9f96f7)** `cmd/fhir-subs/probes.go:91-108` — `/metadata` returns OperationOutcome stub. Production wiring should mount the new `handlers.RegisterPublicRoutes` for `/metadata` (CapabilityStatement) outside auth; the cmd's stub remains as a fallback when handlers aren't wired yet.
- **S-1.8 RESOLVED (6807f87)** `cmd/fhir-subs/config.go:214` (default `Bind: 0.0.0.0:8443`), `cmd/fhir-subs/run.go:161-167` (warn-log on wildcard+insecure), `cmd/fhir-subs/run.go:286-288` (`isWildcardBind` predicate) — defaults bind `0.0.0.0:<port>` with no loopback opt-in path. Default kept (backwards-compat); a warn-level log line now fires whenever the listener binds wildcard AND `insecure=true`. The audit's original pointers to `defaults.go` / `metrics.go` under `cmd/fhir-subs/` are stale — those files don't exist; the actual fix lives in `config.go` (default value) and `run.go` (warn-log wiring).

#### S-2: API / handlers — RESOLVED in merge `d027dab` (5 sub-bullets explicitly DEFERRED — cross-package storage refactor)
- **S-2.1 RESOLVED (a9f96f7)** `internal/api/handlers/router.go:107-109` — `/metadata` is mounted inside auth middleware; FHIR conformance probes hit it unauthenticated. New `RegisterPublicRoutes` exposes a pre-auth `/metadata` mount; production wiring should use it.
- **S-2.2 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:104, 384` — body size limit hardcoded `1<<20`; no shared config knob. New `Deps.MaxBodyBytes` (default 1 MiB); oversize bodies now answer 413.
- **S-2.3 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:113, 391, 397` — schema-validation error from json-schema library returned verbatim to client; cap length and stabilize wording. New `Deps.MaxSchemaErrorBytes` caps diagnostics.
- **S-2.4 DEFERRED (out of scope)** `internal/api/handlers/subscription_handlers.go:140-152` — If-None-Exist evaluates O(N) `ListByClient` per POST; push predicate into SQL. Requires storage repo / `SubscriptionsStore` interface change; tracked under separate work because it touches packages outside this branch's scope.
- **S-2.5 RESOLVED (caa68a4)** `internal/api/handlers/subscription_handlers.go:170-174` — channel registry plain-map read across goroutines; doc-comment now specifies the registry is constructed once and treated as immutable; mutation after `RegisterRoutes` is undefined.
- **S-2.6 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:198, 375-382` — ETag is the resource id (not a version) and `If-Match` accepts unquoted form; lost-update cannot be detected. Update handler now requires the weak-tag form `W/"<id>"`.
- **S-2.7 RESOLVED (a9f96f7)** `internal/api/handlers/subscription_handlers.go:205-227` — six `_ = err` swallows in `activate`; no logger; DB/audit/channel failures invisible. New `Deps.Logger` (nil-safe) routes the per-call DB / audit / channel errors to a structured log line.
- **S-2.8 DEFERRED (out of scope)** `internal/api/handlers/subscription_handlers.go:316-346` — `searchSubscriptions` has no pagination. Requires `SubscriptionsStore` interface to grow page/cursor params; tracked separately.
- **S-2.9 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:551, 629-635, 642` — magic timestamp format repeated 5×; emits `+00:00` not `Z`. Now use single package constant `instantFormat` with millisecond precision and `Z` suffix.
- **S-2.10 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:617-618` — `since, _ := strconv.ParseInt(...)` discards parse errors. New `parseEventNumberParam` answers 400 for malformed / negative values.
- **S-2.11 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:558-589` — `$status` bulk has no cap on `id` query params. New `Deps.MaxStatusBulkIDs` (default 256); over-cap requests return 400.
- **S-2.12 RESOLVED (a9f96f7)** `internal/api/handlers/subscription_handlers.go:786-805` — `buildSubscriptionStatus` uses `context.Background()` for DB read. Threaded `r.Context()` through every caller.
- **S-2.13 RESOLVED (4743ce7)** `internal/api/handlers/subscription_handlers.go:898-899` — `fhirVersion` hardcoded `"5.0.0"`. Now sourced from `Deps.FHIRVersion` (default `5.0.0`).
- **S-2.14 DEFERRED (out of scope)** `internal/api/handlers/pg_stores.go:43-188` — no per-query deadline. Requires production wiring (B-4) to fully land before per-query timeouts can be tuned end-to-end.
- **S-2.15 DEFERRED (out of scope)** `internal/api/handlers/pg_stores.go:159-188` — `$events` replay capped at hardcoded `LIMIT 1000` with no client signal of truncation. Surfaces best after S-2.8 pagination lands.
- **S-2.16 DEFERRED (out of scope)** `internal/api/handlers/pg_stores.go:251-263` — `Hash: []byte{0}` placeholder; production path silently has no hash-chain integrity. Replaced by the observability/audit hash-chained store under B-4 production wiring.
- **S-2.17 RESOLVED (a9f96f7)** `internal/api/handlers/tracing.go:32, 44` — unvalidated `X-Correlation-ID` reflected into spans / outbound headers. `correlation.ExtractFromHeaders` now drops non-UUID values and generates a fresh one.
- **S-2.18 RESOLVED (a9f96f7)** `internal/api/metrics/metrics.go:181-183` — `/metrics` has no auth. New `metrics.AuthGuard(bearer)` middleware factory; production deployment wraps `/metrics` with this.
- **S-2.19 RESOLVED (a9f96f7)** `internal/api/metrics/metrics.go:208, 217-227` — `routePattern` falls back to `r.URL.Path` for unmatched routes. Now returns the constant label `<unmatched>`.
- **S-2.20 RESOLVED (caa68a4)** validator lives in `internal/infra/observability/metrics/metrics.go:309-338` (`validateLabels`), not in `internal/api/metrics/metrics.go` as the original audit pointer suggested. Histograms previously had no bucket-count cap and the validator only caught `subscription_id` and `peer_addr`; the validator now rejects `endpoint`, `topic_url`, `client_id`, `correlation_id`, and `actor_id` as labels everywhere with the rationale doc'd inline.

#### S-3: Auth — RESOLVED in merge `1078e41` (1 sub-bullet DEFERRED — handlers rate-limit lives in different package)
- **`internal/api/auth/token_endpoint.go:106-108`** — 60s `ClockSkew` default is generous; widens replay window. Configurable; document <30s prod recommendation. — **RESOLVED** (`a2318e9`) — default lowered to 30s; field still configurable via `TokenEndpointConfig.ClockSkew`.
- **`internal/api/auth/token_endpoint.go:289-294`** — no rate limit on token endpoint; bursts of bogus assertions DoS the auth path. — **RESOLVED** (`a2318e9`) — per-source-IP token-bucket via `TokenEndpointConfig.RateLimitPerSource`; emits 429 + Retry-After. e2e: `auth_rate_limit_test.go`.
- **`internal/api/handlers/subscription_handlers.go`** (POST /Subscription, $get-ws-binding-token) — no per-client rate limit on subscription creates / WS binding token mints. — **DEFERRED** — out of scope for the auth/channels worktree (lives in `internal/api/handlers/`); the auth package now exports the `RateLimit` primitive a future handlers MR can plug into a chi middleware.
- **`internal/api/auth/token_endpoint.go:232`** — `exp, _ := claimToTime(...)` discards parse error; on zero-time `Put`, JTI replay protection silently disabled for that token. — **RESOLVED** (`a2318e9`) — fail-closed; returns 401 malformed and never Puts a zero-time JTI.
- **`internal/api/principal.go:22-28`** — `HasScope` is O(n); minor. — **RESOLVED** (`a2318e9`) — switched to a sync.Once-guarded set; lookups O(1). Unit test: `principal_scope_set_test.go`.

#### S-4: Channels — rest-hook — RESOLVED in merge `1078e41`
- **`internal/channel/resthook/resthook.go:144-161`** — default `*http.Client` has no `Timeout`; only context deadline protects calls. Set `c.http.Timeout`. — **RESOLVED** (`a2318e9`) — default client now carries `Timeout=RequestTimeout` so header-drip subscribers cannot tie up workers past their envelope deadline.
- **`internal/channel/resthook/resthook.go:148-161`** — `MaxIdleConnsPerHost`/`MaxConnsPerHost` hardcoded; no `TLSClientConfig` knob; no min-version pin. — **RESOLVED** (`a2318e9`) — exposed via `Options.MaxIdleConnsPerHost`, `MaxConnsPerHost`, `TLSMinVersion` (defaults TLS 1.3).
- **`internal/channel/resthook/resthook.go:213-217`** — no enforced max bundle size; `payload=full-resource` with embedded base64 sends MB per attempt × retries. — **RESOLVED** (`a2318e9`) — `Options.MaxBundleBytes` (default 8 MiB) refuses oversize bundles before any I/O. e2e: `channels_resthook_hardening_test.go`.
- **`internal/channel/resthook/resthook.go:285-291`** — `allowSubscriberHeader` is default-permit; allowlist lookup is dead code; subscribers can forge `X-Internal-Trust`, `X-Auth-User`, etc. — **RESOLVED** (`a2318e9`) — defense-in-depth deny-list expansion (X-Internal-*, X-Auth-*, X-Trusted-*, X-Real-IP, etc., plus deny-prefixes `x-internal-`, `x-trusted-`, `x-auth-`); legitimate FHIR headers (Prefer, If-Match, etc.) still pass.
- **`internal/channel/resthook/resthook.go:368-374`** — NXDOMAIN classified as `PermanentFailure`; transient DNS conditions get dead-lettered immediately. — **RESOLVED** (`a2318e9`) — DNS errors all classified Transient; scheduler retry budget is the right backstop.
- **`internal/channel/resthook/resthook.go:434-441`** — `readBodyExcerpt` reads up to 256B of subscriber 4xx response into `out.Reason` which is logged; PHI may leak via redaction-bypass. — **RESOLVED** (`a2318e9`) — `Options.IncludeResponseBodyExcerpt` opt-in; default OFF. e2e asserts no PHI leakage at default.

#### S-5: Channels — message — RESOLVED in merge `1078e41`
- **`internal/channel/message/message.go:152-175`** — same default-client-no-Timeout as resthook. — **RESOLVED** (`a2318e9`) — mirror of S-4 fix.
- **`internal/channel/message/message.go:264-266`** — non-`fhir+json` content type fails at delivery time as PermanentFailure rather than being rejected at subscription create. — **RESOLVED** (`a2318e9`) — `Channel.ValidateContentType` exposed for callers (API layer) to reject at subscription-create boundary. e2e: `channels_message_hardening_test.go`.
- **`internal/channel/message/message.go:319`** — `Bundle.timestamp` uses `time.RFC3339` (second precision); FHIR `instant` expects sub-second. — **RESOLVED** (`a2318e9`) — outer Bundle.timestamp serialized with `time.RFC3339Nano`.
- **`internal/channel/message/message.go:359-374`** — same default-permit allowlist semantics as resthook. — **RESOLVED** (`a2318e9`) — mirror of the S-4 deny-list expansion.

#### S-6: Channels — email — RESOLVED in `972dda7` (RED tests `5d28940`; e2e in `c8e428e`)
- **`internal/channel/email/email.go:548-555`** — uses `dialer.Deadline` from ctx but never sets `dialer.Timeout`; stdlib `Deadline` is absolute time so OK in practice but fragile.
  - **Resolved:** dialer.Timeout now bound to `RequestTimeout`, narrowed by ctx.Deadline when present. Post-dial `conn.SetDeadline` falls back to RequestTimeout when ctx has no deadline, so an envelope with no Deadline still bounds connect + EHLO + STARTTLS + AUTH chatter. Test: `TestS6_DialerHasTimeoutEvenWithoutDeadline`.
- **`internal/channel/email/email.go:574-595`** — no metric when STARTTLS-Preferred fallback to plaintext occurs; operator has no compliance signal.
  - **Resolved:** new `MetricSTARTTLSOutcomeTotal = "fhir_subs_channel_email_starttls_outcome_total"` with labels `{channel, policy, upgraded}`. `dial()` returns the upgrade outcome; the channel emits the counter on every dial. Operators alert on `rate(...{policy="preferred",upgraded="false"}) > 0`. Tests: unit `TestS6_STARTTLSPreferredFallbackMetric` / `TestS6_STARTTLSPreferredUpgradedMetric`; e2e `TestE2E_Email_STARTTLSPreferredFallback_EmitsMetric`.
- **`internal/channel/email/email.go:707-735`** — `smtpErrorCode` parses error strings via custom byte loop with dead `smtpErr` interface lines; use `errors.As(err, new(*textproto.Error))`.
  - **Resolved:** `smtpErrorCode` / `smtpErrorMessage` now use `errors.As(err, &perr)` against `*textproto.Error`. Removed the dead `smtpErr` interface, the dead Error()-string parser, and `isDigit`. A stringy "451 graylist" error returns 0 (not a protocol code) — the prior parser would mis-classify it. Test: `TestS6_SMTPErrorCodeFromTextprotoError` covers direct, `errors.Join`-wrapped, and non-textproto cases.
- **`internal/channel/email/email.go:298-300, 563-565`** — `Close()` errors silently swallowed.
  - **Resolved:** deferred `client.Close` in `deliverInner` and intermediate Close calls in `dial` log non-benign errors via `slog.WarnContext`. `isBenignCloseErr` filters nil and "use of closed network connection"; EOF, broken pipe, and i/o timeout are surfaced. Tests: `TestS6_BenignCloseErrFiltering`, `TestS6_CloseErrorsAreLogged`. Also removed the unused `dialFunc` test seam — `defaultDial` is now a method `(*Channel).dial` that captures metrics + logger directly.

#### S-7: Channels — websocket — RESOLVED in `4775357` (RED tests `b119f94`; e2e in `c8e428e`)
- **`internal/channel/websocket/websocket.go:113-129`** — `sessions` map has no upper bound, no per-client cap.
  - **Resolved:** `Options.MaxSessions` (default 50000) bounds the channel-wide session map; `Options.MaxSessionsPerClient` (default off) caps concurrent sessions per ClientID. Both reject at bind with a bind-error frame and emit `fhir_subs_channel_websocket_bind_rejected_total{reason}`. Tests: unit `TestS7_MaxSessionsCap`, `TestS7_MaxSessionsPerClientCap`; e2e `TestE2E_WebSocket_MaxSessionsCapRejectsBind`.
- **`internal/channel/websocket/websocket.go:213-228`** — no body-size limit / read-header-timeout on the upgrade handler; slowloris on handshake.
  - **Resolved:** new `Channel.ConfigureServer(s *http.Server)` applies `Options.UpgradeReadHeaderTimeout` (default 5s) to the host's `*http.Server`, bounding the handshake. The bind frame itself is read under a 4 KiB inbound limit so an oversize handshake is rejected before we trust the peer. Test: `TestS7_UpgradeReadHeaderTimeout`.
- **`internal/channel/websocket/websocket.go:236`** — `conn.SetReadLimit` never called; default 32KB inbound limit conflicts with `Options.MaxFrameBytes` (8MB) used outbound.
  - **Resolved:** `upgrade()` now calls `conn.SetReadLimit(max(MaxFrameBytes, defaultBindReadLimit=4KiB))`. After bind succeeds the limit is tightened to MaxFrameBytes so a deliberately tiny MaxFrameBytes (rare; mainly tests) does not block the bind frame. Test: `TestS7_SetReadLimitEnforced`.
- **`internal/channel/websocket/websocket.go:233-247`** — bind-read timeout hardcoded 10s.
  - **Resolved:** `Options.BindTimeout` (default 10s) replaces the hard-coded value. Test: `TestS7_BindTimeoutConfigurable`.
- **`internal/channel/websocket/websocket.go:391-408`** — pingLoop uses `context.Background()` parent; no Channel-level ctx; goroutine-leak window on hung Reads.
  - **Resolved:** `New()` owns a `context.WithCancel`; `pingLoop` and `readLoop` are bound to it so a `Close()` cancels in-flight pings/reads. The ping write itself uses a derived `context.WithTimeout(c.ctx, c.pingWriteTimeout)`. (Combined with the `WaitGroup` join, no goroutine survives `Close`.)
- **`internal/channel/websocket/websocket.go:399`** — single ping uses full `idleTimeout` (5min default); should use a short write timeout, separately track last-read for idle detection.
  - **Resolved:** `Options.PingWriteTimeout` (default 10s) is the wall-clock budget for a single ping write, replacing the prior "use IdleTimeout for the ping" anti-pattern. Test: `TestS7_PingWriteTimeoutConfigurable`.
- **`internal/channel/websocket/websocket.go:360-389`** — readLoop has no idle-timeout enforcement; documented "5min idle" is unimplemented.
  - **Resolved:** per-session `lastReadAtNS` is updated by `readLoop` on every received frame (atomic int64). The `pingLoop`'s ticker now also polices idle: when `now - lastReadAtNS > IdleTimeout` the session is closed with `StatusGoingAway` and `fhir_subs_channel_websocket_idle_closed_total` increments. Test: `TestS7_ReadLoopEnforcesIdleTimeout`.
- **`internal/channel/websocket/websocket.go:331-348`** — replay loop materializes the entire `ReplaySince` slice; client requesting replay-from-zero on a million-event subscription triggers OOM.
  - **Resolved:** `Options.MaxReplayEvents` (default 10000) caps the replay loop. After the cap the channel writes a JSON `replay-truncated` control frame `{type, reason, capped, missing}` and stops. Increments `fhir_subs_channel_websocket_replay_truncated_total`. Tests: unit `TestS7_ReplayCappedAtMaxReplayEvents`; e2e `TestE2E_WebSocket_ReplayCapTruncates`.
- **`internal/channel/websocket/websocket.go:486-500`** — `Close` doesn't `WaitGroup`-join per-session goroutines; non-deterministic shutdown.
  - **Resolved:** per-session goroutines (ping + read) are spawned under `c.wg`. `Close` cancels `c.ctx`, force-closes each conn, then `wg.Wait()` so callers can rely on no goroutine touching held state after Close returns. Test: `TestS7_CloseWaitsForGoroutines`. Also tightened `e2e/orchestrator/channels_websocket_ack_race_test.go` to call `ch.Close()` before sampling and poll for the minimum delta over a 2s window — the absolute "<= 8" delta was fragile under parallel test scheduling because `runtime.NumGoroutine` counts the entire process.

#### S-8: Scheduler — RESOLVED in merge `c1c6b32` (1 sub-bullet DEFERRED — DeliveriesRepo refactor)
- **`internal/engine/scheduler/worker.go:230-232`** — RESOLVED in `acd798d`: `Config.DispatchConcurrency` (default 1) bounds parallel dispatchOne calls per batch via a semaphore so one slow channel cannot head-of-line-block siblings.
- **`internal/engine/scheduler/worker.go:241-249`** — RESOLVED in `acd798d`: `ClassifyRequeueReason` + `ReasonSubscriptionUnavailable` / `ReasonEhrEventUnavailable` route not-found to dead-letter immediately. Pure DB load errors stay transient.
- **`internal/engine/scheduler/worker.go:269-278`** — RESOLVED in `acd798d`: `ClassifyBuildError` + `isPermanentBuildError` recognize deterministic build failures (nil id, decode focus, marshal status/bundle) and dead-letter immediately.
- **`internal/engine/scheduler/scheduler.go:60-62`** — RESOLVED in `acd798d`: `RetryConfig.PerChannel` map + `MaxAttemptsFor(channelType)` + `ClassifyOutcomeForChannel` give per-channel-type override of MaxAttempts.
- **`internal/engine/scheduler/scheduler.go:118-123`** — RESOLVED in `acd798d`: `MaxJitter=0.5` constant; `applyDefaults` now clamps Jitter to [0, 0.5] so the (1+offset) multiplier cannot approach zero.
- **`internal/engine/scheduler/worker.go:336-378`** — DEFERRED: inline UPDATE SQL in worker still present. The shared `applyBailoutDecision` consolidates the bail-out paths; full migration of the Decision SQL to the DeliveriesRepo is tracked under follow-up storage refactor.

#### S-9: Pipeline / MLLP / HL7 processor — RESOLVED in merge `c1c6b32` (2 sub-bullets DEFERRED — per-message frame deadline + persistCtx hardening; 1 PARTIALLY)
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

#### S-10: Matcher — RESOLVED in merge `c1c6b32` (1 sub-bullet PARTIALLY — applyDecision wiring tracked under storage refactor)
- **`internal/matcher/matcher.go:198-213`** — RESOLVED in `d3fad44`: `SetMalformedResourceReporter` + `reportMalformedResource("search_expression", err)` callback fires on json.Unmarshal failure. Behavior unchanged (still fail-closed); metric is opt-in via wiring.
- **`internal/matcher/matcher.go:255-273`** — RESOLVED in `d3fad44`: bare-clause path now also tries `equalsString` so the matcher matches submatcher.bareClause (S-10.2).
- **`internal/matcher/matcher.go:255`** — RESOLVED in `d3fad44`: `SetUnsupportedModifierReporter` + `reportUnsupportedModifier("in", parameter)` callback fires on the `:in` path.
- **`internal/matcher/matcher.go:466-479`** — RESOLVED in `d3fad44`: `parseFlexibleDateWithFlag` returns `(time, imputedTZ, ok)` so callers can metric when a non-RFC3339 input was silently coerced to UTC.
- **`internal/matcher/matcher.go:546`** — RESOLVED in earlier B-24 work: `unknownFHIRPathReporter` fires on every fail-closed FHIRPath evaluation.
- **`internal/matcher/matcher.go:610-638`** — PARTIALLY RESOLVED in `d3fad44`: `Config.MaxRowAttempts` (default 8) added to the Config surface; full applyDecision wiring (incrementing the counter on tx failure + dead-lettering at cap) tracked under storage refactor.

#### S-11: Topics catalog — RESOLVED in merges `c1c6b32` + story/54 (1 PARTIALLY — Prometheus wiring lives in callers)
- **`internal/topics/catalog/catalog.go:421-452`** — RESOLVED in `d3fad44` (defense-in-depth) + JSON schema (primary): `compileTrigger` rejects supportedInteraction values not in {create,update,delete}.
- **`internal/topics/catalog/catalog.go:374-382`** — RESOLVED in `d3fad44`: `Topic.EventCodings []EventCoding` now carries (system, code) pairs alongside the legacy code-only `EventCodes`. Callers that need cross-system disambiguation read EventCodings.
- **`internal/topics/catalog/catalog.go:655-672`** — RESOLVED in story/54 (S-11.3): `compileOne` now rejects topics that declare more than one `notificationShape` entry. Reason text includes the topic URL and the phrase `multi-entry notificationShape` so operators see the failure during deploy rather than receiving incorrect Bundles at runtime. Per-entry compile end-to-end remains future-work; today's builder honors a single shape only.
- **`internal/topics/catalog/catalog.go`** — PARTIALLY RESOLVED in earlier B-25 work: `Catalog.Rejected()` and `Catalog.Overridden()` expose the diagnostic surface; Prometheus `topics_rejected_total{origin,reason}` / `topic_overridden_total{from,to}` are wired in callers, not this package.

#### S-12: Engine / submatcher / builder — RESOLVED in merge `c1c6b32` (3 sub-bullets DEFERRED — repo pagination + fanout batching + API-layer fhir+xml rejection)
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

#### N-1: Polish — RESOLVED 2026-06-18 (commits c1ff9eb, 6649d9b, 2c8e258, 79921ca, ffe847b)

- **ALREADY RESOLVED (a2318e9)** `internal/api/auth/principal.go:22-28` — `HasScope` switched to a `sync.Once`-built `map[string]struct{}` set; lookups O(1). Resolved during S-3 work.
- **RESOLVED (6649d9b)** `internal/api/handlers/subscription_handlers.go:480-490` — `equalJSON` no longer Marshal(nil)'s malformed inputs to "null"; falls back to `bytes.Equal` on parse failure. Test: `TestN1_EqualJSONMalformedFallsBackToBytes`.
- **RESOLVED (6649d9b)** `internal/api/handlers/subscription_handlers.go:680-686` — new `RandFailureRecorder` interface + `Metrics.RandFailuresTotal` counter; `$get-ws-binding-token` calls it on `crypto/rand.Read` failure.
- **DEFERRED (out of scope)** `internal/api/handlers/router.go:111-117` — chi.Middleware compile-time guarantee for auth requires changing the public Deps surface and is broader than polish; tracked for the next router refactor.
- **RESOLVED (2c8e258)** `internal/channel/email/email.go:392` — `Subject` header now flows through `stripCRLF` before write. Test: `TestN1_BuildMIMEStripsCRLFFromSubject`.
- **RESOLVED (2c8e258)** `internal/channel/resthook/resthook.go:421-431` — `formatTraceparent` drops non-hex bytes pre-pad, guaranteeing W3C-valid output. Test: `TestN1_FormatTraceparentEmitsHexOnly`.
- **RESOLVED (79921ca)** `internal/channel/websocket/websocket.go:381-388` — `deliverAck` now returns whether eventNumber was in the sent-set; readLoop emits new `MetricUnknownAckTotal` on rogue acks.
- **RESOLVED (79921ca)** `internal/channel/websocket/websocket.go:418-484` — `Deliver` doc-block now spells out the Deliver/Close concurrency contract (transient on race; underlying conn library guards double-close).
- **RESOLVED (story/59)** `internal/channel/message/message.go` — `wrapInMessageBundle` rewritten on typed structs (`outerBundle`, `messageHeader` family) with `json.RawMessage` inner-entry passthrough; `Options.Clock` and `Options.NewID` make non-determinism sources injectable. Tests: `TestMessageBundleDeterminism`, `TestMessageBundleDeterminismWithInnerID`, `TestMessageBundleDeterminismMapHeavyResource` (100 wraps byte-identical) + e2e `TestE2E_Message_BundleBytesDeterministic`. Trade-off recorded in [ADR 0011](high-level-design/decisions/0011-message-channel-determinism.md): typed-struct + raw-message passthrough rather than full JCS at the channel seam (JCS remains the choice at the hash/signature seam per ADR 0010 §3).
- **RESOLVED (79921ca)** `internal/engine/scheduler/scheduler.go:107-114` — `maxBackoffDoublingSteps = 64` constant explicitly caps the doubling loop. Test: `TestN1_ComputeBackoffIterationCap`.
- **DOCUMENTED (ffe847b)** `internal/engine/scheduler/worker.go:289-299` — already retried under `RetryConfig.MaxAttempts`; doc-block now states the budget is responsible. Classify-as-permanent on N consecutive setup errors is a follow-up.
- **DOCUMENTED (ffe847b)** `internal/hl7processor/processor.go:425-468` — pending_kind `create` overload documented as a known schema constraint; rename is a migration outside polish scope.
- **DOCUMENTED (ffe847b)** `internal/hl7processor/processor.go:614-635` — cardinality contract documented in `metrics.go`. All three label values are deployment-bound (closed sets); no runtime cap is needed because no user-supplied input flows in.
- **DOCUMENTED (ffe847b)** `internal/hl7processor/translate.go` — charset normalization contract spelled out: adapters MUST transcode to UTF-8 before calling translate.
- **RESOLVED (ffe847b)** `internal/matcher/matcher.go:572-588` — new `SetBackoffReporter` optional gauge; `Worker.Run` fires it on every transient retry and clears it on a healthy tick. Test: `TestN1_SetBackoffReporterStoresAndUnsetsCallback`.
- **RESOLVED (ffe847b)** `internal/matcher/matcher.go:670-679` — variable renamed `committed` → `txDone` to honestly describe both the commit and rollback paths.
- **RESOLVED (ffe847b)** `internal/topics/catalog/catalog.go:240-282` — Catalog immutability + RawJSON lifetime contracts now spelled out in the type doc; AtomicCatalogProvider's swap-pointer-on-reload semantics already explicit at its call sites.
- **DOCUMENTED (ffe847b)** `internal/topics/catalog/catalog.go:415-417` — RawJSON lifetime documented as intentional; lazy-load is a follow-up if a 10k+ topic deployment ever materializes.
- **RESOLVED (ffe847b)** `internal/topics/catalog/catalog.go:271-282` — new `Override.LogFields()` returns a structured map for slog/logr emitters so the wiring layer can emit one record per override.
- **RESOLVED (79921ca, ffe847b)** `internal/mllp/connection.go:119-145` — `ListenerConfig.ReadBufBytes` knob (default 8192). Per-read `append` is unchanged because the per-conn copy is required to ferry bytes across the goroutine boundary into `readResult.buf`; pooling buffers across connections is a separate optimization.
- **RESOLVED (ffe847b)** `internal/mllp/connection.go:296` — `Body: append([]byte(nil), body...)` removed; framer already returns a fresh slice.
- **RESOLVED (79921ca)** `internal/mllp/connection.go:430-435` — `ListenerConfig.AckWriteTimeout` (default 2s) replaces the inline hardcode; threaded through `writeACK` / `writeNACK`.
- **RESOLVED (ffe847b)** `internal/mllp/framer.go:225-236` — `closedScanned` offset added; new `scanEndPairRange(b, from, to)` windowed helper. Avoids O(n²) re-scan on pre-frame noise.
- **RESOLVED (79921ca)** `internal/mllp/listener.go:148-174` — `time.After` swapped for `time.NewTimer + defer Stop`.
- **DEFERRED — see `## Reclassified` below** `internal/mllp/endpoint.go:90` — PROXY protocol v2 requires an additional dependency (proxyproto.Listener) and a configuration surface; scope larger than N-1.
- **RESOLVED (ffe847b)** `internal/infra/lifecycle/sequencer.go:285-306` — probeWindow path now uses `time.NewTimer + Stop` to release the timer slot on early `parent.Done()`. (The deadline-path Timer was already correct from S-15.)
- **RESOLVED (c1ff9eb)** `internal/infra/observability/audit/audit.go:264-274` — `bytesEqual` reinvented helper deleted; switched to `bytes.Equal`.
- **DOCUMENTED (c1ff9eb)** `internal/infra/observability/audit/audit.go:298-339` — `Writer.Emit` doc-block now spells out fail-open sink semantics and the rationale (no internal queue; durable row is the source of truth).
- **RESOLVED (c1ff9eb)** `internal/infra/observability/audit/audit.go:57-65` — `Writer.Emit` now takes a defensive shallow copy of `evt.Payload` at entry. Test: `TestN1_EmitDefensiveCopiesPayload`.
- **RESOLVED (c1ff9eb)** `internal/infra/observability/audit/pgstore.go:43-44` — `AuditChainAdvisoryLockID` int64 constant published; FNV-1a runtime hash removed. Test: `TestN1_AuditChainAdvisoryLockIDIsDocumented`.
- **DOCUMENTED (ffe847b)** `internal/infra/storage/codec/codec.go` — envelope format byte `0x01` documented; existing Decrypt error already includes the observed format byte (`0x%02x`).
- **RESOLVED (79921ca)** `internal/infra/storage/repos/deliveries.go:53-95` — `ORDER BY next_attempt_at ASC, id ASC` adds a deterministic tiebreaker.
- **RESOLVED (79921ca)** `internal/infra/storage/repos/subscription_topics.go:60-87` — `ListByStatus` now bounded by `DefaultListByStatusCap = 1000`; new `ListByStatusPage(limit, offset)` for callers needing more.
- **RESOLVED (79921ca)** `internal/infra/storage/claim/claim.go:40` — `hasSkipLocked` now strips line/block comments and string literals via `stripSQLCommentsAndStrings` before substring match. Test: `TestN1_HasSkipLockedIgnoresCommentsAndStrings`.
- **RESOLVED (ffe847b)** `internal/infra/storage/partition/partition.go:33-65` — `Run` re-reads `cfg.RunInterval` and `cfg.TickTimeout` on every iteration so SIGHUP-driven reloads take effect.

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

---

## Discovered during B-4 full wiring

The full B-4 wiring exercise surfaced four new findings that exist *because* the production binary now actually constructs every dependency. None of them block deployment — the binary boots, serves API, handles MLLP, and shuts down cleanly — but they should be tracked.

### D-1 (RESOLVED 3d0945f): Production binary loads an empty catalog
- **File:** `cmd/fhir-subs/wiring.go::buildProductionRuntime` — was `catalog.Load(catalog.Sources{})`
- **What:** The matcher's `CatalogProvider` was initialized to an empty `*catalog.Catalog`. `subscription_topics` rows did nothing because no topic mapping was loaded.
- **Why it mattered:** Even with subscriptions in `active` and HL7 messages flowing, no `ehr_events` rows were produced — the pipeline silently halted at the matcher.
- **Resolution:** Added `topics.catalog_dir` to the typed config (`cmd/fhir-subs/config.go`); a new `loadTopicSources` helper (`cmd/fhir-subs/topics.go`) walks the dir non-recursively, loads every `*.json` as an `Operator`-precedence `RawTopic`, and the wiring pipes it into `catalog.Load`. SIGHUP routes through `lcMod.SetReloadHandler` to re-walk the dir and `Store()` a fresh catalog into the `AtomicCatalogProvider` (race-free; B-29 contract preserved). RED unit tests in `topics_d1_test.go` and `config_d1_test.go`; e2e harness coverage in `e2e/orchestrator/prod_binary_topics_catalog_d1_test.go` asserts both startup load and SIGHUP reload via captured stderr lines.

### D-2 (RESOLVED 3d0945f): rest-hook channel handshake is a placeholder
- **File:** `cmd/fhir-subs/wiring.go` — `rest-hook` slot in the `handlers.ChannelRegistry`
- **What:** The `ChannelActivator` registered for rest-hook was `defaultActivator{}`, a stub that always returned `HandshakeSucceeded`. The audit-log entry said "handshake.succeeded" even when the subscriber endpoint would 404.
- **Why it mattered:** A subscription created in production immediately flipped to `active` regardless of whether the subscriber endpoint actually existed or was reachable.
- **Resolution:** New `restHookActivator` (`cmd/fhir-subs/activators.go`) POSTs a synthetic FHIR R5 handshake Bundle (`SubscriptionStatus` with `type=handshake`) to `row.Endpoint` with `Content-Type: application/fhir+json`. 2xx → `HandshakeSucceeded`; non-2xx, dial error, timeout, scheme rejection → `HandshakeFailed`. Per-outcome counters (`successHits` / `failureHits`) surface for future metric wiring. Endpoint URL is redacted (userinfo + query stripped) before it lands in audit logs. Five RED unit tests in `activators_d2_test.go` cover 2xx success, 4xx failure, dial-error failure, handshake-bundle shape, and the `AllowHTTP=false` reject-before-dial branch. `websocket` and `email` keep the `defaultActivator` placeholder — see `docs/future-work.md` for tracking.
- **Test caveat:** Activator behavior is covered by unit tests; an end-to-end test that drives the activator via the production binary's `POST /Subscription` route requires a debug-mode auth seam (no audience → no principal → 401), so the API-driven e2e is deferred. The unit tests exercise the same code path the API uses (wired in `wiring.go`).

### D-3 (RESOLVED 3d0945f): pgxpool startup ping retries past pingCtx in some paths
- **File:** `cmd/fhir-subs/wiring.go::buildProductionRuntime` — Postgres pool construction
- **What:** When the configured Postgres was unreachable on a closed port, pgxpool's internal connect-retry loop occasionally outran the 5s `pingCtx`.
- **Why it mattered:** The diagnostic surfaced only after the lifecycle module's signal-driven shutdown phase rather than the `buildProductionRuntime` failure path. Diagnostic-latency only — not a correctness gap.
- **Resolution:** New `buildPoolConfig` helper (`cmd/fhir-subs/pool.go`) calls `pgxpool.ParseConfig`, overrides `ConnConfig.ConnectTimeout` with a 5s default (configurable in future), then constructs the pool via `pgxpool.NewWithConfig`. RED unit tests in `pool_d3_test.go` cover: explicit timeout injected, zero falls back to a positive default, malformed URL surfaced as a parse error.

### D-4 (RESOLVED 3d0945f): Adapter version pin error path uses generic Go error
- **File:** `cmd/fhir-subs/main.go::realMain` — was `fmt.Fprintln(stderr, "error: run:", err)`
- **What:** When the configured `adapter.id` was unknown, the binary failed with `error: run: production wiring: adapter load: registry: unknown adapter "X" (bundled: [default])`. The error chained through `fmt.Errorf` rather than the typed `*registry.UnknownAdapterError`, so an operator-facing tool that wanted to recommend the bundled list had to grep the message.
- **Why it mattered:** Operability — error messages were correct but not machine-readable.
- **Resolution:** New `formatRunError` helper (`cmd/fhir-subs/main.go`) `errors.As`-switches on `*registry.UnknownAdapterError` and emits a structured operator-facing line listing the requested id and the bundled list explicitly. Other errors fall back to the legacy `error: run: <err>` prefix. RED unit tests in `main_d4_test.go` cover the typed-error branch and the legacy fallback.

---

## Reclassified

Items originally captured under N-1 (NICE-TO-HAVE / Polish) that proved larger than the polish bar during the 2026-06-18 N-1 sweep. Each is genuinely follow-up work, not lost work.

- **N-1 → follow-up: chi.Middleware-typed Auth wiring** (`internal/api/handlers/router.go:111-117`). Compile-time guaranteeing `NotFound`/`MethodNotAllowed` run behind auth requires changing the public `Deps` surface so `RegisterRoutes` accepts a typed `chi.Middleware` parameter rather than relying on the call-site to wrap. Touches every existing wiring caller.

- **RESOLVED (story/59) — N-1.9: deterministic message-channel Bundle bytes** (`internal/channel/message/message.go`). `wrapInMessageBundle` rewritten on typed structs (`outerBundle`, `messageHeader` family) with `json.RawMessage` inner-entry passthrough; `Options.Clock` and `Options.NewID` make non-determinism sources injectable. The wire wrap is byte-stable for the same input — verified across 100 wraps in `TestMessageBundleDeterminism*` and `TestE2E_Message_BundleBytesDeterministic`. The JCS-vs-typed-struct trade-off is recorded in [ADR 0011](high-level-design/decisions/0011-message-channel-determinism.md): JCS stays at the hash / signature seam per ADR 0010 §3; the channel seam uses typed-struct serialization plus raw-message passthrough so adapter-produced inner resources are not re-shaped.

- **N-1 → follow-up: PROXY protocol v2 in MLLP listener** (`internal/mllp/endpoint.go:90`). Behind a Cloud Load Balancer that prepends `PROXY` headers, every accepted conn currently reports the LB's IP rather than the originating EHR. Fix is to wrap the listener in a `proxyproto.Listener` from `github.com/pires/go-proxyproto` and gate via a config knob; introduces a new dependency and config surface.

- **N-1 → follow-up: classify-as-permanent on N consecutive setup errors** (`internal/engine/scheduler/worker.go:289-299`). The current path retries setup errors through the standard `RetryConfig.MaxAttempts` budget; per-subscription same-error counting is a follow-up.
