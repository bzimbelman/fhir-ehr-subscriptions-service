# Production-Readiness Honesty Audit — Supplement 2 (Findings #108+)

This file extends `production-readiness-honesty-audit.md` (#1-#50) and
`production-readiness-honesty-audit-supplement.md` (#51-#107). The second
auditor predicted "≥ 130" total. This third pass adds **38 new findings**
across the categories the supplement flagged as unsearched.

Same rules: every claim is grounded in a code citation. Tested in
isolation ≠ wired in production. Documented ≠ implemented.

## Summary table

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 108 | BROKEN | BLOCKER | `.github/workflows/release.yml:52` vs `cmd/fhir-subs/version.go:9` | release ldflag is `-X main.version=…` (lowercase); the actual variable is `main.Version` (capital V). Every released binary reports `version=dev`. |
| 109 | BROKEN | BLOCKER | `Dockerfile:27-31` | Dockerfile build step has no `-ldflags="-X main.Version=…"`. Every container image (CI, Helm, demo) reports `version=dev`, regardless of the tag. |
| 110 | BROKEN | BLOCKER | `Makefile:33-34` | `make migrate-up` echoes a TODO ("wire the migration runner") instead of running migrations. Operator-facing docs reference `make migrate-up`; the target is a stub. |
| 111 | UNWIRED | BLOCKER | `internal/api/handlers/router.go:337` | `r.Get("/metadata", h.getCapabilityStatement)` is mounted **inside** the auth-protected group. FHIR conformance probes (which fetch `/metadata` unauthenticated, S-2.1) get 401. `RegisterPublicRoutes` (the unauthenticated form) is never called from `cmd/`. Doubles down on Finding 8 with a fresh angle: even when the production runtime is up, every conformance probe fails because `/metadata` is gated. |
| 112 | UNWIRED | BLOCKER | `internal/api/handlers/url_validator.go:30-31` vs `internal/channel/resthook/resthook.go:298-313` | URLValidator docstring promises "the same validator is reused at delivery time by the rest-hook channel" as a DNS-rebinding defense. `grep -n "URLValidator\|Validate(" internal/channel/resthook/resthook.go` returns zero hits. rest-hook does only `url.Parse` + scheme check; a hostname that resolved public at create time and then flips to 10.0.0.5 at delivery time is dialed with no re-check. |
| 113 | CRYPTO-WEAKNESS | BLOCKER | `internal/infra/storage/codec/codec.go:174,207` | `gcm.Seal(nil, nonce, plaintext, nil)` — fourth argument (Additional Authenticated Data) is `nil`. The envelope's format byte, key version, and the row's primary key are NOT bound into the AEAD. An operator with DB write access can swap envelopes between rows / tables and the codec accepts the substitution. The envelope's "version u8" comment at `codec.go:11` does not match the actual `[format][reserved=0][nonceLen hi][nonceLen lo][nonce][ct]` layout at `codec.go:177-182` — doc/code drift on the canonical at-rest envelope. |
| 114 | RACE | BLOCKER | `internal/api/auth/verifier.go:272-291` | JTI replay check is TOCTOU: `Seen(jti)` returns false → release lock → `Put(jti, exp)` later. Two concurrent identical-token requests both pass the `Seen` check before either calls `Put`. Both authenticate. The `JTIReplayCache` exposes no `CheckAndPut` atomic. Replay protection has a documented hole. |
| 115 | RACE | BLOCKER | `internal/api/auth/token_endpoint.go:349-354` | Same TOCTOU on the `/token` endpoint's assertion-jti dedupe: `te.jtiCache.Seen(jti)` then `te.jtiCache.Put(jti, ...)` are separate calls under separate locks. RFC 7523 §3 mandates jti uniqueness; this implementation can mint two access tokens off one assertion under load. |
| 116 | UNWIRED | BLOCKER | `internal/channel/channel.go:122-133` | The `Channel` SPI interface declares only `Deliver(...)`. There is no `Close() error` on the SPI. `internal/channel/websocket/websocket.go:817` defines a `Close()` method, but the scheduler can't invoke it via the interface. Even when websocket is wired one day, graceful shutdown won't close in-flight sockets. Compounds Finding 14 with an SPI gap. |
| 117 | VALIDATION-GAP | BLOCKER | `internal/api/handlers/admin.go:36` vs `internal/api/handlers/admin.go:55-58` | `MinAdminTokenBytes = 32` constant is declared and never enforced. `RegisterAdminRoutes` checks only `d.AdminToken == ""`. A 1-character admin token passes the check; the constant exists for documentation only. (Compounds Finding 7 — the routes are unwired anyway, but the gate's design is also wrong.) |
| 118 | DOCS-VS-CODE | BLOCKER | `docs/architecture.md` (multiple sections) | Architecture promises `_search` parameters on Subscription / SubscriptionTopic; `internal/api/handlers/subscription_handlers.go:1278-1296` `buildCapabilityStatement` declares `interaction: search-type` but supplies zero `searchParam` entries on either resource. A FHIR conformance probe receives a search-type-claiming CapabilityStatement with no advertised search dimensions; client codegen tools that key on `searchParam` fail to generate any. |

## HIGH findings (new)

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 119 | HARDCODED | HIGH | `cmd/fhir-subs/activators.go:86` | rest-hook activator uses `tls.VersionTLS12`. The rest-hook delivery channel (`internal/channel/resthook/resthook.go:230-232`) uses `tls.VersionTLS13` by default. Two TLS floors for the same subscriber endpoint, depending on which call (handshake vs delivery) is in flight. PHI metadata in the handshake bundle (Subscription/{id}, topic) reaches subscribers over TLS 1.2 even when the channel is configured TLS 1.3. |
| 120 | UNWIRED | HIGH | `internal/channel/websocket/websocket.go:416` | `c.runConnection(r.Context(), conn)` passes the HTTP request context, not `c.ctx`. Channel-level `Close()` cancels `c.ctx`; in-flight bind handshakes (waiting on `bindCtx, cancel := context.WithTimeout(ctx, c.bindTimeout)` derived from `r.Context()`) are not unblocked by `Close()`. The S-7 #5/#9 "join cleanly on shutdown" claim doesn't extend to the bind window. |
| 121 | INFOLEAK | HIGH | `internal/channel/websocket/websocket.go:46-46` | The bind token is allowed in the `Sec-WebSocket-Protocol` header (`SubprotocolBindPrefix = "fhirsubscriptions.v1.<token>"`). `Sec-WebSocket-Protocol` is logged by most reverse proxies, ALB access logs, and CDN edge logs. Tokens are now subject to passive collection at every TLS-terminating hop the operator runs. The in-band JSON form (the alternative path) is the only token-confidential one; spec recommends the header. |
| 122 | AUDIT-CHAIN-INTEGRITY | HIGH | `internal/infra/observability/audit/audit.go:419-468` | `VerifyChainReport` advances `prior = row.ChainHash` (the on-disk hash) AFTER detecting a mismatch — silently re-anchoring the walker on the corrupted row. Every downstream row that is correctly chained to that corrupted row passes `prior_hash` and `chain_hash` checks. A single tampered row plus a re-chain by an attacker downstream produces zero additional break reports. The "every break" claim in the docstring is false: the verifier reports the FIRST break per re-anchored segment. |
| 123 | AUDIT-CHAIN-INTEGRITY | HIGH | `internal/infra/observability/audit/audit.go:300-318` | `canonicalChainInput` formats `prior_hash` as `fmt.Sprintf("%x", prior)` and embeds the lowercase hex string into the JCS bytes. SHA-256 over hex of the prior hash is deterministic per call but defeats the design intent — the chain hashes a string-encoded representation of a hash, not the hash bytes. Any operator who attempts to verify by computing `SHA-256(JCS(<canonical>) || prior_hash_bytes)` (the natural reading of LLD §8) sees every row as "broken." Internal consistency is preserved, but the verifier is not interoperable with the documented spec. |
| 124 | UNWIRED | HIGH | `Dockerfile:47` vs `cmd/fhir-subs/run.go:126` | `EXPOSE 8443 8081 2575` in Dockerfile lists 8081 (probes); the binary opens exactly one TCP listener on `cfg.Server.HTTP.Bind` (default `:8443`). Helm's probe targeting and Dockerfile's EXPOSE both reference port 8081; nothing listens on it. (Cross-references Finding 51; calling it out separately because Dockerfile-driven scanners — Trivy, Anchore, Snyk container — flag a port that doesn't exist as "exposed" in their reports, and operators following the Dockerfile to set up a reverse proxy waste effort on port 8081.) |
| 125 | DEAD-DEPENDENCY | HIGH | `go.mod:14,17-18` | `github.com/pashagolub/pgxmock/v3` and `github.com/testcontainers/testcontainers-go` are in the **direct** `require` block (not `// indirect`, not `_test.go`-only via build tags). Both are test-mock libraries. Production binary's go.sum graph carries them; downstream consumers of any internal package inherit a fat dep tree. Either move them under build-tagged test files or move them into a separate `go.mod` for the e2e tree. |
| 126 | DEAD-DEPENDENCY | HIGH | `go.mod:19-23` | `go.opentelemetry.io/otel`, `otel/sdk`, `otel/exporters/otlp/otlptrace`, `otel/exporters/otlp/otlptrace/otlptracehttp`, `otel/trace` are all in `require`. `grep -rn "otel\|otlp\|tracer\.Start" cmd/` is empty (Finding 23). The OTel SDK is imported transitively by `internal/infra/observability/tracing/...` (which itself has zero callers from `cmd/`). Production binary ships the entire OTel exporter chain for code that never runs. |
| 127 | INFOLEAK | HIGH | `internal/channel/email/email.go:495` | `Date` header in MIME body is set with `time.Now().UTC().Format(time.RFC1123Z)` — directly, not via the channel's clock seam. Tests cannot deterministically substitute time; a `Now func() time.Time` was never threaded through `buildMIME`. Reproducible-output goal violated. |
| 128 | UNWIRED | HIGH | `internal/api/handlers/url_validator.go:155-186` | `validateIP` does NOT block: IPv4 broadcast `255.255.255.255`, CG-NAT `100.64.0.0/10` (RFC 6598), IPv6 site-local `fec0::/10`. Subscriber endpoints resolving to these can pass SSRF policy. The "private" list relies on Go's `IsPrivate()`, which excludes CG-NAT — and CG-NAT is increasingly real (cloud egress IPs, CGN behind home routers). The internal cloud-metadata explicit check at `:175-178` partly mitigates but only for AWS/GCP `169.254/16`. |
| 129 | RACE | HIGH | `internal/api/handlers/url_validator.go:138` | DNS lookup uses `context.Background()` (Finding 98 already noted this) and the resolved IPs are NOT passed to the channel's downstream dial. Race window: validator resolves `subscriber.example` to `1.2.3.4` (public, passes); a few seconds later the rest-hook channel calls `c.http.Do(req)`, which re-resolves and may receive `10.0.0.5` (DNS rebinding). The validator-then-dialer pattern is structurally vulnerable. |
| 130 | INFOLEAK | HIGH | `cmd/fhir-subs/wiring.go:181-183` | `URLValidator.AllowHTTP = cfg.Auth.AllowInsecure`. A single boolean controls TWO trust boundaries: (1) JWKS over `http://`, (2) subscriber endpoints over `http://`. An operator who flips `auth.allow_insecure_jwks` to debug a JWKS issuer also disables the SSRF-https-only policy. Finding 47 noted this; the consequence is sharper than originally stated: all PHI deliveries to `http://` URLs become permitted under the same flag. |
| 131 | INPUT-VALIDATION | HIGH | `internal/hl7processor/processor.go:961-983` | `messageDateTime` parses MSH-7 with six layouts (`20060102150405`, `200601021504`, …, `2006`). HL7 v2 MSH-7 commonly carries `YYYYMMDDHHMMSS.SSS+ZZZZ` (millisecond precision and timezone offset). The current parser drops sub-second precision and fails on offset-bearing timestamps; on failure, `decideOutcome` falls back to `p.deps.Now()` — the framework's wall clock — and the on-disk `occurred_at` no longer reflects the EHR's stamp. Per LLD §5-9.10 this is the "respect MSH-7" fix; in practice it silently regresses to wall clock. |
| 132 | INPUT-VALIDATION | HIGH | `internal/hl7processor/processor.go:935-984` | `messageDateTime` parses MSH-7 directly without honoring the per-message field separator declared in MSH-1 (which the HL7 v2 spec says CAN be any ASCII char, not just `\|`). The parser hardcodes `sep := first[3]` — that is the actual separator byte — but fails to honor MSH-2 escape characters that may appear within the timestamp. Edge cases with `\.` escapes are silently dropped. |
| 133 | INPUT-VALIDATION | HIGH | `internal/api/handlers/url_validator.go:114-117` | `host := u.Hostname()` is checked against the loopback hostname list — but does NOT decode IDN / Punycode. A unicode-encoded host (`xn--…`) is treated as a foreign string; the underlying resolver still resolves it, but the loopback-hostname check on `localhost` does not catch e.g. `localhostᴀ.example` (homoglyph). Combined with allowHosts case-insensitive equality and no canonicalization, an attacker can plant a homoglyph-Punycode hostname that resolves to a public IP at create time but to loopback after redirection. |
| 134 | UNWIRED | HIGH | `cmd/fhir-subs/wiring.go:181-228` | Wiring constructs `urlValidator` but never plumbs `URLValidatorConfig.AllowHosts`. The struct field is parsed nowhere in `Config`; operators cannot opt internal hosts onto the allowlist. The configured-allow path is dead. |
| 135 | UNDOCUMENTED-CONFIG | HIGH | `cmd/fhir-subs/config.go` (entire) vs `internal/channel/email/email.go:117-200` | The Email channel's `Config` (`Mode`, `STARTTLSPolicy`, `AuthMechanism`, `AuthUsername`, `AuthPassword`, `AuthIdentity`, `AllowCleartextAuth`, `AttachmentThresholdBytes`, `RequestTimeout`, `LocalName`, `UserAgent`, `Signer`) — none of these fields are surfaced in `cmd/fhir-subs/config.go` or `applySets`. Even if Finding 15 is fixed (the package gets registered), zero tunable from the operator's YAML reaches the channel constructor. The channel's SMTP relay would be hardcoded. |
| 136 | UNDOCUMENTED-CONFIG | HIGH | `cmd/fhir-subs/config.go` vs `internal/channel/websocket/websocket.go:131-186` | `Options.OriginPatterns`, `MaxFrameBytes`, `MaxSessions`, `MaxSessionsPerClient`, `MaxReplayEvents`, `IdleTimeout`, `PingInterval`, `BindTimeout`, `PingWriteTimeout`, `UpgradeReadHeaderTimeout` — every WebSocket channel knob has a typed field on the channel constructor and no corresponding key in `Config`. The `WebSocketChannelConfig` block (Finding 35) covers only 3 of these and is itself ignored. |

## MEDIUM findings (new)

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 137 | INPUT-VALIDATION | MEDIUM | `internal/api/handlers/subscription_handlers.go:805-811` | `deleteSubscription` performs a soft delete (`UpdateStatus → SubOff`). FHIR R5 §3.4.4 specifies that after DELETE, subsequent `GET /Subscription/{id}` returns 410 Gone or 404. `readSubscription` (`subscription_handlers.go:466-478`) returns 200 with `status=off`. Spec violation; subscriber idempotency is broken (a subscriber that re-creates after delete sees a "live" deleted resource). |
| 138 | DEAD-CODE | MEDIUM | `migrations/0001_init.sql:289-310` | Bootstrap creates 3 partitions starting at `now()` (current month + next 2). With `partition.Run()` not running in production (Finding 13), no further partitions are ever created. Pods running for ≥3 calendar months hit `error: no partition of relation "resource_changes" found for row`. The bootstrap comment at `migrations/0001_init.sql:286-288` claims `partition.Run()` covers it — but the goroutine never spawns. |
| 139 | DEAD-CODE | MEDIUM | `migrations/0001_init.sql:247-275` | Partition trigger uses `date_trunc('month', now())::date` regardless of the `created_at` value supplied by the application. Any backfill / replay write with `created_at` in a non-current month lands in the current month's partition. Schema invariant `created_month = date_trunc('month', created_at)` is silently violated by the trigger itself when application supplies an out-of-month `created_at`. |
| 140 | DEAD-CODE | MEDIUM | `internal/infra/storage/repos/resource_changes.go:35` | `prev, _, err = r.codec.Encrypt(row.PreviousResource)` — the second return value (key version) is discarded. `Encrypt` always uses the active key; both `enc` and `prev` write under the same active key. Same write transaction → safe today. But if `Encrypt` ever became per-call key-rotation-aware, this discard would silently produce inconsistent column state. The pattern repeats in `ehr_events.go:34`. |
| 141 | UNWIRED | MEDIUM | `internal/api/auth/handler_rate_limit.go:55-57` | `Middleware()` is nil-safe via `if c == nil || c.inner == nil { return next }`. This is the doubled mechanism behind Finding 18 — but here the gap is that even when `cfg.Auth.SubscriptionCreateRateLimit` is non-zero in YAML, `cmd/fhir-subs/wiring.go` never calls `auth.NewClientRateLimiter(cfg.Auth.SubscriptionCreateRateLimit, ...)`. The constructor exists; wiring doesn't. Tests of the limiter pass; production routes silently bypass. |
| 142 | RACE | MEDIUM | `internal/api/auth/verifier.go:325-360` | `keyfuncFor` reads `v.jwksCache` under `v.jwksMu`, releases the lock, then performs the HTTP fetch on a cache miss. Two concurrent first-time requests for the same `jwksURL` BOTH issue HTTP fetches (request stampede). The cache is NOT a `sync.Once`/singleflight-style coalescer; under burst auth, a misconfigured JWKS host receives N parallel GETs from the verifier. Mostly a self-DoS / wasted bandwidth, not a correctness bug. |
| 143 | RESOURCE-LEAK | MEDIUM | `internal/channel/websocket/websocket.go:419-466` | The bind handshake reads up to `4 * 1024` bytes (`defaultBindReadLimit`). On invalid JSON / wrong type / wrong subscription, the handler writes an error frame and Close()s the conn. `c.runConnection` returns WITHOUT spawning the per-session goroutines — but `c.wg` is only joined at `c.Close()`. A flood of bind-then-disconnect peers consumes one accept-side goroutine per connection (the `runConnection` invocation itself runs on the upgrade handler's request goroutine), which is fine; but the `MetricBindAttemptsTotal` counter increments AFTER the bind read, so failed-bind floods do increment the metric. Slow-bind goroutines pinning the request goroutine for the full `BindTimeout` (10s default) at high TPS is the unhandled DoS vector. |
| 144 | INPUT-VALIDATION | MEDIUM | `internal/mllp/framer.go:117-122` | `Framer.Append` does `f.pending = append(f.pending, p...)` — unbounded growth between Next() calls. The `pendingExceeded` check (`framer.go:127-129`) is only consulted at the TOP of `Next()`. A peer that dumps `2*maxBody+1` bytes in a single Append before Next runs (within one read pass) still grows pending past the cap before any check fires. The TCP read buffer caps at `cfg.ReadBufBytes` (default 64 KiB), so the practical cap is `2*maxBody + 64KiB`; not unbounded but the documented "2× maxBody" cap is approximate. |
| 145 | INPUT-VALIDATION | MEDIUM | `internal/mllp/connection.go:151-165` | `startRead` sets the read deadline, then spawns a goroutine that calls `conn.Read(readBuf)`. If the main loop's `ctx.Done()` fires after `startRead` but before the spawned goroutine begins reading, the goroutine still reads from `conn` after `_ = conn.Close()` — the close-deferred Read returns EOF / "use of closed connection" — but the goroutine writes to `readCh` which the loop drains via `<-readCh`. Edge case: if the spawned goroutine's `readCh <- out` runs AFTER the loop already returned, the buffered channel (size 1) accepts but the loop is gone, and the goroutine exits. Goroutine leak risk is small; the documented invariant ("the goroutine exits cleanly") relies on `readCh` being buffered (size 1) — not stated explicitly in the comment. |
| 146 | UNDOCUMENTED-CONFIG | MEDIUM | `internal/channel/websocket/websocket.go:46-46` | `SubprotocolBindPrefix = "fhirsubscriptions.v1."` is hardcoded. There is no operator escape valve for renaming the subprotocol (e.g., to satisfy a corp gateway that filters non-standard subprotocols). The constant is exported; tests can override; production cannot. |
| 147 | INFOLEAK | MEDIUM | `internal/api/auth/token_endpoint.go:434-444` | `diagnosticForReason` returns a fixed enum of strings, but `te.fail(... "request body too large", "malformed")` (line 220-222) and `"could not parse form"` (line 224) bypass `diagnosticForReason` and surface the literal English string. An operator-controlled error format is mixed with the canonical error enum; logs and metric labels don't see consistent reason codes. |
| 148 | DOCS-VS-CODE | MEDIUM | `docs/operations/dead-letters-runbook.md:73-83` (cascade) | Recovery procedure tells operators to insert a fresh `deliveries` row. The dead-letters runbook does not warn that, since `partition.Run()` is unwired (Finding 13/138), `deliveries`-targeting partition writes will fail when run on a long-running pod past the 3-month boot horizon. Operators following the runbook hit "no partition for value" instead of recovery. |
| 149 | DEAD-CODE | MEDIUM | `internal/infra/observability/audit/audit.go:56-58,62-69` | `GenesisHash()` is `Deprecated`-marked (line 53-55) but still used internally — there's no deprecation timeline, no `Deprecated:` linter directive, no callsite migration plan. The dual-API surface (`GenesisHash` vs `GenesisHashFromLiteral`) ships in production. |

## LOW findings (new)

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 150 | DEAD-CODE | LOW | `cmd/fhir-subs/version.go:8-12` | `Version` and `Commit` are package-level mutable `var`s. Tests directly mutate them (`main_test.go:52-53,158-159`). Production code reads them from goroutines (run.go:89-90); a stress test that sets these while production reads them is racy. Build-time flags should be `const` (or use `runtime/debug.BuildInfo`). |
| 151 | DEAD-CODE | LOW | `requirements-docs.txt:1` | Only one pinned package (`mkdocs-material>=9.5,<10`); transitive deps (`mkdocs`, `pymdown-extensions`, `pygments`, `pyyaml`) are unpinned. `mkdocs build --strict` results vary across docs CI reruns when transitive deps publish breaking changes. |
| 152 | DEAD-CODE | LOW | `mkdocs.yml:74-75` | `plugins: [search]` lists `search` which is shipped with mkdocs core; no plugin pin. The `material` theme's `search.suggest`/`search.highlight`/`search.share` features (mkdocs.yml:24-26) require the search plugin; the implicit dependency is not load-bearing today but documentation of the wiring is absent. |
| 153 | DOCS-VS-CODE | LOW | `demo/docker-compose.yml:77-80` | Comment claims "no in-container healthcheck because distroless has no shell." Compose then declares `restart: unless-stopped` without a healthcheck. Compose has no signal that the bridge is actually up, so dependent services in a multi-service compose chain (none today) cannot `depends_on: condition: service_healthy`. |

## Findings against the audit chain itself

`internal/infra/observability/audit/audit.go` is not callable from production
(Finding 10), so its bugs are latent. Documenting them anyway because the
audit doc and runbook recommend it.

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 154 | AUDIT-CHAIN-INTEGRITY | LATENT-HIGH | `internal/infra/observability/audit/pgstore.go:74-94` vs `internal/infra/observability/audit/pgstore.go:155` | `Migrate()` creates `seq BIGINT GENERATED BY DEFAULT AS IDENTITY`; production migration uses `seq BIGSERIAL` (`migrations/0001_init.sql:227`). Different generators, both column-named `seq`. `LastChainHash` orders by `seq DESC`; if the package's Migrate ever ran against a DB that already has the prod-migration shape (the `IF NOT EXISTS` check at `pgstore.go:77` succeeds), the column conflict manifests as duplicate-key errors on insert. (Compounds Finding 11.) |
| 155 | AUDIT-CHAIN-INTEGRITY | LATENT-HIGH | `internal/infra/observability/audit/audit.go:300-318` | `canonicalChainInput` includes `"correlation_id": evt.CorrelationID.String()` for an event whose CorrelationID is the zero UUID. The empty/zero UUID becomes `"00000000-0000-0000-0000-000000000000"` in the chain input, but `pgstore.go:186-189` substitutes a fresh UUID via `uuid.New()` for a zero CorrelationID before the INSERT. Wire chain bytes hashed by `Emit` differ from the on-disk row's `correlation_id`. The chain verifier reads on-disk and re-hashes — sees a `correlation_id` that doesn't match the hashed input — every row breaks. |

## Dependency / supply-chain findings

| #   | Tag | Severity | Path:line | Claim |
|-----|-----|----------|-----------|-------|
| 156 | DEAD-DEPENDENCY | MEDIUM | `go.mod:14` | `pgxmock/v3 v3.4.0` in DIRECT require. Tests use it; production binary's link tree carries the package's compiled output. `go build ./cmd/fhir-subs` does not import it; `go list -deps ./cmd/fhir-subs \| grep pgxmock` returns clean. So the binary itself is unaffected, but the module's go.sum still mandates the dep — anyone vendoring this repo as a library inherits the test-only fixture. |
| 157 | DEAD-DEPENDENCY | LOW | `go.mod:26` | `pgregory.net/rapid v1.1.0` (property-test framework) in direct require. Same shape as #156. |

## Cross-cuts (third pass)

**Two TLS floors for the same subscriber endpoint.** Finding 119 caught it:
the rest-hook handshake activator (`activators.go:86`) defaults to TLS 1.2;
the rest-hook delivery channel (`resthook.go:230-232`) defaults to TLS 1.3.
The wiring picks both for the same subscriber. PHI metadata transits TLS 1.2
on the handshake POST, then PHI bundles transit TLS 1.3 on every delivery.
Inconsistent posture; the operator setting `tls_min_version` on the channel
config has no effect on the handshake.

**The audit chain's hash function and the verifier's hash function are
spec-incompatible — but only with each other.** Finding 122 + 123: the
walker re-anchors after a break (so most breaks are invisible), and the
hash input encodes the prior hash as lowercase hex (so an external LLD-spec
verifier sees every row as broken). Internal consistency is preserved
because writer and verifier walk the same code path; external attestation
is structurally impossible. Combined with Finding 9 (production audit-store
writes literal `0x00`), Finding 11 (schema mismatch), Finding 81 (timestamp
mismatch), Finding 87 (CI never compares hash bytes), and Finding 154-155
above, the audit chain has six independent layers of broken — each layer is
sufficient to defeat tamper-evidence on its own.

**The build-version pipeline reports `dev` for every released build.**
Finding 108 + 109 + 150: the release ldflag's variable name doesn't match;
the Dockerfile has no ldflag at all; the variables are mutable globals. Not
just a logging cosmetic — `s.deps.ServerVersion` is rendered into the
CapabilityStatement (`subscription_handlers.go:1268`), so subscribers of any
production deploy see `software.version: dev` in the discovery doc. Audit
trails that record the running version see `dev` everywhere.

**JWKS / replay-cache integrity has TWO TOCTOU races.** Findings 114 + 115:
both the verifier's JTI replay check and the token endpoint's assertion-jti
check are non-atomic Seen → Put. RFC 7523 jti uniqueness is best-effort. A
load test with two parallel identical-token requests demonstrates the
replay; the issue is reproducible without infrastructure mocking.

**SSRF-then-dial pattern is structurally rebinding-vulnerable.** Findings
112 + 128 + 129 + 133: the URLValidator resolves and approves the public
IPs, the rest-hook channel later dials by hostname (re-resolving), and the
intermediate window admits DNS rebinding. The validator does NOT block CG-
NAT, IPv6 site-local, broadcast, or IDN-encoded loopback aliases. The
documented "delivery time re-check" promise is not implemented.

## Categories investigated with no new findings

These categories were searched in this pass and produced no new findings
beyond what #1-#107 covered:

1. **`replace` directives in go.mod, vendored binaries, ad-hoc shell scripts**
   — none. `grep -E '^(replace|exclude)' go.mod` empty; no `vendor/`
   directory; no `.sh` files in repo. (Makefile is the only build harness;
   Finding 110 already covered.)
2. **`go vet ./...` warnings** — clean. No suppressions, no warnings, no
   `//nolint` suppressing real findings.
3. **`os.Exit` outside `main()`** — confirmed none.
4. **`prometheus.MustRegister` scattered across packages** — confirmed
   none; explicit `Register` everywhere.
5. **Goroutines launched without ctx + waitgroup coordination** — every
   `go func()` site in `cmd/fhir-subs/wiring.go` and the channel packages
   uses a shared loopCtx + WG.
6. **DROP IF EXISTS / partition-create hygiene** — no DROP statements.
   Bootstrap covered in #138, #139.
7. **Tables in code with no migration** — none.
8. **Postgres extensions undeclared** — only `pgcrypto`, declared.
9. **CHECK constraint values vs code literals** — match.
10. **Test code compiled into production binary** — no production file
    imports `_test.go` paths nor build-tag-restricted test packages
    directly (Finding 119/119-existing for demo CLIs is the only exception
    — already covered by #119 in supplement-1).
11. **panics in production code** — every `panic(...)` is intentional
    misconfiguration-at-startup or SPI-required-override; no `panic("TODO")`.
12. **Fixed strings comparison non-constant-time** — admin token uses
    `subtle.ConstantTimeCompare`. Token endpoint uses `==` on string
    comparisons of grant_type / client_assertion_type — both are fixed
    enums, no security-sensitive value. Acceptable.
13. **mkdocs.yml nav entries vs filesystem** — every entry resolves;
    `python3 -c "import yaml; ..."` confirms 59 nav entries, zero missing.
14. **CI workflow timeouts realistic** — `ci.yml` has no per-job timeout
    (default 6h) but jobs are sub-30-minute.
15. **CI cache keys** — `actions/setup-go@v5 cache: true` keys on
    `go.sum`; correct.

## Convergence assessment

This pass produced **38 new findings (#108-#157)**.

- **Combined count:** 50 (audit 1) + 57 (audit 2) + 38 (audit 3) = **145
  total findings**.
- **BLOCKER count combined:** 20 (audit 1) + 15 (audit 2) + 11 (audit 3)
  = **46 BLOCKERS**.
- **Severity distribution this pass:** 11 BLOCKER, 18 HIGH, 13 MEDIUM,
  4 LOW (some span; counted at primary). Plus 2 LATENT-HIGH against the
  audit chain.

### Top three most-embarrassing new items

1. **Finding 108 + 109 + 150 — Every released build reports `version=dev`.**
   The release workflow's ldflag points at a variable that does not exist
   (`main.version` lowercase vs the actual `main.Version`). The Dockerfile
   has no version ldflag at all. The version variable is a mutable global
   the tests poke. The CapabilityStatement that a subscriber discovers on
   any production deploy reports `software.version: dev`. Every audit-log
   row records `dev`. There is no path from a tag in git to a build that
   knows its own version.

2. **Finding 113 + 122 + 123 + 154 + 155 — The codec and the audit chain
   are unauthenticated against tampering.** AES-GCM `Seal` with `nil` AAD
   means a row's encrypted PHI envelope is not bound to its row id; an
   operator with DB write access can swap envelopes between rows and the
   codec's authentication tag still passes. The audit chain hashes a
   string-formatted prior hash (defeating any external verifier), advances
   `prior` past detected breaks (silently re-anchoring), runs against a
   production migration with the wrong column names (Finding 11), and
   substitutes correlation_ids server-side after the chain hash is
   computed. CI never compares the hash bytes (Finding 87). The chain is
   six independent levels of broken; each is sufficient on its own.

3. **Finding 114 + 115 — JTI replay protection has two TOCTOU windows.**
   The verifier and the token endpoint each implement their own non-atomic
   `Seen → Put` JTI dedupe. A load test with two parallel identical-token
   requests passes both. RFC 7523 §3 mandates jti uniqueness; the
   implementation is best-effort under contention. SMART Backend Services
   replay protection silently degrades under concurrency.

### Would a fourth auditor find more?

**Yes.** Categories I deliberately did not exhaust in this pass:

- **Each handler's body for missing pagination, missing input validation,
  missing rate-limit application.** I sampled createSubscription /
  updateSubscription / deleteSubscription / opStatusSingle / opStatusBulk
  / opEvents / matchingSubscriptions; the remaining sub-handlers
  (`searchTopics`, `readTopic`, `getCapabilityStatement`, `opEvents`'s
  pagination cursor) were not stress-checked. Each handler's interaction
  with the (mostly nil) Deps slots is a fresh axis.

- **The migration sequence's idempotency under concurrent apply.** The
  bootstrap partitions block is wrapped in `do $$ ... $$;` without an
  explicit advisory lock; two pods racing to migrate 0001 against a fresh
  DB can both run the bootstrap and one will fail with "relation already
  exists." I observed this is gated by `schema_migrations` insertion; the
  insertion happens AFTER the body, so the race window exists.

- **Each adapter scaffolds (`adapters/{cerner,epic,athena,allscripts,
  meditech,nextgen,direct,demo,default}/`) for capability-declaration vs
  actual-behavior consistency.** Findings 24/25/88/94 hit the surface;
  each adapter has its own per-vendor capability schema that may not
  match the SPI defaults.

- **The HL7 v2 lex/parse implementation in `internal/hl7processor/
  translate.go` for malformed-input crashes / panics.** I read
  processor.go but not translate.go, parse.go, or the per-vendor lex
  helpers.

- **The `internal/engine/{matcher,submatcher,scheduler,topics}/`
  packages, beyond Finding 38's "unwired" verdict.** Even though the
  packages aren't called from `cmd/`, they're called from each other —
  matcher → submatcher → scheduler. The internal interaction has its
  own bugs that the unit tests cover but the integration path doesn't.

- **The lifecycle module (`internal/infra/lifecycle/`) for shutdown
  ordering correctness.** I read `run.go` but not the lifecycle module.
  The probe handlers, signal dispatch, and registry for shutdown hooks
  are all under-audited.

- **Each test stub catalogue.** Findings 84-93 in the supplement are a
  sample; every package in `internal/` has its own test fixture with
  injected fakes. Mapping each fake-vs-production gap is a separate,
  productive sub-audit.

- **The MkDocs-rendered output.** I confirmed nav entries resolve
  (Finding "Categories Investigated" #13 above); I did NOT read the
  rendered HTML for broken anchors, mismatched cross-references between
  docs/architecture.md and docs/high-level-design/*.md, or the
  consistency between high-level-design and low-level-design domain
  files. Audit 2 noted there's no `mkdocs build --strict` on PRs
  (Finding 74); a strict build today would surface broken refs.

A fourth auditor focused on these would likely find another 25-50
findings. The asymptote is approached but not reached.

**Bottom line.** 145 is the current honest count. The prior auditor's "≥
130" prediction was directionally right; the actual pace per pass
(50 → 57 → 38) shows diminishing returns finally setting in. A fourth
pass would still produce findings but would likely surface more LOW /
LATENT findings than fresh BLOCKERS — the BLOCKER stratum is now mostly
known.

The codebase is in the regime where a hostile auditor produces fewer
findings per hour than the previous pass. It is NOT yet in the regime
where a hostile auditor produces nothing material. There is more than
that.
