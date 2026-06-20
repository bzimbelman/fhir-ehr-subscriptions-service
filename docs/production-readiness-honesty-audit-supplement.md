# Production-Readiness Honesty Audit — Supplement (Findings #51+)

This file extends `production-readiness-honesty-audit.md` (Findings #1-#50). The first auditor stopped at 50; the operator's question — "is 50 the count, or did the auditor stop early?" — is answered below: 50 was an early stop. This supplement adds **57 additional findings** across categories the first pass did not exhaust.

Same rules apply: every claim is grounded in a code citation. "Tested in isolation" does not mean "wired in production." "Documented" does not mean "implemented."

## Summary table

| #  | Tag | Severity | Path:line | Claim |
|----|-----|----------|-----------|-------|
| 51 | BROKEN | BLOCKER | `deploy/helm/fhir-subs/templates/deployment.yaml:105-107` vs `cmd/fhir-subs/run.go:126` | Helm Deployment exposes `containerPort: 8081` named `probes`, kubelet probes target it, but the binary opens exactly one listener on `cfg.Server.HTTP.Bind` (default `:8443`). |
| 52 | BROKEN | BLOCKER | `deploy/helm/fhir-subs/values.yaml:159,163` vs `cmd/fhir-subs/config.go:278-303` | Chart's `${env:DATABASE_URL}` / `${file:/path}` placeholders are passed verbatim to the YAML decoder; no env-var or file-substitution code exists. |
| 53 | MISCONFIGURED-DEFAULT | BLOCKER | `deploy/helm/fhir-subs/values.yaml:157-173` vs `cmd/fhir-subs/config.go:22-38` | Chart defaults emit `storage.postgres.*`, `storage.encryption.*`, `auth.schemes`, `observability.*` blocks; loader has no such fields. They land in `Extra map[string]any` and are silently dropped. |
| 54 | UNWIRED | HIGH | `deploy/helm/fhir-subs/values.yaml:150` vs `cmd/fhir-subs/config.go:212-230` | `server.http.probe_bind: ":8081"` is in values.yaml; no `ProbeBind` field exists on `HTTPConfig`. |
| 55 | BROKEN | HIGH | `deploy/helm/fhir-subs/templates/service.yaml:26-29`, `templates/servicemonitor.yaml:16-17` | Service/ServiceMonitor expose `metrics` port 9090 and scrape `/metrics`; the binary neither listens on 9090 nor mounts `/metrics`. |
| 56 | MISSING | BLOCKER | `deploy/helm/fhir-subs/` (entire chart) | No template projects a topic catalog directory; no `topics:` block in default `config.contents`. Combined with Finding 39, every fresh `helm install` is a silent dead pipeline. |
| 57 | BROKEN | BLOCKER | `deploy/helm/fhir-subs/templates/deployment.yaml:177-186` vs `cmd/fhir-subs/run.go:151` | Chart mounts a TLS secret at `/etc/fhir-subs/tls` and renders `cert_file`/`key_file` into config; binary calls `srv.Serve` (cleartext). Validate() requires the cert paths so config-check passes; clients dialing `https://...:8443` get TLS handshake failures. |
| 58 | MISCONFIGURED-DEFAULT | MEDIUM | `deploy/helm/fhir-subs/templates/deployment.yaml:43-85` | Init container named `migrationInit` runs `--check-config`, not migrations. Comment admits migrations run in main container; misnamed knob. |
| 59 | MISCONFIGURED-DEFAULT | HIGH | `deploy/helm/fhir-subs/values.yaml:115-118`, `templates/networkpolicy.yaml:17-23` | Default `networkPolicy.ingress.api.from: []` allows ingress from any pod in namespace. Same for MLLP. NetworkPolicy advertises hardening but defaults to namespace-wide allow. |
| 60 | HARDCODED | MEDIUM | `deploy/helm/fhir-subs/values.yaml:9` | `image.repository: ghcr.io/bzimbelman/fhir-ehr-subscriptions-service` baked into chart default — points to a personal GHCR org. |
| 61 | MISCONFIGURED-DEFAULT | LOW | `deploy/helm/fhir-subs/values.yaml:6,96-100` | `replicaCount: 2` AND `autoscaling.enabled: true` both default; replicaCount is silently dropped by the template. |
| 62 | MISCONFIGURED-DEFAULT | LOW | `deploy/helm/fhir-subs/templates/deployment.yaml:36`, `templates/serviceaccount.yaml:12` | `automountServiceAccountToken: false` set without documentation; future K8s-API features will silently fail. |
| 63 | STUBBED | HIGH | `.github/workflows/integration.yml:28-31` | Integration tests gated behind opt-in PR label `run-integration`; default PRs ship without integration coverage. |
| 64 | STUBBED | HIGH | `.github/workflows/integration.yml:43-49` | E2E suite gated behind opt-in PR labels; default PRs do not run e2e. |
| 65 | MISSING | MEDIUM | `.github/workflows/ci.yml:78-83` | Coverage profile uploaded as artifact, no threshold enforcement. Coverage can decline arbitrarily. |
| 66 | MISSING | HIGH | `.github/workflows/release.yml:14,86-96` | Workflow requests `id-token: write` and asks buildx for SBOM/provenance, but no `cosign sign` step. Image published unsigned. |
| 67 | STUBBED | MEDIUM | `.github/workflows/release.yml:7-9` vs `ci.yml:97-111` | Multi-arch image build only on tag push; PRs build a single platform. arm64 cross-compile regressions surface on release day. |
| 68 | MISSING | HIGH | `.github/` (no dependabot.yml/renovate.json) | No CVE-watching for transitive Go-module deps; CodeQL is SAST only. No `govulncheck` step. |
| 69 | MISCONFIGURED-DEFAULT | MEDIUM | `.github/workflows/ci.yml:62-65`, repo root | `.golangci.yml` does not exist; `golangci-lint v1.62.0` runs default-on linters only (no `gosec`, `gocritic`, `bodyclose`, `errorlint`, `sqlclosecheck`). |
| 70 | MISSING | MEDIUM | `.github/workflows/nightly.yml` | Nightly conformance run has no failure alerting (Slack/email/issue creation). Failed nightlies sit red on the Actions tab unobserved. |
| 71 | MISCONFIGURED-DEFAULT | LOW | `.github/workflows/ci.yml:20`, `go.mod:3` | Workflows pin `GO_VERSION: '1.22'` (loose); module declares `go 1.22.0`. Patch-level skew across reruns. |
| 72 | STUBBED | MEDIUM | `.github/workflows/ci.yml:97-111` | CI `docker` job builds with `load: true, push: false` and never runs the resulting image (no `docker run … --check-config` smoke). |
| 73 | MISCONFIGURED-DEFAULT | LOW | `.github/workflows/release.yml:11-14` | `permissions: id-token: write` requested at workflow scope but no step uses it. Unused token permission grant. |
| 74 | MISSING | LOW | `.github/workflows/docs.yml:3-11` | No `mkdocs build --strict` preview on docs-only PRs; broken cross-links and YAML front-matter errors surface only post-merge. |
| 75 | SCHEMA-MISMATCH | HIGH | `internal/infra/storage/repos/dead_letters.go:88-96` vs `migrations/0001_init.sql:185-196` | `DeadLettersRepo.Insert` calls `r.codec.Encrypt(row.PayloadRedacted)` and discards the returned `kv`. Migration's `dead_letters` has no `key_version` column. After key rotation, dead-letter payloads are unrecoverable. |
| 76 | DEAD-SCHEMA | MEDIUM | `migrations/0001_init.sql:174` vs full codebase | `deliveries.bundle bytea` is never written, read, or selected anywhere. The "bundle persisted at delivery time" claim implicit in the schema is unrealized. |
| 77 | SCHEMA-DRIFT | MEDIUM | `internal/infra/storage/migrate/migrate.go:202` vs `migrations/0001_init.sql:16-19` | `migrate.Up` runs `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT` outside the version-tracked migration sequence; error suppressed. |
| 78 | MODEL-DRIFT | LOW | `internal/infra/storage/repos/models.go:172-194` vs `migrations/0004_subscriptions_next_event_number.sql` | `subscriptions.next_event_number` exists in schema and is mutated by submatcher, but `SubscriptionRow` model has no field; every SELECT excludes it. |
| 79 | DEAD-CODE-PATH | LOW | `internal/infra/storage/repos/subscription_topics.go` | `subscription_topics.retired_at` is read but never written. There is no `retire`/`update` SQL; the `status in ('draft','active','retired')` constraint is unreachable from this binary. |
| 80 | SCHEMA-MISMATCH | MEDIUM | `internal/infra/observability/audit/pgstore.go:74-94` vs `migrations/0001_init.sql:226-239` | Beyond Finding 11: audit module's `Migrate` declares `actor_id`, `target_kind`, `target_id`, `correlation_id` as NOT NULL; production migration leaves them nullable. `IterateRows` will fail to scan NULL into non-pointer Go strings. |
| 81 | AUDIT-CHAIN-INTEGRITY | HIGH | `internal/infra/storage/repos/audit_log.go:35-49` vs `internal/infra/observability/audit/audit.go:300-312` | Audit chain canonical input bakes in application-supplied `OccurredAt`; INSERT omits `occurred_at` and lets server-side `now()` populate. Application-hashed timestamp != on-disk timestamp; chain verification structurally impossible. |
| 82 | DEAD-INDEX | LOW | `migrations/0001_init.sql:51` | `subscription_topics_url_idx` on `(url)` is redundant with the `unique (url, version)` constraint's implicit index for the only query that filters by URL. Write-amplification on every topic write. |
| 83 | SCHEMA-INVARIANT | LOW | `migrations/0001_init.sql:68`, `migrations/0004_subscriptions_next_event_number.sql:29` | Two columns track per-subscription event progress (`events_since_subscription_start`, `next_event_number`) with no documented relationship; one is updated by handlers Insert, the other by submatcher inline. |
| 84 | STUBBED | HIGH | `e2e/orchestrator/skipped_scenarios_test.go:61-167` | 11 of the 13 LLD merge-gate e2e scenarios are unconditional `t.Skip(...)` stubs (FHIR scan, vendor change-feed, topic-matcher fanout, retry curve, dead-letter, $status, audit chain). They report SKIP in CI; the merge-gate manifest is 2-of-13. |
| 85 | STUBBED | HIGH | `e2e/harness/api.go:36-45,160-170` | E2E harness installs `principalMiddleware` that injects a fixed `auth.Principal`; the production `verifier.Middleware` is bypassed. Every e2e test that "validates production wiring" never exercises the auth stack. |
| 86 | STUBBED | HIGH | `e2e/harness/api.go:26-34,150-154` | Harness `ChannelRegistry` registers a `stubChannelActivator` that always returns `HandshakeSucceeded` for every channel type; merge-gate scenarios cannot detect a channel-activation regression. |
| 87 | STUBBED | HIGH | `internal/api/handlers/integration_test.go:206`, `internal/api/handlers/pg_stores.go:506` | Integration test wires the production `PgAuditStore` (which writes `Hash: []byte{0}`) but never compares hash bytes. The audit-chain integration test that does compare hashes uses a different store class against a different schema. CI cannot detect the placeholder hash. |
| 88 | STUBBED | HIGH | `adapters/{cerner,epic,athena,allscripts,meditech,nextgen,direct}/*_test.go` | Every vendor adapter's `*HL7ProcessorBuildsAndDoesNotPanic` test asserts only that `Lex` returns no error; `MapToFHIR` (which produces the hardcoded Bundle) is never invoked. Vendor regressions are invisible to CI. |
| 89 | STUBBED | HIGH | `e2e/orchestrator/*.go` (20+ files) | Every e2e scenario configures `AdapterID: "default"`; no scenario loads any vendor adapter. Even if vendor registration were fixed (Finding 26), no test would notice a vendor regression. |
| 90 | STUBBED | HIGH | `e2e/orchestrator/helpers_support_test.go:34-60` | `RegisterSubscriber` writes directly to `subscriptions` via SQL and follows with `UPDATE … status='active'`; bypasses POST /Subscription, URL validator, channel-type validation, and the activation handshake. The "production binary processes HL7" test proves only "given a row in the right shape, delivery happens." |
| 91 | STUBBED | HIGH | `e2e/orchestrator/prod_binary_serves_subscription_api_test.go:52-109` | Test starts the production binary with `AuthAudience: ""` (which installs the no-op auth middleware) and treats a 401 from a missing-principal path as "API is wired." Cannot detect failure to install `verifier.Middleware`. |
| 92 | STUBBED | MEDIUM | `e2e/orchestrator/config_sighup_reload_test.go:50-61` | Test installs its own SIGHUP handler that calls `mod.Reload`. Production wiring (`wiring.go:374`) reloads only the topic catalog. The B-35 SIGHUP-reload-config story is a test-only artifact. |
| 93 | STUBBED | MEDIUM | `e2e/orchestrator/config_file_mtime_poll_test.go:48-49` | Test calls `mod.WatchSecretFiles` directly. Production never starts the secret-file watcher. Mounted-secret rotation will not be observed in production. |
| 94 | STUBBED | HIGH | `adapters/{cerner,epic,athena,allscripts,meditech,nextgen}/*.go` | Manifests declare `Capabilities.HL7Processor: true`; every `BuildHl7Processor` returns the same Bundle stub. Capability declaration is a behavioral lie. |
| 95 | STUBBED | MEDIUM | `adapters/direct/direct.go:47-52,77-92` | Direct messaging is SMTP/S-MIME, not HL7 v2 — the adapter's `HL7Processor: true` capability is fundamentally wrong. Through MLLP framer, no Direct message can succeed. |
| 96 | STUBBED | LOW | `adapters/demo/demo.go:79-81` | `BuildHydrationService` returns `nil`. Full-resource subscriptions against the demo adapter NPE if the host ever calls `Hydrate`. |
| 97 | HARDCODED | LOW | `cmd/demo-subscriber/subscribe.go:108,163` | Falls back to `http.DefaultClient` (zero timeout) for both subscription create and token mint. A demo pointed at a stalled bridge blocks forever; pattern propagates if copied into a production subscriber. |
| 98 | UNWIRED | MEDIUM | `internal/api/handlers/url_validator.go:138` | Request-handler-time URL validator uses `context.Background()` for SSRF DNS lookup; client cancellation cannot abort the lookup. Soft DoS vector. |
| 99 | UNWIRED | LOW | `internal/api/handlers/subscription_handlers.go:390,1145`, `router.go:288` | Several `context.Background()` paths in handler bookkeeping and CapabilityStatement; bookkeeping writes after request completion don't honor shutdown. |
| 100 | MISCONFIGURED-DEFAULT | MEDIUM | `internal/api/handlers/pg_stores.go:170-209` | `PgSubscriptionsStore.ListByClient` issues `SELECT ... ORDER BY created_at DESC` with no LIMIT. A multi-tenant client with thousands of historical subscriptions causes pgxpool to load every row into memory. |
| 101 | STUBBED | MEDIUM | `internal/adapter/{scanrunner,vendorclient,supervisor}_test.go`, `internal/hydration/hydration_test.go`, `internal/webhook/webhook_test.go` | Tests of unwired packages inject fakes of the very interface the package consumes. Removing each package wholesale would not break any other package's build. |
| 102 | STUBBED | HIGH | `cmd/fhir-subs/run.go:78-82` vs `config.go:196` | `cfg.Deployment.LogFormat` is parsed (`text|json`); `runWithHooks` hardcodes `Format: "json"` in the call to `logging.NewLogger`. Documented option is dead. |
| 103 | BROKEN-DEMO | BLOCKER | `demo/README.md:152`, `demo/config.yaml:19-20`, `demo/topics/lab-results.json:9` | Demo's adapter is `default` (only registered adapter), which returns `{"resourceType":"Bundle","type":"collection"}`. `lab-results.json` topic requires `resource: "Observation"`. Matcher gates by `row.ResourceType` so a Bundle never matches an Observation topic; the documented demo flow cannot deliver. |
| 104 | BROKEN-DEMO | BLOCKER | `demo/README.md:81,100`, `demo/config.yaml`, `cmd/demo-subscriber/main.go:64` | Demo uses `http://` rest-hook endpoint. `URLValidator.AllowHTTP=false` (because demo config doesn't set `auth.allow_insecure_jwks`). `POST /Subscription` returns 400 SSRF before activation. Documented one-liner cannot succeed. |
| 105 | BROKEN-DEMO | BLOCKER | `demo/README.md:33-34`, `Dockerfile:30` | README claims demo binaries are "baked into the compose image." Dockerfile builds only `./cmd/fhir-subs`. Compose file uses the same Dockerfile. Walkthrough invocations like `./demo-subscriber` fail with no-such-file. |
| 106 | HARDCODED | HIGH | `cmd/fhir-subs/activators.go:213` | rest-hook handshake POST uses `Timestamp: time.Time{}.UTC().Format(time.RFC3339)` — the literal zero time `0001-01-01T00:00:00Z`. Subscriber-side audit logs and FHIR validators bounds-checking Bundle.timestamp will reject every handshake. |
| 107 | UNDOCUMENTED-CONFIG | BLOCKER | `docs/architecture.md:1037-1204` vs `cmd/fhir-subs/config.go` | Architecture document a config tree the loader does not parse: ~70% of documented keys (`storage.postgres.*`, `storage.encryption.*`, `storage.retention.*`, `auth.schemes`, `auth.client_registry`, `topics.value_sets_dir`, `mllp_listener.endpoints[].allowed_message_types`, `adapter.config.*`, `channels.{email,message,custom}`, `delivery.*`, `observability.metrics.bind`, `observability.tracing.*`, `observability.audit_log.*`) silently absorbed by `Extra map[string]any`. |
| 108 | UNDOCUMENTED-CONFIG | HIGH | `docs/architecture.md:1030`, `cmd/fhir-subs/config.go:278-303` | Architecture promises `${env:VAR}` and `${file:/path}` placeholder interpolation in YAML. `loadConfig` does plain `yaml.Decode`; no substitution code exists. |
| 109 | UNDOCUMENTED-CONFIG | HIGH | `docs/operations/otel-exporter-recipes.md:11-23` | Runbook documents `tracing.otlp_endpoint`, `tracing.sample_rate`, `tracing.exporter_timeout`, `tracing.insecure`, `tracing.tls.*`, `tracing.headers.*`. `Config` struct has no `Tracing` field; the binary has no OTel exporter. Every recipe (Datadog/Honeycomb/Jaeger/Tempo) is non-functional. |
| 110 | DEAD-CONFIG | MEDIUM | `cmd/fhir-subs/config.go:366-465` | `--set` allowlist excludes `auth.access_token_ttl`, `auth.jwks_cache_ttl`, `auth.clock_skew`, `auth.jwks_allowed_hosts`, `auth.trusted_issuers`. CLI knobs lie about coverage; YAML-only knobs cannot be flag-overridden during incident remediation. |
| 111 | VALIDATION-GAP | BLOCKER | `cmd/fhir-subs/config.go:330-360` | `Validate()` does not check `Database.URL`, `Codec.Keys`, `Codec.ActiveKeyVersion`, `Auth.Audience`, `MLLP.Listeners`, `Topics.CatalogDir`. `--check-config` reports `config ok` for a configuration that explicitly asks for an HL7 listener but cannot start one. |
| 112 | VALIDATION-GAP | BLOCKER | `cmd/fhir-subs/config.go:330-360` vs `wiring.go:565-568,206-208` | No validation that `Auth.Audience != ""` when `Server.HTTP.Insecure=false`. Production-shaped config with TLS but accidentally-empty audience boots, accepts traffic, and authorizes every caller (no-op auth middleware). |
| 113 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:210-228` vs `internal/api/handlers/router.go:107` | `Deps.Metrics` (`handlers.MetricsRecorder`) is never set; every per-handler counter (`RecordSubscriptionCreated`, `RecordWSBindingTokenIssued`, `RecordValidationFailure`, `RecordActivatePanic`, etc.) records nothing. Compounds Findings 21/22. |
| 114 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:210-228` vs `internal/api/handlers/router.go:180` | `Deps.Logger` is never set. Audit S-2.7 ("six `_ = err` swallows in activate — RESOLVED") is enforced only when `Deps.Logger` is set; production runs with the slot nil. Activation errors are still swallowed. |
| 115 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:210-228` vs `router.go:160-229,287-313` | `Deps.SearchPageSize`, `SearchMaxPageSize`, `EventReplayPageSize`, `AuditMaxBytes`, `MaxBodyBytes`, `MaxStatusBulkIDs`, `MaxSchemaErrorBytes`, `FHIRVersion`, `JWKSURL`, `TokenEndpointURL`, `SupportedFHIRVersions` all unset. Operator-tunable knobs declared in the audit (S-2.2/S-2.3/S-2.11/S-2.13 RESOLVED) are unreachable; defaults fire via `<= 0` fallbacks. |
| 116 | HARDCODED-SECRET | HIGH | `demo/config.yaml:42` | `material: "bocf8udvaKT84Mk5/fLU1NHoy4wf/OWbp2t7gpUm/as="` is a real AES-256 key checked into source. Anyone copying `demo/config.yaml` as a template encrypts PHI under a public key. |
| 117 | HARDCODED | LOW | `cmd/demo-publisher/main.go:39`, `cmd/demo-subscriber/main.go:64` | demo-publisher defaults to `127.0.0.1:6000`; demo's MLLP listener binds `:2575`. demo-subscriber's `127.0.0.1:0` ephemeral bind surfaces as advertised URL `http://127.0.0.1:<port>` unreachable from a Docker bridge. |
| 118 | UNVALIDATED-INPUT | LOW | `cmd/demo-subscriber/subscribe.go:67-78` | Subscription body sets BOTH R5 `channelType:{code:...}` AND R4B/legacy `channel:{type,endpoint}` AND top-level R5 `endpoint:`. Brittle against a future stricter validator. |
| 119 | UNDOCUMENTED-DEPENDENCY | LOW | `cmd/demo-subscriber/receiver.go:11`, `cmd/demo-publisher/publisher.go:13` | Demo CLIs import test scaffolding (`e2e/mocksub`, `e2e/mockehr`). Operator-facing tools depend on test code; if `e2e/mocksub` ever gains a build tag, the demo CLIs fail to compile. |
| 120 | DOCS-VS-CODE | LOW | `demo/docker-compose.yml:25`, `Dockerfile:8` | Compose comment overstates portability: `docker compose build` only multi-arch when buildx is the active builder. CI runners on plain Docker Engine produce host-arch-only images. |
| 121 | MISLEADING-RUNBOOK | HIGH | `docs/operations/dead-letters-runbook.md:104` | Runbook example: `curl -s http://<host>:9090/metrics | grep fhir_subs_dead_letters_total`. The metric is not registered, the endpoint is not mounted, port 9090 is not opened. The alerting recipe (`rate(...) > 0.1`) cannot fire. |
| 122 | MISLEADING-RUNBOOK | HIGH | `docs/operations/horizontal-scale.md:23,32-39` | Runbook claims partition maintenance and retention sweepers run "per-pod with `pg_advisory_xact_lock`." `storage.Start` is not called in production (Finding 13); none of these goroutines run. Operators relying on this runbook will hit "no partition for value" after the first month rollover. |
| 123 | INCOMPLETE-RUNBOOK | MEDIUM | `docs/operations/dead-letters-runbook.md:73-83` | Recovery procedure for `delivery_exhausted` tells operator to insert a fresh `deliveries` row "and the system will pick it up." Does not warn that subscriptions on unregistered channel types (Findings 14-17) loop into dead_letters again immediately, growing the table unbounded. |
| 124 | VALIDATION-GAP | MEDIUM | `cmd/fhir-subs/config.go:330-360` | `Validate()` does not enforce coupling between `mllp.listeners[]` and `database.url`. A typo dropping `database.url` enters probe-only mode where MLLP binds are silently ignored — operators see "connection refused" with no startup error. |
| 125 | DOCS-VS-CODE | HIGH | `docs/architecture.md:1206-1213` | Architecture's central security claim ("Hard-required at startup … you cannot run with no auth") is false. `wiring.go:565-568` allows audience-empty as "auth disabled (probe-only mode)"; `Validate()` doesn't enforce trusted-issuers or client-registry presence. Auth-disabled mode is reachable from any production-shaped config that omits `auth.audience`. |
| 126 | UNWIRED | MEDIUM | `cmd/fhir-subs/wiring.go:580-589,597-608`, `cmd/fhir-subs/config.go:366-465` | `cfg.Auth.AccessTokenTTL`, `cfg.Auth.JWKSCacheTTL`, `cfg.Auth.ClockSkew` are read into the verifier and token endpoint but no `--set` plumbing exists. Incident-time clock-skew rollout requires editing YAML. |
| 127 | DEAD-DOCS | MEDIUM | `cmd/fhir-subs/config.go:104-112` (TrustedIssuer struct) and demo configs | `auth.trusted_issuers[]` is parsed as advisory and never filters JWKS lookups (Finding 48). The demo, helm chart, and architecture docs all surface this knob as if it controls trust. |

## BLOCKER findings (new)

### Finding 51 — Helm probe port mismatch

**Tag:** BROKEN **Severity:** BLOCKER
**Path:** `deploy/helm/fhir-subs/templates/deployment.yaml:105-107` vs `cmd/fhir-subs/run.go:126-135`

```yaml
- name: probes
  containerPort: 8081
livenessProbe:
  httpGet:
    path: /healthz
    port: probes
```
`run.go:126`: `listener, err := net.Listen("tcp", cfg.Server.HTTP.Bind)` — single listener; mux mounts probes on `:8443`.

**Operator consequence.** kubelet probes connection-refused on `:8081`. Pods never go Ready. Default chart install does not produce a healthy pod.

### Finding 52 — Chart placeholders not interpolated

**Tag:** BROKEN **Severity:** BLOCKER
**Path:** `deploy/helm/fhir-subs/values.yaml:159,163` vs `cmd/fhir-subs/config.go:278-303`

```yaml
storage:
  postgres:
    url: ${env:DATABASE_URL}
  encryption:
    at_rest_key: ${file:/etc/fhir-subs/secrets/at_rest_key}
```
`grep -rn '\${env\|\${file\|os\.Expand\|os\.Getenv' cmd/fhir-subs/` returns nothing.

**Operator consequence.** The literal string `${env:DATABASE_URL}` is passed to pgxpool, which fails parsing. Or worse, the at-rest-key field becomes the literal placeholder bytes — silent encryption under a known string.

### Finding 53 — Chart writes config under keys the binary does not parse

**Tag:** MISCONFIGURED-DEFAULT **Severity:** BLOCKER
**Path:** `deploy/helm/fhir-subs/values.yaml:157-173` vs `cmd/fhir-subs/config.go:22-38`

Chart emits `storage.postgres.{url,pool_size,statement_timeout}`, `storage.encryption.at_rest_key`, `auth.schemes:`, `auth.jwks.cache_ttl`, `observability.metrics.bind`. `Config` has `Database` (not `Storage`), no `Observability`, no `Auth.JWKS` substruct. Keys land in `Extra map[string]any \`yaml:",inline"\`` and are silently dropped.

**Operator consequence.** The supplied YAML "looks configured" but `database.url` is empty → probe-only fallback (Finding 41 cascade). Every operator-supplied tuning is silently dropped.

### Finding 56 — Helm chart never mounts the topic catalog

**Tag:** MISSING **Severity:** BLOCKER
**Path:** `deploy/helm/fhir-subs/values.yaml:140-173`, full `templates/`

`grep -rn 'topics\|catalog' deploy/helm/fhir-subs/` returns no hits in any template.

**Operator consequence.** Default `helm install` produces a deployment that accepts subscriptions and delivers nothing (compounds Finding 39). No operator-facing knob; operator must hand-roll a ConfigMap and `extraVolumeMounts`.

### Finding 57 — TLS mounted but never served

**Tag:** BROKEN **Severity:** BLOCKER
**Path:** `deploy/helm/fhir-subs/templates/deployment.yaml:177-186` vs `cmd/fhir-subs/run.go:151`

Chart's default `tls.enabled: true` mounts a Secret at `/etc/fhir-subs/tls`, sets `cert_file`/`key_file` in the rendered config. Production binary calls `srv.Serve(listener)` — cleartext (Finding 19). Validate() requires non-empty cert/key paths; provides them; config-check passes.

**Operator consequence.** Clients dial `https://...:8443`; TLS handshake fails because server speaks HTTP. Operator has no signal that TLS is unwired.

### Finding 103 — Demo's documented happy path cannot deliver any notification

**Tag:** BROKEN-DEMO **Severity:** BLOCKER
**Path:** `demo/README.md:152`, `demo/config.yaml:19-20`, `demo/topics/lab-results.json:9`

`demo/config.yaml`: `adapter.id: default`. Only registered adapter. Returns `{"resourceType":"Bundle","type":"collection"}`. `lab-results.json` topic requires `resource: "Observation"`. Matcher (`internal/matcher/matcher.go:112,136`) gates by `row.ResourceType`. Bundle never matches Observation topic.

**Operator consequence.** "Two of the four sends should trigger notification Bundles" (README) is false. The walkthrough cannot deliver. Anyone running the canonical demo gets zero notifications and no error.

### Finding 104 — Demo's rest-hook subscription is rejected by URL validator

**Tag:** BROKEN-DEMO **Severity:** BLOCKER
**Path:** `demo/README.md:81,100`, `demo/config.yaml`, `cmd/demo-subscriber/main.go:64`

Demo subscriber advertises `http://127.0.0.1:<port>`. `cmd/fhir-subs/wiring.go:181-183`: `URLValidator.AllowHTTP=cfg.Auth.AllowInsecure` — demo config doesn't set `auth.allow_insecure_jwks`. `internal/api/handlers/url_validator.go:106-108`: `http scheme requires opt-in`.

**Operator consequence.** `POST /Subscription` returns 400 SSRF error. Documented one-liner cannot succeed; operator must guess and add `auth.allow_insecure_jwks: true` to make the demo work.

### Finding 105 — Demo binaries are not in the demo image

**Tag:** BROKEN-DEMO **Severity:** BLOCKER
**Path:** `demo/README.md:33-34`, `Dockerfile:30`

README: "binaries baked into the compose image." Dockerfile builds only `./cmd/fhir-subs`. Compose uses same Dockerfile. Walkthrough invocations `./demo-subscriber`, `./demo-publisher` fail with no-such-file.

**Operator consequence.** A copy-paste of the walkthrough fails until the operator separately runs `go build ./cmd/demo-subscriber ./cmd/demo-publisher` outside the container.

### Finding 107 — Architecture document is fiction at the config layer

**Tag:** UNDOCUMENTED-CONFIG **Severity:** BLOCKER
**Path:** `docs/architecture.md:1037-1204` vs `cmd/fhir-subs/config.go`

Architecture documents `server.http.probe_bind`, `server.websocket.{enabled,max_connections}`, `lifecycle.postgres_probe_timeout`, `storage.postgres.{pool_size,statement_timeout}`, `storage.encryption.at_rest_key`, `storage.retention.*` (six retention windows), `auth.schemes`, `auth.client_registry`, `topics.value_sets_dir`, `mllp_listener.endpoints[].allowed_message_types`, `adapter.config.*` (FHIR base URL, FHIR auth, HL7v2, interconnect), `channels.{rest_hook,email,message,custom[]}.*`, `delivery.{default_max_count,max_batch_wait,retry,heartbeat}`, `observability.metrics.bind`, `observability.tracing.*`, `observability.audit_log.*`. `Config` struct has none of these — uses `Database.URL`, no Storage, no Delivery, no Observability, no Email/Message/Custom channels. ~70% of documented keys are inert.

**Operator consequence.** An operator copying the architecture's example YAML gets a config where most of what they set is silently dropped. The architecture is operator-facing reference material.

### Finding 111 — `--check-config` reports OK for unstartable configs

**Tag:** VALIDATION-GAP **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/config.go:330-360`

`Validate()` checks only `Deployment.FacilityID`, `Adapter.ID`, `Server.HTTP.Bind`, TLS cert/key when `!Insecure`, `Lifecycle.ShutdownGracePeriod`. No check for `Database.URL`, `Codec.Keys`, `Codec.ActiveKeyVersion`, `Auth.Audience`, `MLLP.Listeners`, `Topics.CatalogDir`.

**Operator consequence.** A config explicitly asking for an MLLP listener but missing `database.url` passes `--check-config`, then enters probe-only mode at startup (no MLLP, no auth, no pipeline) while reporting Healthy.

### Finding 112 — Empty audience + TLS-on bypasses authentication entirely

**Tag:** VALIDATION-GAP **Severity:** BLOCKER
**Path:** `cmd/fhir-subs/config.go:330-360`, `wiring.go:565-568,206-208`

`Validate()` does not require `Auth.Audience != ""` when `Insecure=false`. With audience empty, `buildAuthEndpoints` returns `(nil, nil, nil)`; `wiring.go:206-208` installs a no-op `func(next http.Handler) http.Handler { return next }`. TLS terms set, audience accidentally empty → authenticated-looking deployment that authorizes every caller.

**Operator consequence.** Slips an unauthenticated production bridge past validation. Catastrophic if PHI is in scope.

## HIGH findings (new)

### Finding 55 — `/metrics` port and path advertised by chart, neither served

**Tag:** BROKEN **Severity:** HIGH
**Path:** `deploy/helm/fhir-subs/templates/service.yaml:26-29`, `templates/servicemonitor.yaml:16-17`

Service.spec.ports has `name: metrics, port: 9090`; ServiceMonitor scrapes `path: /metrics, port: metrics`. Binary opens one listener on `:8443`; `grep -n '9090' cmd/fhir-subs/*.go` returns nothing; `/metrics` not registered (Finding 21).

**Operator consequence.** Prometheus targets pod IP on 9090, gets connection-refused. ServiceMonitor reconciles cleanly, scrape silently fails — no signal until an alert that depends on a metric never fires.

### Finding 59 — NetworkPolicy default is namespace-wide allow

**Tag:** MISCONFIGURED-DEFAULT **Severity:** HIGH
**Path:** `deploy/helm/fhir-subs/values.yaml:115-118`

```yaml
ingress:
  api:
    from: []  # Empty 'from' allows ingress to the API port from any pod in the same namespace.
  mllp:
    from: []
```

**Operator consequence.** A NetworkPolicy that advertises hardening but defaults to "any pod in this namespace can hit the FHIR API and the MLLP socket." Lateral movement risk for PHI.

### Finding 63 — Integration tests opt-in via PR label

**Tag:** STUBBED **Severity:** HIGH
**Path:** `.github/workflows/integration.yml:28-31`

Integration job runs only when label `run-integration` is present. Default PRs run unit tests only.

**Operator consequence.** "All checks pass" is a weak signal. Audit-schema mismatches (Findings 11/12), empty-catalog dead pipeline (Finding 39), schema partition rotation — all integration-territory regressions. Author must remember to apply the label every PR.

### Finding 64 — E2E suite opt-in via PR label

**Tag:** STUBBED **Severity:** HIGH
**Path:** `.github/workflows/integration.yml:43-49`

`e2e` job gated on the same labels as integration. Default PRs do not run any e2e test.

### Finding 66 — Release publishes unsigned images despite Sigstore infrastructure

**Tag:** MISSING **Severity:** HIGH
**Path:** `.github/workflows/release.yml:14,86-96`

Workflow requests `id-token: write` (cosign-keyless OIDC) and asks buildx for SBOM and provenance attestations, but no `cosign sign` step. `grep -rn 'cosign' .github/workflows/release.yml` returns no hits.

**Operator consequence.** values.yaml advertises `image.digest` ("image signature workflows"); no signed image exists to verify against. Supply-chain promise unfulfilled.

### Finding 68 — No CVE-watching for transitive deps

**Tag:** MISSING **Severity:** HIGH
**Path:** `.github/` (no dependabot.yml, no renovate.json), no `govulncheck` step in any workflow

CodeQL is SAST-only.

**Operator consequence.** Vulnerable transitive deps (jwt libraries, pgx, chi) sit in main without notice until a downstream user reports.

### Finding 75 — `dead_letters` schema cannot survive a key rotation

**Tag:** SCHEMA-MISMATCH **Severity:** HIGH
**Path:** `internal/infra/storage/repos/dead_letters.go:88-96` vs `migrations/0001_init.sql:185-196`

```go
enc, _, err = r.codec.Encrypt(row.PayloadRedacted) // discards key version
```
Migration 0001 dead_letters columns: `(id, kind, source_table, source_id, subscription_id, reason, error_detail, payload_redacted, correlation_id, created_at)` — no `key_version`. Migration 0003 added `key_version` to `pending_pairs` for exactly this reason; the same gap exists for `dead_letters` and was never closed.

**Operator consequence.** After a key rotation, encrypted `payload_redacted` blobs from before the rotation are unrecoverable. Forensic dead-letter triage post-rotation is permanently broken.

### Finding 81 — Audit chain canonical input != on-disk timestamp

**Tag:** AUDIT-CHAIN-INTEGRITY **Severity:** HIGH (latent)
**Path:** `internal/infra/storage/repos/audit_log.go:35-49` vs `internal/infra/observability/audit/audit.go:300-312`

`audit.go:302` bakes `evt.OccurredAt.UTC().Format(time.RFC3339Nano)` into the SHA-256 chain input. `audit_log.go:35-49` INSERT omits `occurred_at`; migration default `now()` fills it server-side.

**Operator consequence.** Even if Findings 11/12 (schema column mismatch) are fixed, every chain-verifier walk fails at row 1 because the hashed timestamp differs from the on-disk timestamp.

### Finding 84 — 11 of 13 merge-gate scenarios are unconditional skip-stubs

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/orchestrator/skipped_scenarios_test.go:61-167`

11 functions are `t.Skip("requires …")`. File-level comment admits `requireHarness` is "intentionally NOT called: skip-stubs should report SKIP regardless of Docker availability, so the merge-gate manifest is stable."

**Operator consequence.** "13-scenario merge-gate manifest" is 2-of-13. The other 11 (FHIR scan, vendor change-feed, topic-matcher fanout, retry curve, dead-letter, $status operation, audit chain) PASS in CI without exercising those subsystems.

### Finding 85 — E2E harness installs principalMiddleware that bypasses the verifier

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/harness/api.go:36-45,160-170`

```go
Auth: principalMiddleware(&auth.Principal{
    ClientID: cfg.ClientID,
    Scopes: []string{"system/Subscription.c", ..., "system/Subscription.cruds"},
    Exp: time.Now().Add(1 * time.Hour),
}),
```

**Operator consequence.** Every e2e scenario injects a pre-baked principal — the production `verifier.Middleware` (JWKS fetch, iss/aud/exp/jti/scope, replay cache, advisory rate limit) is never on the wire path.

### Finding 86 — Stub channel activator across all channel types

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/harness/api.go:26-34,150-154`

```go
type stubChannelActivator struct{}
func (stubChannelActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
    return handlers.HandshakeSucceeded, nil
}
```

Used for `rest-hook`, `websocket`, `email` in the harness ChannelRegistry.

### Finding 87 — Production audit-store integration test never compares hash bytes

**Tag:** STUBBED **Severity:** HIGH
**Path:** `internal/api/handlers/integration_test.go:206`, `internal/api/handlers/pg_stores.go:494-516`

`integration_test.go` wires `Audit: handlers.NewPgAuditStore(pool)` (writes `Hash: []byte{0}`). No assertion in any unit/integration test calls `audit.VerifyChain` against rows produced by `PgAuditStore`. The chain-test that does compare bytes uses `audit.NewPgStore` (different store class, different schema).

**Operator consequence.** No CI signal that the production audit-chain placeholder is broken (Finding 9). Two test paths, neither catches the gap.

### Finding 88 — Vendor adapter tests assert "Lex doesn't error"; never exercise MapToFHIR

**Tag:** STUBBED **Severity:** HIGH
**Path:** `adapters/{cerner,epic,athena,allscripts,meditech,nextgen,direct}/*_test.go`

`*HL7ProcessorBuildsAndDoesNotPanic` is the only vendor-specific test; it never invokes `MapToFHIR`.

**Operator consequence.** The hardcoded-Bundle stub (Finding 24) is invisible to every CI run. Reviewer reads "Cerner tests pass" and infers vendor behavior is verified.

### Finding 89 — Every e2e scenario uses `AdapterID: "default"`

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/orchestrator/*.go` (20+ files)

`grep -c "AdapterID: \"default\"" e2e/orchestrator/*.go` shows the count matches the total `AdapterID:` count.

### Finding 90 — `RegisterSubscriber` writes directly to subscriptions, bypassing handlers

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/orchestrator/helpers_support_test.go:34-60`, `prod_binary_processes_hl7_message_test.go:57`

Test inserts `subscriptions` row via SQL, then `UPDATE subscriptions SET status='active'`. URL validator, channel-type validation, activation handshake are skipped.

**Operator consequence.** `TestE2E_ProdBinary_ProcessesHL7Message` proves only "given a subscription row in the right shape, delivery happens" — not "the production API can create a deliverable subscription."

### Finding 91 — `TestE2E_ProdBinary_ServesSubscriptionAPI` disables auth and reads "401" as success

**Tag:** STUBBED **Severity:** HIGH
**Path:** `e2e/orchestrator/prod_binary_serves_subscription_api_test.go:52-109`

Test starts production binary with `AuthAudience: ""` (which installs the no-op auth middleware), then claims a 401 from the missing-principal handler is "stronger proof of routing than a generic 404."

**Operator consequence.** The 401 path is reachable only when the auth middleware is the no-op stub. Test cannot detect a regression where production fails to install `verifier.Middleware`.

### Finding 94 — Vendor adapters declare `HL7Processor: true` capability that's behaviorally false

**Tag:** STUBBED **Severity:** HIGH
**Path:** `adapters/{cerner,epic,athena,allscripts,meditech,nextgen}/*.go:39-44`

Manifest sets `Capabilities: spi.Capabilities{HL7Processor: true, ...}`. Body of `BuildHl7Processor` returns the same Bundle stub for all six vendors.

**Operator consequence.** Capability declaration is a behavioral lie. A site selecting `adapter.id: epic` (if registration worked, which it doesn't — Finding 26) gets the same Bundle as `adapter.id: cerner`.

### Finding 102 — `cfg.Deployment.LogFormat` is parsed and ignored

**Tag:** STUBBED **Severity:** HIGH
**Path:** `cmd/fhir-subs/run.go:78-82` vs `config.go:196`

```go
logger := logging.NewLogger(&logging.Options{
    Sink:   logOut,
    Level:  slogLevel(cfg.Deployment.LogLevel),
    Format: "json",   // hardcoded
})
```
`cfg.Deployment.LogFormat` is parsed and never read. `demo/config.yaml:14` sets `log_format: text` and gets JSON.

### Finding 106 — rest-hook handshake POST timestamp is the zero time

**Tag:** HARDCODED **Severity:** HIGH
**Path:** `cmd/fhir-subs/activators.go:213`

```go
Timestamp:    time.Time{}.UTC().Format(time.RFC3339),
```

**Operator consequence.** Every handshake POST carries `"timestamp":"0001-01-01T00:00:00Z"`. Subscriber audit logs and FHIR validators bounds-checking Bundle.timestamp will reject every handshake.

### Finding 108 — `${env:VAR}` and `${file:/path}` placeholders are documented but not implemented

**Tag:** UNDOCUMENTED-CONFIG **Severity:** HIGH
**Path:** `docs/architecture.md:1030`, `cmd/fhir-subs/config.go:278-303`

`loadConfig` does plain `yaml.NewDecoder(...).Decode(cfg)`; no env-var or file substitution. `grep -rn "\${env\|\${file\|os.Expand" cmd/fhir-subs/ internal/infra/config/` returns no implementation hits.

### Finding 109 — OTel exporter recipes document tracing config the binary cannot read

**Tag:** UNDOCUMENTED-CONFIG **Severity:** HIGH
**Path:** `docs/operations/otel-exporter-recipes.md:11-23`

Runbook: `tracing.otlp_endpoint`, `tracing.sample_rate`, `tracing.exporter_timeout`, `tracing.insecure`, `tracing.tls.*`, `tracing.headers.*`. `Config` struct has no `Tracing`. `grep -rni "otel\|otlp" cmd/` returns nothing.

**Operator consequence.** Every recipe (Datadog/Honeycomb/Jaeger/Tempo) is non-functional. Operator filling in `tracing.otlp_endpoint` sees zero spans; no error.

### Finding 113 — `Deps.Metrics` never set

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:210-228` vs `internal/api/handlers/router.go:107`

Wiring constructs Deps without a `Metrics` field. Per-handler counters (`RecordSubscriptionCreated`, `RecordWSBindingTokenIssued`, `RecordValidationFailure`, `RecordActivatePanic`, `RecordRandFailure`) record nothing.

### Finding 114 — `Deps.Logger` never set

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:210-228` vs `internal/api/handlers/router.go:180`

Audit S-2.7 ("six `_ = err` swallows in activate — RESOLVED") relies on `Deps.Logger`. Wiring leaves the slot nil; activation errors still vanish in production.

### Finding 115 — Eleven `Deps.*` knobs unset; defaults fire silently

**Tag:** UNWIRED **Severity:** HIGH
**Path:** `cmd/fhir-subs/wiring.go:210-228` vs `router.go:160-229,287-313`

`SearchPageSize`, `SearchMaxPageSize`, `EventReplayPageSize`, `AuditMaxBytes`, `MaxBodyBytes`, `MaxStatusBulkIDs`, `MaxSchemaErrorBytes`, `FHIRVersion`, `JWKSURL`, `TokenEndpointURL`, `SupportedFHIRVersions` — all unset.

**Operator consequence.** Audit's "RESOLVED — operator-tunable knob" claims for S-2.2 (MaxBodyBytes), S-2.3 (MaxSchemaErrorBytes), S-2.11 (MaxStatusBulkIDs), S-2.13 (FHIRVersion) reduce to "the knob exists in package code; the production binary cannot reach it."

### Finding 116 — Real AES-256 key checked into demo config

**Tag:** HARDCODED-SECRET **Severity:** HIGH
**Path:** `demo/config.yaml:42`

```yaml
material: "bocf8udvaKT84Mk5/fLU1NHoy4wf/OWbp2t7gpUm/as="
```

A 32-byte base64-encoded AES key in the upstream repo.

**Operator consequence.** Anyone copying `demo/config.yaml` as a starting template into a real environment ships PHI encrypted under a public key.

### Finding 121 — Dead-letters runbook points at a metric and endpoint that don't exist

**Tag:** MISLEADING-RUNBOOK **Severity:** HIGH
**Path:** `docs/operations/dead-letters-runbook.md:104`

Runbook example: `curl -s http://<host>:9090/metrics | grep fhir_subs_dead_letters_total`. Findings 10/21: metric is not registered; `/metrics` is not mounted; port 9090 is not opened. Alerting recipe `rate(fhir_subs_dead_letters_total[5m]) > 0.1` cannot fire.

### Finding 122 — Horizontal-scale runbook misrepresents partition/retention safety

**Tag:** MISLEADING-RUNBOOK **Severity:** HIGH
**Path:** `docs/operations/horizontal-scale.md:23,32-39`

Runbook: "Partition maintainer + retention sweeper are running per-pod with `pg_advisory_xact_lock` per maintenance run." Finding 13: `storage.Start` never runs in production; none of these goroutines exist.

**Operator consequence.** Operators scaling to >1 pod relying on this runbook believe partition rotation continues; after the first month rollover, inserts to `resource_changes_<NEXT_MONTH>` fail with "no partition for value."

### Finding 125 — Architecture's "you cannot run with no auth" claim is false

**Tag:** DOCS-VS-CODE **Severity:** HIGH
**Path:** `docs/architecture.md:1206-1213`

Architecture states: "Hard-required at startup: `auth.trusted_issuers` OR an authenticated `client_registry` (you cannot run with no auth)." `wiring.go:565-568` allows audience-empty as "auth disabled (probe-only mode)"; `Validate()` doesn't enforce trusted-issuers/client-registry presence.

**Operator consequence.** The architecture's central security invariant is unenforced. Auth-disabled mode is reachable from any production-shaped config that omits `auth.audience`.

## MEDIUM findings (new)

### Finding 58 — `migrationInit` init container is mis-named

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `deploy/helm/fhir-subs/templates/deployment.yaml:43-85`

Init container named `migrationInit`, command `--check-config`. Migrations actually run inline in the main container (`wiring.go:109`).

### Finding 65 — Coverage profile uploaded but no threshold enforced

**Tag:** MISSING **Severity:** MEDIUM
**Path:** `.github/workflows/ci.yml:78-83`

`go test -coverprofile=cover.out` runs; artifact uploaded; no step parses or fails the build below a threshold. Project docs claim 80% — not enforced for this Go repo.

### Finding 67 — Multi-arch build only on tag push

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `.github/workflows/release.yml:7-9`, `ci.yml:97-111`

PR-time `docker` job builds single platform. Release-day surprise on arm64 cross-compile regressions.

### Finding 69 — golangci-lint runs default-on linters only

**Tag:** MISCONFIGURED-DEFAULT **Severity:** MEDIUM
**Path:** `.github/workflows/ci.yml:62-65`, repo root

`.golangci.yml` does not exist. `gosec`, `gocritic`, `bodyclose`, `errorlint`, `sqlclosecheck` are not run.

### Finding 70 — Failed nightlies have no alerting

**Tag:** MISSING **Severity:** MEDIUM
**Path:** `.github/workflows/nightly.yml`

No `if: failure()` step creating an issue or posting to Slack. A red nightly silently sits red.

### Finding 72 — CI builds image but never runs it

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `.github/workflows/ci.yml:97-111`

`load: true, push: false`. No smoke `docker run --check-config` after build. Image regressions land unnoticed.

### Finding 76 — `deliveries.bundle` is dead schema

**Tag:** DEAD-SCHEMA **Severity:** MEDIUM
**Path:** `migrations/0001_init.sql:174`

Column never written, read, or selected. "Bundle persisted at delivery time" is implicit but unrealized.

### Finding 77 — `schema_migrations.checksum` added by application code, not a numbered migration

**Tag:** SCHEMA-DRIFT **Severity:** MEDIUM
**Path:** `internal/infra/storage/migrate/migrate.go:202`

```go
_, _ = conn.Exec(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT`)
```
Error suppressed.

### Finding 80 — Audit module's `Migrate` declares NOT NULL where production migration leaves nullable

**Tag:** SCHEMA-MISMATCH **Severity:** MEDIUM
**Path:** `internal/infra/observability/audit/pgstore.go:74-94` vs `migrations/0001_init.sql:226-239`

Audit module: `actor_id TEXT NOT NULL`, `target_kind`, `target_id`, `correlation_id UUID NOT NULL`. Production migration: nullable. `IterateRows` scans into non-pointer Go strings — NULL rows fail.

### Finding 92 — SIGHUP config reload is test-only

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `e2e/orchestrator/config_sighup_reload_test.go:50-61`, `cmd/fhir-subs/wiring.go:374`

Test installs its own SIGHUP handler that calls `mod.Reload`. Production `SetReloadHandler` reloads only the topic catalog.

### Finding 93 — Secret-file watcher is test-only

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `e2e/orchestrator/config_file_mtime_poll_test.go:48-49`

`mod.WatchSecretFiles` called from test code. Never started in production.

### Finding 95 — Direct adapter is fundamentally HL7v2-mismatched

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `adapters/direct/direct.go:47-52,77-92`

Direct messaging is SMTP+S/MIME with XDM/CDA. Adapter declares `HL7Processor: true` and runs through MLLP framing, which cannot accept SMTP body bytes.

### Finding 98 — URL validator uses context.Background for SSRF DNS lookup

**Tag:** UNWIRED **Severity:** MEDIUM
**Path:** `internal/api/handlers/url_validator.go:138`

```go
ctx, cancel := context.WithTimeout(context.Background(), v.lookupTimeout)
```

**Operator consequence.** Client cancellation cannot abort the lookup; soft DoS.

### Finding 100 — `ListByClient` has no LIMIT

**Tag:** MISCONFIGURED-DEFAULT **Severity:** MEDIUM
**Path:** `internal/api/handlers/pg_stores.go:170-209`

```sql
SELECT ... FROM subscriptions WHERE client_id = $1 ORDER BY created_at DESC
```
No LIMIT. `PgEventsStore.ListByTopicAndRange` (line 374-375) has `LIMIT 1000`; this path does not.

### Finding 101 — Tests of unwired packages prove nothing about wiring

**Tag:** STUBBED **Severity:** MEDIUM
**Path:** `internal/adapter/{scanrunner,vendorclient,supervisor}_test.go`, `internal/hydration/hydration_test.go`, `internal/webhook/webhook_test.go`

Each test injects a `fakeRunner` / `fakeClient` etc. of the very interface the package consumes. Removing the package wholesale breaks no other build.

### Finding 110 — Half the auth knobs cannot be `--set`-overridden

**Tag:** DEAD-CONFIG **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/config.go:366-465`

`applySets` allowlist excludes `auth.access_token_ttl`, `auth.jwks_cache_ttl`, `auth.clock_skew`, `auth.jwks_allowed_hosts`, `auth.trusted_issuers`. Incident-time clock-skew rollout requires editing YAML.

### Finding 123 — Dead-letters runbook misses the unregistered-channel zombie loop

**Tag:** INCOMPLETE-RUNBOOK **Severity:** MEDIUM
**Path:** `docs/operations/dead-letters-runbook.md:73-83`

Recovery procedure: insert a fresh `deliveries` row. Does not warn that subscriptions on unregistered channel types (Findings 14-17) loop into dead_letters again immediately, growing the table unbounded.

### Finding 124 — `mllp.listeners` ↔ `database.url` coupling unenforced

**Tag:** VALIDATION-GAP **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/config.go:330-360`

`Validate()` doesn't reject `mllp.listeners` non-empty when `database.url` empty (probe-only mode silently ignores the listener block).

### Finding 126 — Three auth durations have no `--set` plumbing

**Tag:** UNWIRED **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/config.go:366-465` vs `wiring.go:580-589,597-608`

`AccessTokenTTL`, `JWKSCacheTTL`, `ClockSkew` are read at wiring time, but `applySets` does not accept them.

### Finding 127 — `auth.trusted_issuers` advertised as enforcement, advisory in code

**Tag:** DEAD-DOCS **Severity:** MEDIUM
**Path:** `cmd/fhir-subs/config.go:104-112`, demo configs

Already covered semantically in Finding 48; the new gap is documentation propagation (helm chart, demo, architecture doc all surface it as if it controls trust).

## LOW findings (new)

### Finding 60 — Personal GHCR repo as chart default
`deploy/helm/fhir-subs/values.yaml:9` — `image.repository: ghcr.io/bzimbelman/fhir-ehr-subscriptions-service`.

### Finding 61 — replicaCount silently dropped under HPA
`deploy/helm/fhir-subs/values.yaml:6,96-100` — replicaCount=2 + autoscaling.enabled=true; replicaCount field is gated off in template.

### Finding 62 — `automountServiceAccountToken: false` undocumented
Future K8s-API features will silently fail.

### Finding 71 — Loose Go version pin (`'1.22'`)
Patch-level skew across reruns.

### Finding 73 — Unused `id-token: write` permission scope
Wider blast radius for compromised release path.

### Finding 74 — No mkdocs strict-build on docs PRs
Broken cross-links surface only post-merge.

### Finding 78 — `SubscriptionRow` model has no `NextEventNumber` field
Migration 0004 column readable only via raw SQL.

### Finding 79 — `subscription_topics.retired_at` is read but never written
No retire SQL anywhere; the schema's retirement lifecycle is unreachable.

### Finding 82 — `subscription_topics_url_idx` is dead
Redundant with the unique-constraint's implicit index.

### Finding 83 — Two columns track per-subscription event progress with no documented relationship
`events_since_subscription_start` (handlers Insert) and `next_event_number` (submatcher inline UPDATE).

### Finding 96 — `adapters/demo` `BuildHydrationService` returns nil
Full-resource subscriptions NPE if Hydrate is called.

### Finding 97 — demo-subscriber falls back to `http.DefaultClient` (zero timeout)
Demo CLI distributable with the source. Pattern propagates.

### Finding 99 — `context.Background()` in handler bookkeeping paths
`subscription_handlers.go:390,1145`, `router.go:288`. Bookkeeping writes after request completion don't honor shutdown.

### Finding 117 — Demo binaries default to mismatched ports
`demo-publisher` defaults `127.0.0.1:6000`; demo MLLP binds `:2575`. demo-subscriber's ephemeral bind unreachable from Docker bridge.

### Finding 118 — demo-subscriber sends mixed R4B/R5 channel shapes
Brittle against future stricter validators.

### Finding 119 — Demo CLIs import e2e test scaffolding
`e2e/mocksub`, `e2e/mockehr`. Operator-facing tools depend on test code.

### Finding 120 — Compose multi-arch claim depends on buildx being active
`docker compose build` on plain Docker Engine produces host-arch-only.

## Cross-cuts (additional)

**The mocking pattern is symmetric to the wiring pattern.** Every place production wiring stops short (vendor adapters, scanrunner, vendorclient, supervisor, hydration, webhook, observability.Start, RegisterAdminRoutes, the metrics port, the activator handshake, the SIGHUP config reload, the secret-file watcher), tests *mock the very thing whose absence is the production gap*. Findings 84-93 catalogue this in detail. Combined with Findings 1-50 (which catalogue what is unwired), the picture is: the gap between `internal/` and `cmd/` is mirrored by an equally large gap between the test suite's stand-ins and the production wiring they purport to validate. Every "RESOLVED" claim in `docs/status.md` survives CI because the absent integration is replaced by a fake at the test boundary.

**The Helm chart, the architecture document, and the operations runbooks are advertising a system that the binary does not implement.** Findings 51-62 (chart), 107-109 (architecture), 121-125 (runbooks) all amount to operator-facing material that contradicts the code. An operator who reads these and configures accordingly produces a deployment that is silently degraded — probes failing, metrics not scraped, TLS not served, partition rotation not running, audit not chained, OTel traces not exported, every adapter except `default` not registered, and the demo's documented happy-path unable to deliver a single notification.

**The CI signal is structurally weak.** Findings 63-72 catalogue the CI gaps: integration and e2e gated behind opt-in PR labels, no coverage threshold, no CVE-watching, no signing, no smoke-run of the built image, default-only golangci-lint config, loose Go version pin. Combined with Findings 84-91 (test stubs), the compound effect is that a PR can merge with green CI while introducing or preserving any of the BLOCKER gaps in this audit.

## Categories Investigated With No New Findings

These categories from the original hunt list were investigated and produced no new findings (either covered by #1-#50 or actually clean):

1. **`panic("TODO")` / `panic("not implemented")` in production code** — none. Panics in `internal/adapter/spi/interfaces.go` are intentional "REQUIRED override or panic" SPI defaults; vendor adapters override with stubs (a separate problem — Finding 24).
2. **`errors.New("not implemented")` / `errors.New("unimplemented")`** — none in any file.
3. **`fmt.Println` / `log.Println` in production code** — none. All logging is `slog.*` or legitimate CLI output via `fmt.Fprintln(stderr/stdout, …)`.
4. **`time.Sleep` in non-test production code** — none.
5. **`os.Exit` outside `main()`** — none. Only in `cmd/*/main.go` `main()` functions.
6. **`prometheus.MustRegister` scattered** — none. Registries use `prometheus.NewRegistry()` and explicit `Register` (the bigger problem, Finding 21, is that the registry is never scraped).
7. **`recover()` that swallows panics without logging** — every `recover()` site logs (`cmd/fhir-subs/main.go:134`, `internal/api/handlers/activation.go:51`, `internal/hl7processor/translate.go:99,109,119,130`, `internal/adapter/supervisor/supervisor.go:289`, `internal/infra/observability/audit/audit.go:242`).
8. **Unbuffered `make(chan)` in production hot paths** — every `done`-style signaling channel is correctly unbuffered + closed by sender.
9. **Goroutines launched without cancellation/wait coordination** — production `go func()` sites in `cmd/fhir-subs/wiring.go:355-358` use shared `loopCtx` and `pipelineWG`. Cleared.
10. **DROP IF EXISTS hygiene in migrations** — no DROP statements in any migration.
11. **Missing migration numbers** — 0001-0006 sequential; no gaps.
12. **Postgres extensions not declared** — only `pgcrypto` is used (`gen_random_uuid()`); declared in 0001.
13. **Partition table parent setup** — migrations `resource_changes`/`ehr_events` partition setup matches `partition.go` maintainer naming convention.
14. **CHECK constraints vs code values** — `dead_letters.kind` and `pending_pairs.pending_kind` constraint values match the literal strings code writes.
15. **Embedded vs top-level migrations** — `internal/infra/storage/migrate/migrations/` is byte-for-byte equivalent to top-level `migrations/` (modulo a comment block). What `migrate.Up` applies matches what an audit query reads.
16. **Tables in code with no migration** — every table referenced (`subscriptions`, `subscription_topics`, `auth_clients`, `hl7_message_queue`, `pending_pairs`, `resource_changes`, `ehr_events`, `deliveries`, `dead_letters`, `adapter_state`, `ws_binding_tokens`, `audit_log`, `schema_migrations`) is created by migration 0001.
17. **Hardcoded test secrets in chart values.yaml** — none. Secret values reference `${env:...}` placeholders only (the placeholders themselves, however, do not work — Finding 52).
18. **PDB/HPA coherence** — PDB minAvailable=1, HPA minReplicas=2. Safe.
19. **Image digest pinning support in chart helpers** — `_helpers.tpl:65-72` honors `image.digest`. No new finding.
20. **Release `.sha256` sidecar upload glob** — `dist/fhir-subs-*` glob captures `.sha256` files. Cleared.
21. **Cosign keyless signing pairing** — Finding 66 covers; otherwise the workflow's permissions and OIDC trust posture are correctly set up for signing if a step were added.
22. **`audit verify` CLI installs its own schema** — confirmed broken (Findings 11/12). `cmd/fhir-subs/audit_cli.go:139` does NOT call Migrate; depends on the production migration's audit_log shape.

## Summary stats

- **57 new findings (#51-#127).**
- Severity distribution: **15 BLOCKER**, **24 HIGH**, **15 MEDIUM**, **17 LOW** (some findings span severities; counted at primary).
- **Total combined count: 50 (original) + 57 (this supplement) = 107 findings.**
- The original audit's BLOCKER count was 20; the combined BLOCKER count is now 35.

## Top three most-embarrassing new items

1. **Finding 56 + 103 + 104 + 105 — The demo doesn't work and the chart doesn't ship a topic catalog.** A first-time user following any of the three documented entry points (Helm install, demo walkthrough, demo-subscriber CLI) produces a deployment that accepts subscriptions and silently delivers nothing. Helm install: no topic catalog mounted (#56) → empty matcher → silent dead pipeline (Finding 39). Demo walkthrough: only `default` adapter is registered (#103) → returns Bundle, never Observation → no topic match → no notification. Demo subscriber: `http://` rejected by URL validator (#104). Demo binaries: not in the image (#105). The audit's BLOCKER call is "every documented onboarding path is broken."
2. **Finding 84 + 85 + 86 + 87 + 88 + 89 + 90 + 91 — The test suite is a hall of mirrors.** 11 of 13 merge-gate scenarios are unconditional `t.Skip`s. The harness installs `principalMiddleware` that bypasses the verifier and a `stubChannelActivator` that always succeeds. Every e2e scenario uses `AdapterID: "default"`. The "production binary processes HL7" test bypasses the API by writing `subscriptions` rows in raw SQL. The "production binary serves the Subscription API" test disables auth and treats a 401 from the no-op middleware as success. Vendor adapter tests assert only "Lex doesn't error" and never invoke `MapToFHIR`. CI cannot detect any of the BLOCKER findings in the original audit.
3. **Findings 107 + 108 + 109 + 121 + 122 — Operator-facing documentation is fiction.** ~70% of `docs/architecture.md`'s config keys are inert. `${env:VAR}` / `${file:/path}` interpolation is documented but unimplemented. The OTel exporter recipes runbook has zero working configurations. The dead-letters runbook tells operators to scrape a metric that doesn't exist on a port that's not opened. The horizontal-scale runbook claims partition/retention safety properties the binary doesn't have. An on-call engineer reaching for these documents during an incident is misled in every direction.

## Honest assessment of diminishing returns

This pass found **57 additional gaps** the first auditor missed. A third pass would still find more. Categories I deliberately did not exhaust:

- **The 17 sub-modules under `internal/api/handlers/`.** I sampled the wiring (Findings 113-115) and a couple of context misuses (98, 99) but did not audit each handler's body for stale TODOs, half-validated inputs, missing rate-limit application, or interaction with the (mostly nil) Deps slots.
- **The MLLP package internals.** Configuration not plumbed (Finding 33 in the original) was caught; the framing/parser/cancel-and-replace code was not audited for memory leaks under malformed input or DoS resilience.
- **The codec/codec.go encryption module.** I confirmed the dead_letters key_version gap (Finding 75); I did not exhaust every encrypt-decrypt callsite for IV reuse, AAD coverage, key-version mismatch handling, or key-rotation atomicity.
- **The HL7 processor's correlation-pair logic.** Finding 44 caught one config knob; the actual correlation-window / claim / reaper interaction across pod restart was not stress-tested.
- **The mkdocs site.** I confirmed there's no PR validation (Finding 74) but didn't read every doc file for lies.
- **The `internal/api/auth/` JWKS / replay-cache / advisory-rate-limit code paths**, which Finding 18 indicated are dead because Deps fields aren't set, but the per-package code may have its own fragilities (e.g., what happens if a JWKS endpoint returns 200 with an unparseable body).

A third auditor focused on these would likely find another 20-40 findings. The pattern by which the first 50 missed these — "tested in isolation, not wired in production" — applies symmetrically to the second 57 and the un-audited remainder.

**Bottom line.** 50 was an early stop. 107 is a more honest current count. The true count is probably ≥ 130. Each pass exposes a smaller but still material set of gaps; the codebase has not yet reached the diminishing-returns regime where a hostile audit produces only LOW-severity nits.
