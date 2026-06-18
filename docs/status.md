# Project Status — 2026-06-18 (refresh after P1 batch + P1.4 + D-1..D-4 merges)

Single-source-of-truth status for every tracked work item across the audit, future-work, and demo docs. Each row is verified against the code on `origin/main` (`a970f3d`); rows where doc and code disagree are surfaced in the **Discrepancies** section below.

## Headline

- **Code state:** the P1 batch (`f9fc4b9`), the P1.4 ICU folding fix (`3d4f810`), and the D-1..D-4 production-binary fixes (`a970f3d`) are now on main. `go build ./...` and `go vet ./...` are taken on faith from CI; the full `go test ./... -race -count=1` was not re-run for this snapshot but each merge ran it.
- **Doc trustworthiness:** the audit doc (`production-readiness-audit.md`) is materially accurate — 35/35 BLOCKER, 35/35 N-* polish, 4/4 D-* discovered, and the SHOULD-FIX cohort (~95%) all RESOLVED with verified pointers. The **future-work doc** is now closely aligned with shipped code: P1.2 (FHIRPath MVP), P1.3 (`:in` fail-loud at load), P1.4 (ICU folding), P1.6 (admin surface), P1.7 (CapabilityStatement enrichment), P1.9 (Sec-WebSocket-Protocol), P1.10 (manifest JSON Schema + URL collision), P1.11 (WSS bind token hashing) and P1.12 (dead-letter metric + runbook) all RESOLVED on main. Demo doc gaps 1, 2, and 7 RESOLVED.
- **Top items to land next** (impact-ordered):
  1. **P1.5 matcher metrics.** Re-implementation tracked under `bd` #180. Without it, operators have zero visibility into matcher throughput, slow topics, or fhirpath timeouts; production triage stays blind.
  2. **P2.4 R4B/R5 wire negotiation completeness** (`bd` #184). The handler exposes `SupportedFHIRVersions` in `CapabilityStatement` but the production wiring (`cmd/fhir-subs/wiring.go:201`) doesn't populate it; full Subscription R4B↔R5 conversion is also missing.
  3. **P2.6 Heartbeats and handshakes** (`bd` #186). Scheduler doesn't emit heartbeats; subscribers have no liveness signal.
  4. **P2.7 Auth re-check at delivery prep** (`bd` #187). `submatcher` has a `FanoutAuthRevoked` decision but no `AuthValidator.Recheck` SPI.
  5. **P2.8 OTel exporter recipe docs** (`bd` #188). Configuration surface is shipped (S-14.9, `9e7fa45`); deployment recipes for Datadog/Honeycomb/Jaeger remain unwritten.
  6. **P2.1 / P2.2 adapter framework workers** (`bd` #181, #182). FHIR Scan Runner and Vendor API Client SPIs exist; no production worker invokes them.

## Master Status Table

The single comprehensive view. `Source` is `audit` (production-readiness-audit.md), `future` (future-work.md), `demo` (subscription-sidecar-demo.md). `Documented Status` is what the source doc says today. `Verified Status` is what the code on origin/main shows; ✓ means doc and code agree, ✗ means they diverge, ⚠ means partial / nuanced.

| Source | ID | Title | Documented Status | Verified Status | Commit/Branch | Notes |
|--------|----|-------|-------------------|-----------------|---------------|-------|
| audit | B-1 | /readyz always 503 in production entry point | RESOLVED | RESOLVED ✓ | 35b2cea, 192ab8e (merged 8096936) | confirmed `cmd/fhir-subs/run.go:264` mounts lifecycle Probes().Readyz |
| audit | B-2 | HTTP server missing Write/Idle/MaxHeaderBytes | RESOLVED | RESOLVED ✓ | 35b2cea | confirmed `cmd/fhir-subs/run.go:138-142` and `config.go:182-194` |
| audit | B-3 | markStartupComplete fires before ready | RESOLVED | RESOLVED ✓ | 35b2cea, 192ab8e | gated on lifecycle Start success at run.go:179 |
| audit | B-4 | Production run.go never calls handlers.RegisterRoutes | RESOLVED | RESOLVED ✓ | 192ab8e + 2e28aa8/c1e5b41/76f92bb/743cddc/0a5e3bc/b6004e7 | `cmd/fhir-subs/wiring.go::buildProductionRuntime` lines 65-321 wire pool/codec/auth/handlers/MLLP/pipeline; D-1..D-4 are sub-findings |
| audit | B-5 | jwksCache map race | RESOLVED | RESOLVED ✓ | f786914 | sync.Mutex at internal/api/auth/token_endpoint.go:509 |
| audit | B-6 | Token endpoint missing body-size limit | RESOLVED | RESOLVED ✓ | c194ad3 | MaxTokenRequestBodyBytes |
| audit | B-7 | Missing jti silently accepted | RESOLVED | RESOLVED ✓ | 284da10 | rejected at token_endpoint.go:335 and verifier.go:264 |
| audit | B-8 | Raw JWT parser error in HTTP body | RESOLVED | RESOLVED ✓ | c544f01 | diagnosticForReason at token_endpoint.go:430 |
| audit | B-9 | JTI cache eviction broken | RESOLVED | RESOLVED ✓ | 708c2ad | |
| audit | B-10 | Fire-and-forget activation goroutines | RESOLVED | RESOLVED ✓ | a6e042f, 8c7db78 | activation.go::spawnActivate |
| audit | B-11 | SSRF on subscriber endpoint URL | RESOLVED | RESOLVED ✓ | 9689730, 16a1806 | url_validator.go |
| audit | B-12 | JWKS fetch unauthenticated/no body cap | RESOLVED | RESOLVED ✓ | 9045845 | |
| audit | B-13 | Audit log persists full request body | RESOLVED | RESOLVED ✓ | 25dffba, 759c3a8 | audit_redact.go |
| audit | B-14 | Email STARTTLS default Preferred | RESOLVED | RESOLVED ✓ | a6a36aa | default flipped to Required |
| audit | B-15 | SMTP PLAIN/LOGIN over plaintext | RESOLVED | RESOLVED ✓ | a6a36aa | refuses cleartext AUTH unless explicit opt-in |
| audit | B-16 | Email header injection via CorrelationID | RESOLVED | RESOLVED ✓ | a6a36aa | |
| audit | B-17 | WebSocket InsecureSkipVerify=true | RESOLVED | RESOLVED ✓ | a0360e4 | OriginPatterns default-deny |
| audit | B-18 | WebSocket ack-channel close-of-closed race | RESOLVED | RESOLVED ✓ | a0360e4 | sync.Once-guarded ackWaiter |
| audit | B-19 | MLLP listener missing connection caps | RESOLVED | RESOLVED ✓ | b8c1209 | MaxConnections + MaxConnectionsPerIP |
| audit | B-20 | MLLP listener missing TLS/mTLS | RESOLVED | RESOLVED ✓ | b8c1209 | |
| audit | B-21 | HL7 processor decrypts hardcoded key_version | RESOLVED | RESOLVED ✓ | 07d7be2, 6d0e5a2 | KeyVersion column read at processor.go:391, 571 |
| audit | B-22 | pending_pairs migration omits key_version | RESOLVED | RESOLVED ✓ | 07d7be2, migrations/0003 | |
| audit | B-23 | Matcher silent unknown search params | RESOLVED | RESOLVED ✓ | 3d80c7d, 04e2c36 | catalog rejects at load |
| audit | B-24 | FHIRPath runFHIRPath fail-OPEN | RESOLVED | RESOLVED ✓ | 51b8e53, a1f4b12 | matcher.go:561+ now fail-closed; P1.2 widened the MVP subset |
| audit | B-25 | Topic catalog rejections don't fail startup | RESOLVED | RESOLVED ✓ | 3d80c7d | LoadStrict + Override tracking |
| audit | B-26 | nextEventNumber race | RESOLVED | RESOLVED ✓ | f600d42, 1ba1c45 | submatcher/worker.go:365 uses subscriptions.next_event_number |
| audit | B-27 | Cursor monotonicity assumes no deletes | RESOLVED | RESOLVED ✓ | f600d42, 1ba1c45 | |
| audit | B-28 | Bundle JSON encoding nondeterministic | RESOLVED | RESOLVED ✓ | b76f1b0, 0b39e95 | struct-based encoding |
| audit | B-29 | CatalogProvider swap not torn-read-safe | RESOLVED | RESOLVED ✓ | 51b8e53, a1f4b12 | AtomicCatalogProvider |
| audit | B-30 | High-cardinality MSH-9 label | RESOLVED | RESOLVED ✓ | 7e797f3 | bucketMessageTypeLabel |
| audit | B-31 | Scheduler shutdown doesn't drain | RESOLVED | RESOLVED ✓ | 5017364, 945a160 | |
| audit | B-32 | Retention sweeper SQL injection / audit-chain DELETE | RESOLVED | RESOLVED ✓ | e697162, 52ed074 | allowedTargets whitelist; audit_log excluded |
| audit | B-33 | Multi-pod migration race | RESOLVED | RESOLVED ✓ | 52ed074 | pg_advisory_lock(0xFEEDFACE) + explicit @CONCURRENT directive |
| audit | B-34 | Audit log file fsync; pgstore lock leak | RESOLVED | RESOLVED ✓ | ad6ddd2, de73974 | xact-scoped advisory lock; defer recover |
| audit | B-35 | SIGHUP not registered | RESOLVED | RESOLVED ✓ | 3a81559, 57fafb1 | SIGHUP + file-mtime polling |
| audit | S-1.1 | applySets errors print raw RHS | RESOLVED | RESOLVED ✓ | 47e6916 | |
| audit | S-1.2 | signal.NotifyContext SIGTERM/SIGINT only | RESOLVED | RESOLVED ✓ | 3a81559 | already-done at audit time |
| audit | S-1.3 | No top-level defer recover() over realMain | RESOLVED | RESOLVED ✓ | ccdc7a1 | |
| audit | S-1.4 | JSON logger bypasses observability | RESOLVED | RESOLVED ✓ | 35d57e1 | |
| audit | S-1.5 | srv.Close() error after Shutdown dropped | RESOLVED | RESOLVED ✓ | dbc8356 | |
| audit | S-1.6 | Magic 2s slack on shutdown wait | RESOLVED | RESOLVED ✓ | (earlier B-1/B-3 work) | only ShutdownGracePeriod used at run.go:209,233 |
| audit | S-1.7 | /metadata should mount production CapabilityStatement | RESOLVED | RESOLVED ✓ | a9f96f7 + 620b243 | RegisterPublicRoutes wires it; CapabilityStatement body now SMART-enriched (subscription_handlers.go:1016) |
| audit | S-1.8 | Default 0.0.0.0 bind, no loopback opt-in | RESOLVED | RESOLVED ✓ | 6807f87 | warn-log on wildcard+insecure at `cmd/fhir-subs/run.go:161-167`; default at `cmd/fhir-subs/config.go:214` |
| audit | S-2.1 | /metadata mounted inside auth middleware | RESOLVED | RESOLVED ✓ | a9f96f7 | RegisterPublicRoutes |
| audit | S-2.2 | Body size limit hardcoded 1<<20 | RESOLVED | RESOLVED ✓ | 4743ce7 | Deps.MaxBodyBytes |
| audit | S-2.3 | json-schema error returned verbatim | RESOLVED | RESOLVED ✓ | 4743ce7 | Deps.MaxSchemaErrorBytes |
| audit | S-2.4 | If-None-Exist O(N) ListByClient | DEFERRED | DEFERRED ✓ | — | requires SubscriptionsStore interface change |
| audit | S-2.5 | Channel registry plain-map race | RESOLVED | RESOLVED ✓ (doc-only) | caa68a4 | doc-comment marks registry immutable post-RegisterRoutes; no compile-time enforcement |
| audit | S-2.6 | ETag id, not version; If-Match unquoted | RESOLVED | RESOLVED ✓ | 4743ce7 | requires W/"<id>" |
| audit | S-2.7 | Six `_ = err` swallows in activate | RESOLVED | RESOLVED ✓ | a9f96f7 | Deps.Logger |
| audit | S-2.8 | searchSubscriptions no pagination | DEFERRED | DEFERRED ✓ | — | repo interface growth |
| audit | S-2.9 | Magic timestamp format repeated 5× | RESOLVED | RESOLVED ✓ | 4743ce7 | instantFormat const |
| audit | S-2.10 | since, _ := strconv.ParseInt discards err | RESOLVED | RESOLVED ✓ | 4743ce7 | parseEventNumberParam |
| audit | S-2.11 | $status bulk no cap on id params | RESOLVED | RESOLVED ✓ | 4743ce7 | Deps.MaxStatusBulkIDs (256) |
| audit | S-2.12 | buildSubscriptionStatus uses context.Background() | RESOLVED | RESOLVED ✓ | a9f96f7 | r.Context() threaded |
| audit | S-2.13 | fhirVersion hardcoded "5.0.0" | RESOLVED | RESOLVED ✓ | 4743ce7 | Deps.FHIRVersion |
| audit | S-2.14 | pg_stores no per-query deadline | DEFERRED | DEFERRED ✓ | — | needs B-4 fully landed |
| audit | S-2.15 | $events replay hardcoded LIMIT 1000 | DEFERRED | DEFERRED ✓ | — | follows S-2.8 |
| audit | S-2.16 | Hash: []byte{0} placeholder | DEFERRED | DEFERRED ✓ | — | replaced by observability/audit hash-chained store under B-4 |
| audit | S-2.17 | Unvalidated X-Correlation-ID reflected | RESOLVED | RESOLVED ✓ | a9f96f7 | drops non-UUID |
| audit | S-2.18 | /metrics has no auth | RESOLVED | RESOLVED ✓ | a9f96f7 | metrics.AuthGuard at metrics.go:269 |
| audit | S-2.19 | routePattern falls back to URL.Path | RESOLVED | RESOLVED ✓ | a9f96f7 | unmatchedRouteLabel const at metrics.go:253 |
| audit | S-2.20 | Histogram bucket-count cap; cardinality validator narrow | RESOLVED | RESOLVED ✓ | caa68a4 | enforced at `internal/infra/observability/metrics/metrics.go:309-338` |
| audit | S-3.1 | 60s ClockSkew default too generous | RESOLVED | RESOLVED ✓ | a2318e9 | 30s default |
| audit | S-3.2 | No rate limit on token endpoint | RESOLVED | RESOLVED ✓ | a2318e9 | RateLimitPerSource |
| audit | S-3.3 | No per-client rate limit on subscription create / WS bind-token | DEFERRED | DEFERRED ✓ | — | RateLimit primitive exposed |
| audit | S-3.4 | exp, _ := claimToTime discards err | RESOLVED | RESOLVED ✓ | a2318e9 | fail-closed |
| audit | S-3.5 | HasScope is O(n) | RESOLVED | RESOLVED ✓ | a2318e9 | sync.Once set |
| audit | S-4.1 | rest-hook default *http.Client no Timeout | RESOLVED | RESOLVED ✓ | a2318e9 | |
| audit | S-4.2 | MaxIdleConnsPerHost/MaxConnsPerHost hardcoded | RESOLVED | RESOLVED ✓ | a2318e9 | |
| audit | S-4.3 | No max bundle size | RESOLVED | RESOLVED ✓ | a2318e9 | MaxBundleBytes (8 MiB) |
| audit | S-4.4 | allowSubscriberHeader default-permit | RESOLVED | RESOLVED ✓ | a2318e9 | deny-list expansion |
| audit | S-4.5 | NXDOMAIN classified PermanentFailure | RESOLVED | RESOLVED ✓ | a2318e9 | DNS errors transient |
| audit | S-4.6 | readBodyExcerpt may leak PHI | RESOLVED | RESOLVED ✓ | a2318e9 | opt-in default OFF |
| audit | S-5.1 | message channel default-client no Timeout | RESOLVED | RESOLVED ✓ | a2318e9 | |
| audit | S-5.2 | non-fhir+json fails at delivery time | RESOLVED | RESOLVED ✓ | a2318e9 | ValidateContentType |
| audit | S-5.3 | Bundle.timestamp uses RFC3339 | RESOLVED | RESOLVED ✓ | a2318e9 | RFC3339Nano |
| audit | S-5.4 | message channel default-permit allowlist | RESOLVED | RESOLVED ✓ | a2318e9 | |
| audit | S-6.1 | dialer.Timeout not set | RESOLVED | RESOLVED ✓ | 972dda7 | |
| audit | S-6.2 | No metric on STARTTLS-Preferred fallback | RESOLVED | RESOLVED ✓ | 972dda7 | MetricSTARTTLSOutcomeTotal |
| audit | S-6.3 | smtpErrorCode custom byte-loop parser | RESOLVED | RESOLVED ✓ | 972dda7 | errors.As(*textproto.Error) |
| audit | S-6.4 | Close() errors silently swallowed | RESOLVED | RESOLVED ✓ | 972dda7 | |
| audit | S-7.1 | sessions map unbounded | RESOLVED | RESOLVED ✓ | 4775357 | MaxSessions / MaxSessionsPerClient |
| audit | S-7.2 | No upgrade body-size / read-header timeout | RESOLVED | RESOLVED ✓ | 4775357 | ConfigureServer + 4 KiB bind read limit |
| audit | S-7.3 | conn.SetReadLimit never called | RESOLVED | RESOLVED ✓ | 4775357 | |
| audit | S-7.4 | bind-read timeout hardcoded 10s | RESOLVED | RESOLVED ✓ | 4775357 | Options.BindTimeout |
| audit | S-7.5 | pingLoop uses context.Background() parent | RESOLVED | RESOLVED ✓ | 4775357 | bound to channel ctx |
| audit | S-7.6 | Single ping uses full idleTimeout | RESOLVED | RESOLVED ✓ | 4775357 | PingWriteTimeout (10s) |
| audit | S-7.7 | readLoop has no idle-timeout enforcement | RESOLVED | RESOLVED ✓ | 4775357 | per-session lastReadAtNS |
| audit | S-7.8 | Replay loop materializes full slice | RESOLVED | RESOLVED ✓ | 4775357 | MaxReplayEvents (10k) |
| audit | S-7.9 | Close doesn't WaitGroup-join goroutines | RESOLVED | RESOLVED ✓ | 4775357 | per-session c.wg |
| audit | S-8.1 | Single in-flight dispatchOne per batch | RESOLVED | RESOLVED ✓ | acd798d | DispatchConcurrency semaphore |
| audit | S-8.2 | Not-found errors not dead-lettered | RESOLVED | RESOLVED ✓ | acd798d | ClassifyRequeueReason |
| audit | S-8.3 | Permanent build errors retried | RESOLVED | RESOLVED ✓ | acd798d | isPermanentBuildError |
| audit | S-8.4 | MaxAttempts not per-channel-type | RESOLVED | RESOLVED ✓ | acd798d | RetryConfig.PerChannel |
| audit | S-8.5 | Jitter uncapped | RESOLVED | RESOLVED ✓ | acd798d | MaxJitter=0.5 |
| audit | S-8.6 | Inline UPDATE SQL in worker | DEFERRED | DEFERRED ✓ | — | DeliveriesRepo refactor |
| audit | S-9.1 | MLLP read no per-message frame deadline | DEFERRED | DEFERRED ✓ | — | mitigated by S-9.4 |
| audit | S-9.2 | persistCtx decoupled — PersistTimeout cap | DEFERRED | DEFERRED ✓ | — | config validation work |
| audit | S-9.3 | isClosedConnErr substring-matches "closed" | RESOLVED | RESOLVED ✓ | acd798d | errors.Is(net.ErrClosed) |
| audit | S-9.4 | Framer no pending-byte bound | RESOLVED | RESOLVED ✓ | acd798d | pendingExceeded |
| audit | S-9.5 | ExtractMSH doesn't surface MSH-7/MSH-18 | RESOLVED | RESOLVED ✓ | acd798d | |
| audit | S-9.6 | ReaperBatchSize/claim/idle knobs | PARTIAL | PARTIAL ✓ | acd798d | ReaperBatchSize added; others were already exposed |
| audit | S-9.7 | No MetricClaimCycleErrors emission | RESOLVED | RESOLVED ✓ | acd798d | |
| audit | S-9.8 | peekUnprocessed lost-race window | RESOLVED | RESOLVED ✓ (by-design) | — | FOR UPDATE SKIP LOCKED + processed=false |
| audit | S-9.9 | BeginTx failure leaves row unprocessed | DEFERRED | DEFERRED ✓ | — | per-row retry budget |
| audit | S-9.10 | MSH-7 not used for occurred timestamp | RESOLVED | RESOLVED ✓ | acd798d | messageDateTime(parsed) |
| audit | S-9.11 | Same-kind paired-hold collision metric | RESOLVED | RESOLVED ✓ | acd798d | MetricSameKindCollision |
| audit | S-9.12 | Translate panics misclassified | RESOLVED | RESOLVED ✓ | acd798d | vendorPanicError |
| audit | S-10.1 | Matcher json.Unmarshal error not metricised | RESOLVED | RESOLVED ✓ | d3fad44 | SetMalformedResourceReporter |
| audit | S-10.2 | Bare-clause path inconsistent with submatcher | RESOLVED | RESOLVED ✓ | d3fad44 | equalsString parity |
| audit | S-10.3 | :in path no metric on unsupported modifier | RESOLVED | RESOLVED ✓ | d3fad44 | SetUnsupportedModifierReporter |
| audit | S-10.4 | Flexible-date silent UTC coercion | RESOLVED | RESOLVED ✓ | d3fad44 | parseFlexibleDateWithFlag |
| audit | S-10.5 | FHIRPath fail-closed metric | RESOLVED | RESOLVED ✓ | (B-24 work) | unknownFHIRPathReporter |
| audit | S-10.6 | MaxRowAttempts knob + counter | PARTIAL | PARTIAL ✓ | d3fad44 | knob added; counter wiring tracked under storage refactor |
| audit | S-11.1 | compileTrigger missing supportedInteraction enum check | RESOLVED | RESOLVED ✓ | d3fad44 | |
| audit | S-11.2 | Topic.EventCodings missing system+code | RESOLVED | RESOLVED ✓ | d3fad44 | EventCoding slice |
| audit | S-11.3 | notificationShape collapses multi-entry | DEFERRED | DEFERRED ✓ | — | breaking-change scope |
| audit | S-11.4 | Topic catalog Prometheus metrics | PARTIAL | PARTIAL ✓ | (B-25 work) | Rejected/Overridden exposed; metric wiring in callers |
| audit | S-12.1 | ListActiveByTopic materializes full list | DEFERRED | DEFERRED ✓ | — | streaming requires repo refactor |
| audit | S-12.2 | submatcher PoolSize knob missing | RESOLVED | RESOLVED ✓ | d3fad44 | Config.PoolSize |
| audit | S-12.3 | submatcher MaxRowAttempts | PARTIAL | PARTIAL ✓ | d3fad44 | knob added; counter wiring pending |
| audit | S-12.4 | Fanout tx inline events_since_subscription_start UPDATE | DEFERRED | DEFERRED ✓ | — | hot-subscription scaling |
| audit | S-12.5 | resourceTypeOf materializes full body | RESOLVED | RESOLVED ✓ | d3fad44 | streaming json.Decoder |
| audit | S-12.6 | Builder sort non-deterministic on tie | RESOLVED | RESOLVED ✓ | d3fad44 | sort.SliceStable + ID tiebreaker |
| audit | S-12.7 | Bundle/notificationEvent timestamps RFC3339 | RESOLVED | RESOLVED ✓ | d3fad44 | RFC3339Nano |
| audit | S-12.8 | Handshake/heartbeat correlation_id non-deterministic | RESOLVED | RESOLVED ✓ | d3fad44 | deterministic v5 UUID |
| audit | S-12.9 | fhir+xml rejection at builder, not API | DEFERRED | DEFERRED ✓ | — | belongs at subscription-create API path |
| audit | S-13.1 | AES-GCM rotation cadence undocumented | RESOLVED | RESOLVED ✓ | c765c8e | NIST 2^32 limit doc'd |
| audit | S-13.2 | Audit log no chained-append + prev-hash verify | RESOLVED | RESOLVED ✓ | c765c8e | AppendChained |
| audit | S-13.3 | ListActiveByTopic no streaming/page variant | RESOLVED | RESOLVED ✓ | c765c8e | StreamActiveByTopic, ListActiveByTopicPage |
| audit | S-13.4 | pgxpool defaults non-configurable | RESOLVED | RESOLVED ✓ | c765c8e | pool.Config |
| audit | S-13.5 | AfterConnect no statement_timeout / lock_timeout | RESOLVED | RESOLVED ✓ | c765c8e | |
| audit | S-13.6 | Storage tick has no per-tick deadline | RESOLVED | RESOLVED ✓ | c765c8e | TickTimeout + OnTickError |
| audit | S-13.7 | Pool-close budget hardcoded 5s | RESOLVED | RESOLVED ✓ | c765c8e | derived from cfg.Lifecycle.ShutdownGracePeriod |
| audit | S-13.8 | Partition first-run failure silent | RESOLVED | RESOLVED ✓ | c765c8e | flows through OnTickError |
| audit | S-13.9 | dropOnePartition not transactional | RESOLVED | RESOLVED ✓ | c765c8e | DETACH + DROP in single tx |
| audit | S-13.10 | Migrations apply-time now() undocumented | RESOLVED | RESOLVED ✓ | c765c8e | doc'd |
| audit | S-14.1 | LoggingConfig.Sink/DebugLogPayloads not plumbed | RESOLVED | RESOLVED ✓ | 9e7fa45 | |
| audit | S-14.2 | fileSink per-Emit alloc | RESOLVED | RESOLVED ✓ | 9e7fa45 | |
| audit | S-14.3 | Audit chain genesis literal hardcoded | RESOLVED | RESOLVED ✓ | 9e7fa45 | WriterOptions.GenesisLiteral |
| audit | S-14.4 | Audit Emit advisory_lock + LastChainHash + INSERT throughput cap | RESOLVED | RESOLVED ✓ (documented) | 9e7fa45 | doc'd as design constraint |
| audit | S-14.5 | canonicalNumber non-IEEE754 round-trip | RESOLVED | RESOLVED ✓ | 9e7fa45 | strconv.AppendFloat('g', -1, 64) |
| audit | S-14.6 | Correlation ID no length cap / charset | RESOLVED | RESOLVED ✓ | 9e7fa45 | MaxCorrelationIDLen=128 |
| audit | S-14.7 | Logging PHI list narrow / case-sensitive | RESOLVED | RESOLVED ✓ | 9e7fa45 | expanded list, case-insensitive |
| audit | S-14.8 | tracing span values not redacted | RESOLVED | RESOLVED ✓ | 9e7fa45 | tracing.SafeAttribute |
| audit | S-14.9 | OTLP exporter no timeout / TLS / Insecure knob | RESOLVED | RESOLVED ✓ | 9e7fa45 | |
| audit | S-15.1 | config.Start ignores ctx | RESOLVED | RESOLVED ✓ | 081200d | |
| audit | S-15.2 | loader env-var collisions silent | RESOLVED | RESOLVED ✓ | 081200d | EnvCollisions |
| audit | S-15.3 | redaction walker no depth cap | RESOLVED | RESOLVED ✓ | 081200d | MaxRedactDepth=256 |
| audit | S-15.4 | ${file:...} reads no size cap | RESOLVED | RESOLVED ✓ | 081200d | |
| audit | S-15.5 | effective_store no bounded notification pool | RESOLVED | RESOLVED ✓ | 081200d | MaxConcurrentNotifications=32 |
| audit | S-15.6 | reload splitPath doesn't honour \. escape | RESOLVED | RESOLVED ✓ | 081200d | |
| audit | S-15.7 | sequencer resultsMu race on deadline path | RESOLVED | RESOLVED ✓ | 081200d | |
| audit | S-15.8 | isDeadlineExceeded bare comparison | RESOLVED | RESOLVED ✓ | 081200d | errors.Is + Canceled |
| audit | S-16.1 | Empty internal/channels/ duplicate tree | RESOLVED | RESOLVED ✓ | 1ffc778 | |
| audit | S-16.2 | Empty internal/queue, internal/wakeup | RESOLVED | RESOLVED ✓ (audit was wrong) | — | no such packages exist; real ones at internal/infra/{queue,wakeup}/ |
| audit | S-16.3 | Empty internal/adapters, internal/adapterspi | RESOLVED | RESOLVED ✓ | 2a0bbc2, 060d538 | |
| audit | S-16.4 | Empty internal/domain/ packages | RESOLVED | RESOLVED ✓ | eea803a | |
| audit | S-16.5 | lifecycle.go dead `_ = errors.New` | RESOLVED | RESOLVED ✓ | ed87e64 | |
| audit | N-1.1 | HasScope O(n) | RESOLVED | RESOLVED ✓ | a2318e9 | (under S-3.5) |
| audit | N-1.2 | equalJSON swallows unmarshal errors | RESOLVED | RESOLVED ✓ | 6649d9b | bytes.Equal fallback |
| audit | N-1.3 | crypto/rand.Read failure no metric | RESOLVED | RESOLVED ✓ | 6649d9b | RandFailureRecorder |
| audit | N-1.4 | NotFound/MethodNotAllowed rely on auth wiring | DEFERRED | DEFERRED ✓ | — | chi.Middleware-typed wiring |
| audit | N-1.5 | email Subject CRLF strip | RESOLVED | RESOLVED ✓ | 2c8e258 | |
| audit | N-1.6 | formatTraceparent doesn't validate hex | RESOLVED | RESOLVED ✓ | 2c8e258 | |
| audit | N-1.7 | WS ack handling no eventNumber-in-sent-set check | RESOLVED | RESOLVED ✓ | 79921ca | MetricUnknownAckTotal |
| audit | N-1.8 | WS Deliver / Close race documentation | RESOLVED | RESOLVED ✓ (doc-only) | 79921ca | doc-block; no code change |
| audit | N-1.9 | message channel wrapInMessageBundle non-deterministic | DEFERRED | DEFERRED ✓ | — | JCS-canonicalizer |
| audit | N-1.10 | ComputeBackoff doubling-loop iteration cap | RESOLVED | RESOLVED ✓ | 79921ca | maxBackoffDoublingSteps = 64 |
| audit | N-1.11 | Channel setup-error retries forever | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block; behavior unchanged |
| audit | N-1.12 | pending_kind enum reuses 'create' | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block |
| audit | N-1.13 | adapter_id × resource_type × change_kind cardinality | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block |
| audit | N-1.14 | translate has no charset normalization contract | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block |
| audit | N-1.15 | matcher backoff has no gauge | RESOLVED | RESOLVED ✓ | ffe847b | SetBackoffReporter |
| audit | N-1.16 | matcher `committed=true` lies about rollback | RESOLVED | RESOLVED ✓ | ffe847b | renamed `txDone` |
| audit | N-1.17 | Catalog immutability contract undocumented | RESOLVED | RESOLVED ✓ (documented) | ffe847b | type doc |
| audit | N-1.18 | Catalog RawJSON in-memory copy | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block |
| audit | N-1.19 | Override-shadow has no structured log | RESOLVED | RESOLVED ✓ | ffe847b | Override.LogFields |
| audit | N-1.20 | MLLP readBuf 8192 hardcoded | RESOLVED | RESOLVED ✓ | 79921ca, ffe847b | ReadBufBytes knob |
| audit | N-1.21 | MLLP body double-copy | RESOLVED | RESOLVED ✓ | ffe847b | |
| audit | N-1.22 | MLLP write deadline 2s hardcoded | RESOLVED | RESOLVED ✓ | 79921ca | AckWriteTimeout |
| audit | N-1.23 | scanEndPair O(n) per Append | RESOLVED | RESOLVED ✓ | ffe847b | closedScanned offset |
| audit | N-1.24 | MLLP listener time.After leaks on shutdown | RESOLVED | RESOLVED ✓ | 79921ca | NewTimer + Stop |
| audit | N-1.25 | MLLP no PROXY protocol v2 | DEFERRED | DEFERRED ✓ | — | new dependency required |
| audit | N-1.26 | sequencer time.After leaks | RESOLVED | RESOLVED ✓ | ffe847b | |
| audit | N-1.27 | bytesEqual reinvented | RESOLVED | RESOLVED ✓ | c1ff9eb | |
| audit | N-1.28 | Audit sink failure has no buffer/queue | RESOLVED | RESOLVED ✓ (documented) | c1ff9eb | doc-block |
| audit | N-1.29 | Event.Payload taken by reference | RESOLVED | RESOLVED ✓ | c1ff9eb | defensive shallow copy |
| audit | N-1.30 | pgstore advisory lock id is FNV truncation | RESOLVED | RESOLVED ✓ | c1ff9eb | int64 const |
| audit | N-1.31 | codec envelope format hardcoded 0x01 | RESOLVED | RESOLVED ✓ (documented) | ffe847b | doc-block |
| audit | N-1.32 | deliveries ORDER BY no tiebreaker | RESOLVED | RESOLVED ✓ | 79921ca | id ASC tiebreaker |
| audit | N-1.33 | subscription_topics.ListByStatus no LIMIT | RESOLVED | RESOLVED ✓ | 79921ca | DefaultListByStatusCap=1000 |
| audit | N-1.34 | claim FOR UPDATE substring match fragile | RESOLVED | RESOLVED ✓ | 79921ca | stripSQLCommentsAndStrings |
| audit | N-1.35 | partition Run ignores reload changes | RESOLVED | RESOLVED ✓ | ffe847b | re-reads cfg per iteration |
| audit | D-1 | Production binary loads empty catalog | RESOLVED | RESOLVED ✓ | 3d0945f (merged a970f3d) | `topics.catalog_dir` config + `loadTopicSources` walk + SIGHUP reload (`cmd/fhir-subs/topics.go:24`, `cmd/fhir-subs/wiring.go:286`) |
| audit | D-2 | rest-hook channel handshake placeholder | RESOLVED | RESOLVED ✓ | 3d0945f (merged a970f3d) | `restHookActivator` (`cmd/fhir-subs/activators.go:50-156`) POSTs synthetic FHIR R5 handshake Bundle; only 2xx flips to active. Wired at `wiring.go:191`. |
| audit | D-3 | pgxpool startup ping retries past pingCtx | RESOLVED | RESOLVED ✓ | 3d0945f (merged a970f3d) | `buildPoolConfig` (`cmd/fhir-subs/pool.go:25-33`) sets `ConnConfig.ConnectTimeout` (5s default). |
| audit | D-4 | Adapter version pin error path uses generic Go error | RESOLVED | RESOLVED ✓ | 3d0945f (merged a970f3d) | `formatRunError` (`cmd/fhir-subs/main.go:199-204`) `errors.As`-switches on `*registry.UnknownAdapterError`. |
| future | P1.1 | Adapter framework supervisors | (no status) | OPEN ✗ | — | bd #172 pending; no Supervisor types in `internal/adapter/` |
| future | P1.2 | FHIRPath sandboxed evaluator (MVP) | RESOLVED | RESOLVED ✓ | ba9bba8 (merged f9fc4b9) | `internal/matcher/matcher.go:599-700` MVP subset handles `.exists()`, `.empty()`, bare `=`; fail-closed on anything outside the subset (B-24 invariant preserved). Full sandbox (timeout, traversal limit, deny-list) is the post-MVP follow-up. |
| future | P1.3 | :in modifier ValueSet expansion (MVP) | RESOLVED | RESOLVED ✓ | e3f3a31 (merged f9fc4b9) | `internal/topics/catalog/catalog.go:774-779` rejects `:in` at load with a clear error pointing at P1.3 unless an operator-wired `allowInModifier` flag is flipped (no expander shipped yet — fail-loud-at-load is the MVP). |
| future | P1.4 | ICU root-locale folding for all string equality | RESOLVED | RESOLVED ✓ | b8568c5 (merged 3d4f810) | `internal/matcher/matcher.go:422-464` `equalsToken`/`equalsString`/`equalsReference` all route through `foldEqual`; the same fold lands in `submatcher` per the RED test suite at `683b45e`. ADR 0010 #4 satisfied. |
| future | P1.5 | Topic Matcher metrics | (no status) | OPEN ✗ | — | bd #180 pending re-implementation; zero `fhir_subs_matcher_*` metric names emitted from `internal/matcher/`. The earlier P1 cherry-pick deferred this due to conflicts. |
| future | P1.6 | Admin API operator surface (MVP) | RESOLVED | RESOLVED ✓ | d14ed2c + b02ca79 + 792e189 (merged f9fc4b9) | `internal/api/handlers/admin.go:55::RegisterAdminRoutes` mounts read-only operator endpoints (token-gated); covered by `admin_test.go`. |
| future | P1.7 | CapabilityStatement implementation | RESOLVED | RESOLVED ✓ (with config caveat) | 620b243 (merged f9fc4b9) | `subscription_handlers.go:1016::buildCapabilityStatement` covers SMART-on-FHIR security service code with OAuth `token`/`jwks` extension URIs, multi-version `fhirVersion` from `Deps.SupportedFHIRVersions`. **Caveat:** `cmd/fhir-subs/wiring.go:201-218` does not yet populate `Deps.TokenEndpointURL`, `Deps.JWKSURL`, or `Deps.SupportedFHIRVersions` — operator-config wiring is the natural follow-up under P2.4. |
| future | P1.8 | Hydration `_include` / `_revinclude` | (no status) | OPEN ✗ | — | bd #174 pending; default adapter Hydration returns ErrHydrationUnsupported |
| future | P1.9 | WSS Sec-WebSocket-Protocol bind transport | RESOLVED | RESOLVED ✓ | 95aece8 (merged f9fc4b9) | `internal/channel/websocket/websocket.go:46` `SubprotocolBindPrefix = "fhirsubscriptions.v1."`; offered subprotocols parsed at `:371-392`, accepted via `AcceptOptions.Subprotocols`, token extracted from the negotiated subprotocol at `:460-465`. |
| future | P1.10 | Adapter manifest config_schema validation (MVP) | RESOLVED | RESOLVED ✓ (MVP) | 0a7c93b (merged f9fc4b9) | `internal/adapter/registry/registry.go:143-147` runs `validateConfigSchema` (JSON Schema compile via `jsonschema/v5`) and `validateContributedTopicsUnique` on Load. **Out of MVP:** capability-vs-builder cross-check (e.g., `Capabilities.HydrationService=true` ↔ non-nil `BuildHydrationService`) and cross-adapter URL collision detection. |
| future | P1.11 | Authn/Authz hardening of WSS bind token storage | RESOLVED | RESOLVED ✓ | b624b7d (commit 7b5b7c2) | `internal/infra/storage/repos/ws_binding_tokens.go:22-23 hashToken` applied on Insert/Consume/expiry/lookup. |
| future | P1.12 | Dead-letter operational runbook + metric | RESOLVED | RESOLVED ✓ | 90c0215 (merged f9fc4b9) | metric `fhir_subs_hl7processor_dead_letters_total{reason}` emitted from `internal/hl7processor/processor.go:777`; runbook published at `docs/operations/dead-letters-runbook.md`. |
| future | P2.1 | FHIR Scan Runner adapter framework worker | (no status) | OPEN ✗ | — | bd #181 pending; no production worker invokes ScanPlan/RunScan |
| future | P2.2 | Vendor API Client framework worker | (no status) | OPEN ✗ | — | bd #182 pending; SPI exists; no worker |
| future | P2.3 | Email channel S/MIME + Direct SMTP | (no status) | OPEN ✗ | — | bd #183 pending; v1 ships SMTP-only |
| future | P2.4 | R4B/R5 wire negotiation completeness | (no status) | OPEN ✗ | — | bd #184 pending; partial Negotiate; no full Subscription R4B↔R5 conversion. Also wires `Deps.SupportedFHIRVersions` end-to-end (P1.7 caveat). |
| future | P2.5 | Audit chain verifier CLI | (no status) | OPEN ✗ | — | bd #185 pending; no `fhir-subs audit verify` subcommand |
| future | P2.6 | Heartbeats and handshakes | (no status) | OPEN ✗ | — | bd #186 pending; scheduler doesn't emit heartbeats |
| future | P2.7 | Auth re-check at delivery prep | (no status) | OPEN ✗ | — | bd #187 pending; submatcher has FanoutAuthRevoked decision but no AuthValidator.Recheck SPI |
| future | P2.8 | OpenTelemetry trace export configuration | RESOLVED — recipe docs pending | PARTIAL ⚠ | 9e7fa45 (S-14.9) | bd #188 pending; configuration surface (`ExporterTimeout`, `TLSConfig`, `Headers`, `Insecure`) at `internal/infra/observability/tracing/tracing.go:54-65`. Deployment recipes (Datadog/Honeycomb/Jaeger) still pending. |
| future | P2.9 | Webhook ingress (vendor push) | (no status) | OPEN ✗ | — | explicitly out of scope for v1 |
| future | P2.10 | Multi-instance / horizontal scale | (no status) | OPEN ✗ | — | per ADR 0002 single-instance |
| future | P3.1 | Adapter authoring guide | (no status) | OPEN ✗ | — | docs only |
| future | P3.2 | More EHR adapters | (no status) | OPEN ✗ | — | community ask |
| future | P3.3 | Repository unused code cleanup | (no status) | OPEN ✗ | — | WsBindingTokensRepo.Get/Delete unused |
| future | P3.4 | Container / Helm packaging | (no status) | OPEN ✗ | — | Dockerfile exists; no Helm |
| future | P3.5 | Documentation site | (no status) | OPEN ✗ | — | |
| future | P3.6 | CI/CD | (no status) | OPEN ✗ | — | `.github/` is sparse; only basics |
| future | P4.1 | FHIR R6 support | (no status) | OPEN ✗ | — | spec dependency |
| future | P4.2 | Spec extensions | (no status) | OPEN ✗ | — | by-design out-of-scope |
| demo | gap-1 | Production binary doesn't serve FHIR API | RESOLVED | RESOLVED ✓ | e615c31 (B-4 wiring) | `cmd/fhir-subs/wiring.go::buildProductionRuntime` wires DB/codec/auth/handlers/MLLP/pipeline |
| demo | gap-2 | Production binary doesn't start pipeline workers | RESOLVED | RESOLVED ✓ | e615c31 (B-4 wiring) | `cmd/fhir-subs/wiring.go:311-315` launches all four pipeline workers |
| demo | gap-3 | No CLI publisher tool | (open in doc) | OPEN ✗ | — | no `cmd/demo-publisher/` |
| demo | gap-4 | No CLI subscriber tool | (open in doc) | OPEN ✗ | — | no `cmd/demo-subscriber/` |
| demo | gap-5 | Docker-compose for one-command spin-up | (open in doc) | OPEN ✗ | — | Dockerfile exists; no compose under demo/ |
| demo | gap-6 | Demo topic catalog | (open in doc) | OPEN ✗ | — | no demo/topics/ |
| demo | gap-7 | Subscription filter shape demo-friendly | RESOLVED | RESOLVED ✓ | 3d80c7d / 04e2c36 (B-23, merged 8096936) | topic catalog rejects unsupported filters at load, matcher fail-closes on shortlist |
| demo | gap-8 | Default adapter HL7 → FHIR translation | (open in doc) | OPEN ✗ | — | adapters/default/ is still passthrough |
| demo | gap-9 | Pretty-printable terminal output | (open in doc) | OPEN ✗ | — | depends on gap-3, gap-4 |
| demo | gap-10 | README for demo path | (open in doc) | OPEN ✗ | — | |

## Discrepancies

After this round of merges, no item's Documented Status diverges materially from its Verified Status. The future-work doc still uses prose-status rather than a uniform header marker, but every item that's actually shipped now has a "RESOLVED" line in `docs/future-work.md`.

One soft caveat survives:

- **P1.7 production-config wiring.** `subscription_handlers.go:1016` is fully SMART-enriched, but `cmd/fhir-subs/wiring.go:201-218` does not populate `Deps.TokenEndpointURL`, `Deps.JWKSURL`, or `Deps.SupportedFHIRVersions`. The CapabilityStatement therefore renders without the OAuth extension and without the multi-version array unless those Deps are set elsewhere. Tracked under P2.4 (R4B/R5 wire negotiation completeness) since the same wiring lands the multi-version negotiator.

## Recently resolved (this batch)

These items moved from OPEN/PARTIAL to RESOLVED in the rollups merged onto `main` between `7df3187` and `a970f3d`:

- **P1.2** (FHIRPath MVP subset) — `ba9bba8`, merged `f9fc4b9`.
- **P1.3** (`:in` modifier fail-loud at load) — `e3f3a31`, merged `f9fc4b9`.
- **P1.4** (ICU root-locale folding for ALL string equality) — `b8568c5`, merged `3d4f810`. The previously latent equality-path bug is fixed: `equalsToken`/`equalsString`/`equalsReference` now all route through `foldEqual`.
- **P1.6** (Admin API operator surface MVP) — `d14ed2c` + `b02ca79` + `792e189`, merged `f9fc4b9`.
- **P1.7** (CapabilityStatement enrichment) — `620b243`, merged `f9fc4b9`. (One config-wiring caveat above.)
- **P1.9** (WSS Sec-WebSocket-Protocol bind transport) — `95aece8`, merged `f9fc4b9`.
- **P1.10** (manifest config_schema validation MVP) — `0a7c93b`, merged `f9fc4b9`. JSON Schema compile + per-adapter contributed-topic URL uniqueness shipped; capability cross-check + cross-adapter collision detection are post-MVP.
- **P1.12** (dead-letter metric + runbook) — `90c0215`, merged `f9fc4b9`. Metric `fhir_subs_hl7processor_dead_letters_total{reason}` plus `docs/operations/dead-letters-runbook.md`.
- **D-1, D-2, D-3, D-4** (production-binary follow-ons) — `004a29c`, merged `a970f3d`. Topics catalog, rest-hook handshake, pgxpool connect_timeout, typed UnknownAdapterError all wired in `cmd/fhir-subs/`.

## Counts

```
Source         Total | RESOLVED | OPEN | PARTIAL | DEFERRED
audit B-*         35 |       35 |    0 |       0 |        0
audit S-*.X      125 |      107 |    0 |       4 |       14
audit N-*.X       35 |       32 |    0 |       0 |        3
audit D-*          4 |        4 |    0 |       0 |        0
future P1         12 |       10 |    2 |       0 |        0
                     |          | (P1.1, |        |
                     |          |  P1.5, |        |
                     |          |  P1.8) |        |
future P2         10 |        1 |    8 |       1 |        0
                     | (P2.8 surface)|   | (P2.8 |
                     |          |        |  recipes)|
future P3          6 |        0 |    6 |       0 |        0
future P4          2 |        0 |    2 |       0 |        0
demo              10 |        3 |    7 |       0 |        0
TOTAL            239 |      192 |   29 |        5 |       17
```

Notes on the count block:
- P2.8 is split: the configuration surface (S-14.9) is in main; deployment recipe docs are not. Counted as PARTIAL.
- P1.7 is RESOLVED on the implementation side; the production-config wiring caveat is tracked under P2.4 and does not subtract from the P1 count.

## Currently in flight

After this round of consolidation, **no feature branches are ahead of `origin/main`**. The previously-listed worktrees (`feat/future-work-p1-batch`, `fix/p14-icu-folding-equality`, `fix/discovered-d1-d4`, `docs/reconcile-future-work-status`) have all been merged. Stale worktrees can be cleaned up.

`git worktree list` still shows the following local worktrees that are now equivalent to `origin/main` and safe to remove:

- `/private/tmp/fhir-fix-race`, `/private/tmp/fhir-nice-to-have`, `/private/tmp/fhir-sf-auth-channels`
- `~/cz/.worktrees/discovered-d1-d4`, `~/cz/.worktrees/future-work-p1-batch`, `~/cz/.worktrees/p14-icu-folding-equality`, `~/cz/.worktrees/reconcile-future-work-status`
- The five `~/cz/.worktrees/sf-*` SHOULD-FIX worktrees — already merged

This `docs/status-refresh-after-p1` worktree itself remains until the doc commit lands.

## Recommended next actions

1. **Land P1.5 matcher metrics** (`bd` #180). Highest-priority remaining P1 item. Without it, operators can't see matcher throughput, slow topics, or fhirpath timeouts.
2. **Wire `Deps.TokenEndpointURL` / `Deps.JWKSURL` / `Deps.SupportedFHIRVersions` from config** in `cmd/fhir-subs/wiring.go`. Closes the P1.7 caveat; naturally fits inside P2.4 (R4B/R5 wire negotiation).
3. **Expand P1.10 validation** with capability-vs-builder cross-check and cross-adapter URL collision detection. The MVP shipped covers single-adapter compile + uniqueness; the full LLD §8 ask is broader.
4. **Pick up P2.6 heartbeats/handshakes** (`bd` #186) and **P2.7 delivery-time auth re-check** (`bd` #187) — both are operator-visible reliability gaps.
5. **Write OTel deployment recipes** (`bd` #188). Configuration surface is shipped; operators have no copy-pasteable Datadog/Honeycomb/Jaeger config today.

## How this doc stays current

`docs/status.md` is regenerated after every batch of merges to `origin/main`. The orchestrator agent that produces it treats the audit doc, future-work doc, and demo doc as inputs and reconciles their claims against the code. Individual fix agents touch the source docs (audit, future-work) and the regenerator reconciles them here.

When status.md and a source doc disagree, **status.md is the source of truth for "what is the actual state on `main` today"**, and the source doc is the source of truth for "what is this finding's full context (what/why/fix)." If you change one, regenerate the other.

Cadence target: regenerate after every merge that closes 3+ items, or when a new audit/future-work item is filed.
