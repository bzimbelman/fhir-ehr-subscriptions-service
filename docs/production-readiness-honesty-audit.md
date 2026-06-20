# Production-Readiness Honesty Audit

Adversarial verification of `cmd/fhir-subs/wiring.go` and the packages it does (or does not) wire. Conducted against current `main` (commit `fb02d0c`). The published `docs/production-readiness-audit.md` and `docs/status.md` are not trusted; every claim below is grounded in code citations.

The verification rule is simple: "exists in a package and has tests" is not "wired into the production binary." Production behavior is what `cmd/fhir-subs/main.go -> realMain -> run -> runWithHooks -> buildProductionRuntime` actually constructs and starts.

## Summary table

| # | Tag | Severity | Path:line | Claim |
|---|-----|----------|-----------|-------|
| 1 | UNWIRED | BLOCKER | `cmd/fhir-subs/wiring.go:143` | Adapter SPIs `BuildFhirScanRunner`, `BuildVendorAPIClient`, `BuildHydrationService` are never called; only `BuildHl7Processor` is invoked. |
| 2 | UNWIRED | BLOCKER | `internal/adapter/scanrunner/scanrunner.go` | Scanrunner host worker has zero production callers (`scanrunner.New` referenced only by `*_test.go`). |
| 3 | UNWIRED | BLOCKER | `internal/adapter/vendorclient/vendorclient.go` | Vendor API Client host worker has zero production callers. |
| 4 | UNWIRED | BLOCKER | `internal/adapter/supervisor/supervisor.go` | Supervisor framework is never instantiated by production wiring. |
| 5 | UNWIRED | BLOCKER | `internal/webhook/webhook.go` | Webhook ingress handler defined but never mounted. |
| 6 | UNWIRED | BLOCKER | `internal/hydration/hydration.go` | Hydration service has zero production callers. |
| 7 | UNWIRED | BLOCKER | `internal/api/handlers/admin.go:55` | `RegisterAdminRoutes` is only called from `admin_test.go`; admin surface is dead in the production binary. |
| 8 | UNWIRED | BLOCKER | `internal/api/handlers/router.go:354` | `RegisterPublicRoutes` is referenced only in comments — never called. |
| 9 | STUBBED | BLOCKER | `internal/api/handlers/pg_stores.go:506` | Production `PgAuditStore.Append` writes `Hash: []byte{0}` placeholder — no chain integrity. |
| 10 | UNWIRED | BLOCKER | `internal/infra/observability/observability.go:200` | `observability.Start` is never called from `cmd/`; metrics, OTel tracing, and the hash-chained audit writer are not constructed in production. |
| 11 | UNWIRED | BLOCKER | `internal/infra/observability/audit/pgstore.go:88-90` vs `migrations/0001_init.sql:226` | Audit `PgStore` SQL targets columns `chain_hash`, `prior_hash`, `chain_input`, `payload`; production migration has `hash`, `prev_hash`, `canonical_form`. The two are incompatible. |
| 12 | MISSING | BLOCKER | `cmd/fhir-subs/audit_cli.go:139` | `fhir-subs audit verify` queries columns that the production migrations do not create — runs against a real DB will fail with "column does not exist." |
| 13 | UNWIRED | BLOCKER | `internal/infra/storage/storage.go:179` | `storage.Start` is never called from `cmd/`; partition maintainer + retention sweeper background workers never run. |
| 14 | UNWIRED | BLOCKER | `internal/channel/websocket/websocket.go` | WebSocket channel package exists but no constructor is called from `cmd/`; `chReg.Register("websocket", ...)` is absent. |
| 15 | UNWIRED | BLOCKER | `internal/channel/email/email.go` | Email channel package never registered with the scheduler's channel registry. |
| 16 | UNWIRED | BLOCKER | `internal/channel/message/message.go` | Message channel package never registered. |
| 17 | UNWIRED | BLOCKER | `cmd/fhir-subs/wiring.go:178` | Scheduler ChannelRegistry contains only `rest-hook`; subscriptions on any other channel type silently never get delivered. |
| 18 | UNWIRED | BLOCKER | `cmd/fhir-subs/wiring.go:210-228` | `Deps.SubscriptionCreateRateLimit` and `Deps.WSBindingTokenRateLimit` are never populated; the per-client rate limits described in `auth.RateLimit`/config are dead code. |
| 19 | HARDCODED | BLOCKER | `cmd/fhir-subs/run.go:151` | Production HTTP server uses `srv.Serve` (cleartext); `srv.ServeTLS` is never used. `server.http.tls.cert_file/key_file` config knobs are inert. |
| 20 | HARDCODED | BLOCKER | `cmd/fhir-subs/wiring.go:660-672` | MLLP listener TLS / mTLS support exists in `internal/mllp/config.go:18-31` but `buildMLLPListener` never sets `EndpointConfig.TLS`. HL7 traffic transits cleartext. |
| 21 | UNWIRED | HIGH | `internal/infra/observability/metrics/metrics.go:181` | `Emitter.Handler()` exists but `/metrics` is never mounted on the chi router; Prometheus scraping cannot reach it. |
| 22 | UNWIRED | HIGH | `internal/api/metrics/metrics.go` | The 9 API-level Prometheus metrics defined here have zero production callers — `apimetrics.New` only called from `metrics_test.go`. |
| 23 | UNWIRED | HIGH | `cmd/fhir-subs/main.go:151` | The only subcommand is `audit verify`. No OTel tracer initialization anywhere in `cmd/`; P2.8 OTel exporter is documentation only. |
| 24 | STUBBED | HIGH | `adapters/cerner/cerner.go:77`, `adapters/epic/epic.go:96`, `adapters/athena/athena.go`, `adapters/nextgen/nextgen.go`, `adapters/meditech/meditech.go`, `adapters/allscripts/allscripts.go` | Every vendor adapter's `MapToFHIR` returns hardcoded `{"resourceType":"Bundle","type":"collection"}` and discards the raw HL7 input. |
| 25 | STUBBED | HIGH | `adapters/default/default.go:111-122` | The default adapter selected by wiring also returns the same `{"resourceType":"Bundle","type":"collection"}` hardcode and drops the HL7 payload. |
| 26 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:126` | Only the `default` adapter is registered. Operators cannot set `adapter.id: cerner|epic|athena|...` even though the packages exist; `adapter load` returns `UnknownAdapterError`. |
| 27 | STUBBED | HIGH | `cmd/fhir-subs/wiring.go:707-713` | `defaultActivator.ActivateSubscription` always returns `HandshakeSucceeded`; websocket and email subscriptions go straight to `active` without any handshake. The type comment admits "real handshake plumbing replaces this default in a follow-up." |
| 28 | HARDCODED | HIGH | `cmd/fhir-subs/wiring.go:221-222` | `Deps.BaseURL` and `WSBaseURL` are unconditionally prefixed `https://` / `wss://` from `cfg.Server.HTTP.Bind` — even when `cfg.Server.HTTP.Insecure=true` and the bind is `0.0.0.0:8443`. The advertised URL is wrong. |
| 29 | HARDCODED | HIGH | `cmd/fhir-subs/wiring.go:220` | `WSBindingTTL: 5 * time.Minute` is a hardcoded literal — not exposed in `Config` and not operator-tunable. |
| 30 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:210-228` | `Deps.JWKSURL`, `Deps.TokenEndpointURL`, `Deps.SupportedFHIRVersions`, `Deps.FHIRVersion` are never set; the rendered CapabilityStatement omits SMART OAuth-uris extension contents. |
| 31 | MISSING | HIGH | `cmd/fhir-subs/run.go:260-275` | No `/.well-known/jwks.json` route is mounted; the server signs tokens with `IssuedSecret` but never publishes a JWKS for clients to verify against. |
| 32 | MISCONFIGURED-DEFAULT | HIGH | `cmd/fhir-subs/config.go:262-264` | Default `server.http.bind` is `0.0.0.0:8443` (wildcard interface). Comment in `run.go:163-168` even admits this. |
| 33 | HARDCODED | HIGH | `cmd/fhir-subs/wiring.go:638-672` | `MLLP` listener defaults: `ReadIdleTimeout: 30s`, `NackThenDropAfter: 5`, `InflightCapPerConn: 64`, `OnPersistFail: NackThenDrop` — none exposed in `Config`. `FrameAssemblyTimeout` is read by config but never plumbed. |
| 34 | MISCONFIGURED-DEFAULT | HIGH | `cmd/fhir-subs/pool.go:25-35` | Postgres pool config sets only `ConnectTimeout`. `MaxConns`, `MinConns`, `MaxConnLifetime`, `MaxConnIdleTime`, `HealthCheckPeriod` left at pgxpool defaults. `database.*` config has only `url` — no tunables. |
| 35 | UNWIRED | HIGH | `cmd/fhir-subs/config.go:185-189` | `WebSocketChannelConfig` (`OriginPatterns`, `IdleTimeout`, `PingInterval`) is parsed and never read. |
| 36 | UNWIRED | HIGH | `internal/infra/storage/outbox/`, `internal/infra/storage/claim/`, `internal/infra/storage/partition/`, `internal/infra/storage/retention/` | Have zero production callers (`storage.go` references them but `storage.Storage` itself is unused outside tests). |
| 37 | UNWIRED | HIGH | `internal/infra/wakeup/`, `internal/infra/queue/` | Zero importers in production or test code. Phantom packages. |
| 38 | UNWIRED | HIGH | `internal/engine/heartbeat/`, `internal/engine/topicmatcher/`, `internal/topics/filter/` | Zero production callers. |
| 39 | MISCONFIGURED-DEFAULT | HIGH | `cmd/fhir-subs/wiring.go:292-305` | Empty `topics.catalog_dir` (the default) -> empty catalog -> matcher silently emits zero EHR events. The pipeline runs successfully end-to-end and delivers nothing. Doc only logs `"topic catalog: nil after Load (treating as empty)"` at WARN. |
| 40 | MISSING | MEDIUM | `cmd/fhir-subs/main.go:151` | The only subcommand is `audit`; the `dead-letters runbook` references `fhir-subs dead-letters list/replay/forget` (the runbook acknowledges it as deferred but the operator-facing CLI does not exist). |
| 41 | MISCONFIGURED-DEFAULT | MEDIUM | `cmd/fhir-subs/config.go:259-269` | `defaultConfig()` returns `insecure=false` (good) but no `database.url`, no codec keys, no `auth.audience`. With those empty, `runWithHooks` falls back to "probe-only" mode (no DB, no auth, no MLLP, no pipeline). A typo in any required field silently degrades rather than failing fast. |
| 42 | HARDCODED | MEDIUM | `cmd/fhir-subs/wiring.go:226` | `Deps.ActivationTimeout: 30 * time.Second` is a hardcoded literal — not config-driven. |
| 43 | HARDCODED | MEDIUM | `cmd/fhir-subs/wiring.go:341-345` | Scheduler `RetryConfig` values (`Initial: 1s`, `Max: 30s`, `Min: 500ms`, `MaxAttempts: 8`) are hardcoded; no `pipeline.scheduler.retry.*` config block. |
| 44 | HARDCODED | MEDIUM | `cmd/fhir-subs/wiring.go:264-269` | `hl7processor.Config.CorrelationHoldWindow` defaults to `30 * time.Second` if not configured; matches SPI default but not exposed beyond `pipeline.correlation_hold_window`. |
| 45 | UNWIRED | MEDIUM | `internal/api/auth/handler_rate_limit.go` | `auth.NewClientRateLimiter` is not called from `cmd/`; `RateLimitConfig` block in `cmd/fhir-subs/config.go:91-102` is parsed and ignored. |
| 46 | LEAKED | MEDIUM | `cmd/fhir-subs/wiring.go:439-501` | `registerLifecycle` does not register a shutdown for the rest-hook activator's HTTP client transport (`activators.go:73-89`), so idle keep-alive connections may outlive the listener. The `_ = grace` discard at line 440 also documents the unused `grace` arg. |
| 47 | UNWIRED | MEDIUM | `cmd/fhir-subs/wiring.go:159-208` | `auth.AllowInsecure` (config name `allow_insecure_jwks`) flips both the verifier's `AllowInsecureJWKS` AND the URL validator's `AllowHTTP`. Coupling means a dev convenience also weakens subscriber endpoint validation in production if accidentally left on. |
| 48 | MISCONFIGURED-DEFAULT | MEDIUM | `cmd/fhir-subs/config.go:67-87` | `AuthConfig.TrustedIssuers` is parsed as advisory; the verifier does not filter JWKS lookups by it. The struct comment ("today the fields are advisory") confirms it. |
| 49 | UNWIRED | LOW | `cmd/fhir-subs/wiring.go:726` | `var _ channel.Channel = channel.Channel(nil)` is a "silence the unused-import diagnostic" placeholder — its presence proves the channel interface is not actually consumed by anything wiring imports. |
| 50 | HARDCODED | LOW | `cmd/fhir-subs/wiring.go:191-200` | `defaultActivator{}` is hardcoded for `"websocket"` and `"email"` even though the per-channel activator implementations could differ; the placeholder ships in production. |

## BLOCKER findings

### Finding 1 — Adapter SPI Build* methods are mostly never called

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/wiring.go:143`

`buildProductionRuntime` calls only `loadedAdapter.BuildHl7Processor(...)`. The SPI declares four REQUIRED Build methods (`internal/adapter/spi/interfaces.go:275-283`). The base struct panics if any are not overridden, but vendor adapters silently return `nil`.

**Proof.**
```
$ grep -n "Build" cmd/fhir-subs/wiring.go
143:    hl7Proc := loadedAdapter.BuildHl7Processor(adapterspi.AdapterContext{
```
Only one Build call. No `BuildFhirScanRunner`, `BuildVendorAPIClient`, `BuildHydrationService` invocation in `cmd/`.

**Operator consequence.** Any deployment that needs FHIR scan, vendor change-feed, or full-resource hydration cannot get it. The SPI advertises these capabilities; production cannot consume them.

### Finding 2 — `internal/adapter/scanrunner` is dead production code

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/adapter/scanrunner/scanrunner.go`

```
$ grep -rl "scanrunner.New\|scanrunner\\.\\(New\\|Start\\|Run\\)" --include="*.go" .
internal/adapter/scanrunner/scanrunner_test.go:90  90 ...
```
Zero callers outside the package's own `_test.go`.

**Operator consequence.** Periodic FHIR scan plans defined by an adapter manifest are never executed.

### Finding 3 — `internal/adapter/vendorclient` is dead production code

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/adapter/vendorclient/vendorclient.go`

`vendorclient.New` only referenced from `vendorclient_test.go`. Vendor change-feed Consume / Translate path is unreachable.

### Finding 4 — Adapter supervisor never started

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/adapter/supervisor/supervisor.go`

```
$ grep -rn "supervisor\\.\\(New\\|Start\\|Run\\)" --include="*.go" .
```
Hits only land in `internal/adapter/supervisor/supervisor_test.go`. The "host-side worker lifecycle for adapter sub-components" mentioned in the package doc has no host wiring. `Status` snapshots, restart, panic recovery — all unobservable.

**Operator consequence.** A panicking adapter Worker has nothing watching it; the host either crashes the process or the worker silently exits. The audit doc claims P1.1 supervisor RESOLVED — the code is there, but nothing calls it.

### Finding 5 — Webhook ingress is never mounted

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/webhook/webhook.go`

```
$ grep -rn "webhook\\.\\(NewHandler\\|Mount\\|New\\)" --include="*.go" .
internal/webhook/webhook.go:81: //  r.Mount("/webhooks", webhook.NewHandler(deps))
internal/webhook/webhook_test.go:30: ...
```
The mount example lives in a doc comment. No production caller.

**Operator consequence.** Vendor push-style ingress (HMAC-signed POST to `/webhooks/{adapter}`) cannot work — the route does not exist in the built binary.

### Finding 6 — Hydration service unused outside tests

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/hydration/hydration.go`

`hydration.New` only called from `hydration_test.go`. The `_include` / `_revinclude` MVP hydrator (commit `fb02d0c`) is not invoked by the scheduler.

**Operator consequence.** Subscriptions configured for `content: full-resource` cannot get hydrated bundles in production.

### Finding 7 — Admin routes mounted only in tests

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/api/handlers/admin.go:55`

```
$ grep -rn "RegisterAdminRoutes" --include="*.go" .
internal/api/handlers/admin.go:55:func RegisterAdminRoutes(r chi.Router, d Deps) {
internal/api/handlers/admin_test.go:41:    handlers.RegisterAdminRoutes(r, deps)
```
`cmd/fhir-subs/wiring.go:230` builds the router and only calls `handlers.RegisterRoutes(r, deps)`. `Deps.AdminToken` and `Deps.AdminPathPrefix` are never set; `Deps.DeadLetters` is never populated even though `wiring.go:153` constructs `dl := repos.NewDeadLettersRepo(cdc)`.

**Operator consequence.** `/admin/topics`, `/admin/subscriptions`, `/admin/dead_letters` HTTP paths return 404. Operators have no admin surface — the docs that promised one are wrong.

### Finding 8 — `RegisterPublicRoutes` is never called

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/api/handlers/router.go:354`

```
$ grep -rn "RegisterPublicRoutes" --include="*.go" .
cmd/fhir-subs/probes.go:41:// handlers.RegisterRoutes / RegisterPublicRoutes once the DB pool, auth
internal/api/handlers/subscription_handlers.go:1128:// surface (RegisterPublicRoutes). FHIR conformance probes hit
internal/api/handlers/router.go:349:// RegisterPublicRoutes wires the routes that MUST NOT be wrapped in
internal/api/handlers/router.go:354:func RegisterPublicRoutes(r chi.Router, d Deps) {
```
Only mentioned in comments. Per the comment at `subscription_handlers.go:1127-1129`, "FHIR conformance probes hit /metadata without a bearer token (S-2.1)" — but in production, `/metadata` is mounted **inside** the auth-wrapped `RegisterRoutes`. A probe without a bearer token gets 401. The audit/status doc claim S-2.1 RESOLVED disagrees with the code.

### Finding 9 — Production `PgAuditStore.Append` writes a placeholder hash

**Tag:** STUBBED **Severity:** BLOCKER
**Path:** `internal/api/handlers/pg_stores.go:506`

```go
row := repos.AuditLogRow{
    ...
    CanonicalForm: canonical,
    Hash:          []byte{0},
}
```
The comment two lines above (`pg_stores.go:490-493`) reads:
```
// Append writes a degenerate audit row (no hash-chain integrity) so the
// integration tests can observe the API recording events. Production
// deployments should wire the observability/audit module's hash-chained
// store instead — see infra/observability/audit.
```
But `cmd/fhir-subs/wiring.go:217` does exactly the opposite: `Audit: handlers.NewPgAuditStore(pool)`.

**Operator consequence.** Audit chain integrity is impossible because every row has the literal byte `0x00` for its `hash`. `audit verify` (Finding 12) compounds this: it queries different columns entirely.

### Finding 10 — `observability.Start` never called

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/infra/observability/observability.go:200`

```
$ grep -rn "observability\\.Start" --include="*.go" cmd/
(no hits)
```
The aggregator that owns the metrics emitter, the OTel tracer, the PHI-redacting logger, the chained-hash audit writer, and registers them with the dead-letter reporter (`SetDeadLetterReporter`) and matcher metrics emitter — is never started in production.

**Proof of cascade:** `cmd/fhir-subs/audit_cli.go:135` reads "the production wiring at observability.Start uses today" — but `observability.Start` is not in any `cmd/` file.

**Operator consequence.** Dead-letter counts (P1.12), matcher topic metrics (P1.5), audit trail integrity, and OTel traces are silently absent in production. The status doc lists P1.12 RESOLVED.

### Finding 11 — Audit schema mismatch (code vs migration)

**Tag:** MISSING **Severity:** BLOCKER
**Path:** `internal/infra/observability/audit/pgstore.go:88-90` vs `internal/infra/storage/migrate/migrations/0001_init.sql:226`

`pgstore.go` defines/queries:
```
prior_hash BYTEA NOT NULL,
chain_input BYTEA NOT NULL,
chain_hash BYTEA NOT NULL,
payload JSONB
```
`0001_init.sql` creates:
```
canonical_form bytea not null,
hash bytea not null,
prev_hash bytea
```
Different column names, plus the migration has no `payload` and no `chain_input`.

**Operator consequence.** Even if `observability.Start` were wired, `audit.Writer.Emit` would fail with `column "chain_hash" does not exist`. The `Migrate` method on PgStore (`pgstore.go:74`) creates the *correct* shape via `CREATE TABLE IF NOT EXISTS` — but only when the audit module's own bootstrap runs, and its statement is `IF NOT EXISTS` so against a database that already has the migration's `audit_log` table, the columns the code expects are simply missing.

### Finding 12 — `fhir-subs audit verify` runs against a table that doesn't have the columns it queries

**Tag:** MISSING **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/audit_cli.go:139`

```go
store, err := audit.NewPgStore(pool, audit.PgStoreOptions{})
...
res, err := audit.VerifyChainReport(ctx, store, audit.VerifyOptions{...})
```
`VerifyChainReport` uses `pgstore.go` SQL targeting `chain_hash, prior_hash, chain_input, payload`. The production migrations don't create those columns. The CLI verifies a non-existent shape.

**Operator consequence.** `fhir-subs audit verify` errors immediately on a real production DB. The published runbook recipe is broken.

### Finding 13 — Partition maintainer + retention sweeper never start

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `internal/infra/storage/storage.go:179`

```
$ grep -rn "storage\\.Start\\b" --include="*.go" .
internal/infra/storage/storage_test.go: ...
internal/infra/storage/integration_test.go: ...
internal/hl7processor/integration_test.go: ...
internal/matcher/integration_test.go: ...
internal/engine/submatcher/integration_test.go: ...
internal/engine/scheduler/integration_test.go: ...
```
Every `storage.Start` site is a `_test.go` file. Production wiring (`cmd/fhir-subs/wiring.go:84`) calls `pgxpool.NewWithConfig` directly. `storage.Start` is the only path that launches `partition.Run` (storage.go:226-237) and `retention.Run` (storage.go:240-253).

**Operator consequence.** The `resource_changes_YYYYMM` and `ehr_events_YYYYMM` partition tables are never auto-created or auto-dropped; `hl7_message_queue`, `deliveries`, `dead_letters`, `audit_log` rows are never retention-swept. Production tables grow unbounded until insert into a non-existent partition fails. The audit/status docs list S-13.* (storage retention) as RESOLVED.

### Findings 14-17 — Channel registry and channel implementations are dead

**Tag:** UNWIRED **Severity:** BLOCKER
**Paths:** `cmd/fhir-subs/wiring.go:178`, `internal/channel/{websocket,email,message}/`

Wiring does:
```
chReg := scheduler.NewMapRegistry()
chReg.Register("rest-hook", rhCh)
```
That's the entire scheduler-side channel registry. `internal/channel/websocket/`, `internal/channel/email/`, `internal/channel/message/` — none are constructed.

```
$ grep -rn "websocket\\.New\\|email\\.New\\|message\\.New" --include="*.go" cmd/
(no hits)
```

**Operator consequence.** A subscription with `channelType: websocket` (or `email` or `message`) goes through API create with success (because the no-op `defaultActivator{}` returns `HandshakeSucceeded`), then the scheduler tries to look up `websocket` in the registry, the lookup fails, the delivery goes to dead-letter or silently does nothing — and the subscriber never sees a notification. The B-14..B-18 channel hardening (RESOLVED in audit doc) is hardening on packages that production never executes.

### Finding 18 — Per-client rate limiters never instantiated

**Tag:** UNWIRED **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/wiring.go:210-228`

`Deps.SubscriptionCreateRateLimit` and `Deps.WSBindingTokenRateLimit` are typed `*auth.ClientRateLimiter`. The router (`internal/api/handlers/router.go:324, 332`) wraps the create / `$get-ws-binding-token` routes with `d.SubscriptionCreateRateLimit.Middleware()` unconditionally. `Middleware()` is nil-safe: when the receiver is nil it returns a pass-through (`internal/api/auth/handler_rate_limit.go:55-57`). Since wiring never sets the fields, both routes silently bypass rate limiting.

`cmd/fhir-subs/config.go:81-86` parses `subscription_create_rate_limit:` and `ws_binding_token_rate_limit:` blocks. They are read into `cfg.Auth.SubscriptionCreateRateLimit` and never plumbed onward.

**Operator consequence.** S-3.3 (per-client rate limit on `POST /Subscription` and `$get-ws-binding-token`) is **not enforced**. The audit doc lists S-3.3 RESOLVED.

### Finding 19 — HTTP server is cleartext only

**Tag:** HARDCODED **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/run.go:151`

```go
err := srv.Serve(listener)
```
Never `srv.ServeTLS(listener, certFile, keyFile)`. `cfg.Server.HTTP.TLS.CertFile` and `KeyFile` are validated by `Config.Validate` (`config.go:347-350`) — must be non-empty when `insecure=false` — but no code reads them at serve time. The TLS struct's docstring on `config.go:232` explicitly says "Real TLS wiring lands later."

**Operator consequence.** With `insecure=false` set (the production assertion path), `Validate` requires cert files; the server then ignores them and serves plaintext. Subscribers attempting `https://...` see TLS handshake failures.

### Finding 20 — MLLP listener TLS / mTLS never configured by wiring

**Tag:** HARDCODED **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/wiring.go:638-672`

`internal/mllp/config.go:14-31` defines `TLSConfig` and `RequireAndVerifyClientCert`. `EndpointConfig.TLS *TLSConfig` exists. `buildMLLPListener` never sets it; nothing in `cmd/fhir-subs/config.go` exposes a TLS sub-block for MLLP.

```
$ grep -ic "tls" cmd/fhir-subs/wiring.go
0
```
(only the comment word "TLS" appears in the activators_d2 context, never `tls.Config` / `*TLSConfig`.)

**Operator consequence.** MLLP traffic from a hospital interface engine transits cleartext. PHI in HL7 v2 messages on the wire. The audit doc lists B-20 RESOLVED — code support exists, but the production binary never enables it.

## HIGH findings

### Finding 21 — `/metrics` endpoint not mounted

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `internal/infra/observability/metrics/metrics.go:181`

`Emitter.Handler()` returns a `promhttp.Handler`. No call to `Handler()` from `cmd/`; the route `/metrics` is never registered in `buildHTTPMux` (`run.go:260-276`).

**Operator consequence.** Prometheus scrape returns 404 on whichever path the operator points at. No metrics can be ingested.

### Finding 22 — `internal/api/metrics` package never wired

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `internal/api/metrics/metrics.go`

`apimetrics.New` only called from `metrics_test.go`. Nine API metrics (`fhir_subs_api_requests_total`, `fhir_subs_api_request_duration_seconds`, `fhir_subs_api_auth_failures_total`, etc.) are declared and never incremented at request time.

### Finding 23 — OTel tracer never initialized in `cmd/`

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/main.go:151`

```
$ grep -rn "otel\\|OTLP\\|tracer\\.Start" --include="*.go" cmd/
(no hits)
```
The audit doc records P2.8 OTel exporter as "docs only — no code change." That is technically true; the consequence is that the otel-exporter-recipes runbook describes how to point the binary at an OTLP collector and the binary has no code to honor it.

### Finding 24 — Every vendor adapter's `MapToFHIR` is a stub

**Tag:** STUBBED **Severity:** HIGH
**Paths:** `adapters/cerner/cerner.go:77`, `adapters/epic/epic.go:96`, `adapters/athena/athena.go:78`, `adapters/nextgen/nextgen.go:80`, `adapters/meditech/meditech.go:78`, `adapters/allscripts/allscripts.go:80`

Cerner sample (representative of all vendor adapters):
```go
func (h *hl7Processor) MapToFHIR(_ spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
    return spi.FhirResource{
        ResourceType: "Bundle",
        Body:         []byte(`{"resourceType":"Bundle","type":"collection"}`),
    }, nil
}
```
The HL7 input bytes (lex'd into `parsed.Raw`) are dropped on the floor.

**Operator consequence.** A site that picks `adapter.id: epic` and pipes real HL7 ADT/ORU/ORM through MLLP gets a hardcoded empty Bundle pushed into the pipeline for every message. Subscriptions match against a Bundle with no clinical content; deliveries contain nothing useful.

### Finding 25 — Default adapter MapToFHIR also drops HL7

**Tag:** STUBBED **Severity:** HIGH
**Path:** `adapters/default/default.go:111-122`

Same pattern as Finding 24. The adapter the production binary actually loads (because it's the only one registered) is also a stub.

### Finding 26 — Only `default` adapter is registered

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:126`

```go
if regErr := adReg.Register("default", func() adapterspi.EhrAdapter { return defaultadapter.New() }); regErr != nil {
    ...
}
```
Cerner, Epic, Athena, NextGen, Meditech, Allscripts, Direct, Demo each have a `New()` constructor and a `NewRegistered()` helper. None are imported by `cmd/`. An operator config of `adapter.id: cerner` errors out with `UnknownAdapterError`. The "8 vendor adapters in the bundle" claim is not enforceable because the production binary doesn't bundle them.

### Finding 27 — `defaultActivator` always succeeds for websocket and email

**Tag:** STUBBED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:707-713`

```go
func (defaultActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
    return handlers.HandshakeSucceeded, nil
}
```
Used for `"websocket"` and `"email"` channels in the `channels` registry passed to `handlers.Deps`. The type-level comment admits "Without the placeholder, every newly created subscription would stay stuck at `requested`" — but that is exactly what the FHIR R5 spec mandates until a real handshake completes. The placeholder lies about activation.

### Finding 28 — `BaseURL` and `WSBaseURL` ignore `insecure` and bind interface

**Tag:** HARDCODED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:221-222`

```go
BaseURL:   "https://" + cfg.Server.HTTP.Bind,
WSBaseURL: "wss://" + cfg.Server.HTTP.Bind + "/ws",
```
With the default bind `0.0.0.0:8443` and `insecure=true` (a common dev/test default that frequently leaks into staging), the rendered CapabilityStatement.implementation.url is `https://0.0.0.0:8443` — a non-routable advertisement.

### Finding 29 — `WSBindingTTL` hardcoded

**Tag:** HARDCODED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:220`

```go
WSBindingTTL: 5 * time.Minute,
```
No corresponding entry in `cmd/fhir-subs/config.go`. Operators cannot tune the WebSocket binding-token TTL without a recompile.

### Finding 30 — CapabilityStatement omits SMART discovery extensions

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:210-228`

Wiring never sets `Deps.JWKSURL`, `Deps.TokenEndpointURL`, `Deps.SupportedFHIRVersions`, `Deps.FHIRVersion`. The `buildCapabilityStatement` (`subscription_handlers.go:1196`) gates the SMART OAuth-uris extension behind these fields being non-empty — so the rendered CapabilityStatement returns a generic shell.

### Finding 31 — JWKS endpoint is not served

**Tag:** MISSING **Severity:** HIGH
**Path:** `cmd/fhir-subs/run.go:260-275`

```
$ grep -rn "jwks\\b\\|/.well-known" --include="*.go" cmd/
(no hits)
```
The token endpoint mounts at `/token` (`wiring.go:233`); no companion JWKS endpoint. The verifier/token endpoint signs with `IssuedSecret` (HMAC) so a JWKS may not be needed when the signing material is symmetric — but the architecture promises JWKS-based discovery for clients that verify locally, and the doc/audit sections speak of JWKS publication.

### Finding 32 — Wildcard bind in default config

**Tag:** MISCONFIGURED-DEFAULT **Severity:** HIGH
**Path:** `cmd/fhir-subs/config.go:262-264`

```go
Server: ServerConfig{
    HTTP: HTTPConfig{
        Bind: "0.0.0.0:8443",
    },
},
```
`run.go:163-168` warns when bind is wildcard AND `insecure=true`, then continues. Default deployment is "listen on every interface, cleartext."

### Finding 33 — MLLP defaults baked into `buildMLLPListener`

**Tag:** HARDCODED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:638-672`

```go
ReadIdleTimeout:    30 * time.Second,
NackThenDropAfter:  5,
InflightCapPerConn: 64,
OnPersistFail:      mllp.OnPersistFailNack,
```
None of these are exposed through `MLLPConfig` (`cmd/fhir-subs/config.go:115-138`). `FrameAssemblyTimeout` is a YAML field but never passed into `mllp.ListenerConfig`.

### Finding 34 — Postgres pool has no operator tunables

**Tag:** MISCONFIGURED-DEFAULT **Severity:** HIGH
**Path:** `cmd/fhir-subs/pool.go:25-35`

`buildPoolConfig` only overrides `ConnConfig.ConnectTimeout`. `MaxConns`, `MinConns`, `MaxConnLifetime`, `MaxConnIdleTime`, `HealthCheckPeriod` are pgxpool defaults (4 max conns, no min, etc.). `DatabaseConfig` (`config.go:41-43`) is one field: `URL`.

### Finding 35 — `WebSocketChannelConfig` parsed and ignored

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/config.go:185-189`

`OriginPatterns`, `IdleTimeout`, `PingInterval` are parsed; nothing reads `cfg.Channels.WebSocket` in `wiring.go`.

### Finding 36 — `outbox`, `claim`, `partition`, `retention` packages unused outside `storage.Storage`

**Tag:** UNWIRED **Severity:** HIGH
**Paths:** `internal/infra/storage/{outbox,claim,partition,retention}/`

`storage.Storage` is the only host-side caller (lines 226-253 of `storage.go`); `storage.Start` is itself unused in `cmd/`. Therefore every one of these packages is reachable from production code only via a `_test.go` chain.

### Finding 37 — `wakeup` and `queue` packages have zero importers

**Tag:** UNWIRED **Severity:** HIGH
**Paths:** `internal/infra/wakeup/`, `internal/infra/queue/`

```
$ grep -rl "internal/infra/wakeup\\|internal/infra/queue" --include="*.go" .
(only their own files; no importers)
```

### Finding 38 — Engine `heartbeat`, `topicmatcher`, `topics/filter` never imported

**Tag:** UNWIRED **Severity:** HIGH
**Paths:** `internal/engine/heartbeat/`, `internal/engine/topicmatcher/`, `internal/topics/filter/`

```
$ grep -rl "internal/engine/heartbeat\\|internal/engine/topicmatcher\\|internal/topics/filter" --include="*.go" .
(no importers)
```
Phantom packages.

### Finding 39 — Empty topic catalog default = silent dead pipeline

**Tag:** MISCONFIGURED-DEFAULT **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:292-305`, `cmd/fhir-subs/topics.go:24-34`

`loadTopicSources("")` returns `catalog.Sources{}` without error. `catalog.Load(catalog.Sources{})` returns a `Catalog` with zero topics. The pipeline runs; the matcher walks zero topics; submatcher has nothing to deliver against; nothing fails.

The only signal an operator gets is `logger.Warn("topic catalog: nil after Load (treating as empty)")` — at WARN, not ERROR, and `topics.catalog_dir: ""` simply skips the warning.

**Operator consequence.** Drop the binary in a cluster with a forgotten ConfigMap mount; logs say "started"; subscriptions are accepted; nothing is ever delivered. Discoverable only by an end-to-end smoke test the operator must remember to run.

## MEDIUM findings

### Finding 40 — `dead-letters` CLI subcommand does not exist

**Tag:** MISSING **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/main.go:151`

```go
if len(args) > 0 && args[0] == "audit" {
    return runAuditSubcommand(args[1:], stdout, stderr)
}
```
The only subcommand is `audit`. `docs/operations/dead-letters-runbook.md:109` does say the `fhir-subs dead-letters list|replay|forget` admin CLI is "deferred"; the runbook is honest about it. Still, operators reading the runbook header may try the command before reaching that line.

### Finding 41 — Probe-only fallback on missing config silently degrades

**Tag:** MISCONFIGURED-DEFAULT **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/run.go:114-122`

```go
if cfg.Database.URL != "" {
    var rtErr error
    prod, rtErr = buildProductionRuntime(ctx, cfg, logger, lcMod)
```
A typo in `database.url` (or accidentally unset env var) turns a production deployment into "probe-only mode": `/healthz`, `/readyz`, `/metadata` work; nothing else. The pod marks Ready; the service routes traffic; clients hit 404 on `/Subscription`.

### Findings 42-44 — Hardcoded lifecycle / scheduler / processor tunables

**Tag:** HARDCODED **Severity:** MEDIUM
**Paths:** `cmd/fhir-subs/wiring.go:226`, `cmd/fhir-subs/wiring.go:341-345`, `cmd/fhir-subs/wiring.go:264-269`

- `Deps.ActivationTimeout: 30 * time.Second`
- Scheduler retry: `Initial 1s`, `Max 30s`, `Min 500ms`, `MaxAttempts 8` — all literals.
- HL7 processor `IdlePollInterval` falls back to 200ms when zero; no documented config knob for this in the YAML.

### Finding 45 — Rate-limit config block read but never applied

**Tag:** UNWIRED **Severity:** MEDIUM
**Path:** `internal/api/auth/handler_rate_limit.go`

Already covered in Finding 18; mentioned again here to flag the medium-severity manifestation: the YAML block `auth.subscription_create_rate_limit` parses cleanly but does nothing.

### Finding 46 — rest-hook activator HTTP transport not closed at shutdown

**Tag:** LEAKED **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/wiring.go:439-501`

`registerLifecycle` registers shutdowns for MLLP, pipeline drain, activation drain, DB pool — not the rest-hook activator's `http.Client.Transport` (`activators.go:73-89`). On graceful shutdown the keep-alive connections to subscriber endpoints linger.

### Finding 47 — `auth.allow_insecure_jwks` doubles as `urlValidator.AllowHTTP`

**Tag:** UNWIRED **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/wiring.go:181-183`

```go
urlValidator := handlers.NewURLValidator(handlers.URLValidatorConfig{
    AllowHTTP: cfg.Auth.AllowInsecure, // dev convenience: if insecure JWKS allowed, allow http endpoints too
})
```
Coupling means a single boolean controls two trust boundaries. Operators who flip the JWKS bypass for one reason silently relax SSRF protections on subscriber endpoints.

### Finding 48 — `TrustedIssuers` is advisory-only

**Tag:** MISCONFIGURED-DEFAULT **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/config.go:67-87`, `cmd/fhir-subs/config.go:104-112`

`AuthConfig.TrustedIssuers` is parsed; the field's own docstring (`config.go:104-107`) admits "today the fields are advisory: per-client trust is stored in the auth_clients table; this list pins which issuers' tokens the verifier will load JWKS for." Not actually pinned.

## LOW findings

### Finding 49 — `var _ channel.Channel` placeholder remains

**Tag:** UNWIRED **Severity:** LOW
**Path:** `cmd/fhir-subs/wiring.go:723-726`

The trailing line:
```go
// silence the unused-import diagnostic emitted while the package
// scaffolding is still being written. The reference here goes away
// once the e2e harness exercises every component.
var _ channel.Channel = channel.Channel(nil)
```
It is a literal admission, in production code, that the channel interface is not consumed by anything `wiring.go` actually wires.

### Finding 50 — Hardcoded activator placeholder for two channel types

**Tag:** HARDCODED **Severity:** LOW
**Path:** `cmd/fhir-subs/wiring.go:191-200`

`channels` map literal hardcodes `defaultActivator{}` for `"websocket"` and `"email"` keys; even when a future PR ships a real activator package, this map keeps using the placeholder.

## Cross-cuts

**The audit doc (`docs/production-readiness-audit.md`) and the status doc (`docs/status.md`) both attest "B-20 RESOLVED" and "S-3.3 RESOLVED" and "P1.5 RESOLVED" and "P1.12 RESOLVED" and "S-2.1 RESOLVED."** Those claims are based on the existence of code in the relevant package, not on whether `cmd/fhir-subs/wiring.go` integrates that code. Findings 18, 20, 22, 30, 31, 36 directly contradict those claims.

**The fundamental shape of the gap.** The `internal/` tree contains a complete-looking subscriptions service. `cmd/fhir-subs/` contains a thin entry-point that wires roughly half of it. The unwired half includes: scan ingestion, vendor change-feed ingestion, hydration, webhook ingress, admin surface, supervisor lifecycle, OTel + Prometheus metrics, hash-chained audit, partition/retention background workers, every channel except rest-hook, every vendor adapter except `default`, all rate limiters, all TLS support.

**The package-vs-binary discipline is broken everywhere.** Test code uses real production constructors (`storage.Start`, `supervisor.New`, `webhook.NewHandler`, `scanrunner.New`, `vendorclient.New`, `hydration.New`) and proves they work in isolation. The binary uses none of them. The result: every "story marked Done" can pass unit tests and CI yet contribute zero behavior to a deployed pod.

## Notes on what would change the count materially

- If `RegisterAdminRoutes` is mounted from a *different* binary entry point (a subcommand or a sidecar), Finding 7 downgrades. There is no other entry point in this repo.
- If `audit verify` is meant to be run only against an audit DB that the operator provisions separately with `PgStore.Migrate`, Findings 11/12 downgrade — but `cmd/fhir-subs/audit_cli.go:139` constructs the store with `PgStoreOptions{}` (no schema, no Migrate call), and the docstring at line 135 explicitly says the verifier connects to "what the production wiring at observability.Start uses today." Both can't be true.
- If `internal/infra/storage/storage.Start` was deliberately superseded by the inline `pgxpool.NewWithConfig` + `migrate.Up` path in `wiring.go:80-114` (i.e., partition + retention were moved elsewhere), Finding 13 downgrades — but the inline path is missing the partition maintainer and retention sweeper goroutines entirely. There is no alternate launcher.
- If a downstream `cmd/` binary not in this repo (e.g., a future `cmd/fhir-subs-admin/`) wires the dead packages, this audit overstates the gap by ~half. There is no such binary in `git ls-tree`.
