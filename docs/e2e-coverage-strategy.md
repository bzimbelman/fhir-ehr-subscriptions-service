# E2E Coverage Strategy

Strategy doc explaining why four hostile audits surfaced ~199 production-readiness gaps while CI was green, and what test infrastructure must be built so the e2e suite exercises every documented capability against the production binary running with REAL dependencies.

This is an architecture+harness plan. It does not propose code fixes for the findings themselves — those are tracked in the per-finding mapping (Section 4) and the binary-side knobs (Section 6).

## Hard rule: no fakes, no mocks, no stubs

The current e2e suite is structurally broken because it inserts a fake at every place production has a bug. This document mandates the opposite policy:

- **Every test surface drives the production binary against a real implementation of the dependency.** Real Postgres, real TLS sockets, real FHIR servers, real SMTP relays, real OTLP collector, real DNS, real WebSocket clients, real channel activators.
- **No `stubChannelActivator`, no `principalMiddleware` injecting a fake Principal, no `fakeRunner`, no in-memory store, no patched clock, no monkey-patched `time.Now()`, no test-only audit store class.**
- **If a test seam requires a fake, the seam itself is wrong.** Either rework the seam OR boot a real implementation of the dependency in a docker container, a testcontainer, or an in-process server using real-software libraries.
- **For things "real" doesn't apply to (e.g. virtual time)** the binary itself must expose a config or API knob that tests drive — see Section 6.

Every harness component below names the REAL software it runs and how the test infrastructure launches it. If a section reads "use docker-compose to spin up X", that is the harness. The Go-side code for that harness is a thin client that points the binary at real X and asserts against real X's outputs.

Cited file paths are absolute or repo-rooted. Findings are referenced by their numeric ID across the four audits in `docs/production-readiness-honesty-audit*.md`.

---

## Section 1 — Why the suite missed 200 gaps

The audits are not the result of bad coverage numbers. The test architecture's *shape* makes whole categories of bug invisible. Eight structural patterns are doing the damage. Each is a direct consequence of "mocks were the easy way; the test infrastructure to drive real dependencies was never built."

### Pattern A — "Tested in isolation, never wired"

Every package under `internal/` has high unit-test coverage. `cmd/fhir-subs/wiring.go` constructs roughly half of those packages. The suite walks the package code; the binary never imports the package; the binary still ships green. Removing the package wholesale would not break any build outside that package.

Examples:
- Findings #2-6 — `scanrunner`, `vendorclient`, `supervisor`, `webhook`, `hydration` are reachable only from `_test.go` chains. The package tests inject a fake of the very interface the package consumes (`fakeRunner`, `fakeClient`).
- Findings #36-38 — `outbox`, `claim`, `partition`, `retention`, `wakeup`, `queue`, `heartbeat`, `topicmatcher`, `topics/filter` are similarly reachable only via `_test.go`.
- Finding #101 — explicit catalogue of the pattern.

The only test that detects "package X is unwired" is one that boots the actual binary, drives it through the operator's API, and observes the package's side effects in real Postgres / real subscriber sockets. The test infrastructure to do that — booting `cmd/fhir-subs` against a real DB and asserting against real downstream effects — was never built. Mocks were used in its place.

### Pattern B — "Faked at the seam where the bug lives"

The harness inserts a stub at exactly the boundary where production has the bug. The seam is moved into the test, the test passes, the production seam stays broken.

Examples:
- Finding #86 + `e2e/harness/api.go:151-155` — harness registers a `stubChannelActivator` for `rest-hook`, `websocket`, AND `email`. Production wiring stubs the activator only for `websocket`/`email` (Finding #27) with a different stub. Every harness test masks the production regression.
- Finding #85 + `e2e/harness/api.go:174-184` — harness installs `principalMiddleware` injecting a fixed `auth.Principal` with every Subscription scope. The production verifier (JWKS fetch, iss/aud/exp/jti, replay cache, rate limit) is bypassed. Dozens of "auth tests" prove the verifier package works in isolation; none prove `wiring.go` mounts it.
- Finding #87 — integration test wires `handlers.NewPgAuditStore(pool)` (writes `Hash: []byte{0}`, the placeholder per Finding #9). The chain-test that *does* compare bytes uses `audit.NewPgStore` (different class, different schema, Finding #11). Two test paths, neither catches the placeholder.

A test that pre-installs a known-good stub at the seam cannot, by construction, observe a regression in the production stub at the same seam. Under the no-fakes rule the stubs are deleted; the test infrastructure boots a real JWKS issuer, a real rest-hook subscriber, a real email relay.

### Pattern C — "Auth-disabled prod-binary tests"

The single test that boots the production binary against the real subscription API turns auth off. It then treats `401 from missing-principal handler` as proof that the API is wired.

- Finding #91 + `e2e/orchestrator/prod_binary_serves_subscription_api_test.go` starts the binary with `AuthAudience: ""`. With audience empty, `wiring.go:206-208` installs a no-op middleware. The 401 is reachable *only* when the no-op stub is installed.
- Finding #112 — `Validate()` does not require `Auth.Audience` non-empty when `Insecure=false`. A production-shaped TLS-on config with audience accidentally empty boots, accepts traffic, authorizes every caller. No e2e detects this.

Under the no-fakes rule the prod-binary tests boot the binary with auth ON and a real JWKS issuer publishing real RS256 keys; tokens are minted with real signatures; the verifier middleware is on the wire path.

### Pattern D — "Raw-SQL setup bypasses handlers"

Tests that need a row in the DB write SQL directly and drive the downstream pipeline. The handlers, validators, activation handshake, URL validator, channel-type validation, audit emission — none run.

- Finding #90 + `e2e/orchestrator/helpers_support_test.go:34-60` — `RegisterSubscriber` INSERTs into `subscriptions` via SQL and follows with `UPDATE subscriptions SET status='active'`. The "production binary processes HL7" test uses this helper, so it proves only "given a row in the right shape, delivery happens" — not "the production API can create a deliverable subscription."
- Finding #163 — `ListByTopicAndRange` query lacks a `client_id` predicate. A real e2e that goes through `POST /Subscription` for *two tenants* and then drives `$events` would surface the cross-tenant leak.

Under the no-fakes rule, *every* subscription is created via `POST /Subscription` with a real bearer token against the real handler chain. SQL setup is forbidden in tests except for fixture data the handlers cannot create (e.g., seed `auth_clients` rows before any token is mintable — and even that is bootstrapped via the real admin path once it exists).

### Pattern E — "Schema vs code mismatch only caught at runtime"

The migration creates one set of column names; the production code targets a different set. Tests run against a harness DB that follows the migration but never executes the SQL the production code writes.

- Finding #11 — `internal/infra/observability/audit/pgstore.go` writes `chain_hash`, `prior_hash`, `chain_input`, `payload`. `migrations/0001_init.sql:226` creates `hash`, `prev_hash`, `canonical_form`. Different columns. No test boots the production binary, triggers an audit emission, and reads the row through the migration's columns.
- Findings #12, #80, #154, #155 — multiple compounding axes of audit-schema drift.
- Finding #75 — `dead_letters` schema has no `key_version`; production code discards the version returned by `Encrypt`. After a key rotation, payloads are unrecoverable.

Under the no-fakes rule, every database test runs `migrate.Up` against a real Postgres 16 container, and every assertion reads back through the same SQL the production binary uses. Schema-snapshot tests pin the resulting DDL.

### Pattern F — "Skip placeholders count as coverage"

11 of the 13 LLD merge-gate scenarios are unconditional `t.Skip(...)` in `e2e/orchestrator/skipped_scenarios_test.go`. The file-level comment admits `requireHarness` is "intentionally NOT called: skip-stubs should report SKIP regardless of Docker availability, so the merge-gate manifest is stable."

Examples in `skipped_scenarios_test.go`:
- `TestScenario_cancel_and_replace_scan` (#84)
- `TestScenario_AdaptHL7ToFHIR`, `TestScenario_FHIRScanRunnerEmitsResourceChange`, `TestScenario_VendorChangeFeedEmitsResourceChange`, `TestScenario_TopicMatcherFanout`, `TestScenario_CancelAndReplaceWindowExpires`, `TestScenario_DeliveryRetryThenSuccess`, `TestScenario_DeliveryDeadLetterAfterMax`, `TestScenario_SubscriptionStatusOperation`, `TestScenario_AuditChainIsValid` — all `t.Skip`.

Under the no-fakes rule, every skipped scenario becomes a runnable test once docker-compose can boot the real dependency it needs. Skip-stubs are not allowed to remain past the merge of the harness component they depend on.

### Pattern G — "Documentation lies are not testable in code"

The Helm chart, architecture doc, and runbooks describe a config tree, port topology, signing pipeline, and runbook procedure that the binary doesn't implement. There is no test that compares documentation surface to code reality.

- Finding #56 — Helm chart has no `topics:` block / catalog mount template.
- Findings #51, #55 — Helm exposes `containerPort: 8081` (probes) and `port: 9090` (metrics); the binary opens exactly one listener on `cfg.Server.HTTP.Bind`.
- Finding #57 — Helm mounts a TLS Secret, renders `cert_file`/`key_file`; the binary calls `srv.Serve` (cleartext, Finding #19).
- Findings #107-109, #121-123 — architecture and runbooks reference config keys, ports, metrics, and subcommands that don't exist.
- Finding #110 — release ldflag uses `main.version` (lowercase); the variable is `main.Version`.

The fix is not in the binary; it is a docs-vs-code lint that parses every YAML/markdown reference and asserts code reality. Section 3 H8 details the lint.

### Pattern H — "Activator / wiring slot stubs that ship in production"

A handful of `Deps.*` slots are nullable for the test path. Production wiring leaves them nil. The router's middleware is nil-safe (returns pass-through), so the route runs without the protection.

- Finding #18 + #141 — `Deps.SubscriptionCreateRateLimit` and `WSBindingTokenRateLimit` are nil in production; `auth.ClientRateLimiter.Middleware()` is nil-safe and returns pass-through.
- Finding #113 — `Deps.Metrics` never set; per-handler counters record nothing.
- Finding #114 — `Deps.Logger` never set; activate-path error swallows are silent.
- Finding #115 — eleven other `Deps.*` knobs unset.

Under the no-fakes rule the harness deletes its own `Deps` overrides and forces tests to use whatever production wiring populates. A test that needs a populated `Deps.Metrics` must drive a real load through the real `/metrics` scrape and assert deltas — that's the only honest signal.

---

## Section 2 — Categorizing the ~199 findings against test infrastructure needs

Each finding maps to one or more patterns. Closing them requires the suite to acquire harness components that boot REAL dependencies. Below: each finding category, the real-software harness investment that closes it, and the audit IDs in scope.

### Category 1 — Wiring regressions (the dominant class)

Findings: #1, #2, #3, #4, #5, #6, #7, #8, #10, #13, #14, #15, #16, #17, #21, #22, #23, #26, #27, #30, #31, #36, #37, #38, #45, #46, #49, #50, #54, #59, #62, #99, #113, #114, #115, #134, #161, #162, #166, #167, #186.

Real-software needed: real Postgres 16, real auth issuer (HAPI FHIR-compatible JWT minter), real EHR FHIR server, real subscriber relays. Every wiring assertion goes through the production routes against real downstream effects.

### Category 2 — TLS / mTLS path

Findings: #19, #20, #57, #119, #185.

Real-software needed: cfssl or cert-manager-style PKI (the harness invokes the same cfssl binary the operator would) to mint a real CA + server + client certs; a real openssl s_client / Go `crypto/tls` test client to dial; real reverse proxy (nginx) for TLS-terminated topology.

### Category 3 — Multi-modality ingestion

Findings: #1, #2 (scanrunner), #3 (vendorclient), #5 (webhook), #8, #9, #10 SPI, #24, #25, #26, #88, #89, #94, #95, #96, #131, #132, #169, #170.

Real-software needed: HAPI FHIR JPA Server (the reference R4/R4B/R5 implementation) loaded with Synthea-generated test patients for FHIR scan. A real vendor REST API mocked is forbidden — instead, run HAPI FHIR with custom resource shapes generated by Synthea + Bulk Data API extension. For HL7 v2, run the production-grade Mirth Connect or HAPI HL7v2 listener as the upstream sender against the binary's real MLLP listener.

### Category 4 — Audit-chain integrity

Findings: #9, #11, #12, #80, #81, #87, #122, #123, #154, #155, #166, #174.

Real-software needed: real Postgres 16 with the production migration applied. A standalone `audit-chain-walker` Go tool that connects with `pgx`, iterates rows via real SQL, recomputes hashes against the writer's canonicalization, and asserts byte equality. No in-memory mock store anywhere.

### Category 5 — Cross-tenant isolation

Findings: #163, #166, #189, #191.

Real-software needed: real Postgres + real JWKS issuer + N real bearer tokens with distinct `client_id` claims. Every read endpoint hit through the real verifier middleware.

### Category 6 — Rate-limit + admission

Findings: #18, #34, #35, #45, #100, #141, #167, #191, #196.

Real-software needed: real load generator (`vegeta` or `hey` binary invoked as a subprocess) hitting the real binary's HTTP listener with real bearer tokens. No simulated load, no in-process bypass.

### Category 7 — Lifecycle phases

Findings: #160, #161, #180, #92, #93, #178, #179.

Real-software needed: real signal dispatch (`syscall.Kill` against the real `os.Process`); real config-file rewriter (`os.WriteFile` against the on-disk path the binary watches); real `/healthz`, `/readyz`, `/startup` HTTP probes hit by an external client. The test runs the binary as a real OS process via `exec.Cmd`.

### Category 8 — Long-running pipelines (clock control)

Findings: #29, #42, #43, #44, #114, #115, #138, #139, #160, #178, #196, #143.

This is where "real" doesn't directly apply. There is no "real clock-rewinding service." The strategy is to add config-or-API-driven clock seams to the binary itself (Section 6), then drive them via real config writes / real admin API calls. The test never patches `time.Now()` from outside the process. Test runtime trades wall-clock seconds for binary-emitted state changes — when the binary's retention sweeper runs every 30s, the test waits 30s; when it can be tuned to run every 100ms via `pipeline.scheduler.recovery_interval`, the test does that.

### Category 9 — Observability assertions

Findings: #21, #22, #23, #62, #66, #109, #113, #121, #122, #149, #166, #176, #199.

Real-software needed:
- Real Prometheus (the `prom/prometheus` binary) scraping the binary's `/metrics`, with a real `promtool query instant` invocation.
- Real OTel Collector Contrib (`otel/opentelemetry-collector-contrib`) running in docker-compose with file-exporter pipeline; the binary's OTLP/HTTP traffic lands in a real collector and is dumped to JSON for assertions.
- Real slog text/JSON output captured from the process's stdout/stderr (no in-process slog drain mock).

### Category 10 — Channel delivery

Findings: #14, #15, #16, #17, #27, #50, #94, #95, #96, #97, #106, #116, #119, #120, #121, #127, #135, #136, #143, #146, #164, #184, #185, #194.

Real-software needed:
- Real rest-hook subscriber: a tiny standalone Go binary built from `cmd/test-resthook-subscriber` (added under no-fakes rule) that runs as a real HTTP service in docker-compose, captures every request, and exposes an HTTP API for the test to query received payloads.
- Real WebSocket subscriber: the `nhooyr.io/websocket` library's reference client, run as a standalone binary in docker-compose.
- Real SMTP relay: `mailpit` (https://github.com/axllent/mailpit) — open-source SMTP testing relay with REST API for captured-message assertions. Drop-in replacement for any "fake SMTP."
- Real FHIR messaging endpoint (the message channel): HAPI FHIR JPA Server's `$process-message` endpoint.
- Real STARTTLS-stripping MITM: a tiny standalone proxy binary built specifically for the strip-STARTTLS test (added under no-fakes rule), runs in docker-compose. Or an existing tool like `mitmproxy` configured to strip STARTTLS.

### Category 11 — Documentation conformance

Findings: #40, #51, #52, #53, #54, #55, #56, #57, #58, #59, #60, #61, #62, #65, #66, #67, #68, #69, #70, #71, #72, #73, #74, #102, #107, #108, #109, #121, #122, #123, #125, #127, #135, #136, #181, #182, #195.

Real-software needed: docs-lint tool that parses real markdown / real YAML and asserts code reality. Real `helm template` invocation against the binary's `--check-config`. Real `kind` cluster + real `helm install` for the chart-vs-binary contract test.

### Category 12 — Auth (token endpoint, verifier, JWKS, JTI)

Findings: #14, #15, #18, #28, #30, #31, #45, #47, #48, #110, #112, #114, #115, #117, #118, #125, #126, #127, #142, #165, #167.

Real-software needed: a real JWKS issuer. Use `Keycloak` (open-source IAM, official `quay.io/keycloak/keycloak` image) running in docker-compose, configured with a real realm and real client. The test calls Keycloak's real token endpoint to mint real JWTs; the binary's verifier fetches Keycloak's real JWKS; the wire path is identical to production. No in-process JWKS mock.

### Category 13 — Storage retention / partition / migration

Findings: #4, #11, #12, #13, #36, #56, #75, #76, #77, #78, #79, #80, #81, #82, #83, #100, #138, #139, #154, #155, #158, #159, #187, #188.

Real-software needed: real Postgres 16. Long-time scenarios use the binary's tunable retention/partition cadences (Section 6 adds the knobs) so tests run in seconds of wall clock against real timestamps.

### Category 14 — Input validation / SSRF / DNS rebinding

Findings: #98, #112, #128, #129, #130, #133, #137, #163, #164, #168, #171, #190, #194, #197.

Real-software needed: a real DNS server (`coredns`, the standard CNCF DNS) running in docker-compose, with a per-test plugin script that returns different answers per-query-number. The binary points its custom resolver at coredns. No in-process DNS mock.

### Category 15 — Test-stub gaps (the suite's own self-audit)

Findings: #84, #85, #86, #87, #88, #89, #90, #91, #92, #93, #101, #117.

Real-software needed: none — these are deletions. The test infrastructure deletes the harness's `stubChannelActivator`, `principalMiddleware`, `RegisterSubscriber`-via-SQL, and forces every test to use real components. A static lint asserts no production code imports `e2e/`.

---

## Section 3 — Required harness work

Each item below is a separately-deliverable harness component. Every one becomes a story in epic #91.

The list collapsed from 15 to **9** because several items in the prior strategy were wrappers around fakes that under the no-fakes rule reduce to "use docker-compose to run X." Those collapsed into H1 (the docker-compose harness) and H8 (docs-lint).

### H1. RealStackHarness (docker-compose-driven)

**Purpose** — Boot the production `cmd/fhir-subs` binary as an OS process against a real-software dependency stack. Every test scenario is a function that takes a `RealStack` handle, makes real HTTP / MLLP / SMTP / WebSocket calls, and asserts against the real downstream effects.

**Real software in the stack:**
- **Postgres 16** (`postgres:16-alpine`) — the database. `migrate.Up` runs against it.
- **Keycloak 24+** (`quay.io/keycloak/keycloak:24.0`) — real OIDC issuer publishing real JWKS, real token endpoint. Replaces every in-process `principalMiddleware` and every `Authorization: Bearer test` cheat.
- **HAPI FHIR JPA Server** (`hapiproject/hapi:latest`) — real FHIR server pre-loaded with Synthea-generated patient data for the FHIR scan / vendor change-feed paths. Replaces `e2e/mockehr/fhir.go` and `e2e/mockehr/changefeed.go`.
- **Mirth Connect** (`nextgenhealthcare/connect:latest`) OR **HAPI HL7v2** standalone listener — real HL7 v2 sender driving the binary's MLLP listener over real TCP/TLS. Replaces ad-hoc MLLP test clients that bypass framing.
- **Mailpit** (`axllent/mailpit:latest`) — real SMTP relay with REST API for captured-message assertions.
- **CoreDNS** (`coredns/coredns:latest`) — real DNS server for SSRF/rebinding tests, configured via Corefile rewrites per-scenario.
- **Prometheus** (`prom/prometheus:latest`) — real metrics scraper.
- **OTel Collector Contrib** (`otel/opentelemetry-collector-contrib:latest`) — real OTLP receiver with file-exporter pipeline.
- **nginx** (`nginx:stable-alpine`) — real reverse proxy for TLS-terminated topology tests.
- **mitmproxy** (`mitmproxy/mitmproxy:latest`) — real STARTTLS-stripping MITM for the strip-STARTTLS attack scenario (Finding #184).
- **A real test rest-hook subscriber binary** — built from `cmd/test-resthook-subscriber` (new, added under no-fakes rule). Runs as a real HTTP service in docker-compose, captures every request, exposes a REST API for assertions.
- **A real test websocket subscriber binary** — built from `cmd/test-ws-subscriber` (new). Runs as a real WS client in docker-compose.

**Launching:** A `Makefile` target `make e2e-realstack-up` invokes `docker compose -f e2e/realstack/docker-compose.yml up -d`. The Go-side harness is a thin client (`e2e/realstack/`) that:
1. Asserts the compose stack is healthy (real `/healthz` probes against every service).
2. Renders a config file pointing the binary at every service's real address (Postgres URL, Keycloak issuer, OTLP endpoint, etc.).
3. Launches `cmd/fhir-subs` via `exec.Cmd` as a real child process with that config.
4. Waits for the binary's real `/readyz` to report 200.
5. Returns a handle that lets tests issue real HTTP/WS/MLLP requests and query each real dependency's real API for captured state.

**Deliverable:** `e2e/realstack/docker-compose.yml`, `e2e/realstack/{boot.go, services.go, asserts.go, doc.go}`, `cmd/test-resthook-subscriber/`, `cmd/test-ws-subscriber/`. Make targets: `e2e-realstack-up`, `e2e-realstack-down`, `e2e-realstack-test`.

**Acceptance criteria:** Boots the full stack in <90s on CI runners, exercises every documented capability through its real route, supports >50 concurrent test scenarios via per-test docker-compose project namespaces (compose project name = test-name) for isolation. The existing harness files in `e2e/harness/` are deleted (`api.go`, `pipeline.go`, `scripted_adapter.go`, `tls.go`, `topic_seed.go`).

**Severity:** BLOCKER — foundation for every other component.

**Findings closed:** All wiring-regression findings (~40 IDs). Section 4 maps each to a specific real-software-driven test.

### H2. AuditChainWalker (real-DB tool)

**Purpose** — Read the production `audit_log` table from the real Postgres in H1's stack, verify chain linkage byte-for-byte, fail when the real writer's canonicalization disagrees with the on-disk row.

**Real software:** Postgres 16 (from H1), real `pgx` driver, real SHA-256 over real bytes. No in-memory chain.

**Deliverable:** `tools/audit-chain-walker/{main.go, walker.go}` plus a thin Go test-side wrapper at `e2e/realstack/auditchain.go` that calls the tool as a subprocess and asserts on its real output.

**Acceptance criteria:** Findings #9, #11, #12, #80, #81, #87, #122, #123, #154, #155 each correspond to a real-DB regression test that fails until the production code is fixed; walker runs in <1s against a 10k-row real chain.

**Severity:** BLOCKER.

### H3. LoadDriver (real `vegeta`-driven)

**Purpose** — Drive sustained N RPS at the binary's real HTTP / MLLP listeners using the real `vegeta` binary (`tsenart/vegeta`), assert real response codes, real `Retry-After` headers, real 429s.

**Real software:** `vegeta` binary invoked via `exec.Cmd` (no in-process load gen). Tokens minted by Keycloak (H1). Real PostgreSQL queried via `pgx` for downstream effect assertions.

**Deliverable:** `e2e/realstack/load/{vegeta.go, scenarios.go, doc.go}`.

**Acceptance criteria:** Findings #18, #34, #35, #100, #141, #167 each have a regression test with deterministic expected output (e.g., "300 RPS sustained for 30s produces ≥45 responses with status 429 carrying real `Retry-After` headers"); test uses real wall clock against the real binary, no simulated time.

**Severity:** HIGH.

### H4. SkipScenarioGate (deletion-only)

**Purpose** — Delete every `t.Skip(...)` placeholder in `e2e/orchestrator/skipped_scenarios_test.go`. Each scenario is rewritten against H1's real stack. A static lint asserts no `t.Skip` remains in `skipped_scenarios_test.go`.

**Real software:** none (this is a code-removal + rewrite story).

**Deliverable:** `e2e/orchestrator/skipped_scenarios_test.go` deleted; each scenario reborn as a real-stack test under `e2e/orchestrator/scenario_<name>_test.go`.

**Acceptance criteria:** All 13 LLD merge-gate scenarios run against H1's real stack; CI gate manifests 13/13 passing, not 2/13.

**Severity:** HIGH.

### H5. CertFixture (real cfssl-driven)

**Purpose** — Mint real certificate chains (CA → intermediate → server, plus client certs for mTLS) on demand via the real `cfssl` binary or via Go's `crypto/x509` against real CA private keys generated at test setup. Drive the binary's TLS path with real valid, missing, expired, wrong-CA certs.

**Real software:** `cfssl` binary (or Go `crypto/x509` — both produce real X.509 certs). Real `openssl s_client` for client-side dialing variants the test wants to assert on.

**Deliverable:** `e2e/realstack/certfix/{ca.go, server.go, client.go, doc.go}`. The certs are loaded into nginx (for TLS-terminated topology) and into the binary's HTTP / MLLP TLS config.

**Acceptance criteria:** Findings #19, #20, #57, #119, #185 each have a real-cert-driven regression test; tests cover real expiry (cert valid until `t-1h`, `t+24h`, `t-365d`), real CA-chain depth (1, 2, 3), real client-cert presence (none, valid, expired, wrong-issuer).

**Severity:** BLOCKER.

### H6. AdversarialDNS (real CoreDNS-driven)

**Purpose** — Drive DNS-rebinding, IDN/punycode, CG-NAT, IPv6 site-local cases against the real binary's real URL validator AND real rest-hook delivery dialer using a real CoreDNS instance with controlled responses.

**Real software:** CoreDNS (from H1's stack) with a per-scenario `Corefile` that rewrites answers per-query-number (e.g., first answer is `1.2.3.4`, second is `10.0.0.5`). The binary's resolver is pointed at CoreDNS via real `--resolver-addr` flag (Section 6 adds the knob if it doesn't exist).

**Deliverable:** `e2e/realstack/dnsharness/{corefile_templates.go, scenarios.go, doc.go}`.

**Acceptance criteria:** Findings #98, #112, #128, #129, #130, #133, #134 each reproduce in <2s of real wall clock against real DNS responses.

**Severity:** HIGH.

### H7. ObservabilityRig (real Prometheus + real OTel Collector)

**Purpose** — Assert metrics deltas via real `promtool query instant` against the real Prometheus server scraping the real binary; assert spans via real OTel Collector file-exporter dumps.

**Real software:** Prometheus (`prom/prometheus`), OTel Collector Contrib (`otel/opentelemetry-collector-contrib`), both from H1's stack.

**Deliverable:** `e2e/realstack/obs/{prom.go, otel.go, slog.go, doc.go}`. The slog drain captures the binary's real stdout/stderr via `exec.Cmd`'s `Stdout` pipe — no in-process slog handler swap.

**Acceptance criteria:** Findings #21, #22, #23, #62, #66, #109, #113, #149, #166, #175, #176, #199 each reproduce against real telemetry data.

**Severity:** HIGH.

### H8. DocsLint (real markdown/YAML parser)

**Purpose** — Compare every operator-facing document against the binary; fail when they disagree. No "real" alternative needed because the source of truth is text.

**Scope:** A standalone tool `tools/docs-lint/` that:
- Parses YAML examples in `docs/architecture.md`, `docs/operations/*.md`, `deploy/helm/fhir-subs/values.yaml` and asserts every key resolves to a field in `cmd/fhir-subs/config.go`.
- Parses subcommand references (`fhir-subs <subcommand>`) and asserts each is registered in `main.go`.
- Parses metric-name references (`fhir_subs_*_total`) and asserts each is registered in metrics.go AND scrape-able from real Prometheus (chained with H7).
- Parses port references (`:8081`, `:9090`, etc.) and asserts each is opened by the binary (chained against H1's real listener probes).
- Asserts every `.md` under `docs/` is linked from `mkdocs.yml` `nav:` (closes Finding #181).

**Deliverable:** `tools/docs-lint/{main.go, parsers.go, asserts.go, doc.go}`. Runs in CI as a blocking check.

**Acceptance criteria:** Findings #40, #51-#74, #102, #107-#109, #121-#123, #125, #127, #135, #136, #181, #182, #195 each surface as lint failures today; lint runtime <10s.

**Severity:** HIGH.

### H9. ProductionStubLint (static AST lint)

**Purpose** — Static lint that fails the build if production code retains a "test-time stub" the binary should not ship. Real software: Go's `go/ast` package (real AST walking, not a mock).

**Scope:** `tools/prod-stub-lint/` with parsers for:
- `defaultActivator{}` for non-rest-hook channels (Finding #50).
- `var _ channel.Channel = channel.Channel(nil)` placeholder (Finding #49).
- `e2e/` imports from production code (Finding #119).
- `t.Skip(...)` annotations older than 30 days via real `git blame` (Finding #84).

**Deliverable:** `tools/prod-stub-lint/{main.go, ast_walker.go, doc.go}`.

**Acceptance criteria:** Findings #49, #50, #84, #97, #119 each surface as build failures today.

**Severity:** MEDIUM.

---

**Total harness components: 9.**

## Real software / services the test infrastructure depends on

- **PostgreSQL 16** (`postgres:16-alpine`)
- **Keycloak 24+** (`quay.io/keycloak/keycloak:24.0`) — OIDC + JWKS
- **HAPI FHIR JPA Server** (`hapiproject/hapi:latest`) — reference R4/R4B/R5
- **Synthea** (`synthea-international` jar / docker image) — real FHIR patient data generator
- **Mirth Connect** (`nextgenhealthcare/connect:latest`) OR **HAPI HL7v2 listener** — real HL7 v2 sender
- **Mailpit** (`axllent/mailpit:latest`) — SMTP relay
- **CoreDNS** (`coredns/coredns:latest`) — DNS
- **Prometheus** (`prom/prometheus:latest`) — metrics
- **OTel Collector Contrib** (`otel/opentelemetry-collector-contrib:latest`) — tracing
- **nginx** (`nginx:stable-alpine`) — TLS reverse proxy
- **mitmproxy** (`mitmproxy/mitmproxy:latest`) — STARTTLS-stripping MITM
- **vegeta** (`tsenart/vegeta` binary) — load generator
- **cfssl** (`cloudflare/cfssl` binary) OR Go `crypto/x509` — cert minting
- **kind** (`kindest/node:v1.30.0`) — Kubernetes-in-Docker for the Helm contract test
- **helm 3** (`helm/helm:latest` binary)
- **kubectl** (`bitnami/kubectl:latest` binary)
- **Two new in-repo binaries** built under `cmd/test-resthook-subscriber/` and `cmd/test-ws-subscriber/` — real Go services run in docker-compose for subscriber-side capture.

Every one is open-source, freely-pullable, and runs in a single `docker-compose.yml` orchestrated by a `make` target.

---

## Section 4 — Per-finding e2e mapping

Format: `Finding #ID | Category | HarnessDeps | Proposed e2e test`. Each test runs against the real-stack from H1. The "harness deps" column lists which subset of H1's real services the test exercises.

| Finding | Category | Harness | E2E test |
|---|---|---|---|
| 1 | Wiring (SPI Build*) | H1 (HAPI FHIR + Postgres) | `prod_binary_adapter_spi_build_invocation_test.go` |
| 2 | Wiring (scanrunner) | H1 (HAPI FHIR + Postgres) | `prod_binary_fhir_scan_runner_emits_test.go` |
| 3 | Wiring (vendorclient) | H1 (HAPI FHIR change-feed + Postgres) | `prod_binary_vendor_changefeed_emits_test.go` |
| 4 | Wiring (supervisor) | H1 + signal dispatch | `prod_binary_adapter_supervisor_panic_recovers_test.go` |
| 5 | Wiring (webhook) | H1 + real HTTP poster | `prod_binary_webhook_ingress_test.go` |
| 6 | Wiring (hydration) | H1 (HAPI FHIR _include) | `prod_binary_hydration_full_resource_test.go` |
| 7 | Wiring (admin routes) | H1 + Keycloak | `prod_binary_admin_surface_test.go` |
| 8 | Wiring (public routes) | H1 (no token) | `prod_binary_public_metadata_unauth_test.go` |
| 9 | Audit placeholder hash | H1 + H2 (audit-chain-walker) | `prod_binary_audit_hash_not_placeholder_test.go` |
| 10 | observability.Start unwired | H1 + H7 | `prod_binary_observability_started_test.go` |
| 11 | Audit schema mismatch | H1 + H2 | `prod_binary_audit_schema_conformance_test.go` |
| 12 | audit verify CLI broken | H1 + H2 | `prod_binary_audit_verify_cli_test.go` |
| 13 | partition+retention unwired | H1 + Section-6 cadence knobs | `prod_binary_partition_rollover_test.go`, `prod_binary_retention_sweeper_test.go` |
| 14 | WS channel unregistered | H1 + cmd/test-ws-subscriber | `prod_binary_websocket_delivery_test.go` |
| 15 | Email channel unregistered | H1 + Mailpit | `prod_binary_email_delivery_test.go` |
| 16 | Message channel unregistered | H1 + HAPI FHIR `$process-message` | `prod_binary_message_delivery_test.go` |
| 17 | chReg only rest-hook | H1 + cmd/test-resthook-subscriber + cmd/test-ws-subscriber + Mailpit | `prod_binary_channel_registry_completeness_test.go` |
| 18 | Per-client rate limits unwired | H1 + H3 (vegeta) + Keycloak | `prod_binary_subscription_create_rate_limit_test.go` |
| 19 | HTTP cleartext only | H1 + H5 (cfssl) | `prod_binary_http_tls_required_test.go` |
| 20 | MLLP TLS unwired | H1 + H5 + Mirth Connect TLS sender | `prod_binary_mllp_tls_required_test.go` |
| 21 | /metrics not mounted | H1 + H7 (real Prometheus) | `prod_binary_metrics_endpoint_test.go` |
| 22 | API metrics unwired | H1 + H7 | `prod_binary_api_metrics_increment_test.go` |
| 23 | OTel tracer not init | H1 + H7 (real OTel Collector) | `prod_binary_otel_tracing_test.go` |
| 24 | Vendor MapToFHIR stub | H1 + Mirth-driven HL7 | `prod_binary_vendor_adapter_translation_test.go` |
| 25 | Default MapToFHIR stub | H1 + Mirth-driven HL7 | `prod_binary_default_adapter_translation_test.go` |
| 26 | Only default registered | H1 | `prod_binary_adapter_registration_test.go` |
| 27 | defaultActivator no-op | H1 + cmd/test-resthook-subscriber | `prod_binary_real_activator_handshake_test.go` |
| 28 | BaseURL ignores insecure | H1 + H5 | `prod_binary_capabilitystatement_base_url_test.go` |
| 29 | WSBindingTTL hardcoded | H1 + Section-6 knob | `prod_binary_ws_binding_ttl_config_test.go` |
| 30 | SMART discovery missing | H1 + Keycloak | `prod_binary_smart_discovery_extension_test.go` |
| 31 | JWKS endpoint missing | H1 | `prod_binary_jwks_endpoint_test.go` |
| 32 | Wildcard bind default | H8 (docs-lint) | docs-lint check on default config |
| 33 | MLLP defaults hardcoded | H1 + Section-6 knobs | `prod_binary_mllp_config_tunables_test.go` |
| 34 | pgxpool defaults | H1 + Section-6 knobs | `prod_binary_db_pool_tunables_test.go` |
| 35 | WS channel config ignored | H1 + cmd/test-ws-subscriber | `prod_binary_websocket_config_applied_test.go` |
| 36 | outbox/claim/partition/retention unimported | H1 + Section-6 cadence | covered by #13 |
| 37 | wakeup/queue phantom | H9 | static lint, no e2e |
| 38 | engine packages unimported | H9 | static lint |
| 39 | Empty catalog dead pipeline | H1 | `prod_binary_empty_catalog_emits_warn_test.go` |
| 40 | dead-letters CLI missing | H8 | docs-lint |
| 41 | Probe-only fallback | H1 | `prod_binary_probe_only_emits_error_test.go` |
| 42 | ActivationTimeout hardcoded | H1 + Section-6 knob | `prod_binary_activation_timeout_config_test.go` |
| 43 | Scheduler retry hardcoded | H1 + Section-6 knobs | `prod_binary_scheduler_retry_curve_config_test.go` |
| 44 | HL7 processor hardcoded | H1 + Section-6 knobs | `prod_binary_hl7_processor_config_test.go` |
| 45 | RateLimitConfig ignored | H1 + H3 | covered by #18 |
| 46 | rest-hook transport leak | H1 + cmd/test-resthook-subscriber + signal dispatch | `prod_binary_resthook_transport_shutdown_test.go` |
| 47 | allow_insecure_jwks coupling | H1 | `prod_binary_allow_insecure_decoupled_test.go` |
| 48 | TrustedIssuers advisory | H1 + Keycloak (multiple realms) | `prod_binary_trusted_issuers_enforced_test.go` |
| 49 | channel.Channel placeholder | H9 | static lint |
| 50 | defaultActivator hardcoded | H9 | static lint |
| 51 | Helm probe port mismatch | H8 + Helm contract test (kind) | helm-vs-binary contract |
| 52 | Helm placeholder interpolation | H8 + Section-6 (env-substitution) | helm-vs-binary contract |
| 53 | Helm extra keys silently dropped | H8 + Section-6 (KnownFields) | helm-vs-binary contract |
| 54 | probe_bind missing field | H8 | docs-lint |
| 55 | Metrics port 9090 fictional | H8 + Helm contract | helm-vs-binary contract |
| 56 | Helm no topic catalog | H8 + Helm contract | helm-vs-binary contract |
| 57 | Helm TLS mounted not served | H8 + Helm contract + H5 | helm-vs-binary contract |
| 58 | Init container misnamed | H8 | docs-lint (low priority) |
| 59 | NetworkPolicy default | H8 + kind cluster | helm-vs-binary contract |
| 60 | Personal GHCR default | H8 | docs-lint |
| 61 | replicaCount + HPA | H8 | docs-lint |
| 62 | automountServiceAccountToken | H8 | docs-lint |
| 63 | Integration tests opt-in | (CI config) | `make ci` always-on |
| 64 | E2E tests opt-in | (CI config) | `make ci` always-on |
| 65 | No coverage threshold | (CI config) | go test --coverpkg threshold |
| 66 | Unsigned images | H8 | docs-lint or release-lint |
| 67 | Multi-arch only on tag | (CI config) | n/a |
| 68 | No CVE watching | (CI config) | govulncheck step |
| 69 | golangci default | (CI config) | .golangci.yml + ci |
| 70 | Nightly no alerting | (CI config) | n/a |
| 71 | Loose Go pin | (CI config) | n/a |
| 72 | CI builds image, never runs | (CI config) | smoke-run image step |
| 73 | Unused id-token | (CI config) | n/a |
| 74 | No mkdocs strict | H8 | docs-lint |
| 75 | dead_letters key_version | H1 + Section-6 (key rotation API) | `prod_binary_dead_letters_key_rotation_test.go` |
| 76 | deliveries.bundle dead | H9 | static lint |
| 77 | schema_migrations.checksum | H1 | `prod_binary_migration_checksum_test.go` |
| 78 | next_event_number missing | H9 | static lint |
| 79 | retired_at unwritten | H9 | static lint |
| 80 | Audit nullable mismatch | H1 + H2 | covered by #11 |
| 81 | Audit timestamp drift | H1 + H2 | covered by #11 |
| 82 | Dead index | H9 | static lint |
| 83 | Two event-progress columns | H9 | static lint |
| 84 | 11/13 skip stubs | H4 (delete + rewrite) | every skip-stub gains a real test |
| 85 | principalMiddleware bypass | H1 (delete fake) | every test migrated to real Keycloak |
| 86 | stubChannelActivator | H1 (delete fake) | every test migrated to real subscribers |
| 87 | No hash-byte comparison | H1 + H2 | covered by #9 |
| 88 | Vendor adapter Lex-only | H1 + Mirth | covered by #24 |
| 89 | Every test uses default | H1 + Mirth + per-vendor adapter registration | covered by #26 |
| 90 | Raw-SQL subscription setup | H1 (delete helper) | every test goes through POST /Subscription |
| 91 | Auth-disabled prod test | H1 + Keycloak | `prod_binary_auth_required_test.go` (replaces auth-off test) |
| 92 | SIGHUP test-only | H1 + signal dispatch + real config-file rewrite | `prod_binary_sighup_config_reload_test.go` |
| 93 | Secret-file watcher test-only | H1 + real secret-file rewrite | `prod_binary_secret_file_watcher_test.go` |
| 94 | Capability-claim mismatch | H1 + per-channel real subscriber | per-channel capability verifier |
| 95 | Direct adapter HL7 mismatch | H1 + Mirth | covered by #24 |
| 96 | demo Hydrate nil | H1 + HAPI FHIR | covered by #6 |
| 97 | demo HTTP defaultClient | H9 | static lint |
| 98 | URL validator ctx Background | H1 + H6 (CoreDNS) | `prod_binary_ssrf_lookup_cancel_test.go` |
| 99 | context.Background bookkeeping | H1 + signal dispatch | `prod_binary_shutdown_drains_bookkeeping_test.go` |
| 100 | ListByClient no LIMIT | H1 + H3 | `prod_binary_search_subscriptions_limit_test.go` |
| 101 | Unwired-package self-tests | H9 | static lint |
| 102 | LogFormat hardcoded | H1 + H7 (slog drain) | `prod_binary_log_format_text_test.go` |
| 103 | Demo can't deliver | H8 + Helm contract | demo-vs-binary contract |
| 104 | Demo HTTP rejected | H8 + Helm contract | demo-vs-binary contract |
| 105 | Demo binaries not in image | H8 + Helm contract | demo-vs-binary contract |
| 106 | Handshake zero timestamp | H1 + cmd/test-resthook-subscriber | `prod_binary_resthook_handshake_timestamp_test.go` |
| 107 | Architecture config fiction | H8 | docs-lint |
| 108 | ${env:VAR} not implemented | H8 + Section-6 (env interp) | docs-lint |
| 109 | OTel recipes broken | H8 + H7 | docs-lint + observability harness |
| 110 | --set allowlist holes | H8 | docs-lint |
| 111 | Validate gaps | H1 | `prod_binary_check_config_negative_test.go` |
| 112 | Audience-empty bypass | H1 + Keycloak | `prod_binary_audience_required_test.go` |
| 113 | Deps.Metrics nil | H1 + H7 | covered by #22 |
| 114 | Deps.Logger nil | H1 + H7 | covered by #114-router |
| 115 | Eleven Deps unset | H1 | `prod_binary_deps_population_test.go` |
| 116 | Demo AES key checked in | H8 (secret scan) | docs-lint |
| 117 | demo CLI mismatched ports | H8 + Helm contract | demo-vs-binary contract |
| 118 | demo R4B/R5 channel mix | H8 + Helm contract | demo-vs-binary contract |
| 119 | demo imports e2e | H9 | static lint |
| 120 | Compose multi-arch | H8 | docs-lint |
| 121 | Dead-letters runbook fiction | H8 | docs-lint |
| 122 | Horizontal-scale runbook | H8 | docs-lint |
| 123 | Recovery runbook gap | H8 | docs-lint |
| 124 | mllp.listeners + db.url | H1 | covered by #111 |
| 125 | Architecture auth claim | H1 + Keycloak | covered by #112 |
| 126 | --set plumbing holes | H8 | docs-lint |
| 127 | TrustedIssuers advisory docs | H8 | docs-lint |
| 128 | URL validator IPv4/v6 gaps | H1 + H6 | `prod_binary_ssrf_cgnat_block_test.go` |
| 129 | DNS rebinding race | H1 + H6 | `prod_binary_dns_rebinding_test.go` |
| 130 | allow_insecure dual-purpose | H1 | covered by #47 |
| 131 | MSH-7 timestamp parse | H1 + Mirth | `prod_binary_msh7_timestamp_dialect_test.go` |
| 132 | MSH-7 escape handling | H1 + Mirth | `prod_binary_msh7_escape_chars_test.go` |
| 133 | IDN punycode loopback | H1 + H6 | `prod_binary_ssrf_idn_loopback_test.go` |
| 134 | URLValidator AllowHosts | H1 + Section-6 knob | `prod_binary_url_validator_allowhosts_test.go` |
| 135 | Email channel config | H1 + Mailpit | covered by #15 + config-application |
| 136 | WebSocket channel config | H1 + cmd/test-ws-subscriber | covered by #14 + config-application |
| 137 | DELETE soft-delete spec | H1 | `prod_binary_subscription_delete_spec_test.go` |
| 138 | Bootstrap partitions only | H1 + Section-6 cadence | covered by #13 |
| 139 | Partition trigger uses now() | H1 | `prod_binary_partition_backfill_test.go` |
| 140 | Encrypt key version drop | H1 + Section-6 (key rotation) | covered by #75 |
| 141 | Rate-limit Middleware nil-safe | H1 + H3 | covered by #18 |
| 142 | JWKS stampede | H1 + Keycloak + H3 | `prod_binary_jwks_stampede_test.go` |
| 143 | WS bind-timeout DoS | H1 + cmd/test-ws-subscriber + H3 | `prod_binary_ws_bind_timeout_dos_test.go` |
| 144 | MLLP framer pending | H1 + Mirth (oversized frames) | `prod_binary_mllp_framer_oversize_test.go` |
| 145 | MLLP startRead race | H1 + signal dispatch + race detector | static + leak detector |
| 146 | WS subprotocol hardcoded | H8 | docs-lint |
| 147 | token error format mix | H1 + Keycloak | `prod_binary_token_error_format_test.go` |
| 148 | Dead-letters runbook cascade | H8 | docs-lint |
| 149 | GenesisHash deprecated | H9 | static lint |
| 150 | Version mutable globals | H9 | static lint |
| 151 | requirements-docs unpinned | H8 | docs-lint |
| 152 | mkdocs plugin pin | H8 | docs-lint |
| 153 | compose healthcheck | H8 | docs-lint |
| 154 | Audit migrate seq drift | H1 + H2 | covered by #11 |
| 155 | correlation_id chain drift | H1 + H2 | covered by #11 |
| 156 | pgxmock direct require | H9 | static lint |
| 157 | rapid direct require | H9 | static lint |
| 158 | schema_migrations no IF NOT EXISTS | H1 (parallel migrate runners) | `prod_binary_migration_race_fresh_db_test.go` |
| 159 | schemaMigrationsExists race | H1 | covered by #158 |
| 160 | Sequencer ignores caller ctx | H1 + signal dispatch | `prod_binary_lifecycle_ctx_cancels_test.go` |
| 161 | Zero shutdown hooks | H1 + signal dispatch + H7 (phase metrics) | `prod_binary_lifecycle_phases_observed_test.go` |
| 162 | /metadata auth-gated | H1 (no token) | covered by #8 |
| 163 | $events cross-tenant | H1 + Keycloak (two clients) | `prod_binary_events_tenant_isolation_test.go` |
| 164 | Retry-After unbounded | H1 + cmd/test-resthook-subscriber | `prod_binary_retry_after_clamp_test.go` |
| 165 | MinAdminTokenBytes unenforced | H1 | covered by #7 |
| 166 | Admin reads not audited | H1 + H2 | `prod_binary_admin_audit_emission_test.go` |
| 167 | Admin no rate limit | H1 + H3 | `prod_binary_admin_rate_limit_test.go` |
| 168 | _count permissive parse | H1 | `prod_binary_count_param_strict_test.go` |
| 169 | MSH lowercase parse | H1 + Mirth | covered by #131 |
| 170 | MSH escape parse | H1 + Mirth | covered by #132 |
| 171 | DELETE leaks contact | H1 | covered by #137 |
| 172 | CapabilityStatement extension URL | H1 + HAPI FHIR validator | `prod_binary_capabilitystatement_validates_test.go` |
| 173 | search-type no searchParam | H1 | covered by #172 |
| 174 | audit verify dead | H1 + H2 | covered by #12 |
| 175 | Redactor field-name only | H1 + H7 (slog drain) | `prod_binary_log_redactor_endpoint_test.go` |
| 176 | Redactor key-only | H1 + H7 | `prod_binary_log_redactor_value_test.go` |
| 177 | Logger format unreachable | H1 + H7 | covered by #102 |
| 178 | Scheduler dispatch context | H1 + signal dispatch | `prod_binary_scheduler_dispatch_cancel_test.go` |
| 179 | recoverStuck no FOR UPDATE | H1 (multi-pod via two real binaries) + Section-6 cadence | `prod_binary_scheduler_recovery_multipod_test.go` |
| 180 | Startup-race goroutine leak | H1 + signal dispatch + race detector | `prod_binary_startup_race_no_leak_test.go` |
| 181 | Orphan docs pages | H8 | docs-lint |
| 182 | Admin-tool docs orphan | H8 | docs-lint |
| 183 | listDeadLetters Atoi overflow | H1 | `prod_binary_admin_limit_overflow_test.go` |
| 184 | STARTTLS strip silent | H1 + Mailpit + mitmproxy | `prod_binary_email_starttls_strip_test.go` |
| 185 | Email TLS floor | H1 + Mailpit | covered by #119 |
| 186 | RegisterPublicRoutes phantom | H9 | static lint |
| 187 | Migration crash recovery | H1 + signal dispatch mid-migrate | `prod_binary_migration_crash_recovery_test.go` |
| 188 | ALTER suppression | H1 | covered by #77 |
| 189 | StreamActiveByTopic tenant | H1 + Keycloak | covered by #163 |
| 190 | If-Match strong/weak | H1 | `prod_binary_ifmatch_etag_test.go` |
| 191 | $get-ws-binding-token unbounded | H1 + H3 | `prod_binary_ws_binding_token_unbounded_test.go` |
| 192 | opEvents missing additionalContext | H1 + HAPI FHIR | `prod_binary_events_full_resource_test.go` |
| 193 | Brittle string-match classifier | H1 + cmd/test-resthook-subscriber | `prod_binary_resthook_classifier_strings_test.go` |
| 194 | Retry-After zero vs missing | H1 + cmd/test-resthook-subscriber | covered by #164 |
| 195 | mkdocs orphan search | H8 | docs-lint |
| 196 | Scheduler tunables unsurfaced | H1 + Section-6 knobs | `prod_binary_scheduler_config_test.go` |
| 197 | YAML KnownFields off | H1 + Section-6 (KnownFields enabled) | covered by #111 |
| 198 | localhost case-fold | (cleared in audit) | n/a |
| 199 | Path-segment leakage | H1 + H7 | covered by #175 |

The mapping is invariant under the no-fakes rule because the prior strategy's test names were already focused on production-binary behavior; only the harness deps column shifted from in-process fakes to real services.

---

## Section 5 — Open questions

The harness work above resolves most ambiguity, but a handful of items need sign-off before stories are filed.

1. **Are scanrunner / vendorclient / supervisor / hydration / webhook intended to ship in v1, or future-quarters work?** Audit findings (#1-6) treat them as BLOCKERS. If v2, downgrade to "DEFERRED — hide from `docs/architecture.md`" and shelve the HAPI FHIR + Mirth dependencies. Otherwise required.

2. **Is the `default` adapter intended to be a passthrough, or to do real HL7→FHIR translation?** Findings #24-25 treat the hardcoded Bundle as a stub. If "default" is genuinely "no translation, just envelope," vendor-dialect tests do not regress on `default`; they exercise registered vendor adapters via Mirth. Decision: when vendor adapters are registered, does CI run *every* adapter or just one canary?

3. **Is the `audit_log` schema in `migrations/0001_init.sql` canonical, or is `internal/infra/observability/audit/pgstore.go::Migrate` canonical?** Findings #11, #80, #154 are "the two disagree." Strategy assumes the migration is canonical; the writer's INSERTs need updating. H2's audit-chain-walker is invariant either way.

4. **Helm contract test infrastructure: real `kind` cluster vs `helm template` only?** The chart-vs-binary contract test is stronger with a real `kind` cluster (real probes, real ServiceMonitor scrape, real NetworkPolicy enforcement) but adds ~2min to CI. Recommend kind in the nightly job, `helm template` + `--check-config` in the PR job.

5. **Pre-gate or post-gate on `--check-config`?** Validation gaps (Findings #111, #112, #117, #197) are about unstartable configs reporting OK. Pre-gate (close all `--check-config` holes first; gate runs reliably afterward) is the recommendation.

6. **Skip-age policy.** H9's lint flags `t.Skip` older than 30 days. Without a deadline, the pattern recurs.

7. **Findings genuinely untestable even with new harness work.**
   - **#71 (loose Go version pin in CI workflows)** — CI YAML hygiene; no binary test surfaces it. Closes via repo-config change.
   - **#73 (unused `id-token: write` permission scope on release workflow)** — same.
   Both are repo-config-only.

8. **Cross-cuts that need a single owner.** #119 (TLS-floor inconsistency across rest-hook activator / rest-hook delivery / email) touches H1, H5 and exists at the wiring layer. Each cross-cut needs a "lead harness" so the test ends up in someone's queue.

9. **Audit doc references are slightly stale.** `cmd/fhir-subs/wiring.go` already has `handlers.RegisterAdminRoutes` and `observability.Start` calls. Findings #7 and #10 cite an older commit (`fb02d0c`). The findings may be partially closed by in-flight work (issues #15-19 in `bd`). Each story should re-verify against current `main` before assuming the gap is open.

10. **One specific harness item that genuinely requires a fake — flagged for user decision.** The strip-STARTTLS attack scenario (Finding #184) needs an SMTP relay that *advertises* STARTTLS in EHLO and then *fails* the upgrade. mitmproxy can do this with a custom script, and it counts as "real software running in docker-compose" — but the script that controls when STARTTLS gets stripped is necessarily a test artifact (no real-world relay does this on demand). The user's call: (a) keep mitmproxy as the strip-STARTTLS fixture (it is real software with a config file driving its behavior), or (b) drop the test. Recommendation: (a) — mitmproxy is a real, widely-deployed tool; the test is real-software-driven even if the misbehavior is configured.

---

## Section 6 — Code changes required to support real-only testing

To remove the last fakes, the production binary needs configuration knobs and injection points so real-software fixtures can drive every test surface. None of these are test-only code paths; they are operator-tunable knobs that also unblock the e2e suite.

Each item is filed as a separate story under epic #91. Format: `Item | File | Knob | Closes finding(s)`.

| # | File | Knob / change | Closes |
|---|---|---|---|
| C1 | `cmd/fhir-subs/config.go` | Add `FhirScan.SourceURL`, `FhirScan.Auth.{ClientID,Secret,TokenURL}`, `FhirScan.Cadence` so the FHIR scan runner can be pointed at HAPI FHIR running in the test stack. | #1, #2 |
| C2 | `cmd/fhir-subs/config.go` | Add `Vendor.ChangeFeedURL`, `Vendor.Auth.*`, `Vendor.PollInterval` for the vendor change-feed runner. | #1, #3 |
| C3 | `cmd/fhir-subs/config.go` | Add `Webhook.Adapters[]` + `Webhook.HMACSecret` and mount `internal/webhook/webhook.go::NewHandler` at `/webhooks/{adapter}` in `wiring.go`. | #5 |
| C4 | `cmd/fhir-subs/config.go` | Add `Hydration.Enabled` + plumb `internal/hydration/hydration.go::New` into the scheduler builder. | #6 |
| C5 | `cmd/fhir-subs/wiring.go` | Call `handlers.RegisterAdminRoutes(r, deps)` (already partially landed; verify against current main per Section 5 #9). | #7 |
| C6 | `cmd/fhir-subs/wiring.go` + `internal/api/handlers/router.go` | Move `/metadata` out of the auth group OR call `RegisterPublicRoutes(r, deps)` for it. | #8, #111, #162 |
| C7 | `cmd/fhir-subs/wiring.go` | Replace `handlers.NewPgAuditStore(pool)` with `handlers.NewChainedAuditStore(obsMod.AuditWriter())` (already partially landed; verify). | #9, #87 |
| C8 | `internal/infra/storage/migrate/migrations/0007_audit_chain_columns.sql` (new) | Reconcile audit_log columns: rename to `chain_hash`/`prior_hash`/`chain_input`/`payload` to match the writer, OR keep migration shape and update writer SQL. Decision per Section 5 #3. | #11, #12, #80, #81, #154, #155 |
| C9 | `cmd/fhir-subs/wiring.go` | Call `storage.Start(ctx, ...)` to launch partition + retention goroutines. | #13, #36, #138, #139 |
| C10 | `cmd/fhir-subs/wiring.go` + `internal/channel/{websocket,email,message}/` | Register `websocket`, `email`, `message` channels in the scheduler's `chReg`. Construct via `Options` derived from new config blocks. | #14-17, #94, #95 |
| C11 | `cmd/fhir-subs/config.go` + `cmd/fhir-subs/wiring.go` | Surface `EmailChannelConfig` (Mode, STARTTLSPolicy, AuthMechanism, AuthUsername, AuthPassword, AllowCleartextAuth, RequestTimeout, LocalName, UserAgent, Signer, MinTLSVersion). | #15, #135, #184, #185 |
| C12 | `cmd/fhir-subs/config.go` + `cmd/fhir-subs/wiring.go` | Surface `WebSocketChannelConfig` (OriginPatterns, MaxFrameBytes, MaxSessions, MaxSessionsPerClient, MaxReplayEvents, IdleTimeout, PingInterval, BindTimeout, PingWriteTimeout, UpgradeReadHeaderTimeout, BindingTokenTTL, SubprotocolPrefix). | #14, #29, #35, #136, #146 |
| C13 | `cmd/fhir-subs/wiring.go` | Populate `Deps.SubscriptionCreateRateLimit` and `Deps.WSBindingTokenRateLimit` from `cfg.Auth.{SubscriptionCreateRateLimit,WSBindingTokenRateLimit}`. | #18, #45, #141 |
| C14 | `cmd/fhir-subs/run.go` | Replace `srv.Serve(listener)` with `srv.ServeTLS(listener, certFile, keyFile)` when `!cfg.Server.HTTP.Insecure`. | #19, #57 |
| C15 | `cmd/fhir-subs/wiring.go` + `cmd/fhir-subs/config.go` | Surface `MLLP.TLS.{CertFile,KeyFile,CAFile,RequireAndVerifyClientCert,MinVersion}`; plumb into `mllp.EndpointConfig.TLS`. | #20 |
| C16 | `cmd/fhir-subs/wiring.go` | `r.Handle("/metrics", obsMod.PrometheusHandler())` (already partially landed; verify). | #21, #22, #113 |
| C17 | `cmd/fhir-subs/config.go` + `internal/infra/observability/tracing/` | Wire `cfg.Tracing.OTLPEndpoint` etc. into a real OTel exporter started by `observability.Start`. | #23, #109 |
| C18 | `cmd/fhir-subs/wiring.go` + adapter packages | Register all vendor adapters (`cerner`, `epic`, `athena`, `nextgen`, `meditech`, `allscripts`, `direct`) in `adReg`. | #26 |
| C19 | adapter packages | Implement non-stub `MapToFHIR` for each vendor (or document them as canary-only and disable the SPI capability bit). | #24, #25, #88, #94 |
| C20 | `cmd/fhir-subs/wiring.go` | Replace `defaultActivator{}` for `websocket`/`email` with the real per-channel activator implementations (each channel package must own its activator). | #27, #50 |
| C21 | `cmd/fhir-subs/config.go` | Surface `Server.HTTP.PublicURL` for `BaseURL` rendering; plumb into `Deps.BaseURL`/`Deps.WSBaseURL` instead of hardcoded `https://`+bind. | #28 |
| C22 | `cmd/fhir-subs/run.go` | Mount `/.well-known/jwks.json` route. Section depends on whether token endpoint signs with HMAC (no JWKS needed) or RS256 (JWKS required); adjust per architecture. | #31 |
| C23 | `cmd/fhir-subs/config.go` | Default `Server.HTTP.Bind` to `127.0.0.1:8443` instead of `0.0.0.0:8443`. | #32 |
| C24 | `cmd/fhir-subs/config.go` | Surface `MLLP.{ReadIdleTimeout,NackThenDropAfter,InflightCapPerConn,OnPersistFail}`; plumb `FrameAssemblyTimeout` into `mllp.ListenerConfig`. | #33 |
| C25 | `cmd/fhir-subs/config.go` + `cmd/fhir-subs/pool.go` | Surface `Database.{MaxConns,MinConns,MaxConnLifetime,MaxConnIdleTime,HealthCheckPeriod}`. | #34 |
| C26 | `cmd/fhir-subs/config.go` + `cmd/fhir-subs/wiring.go` | Surface `Pipeline.HL7Processor.IdlePollInterval` and similar tunables in YAML and `applySets`. Already partially landed; verify. | #44 |
| C27 | `cmd/fhir-subs/config.go` + `cmd/fhir-subs/wiring.go` | Surface `Pipeline.Scheduler.{Retry.Initial,Retry.Max,Retry.Min,Retry.MaxAttempts,RecoveryInterval,StuckThreshold,DispatchConcurrency,ActivationTimeout}`. | #29, #42, #43, #196 |
| C28 | `cmd/fhir-subs/wiring.go` | Decouple `Auth.AllowInsecure` from `URLValidator.AllowHTTP` — separate config knobs `Auth.AllowInsecureJWKS` and `Subscriber.AllowHTTPEndpoints`. | #47, #130 |
| C29 | `internal/api/auth/verifier.go` | Filter JWKS lookups by `cfg.Auth.TrustedIssuers` instead of treating advisory. | #48, #127 |
| C30 | `cmd/fhir-subs/wiring.go` | `registerLifecycle` shutdown hook that closes the rest-hook activator's `http.Client.Transport.(*http.Transport).CloseIdleConnections()`. | #46 |
| C31 | `internal/infra/storage/repos/dead_letters.go` + migration | Add `key_version` column; persist `kv` from `codec.Encrypt`. | #75, #140 |
| C32 | `cmd/fhir-subs/audit_cli.go` | Once C8 lands, the CLI works against the production schema. | #12, #174 |
| C33 | `internal/infra/observability/audit/audit.go` | Fix chain canonicalization to hash `prior_hash` BYTES (not lowercase hex string); fix `VerifyChainReport` to NOT silently re-anchor after a break. | #122, #123 |
| C34 | `internal/api/auth/{verifier,token_endpoint}.go` | Replace `Seen(jti)` then `Put(jti, exp)` with atomic `CheckAndPut(jti, exp) bool`. | #114, #115 |
| C35 | `internal/api/auth/verifier.go` | Wrap JWKS fetches in `singleflight.Group.Do` to coalesce concurrent first-time requests. | #142 |
| C36 | `cmd/fhir-subs/run.go` | Pass `cfg.Deployment.LogFormat` to `logging.NewLogger` instead of hardcoded `"json"`. | #102, #177 |
| C37 | `cmd/fhir-subs/activators.go` | rest-hook handshake `Timestamp` uses `time.Now().UTC().Format(time.RFC3339)` not zero time. Same code path also takes the channel's `MinTLSVersion` to align with the delivery channel's TLS floor. | #106, #119 |
| C38 | `cmd/fhir-subs/config.go` | Implement `${env:VAR}` and `${file:/path}` placeholder interpolation in `loadConfig`. | #52, #108 |
| C39 | `cmd/fhir-subs/config.go` | Enable `yaml.Decoder.KnownFields(true)`; remove the `Extra map[string]any \`yaml:",inline"\`` field. | #53, #197 |
| C40 | `cmd/fhir-subs/config.go::Validate` | Require non-empty `Database.URL`, `Codec.Keys`, `Codec.ActiveKeyVersion`, `Auth.Audience` (when `!Insecure`), `Topics.CatalogDir`. | #41, #111, #112, #124, #125 |
| C41 | `cmd/fhir-subs/wiring.go` | Populate `Deps.{Metrics,Logger,SearchPageSize,SearchMaxPageSize,EventReplayPageSize,AuditMaxBytes,MaxBodyBytes,MaxStatusBulkIDs,MaxSchemaErrorBytes,FHIRVersion,JWKSURL,TokenEndpointURL,SupportedFHIRVersions}` from config. | #113, #114, #115 |
| C42 | `internal/api/handlers/url_validator.go` | Block IPv4 broadcast `255.255.255.255`, CG-NAT `100.64.0.0/10`, IPv6 site-local `fec0::/10`. Decode IDN/punycode before loopback check. Pass resolved IPs to the channel dialer (not just hostname) to close the rebinding race. Add config knob `URLValidator.ResolverAddr` so tests can point at CoreDNS. | #98, #112, #128, #129, #133 |
| C43 | `cmd/fhir-subs/config.go` + `internal/api/handlers/url_validator.go` | Surface `URLValidator.AllowHosts`. | #134 |
| C44 | `internal/channel/resthook/resthook.go` | Clamp `parseRetryAfter` to a max ceiling (e.g., 1h) and a floor (e.g., 0s). | #164, #194 |
| C45 | `internal/api/handlers/admin.go` | Enforce `len(d.AdminToken) >= MinAdminTokenBytes`. Emit audit_log rows on every admin read. Wire admin rate limiter via `Deps.AdminRateLimit`. | #117, #165, #166, #167 |
| C46 | `internal/api/handlers/subscription_handlers.go::deleteSubscription` | Return 410 Gone after delete; redact contact / endpoint on subsequent reads. | #137, #171 |
| C47 | `internal/api/handlers/pg_stores.go::ListByTopicAndRange` | Add `client_id` predicate. (Critical, HIPAA-scope.) | #163, #189 |
| C48 | `internal/api/handlers/pg_stores.go::ListByClient` | Add `LIMIT $N OFFSET $M` and surface page size knob. | #100 |
| C49 | `internal/api/handlers/subscription_handlers.go::buildCapabilityStatement` | Use absolute extension URLs; add `searchParam[]` entries. | #172, #173 |
| C50 | `internal/api/handlers/subscription_handlers.go::parseCountParam` | Use `strconv.Atoi` instead of `Sscanf`. | #168 |
| C51 | `internal/hl7processor/processor.go::messageDateTime` | Case-insensitive segment match; honor MSH-2 escape characters; parse millisecond + offset MSH-7 forms; emit metric on fallback to wall clock. | #131, #132, #169, #170 |
| C52 | `internal/infra/observability/logging/logging.go` | Strip path segments (not just query string) on PHI URL fields; add value-pattern redaction for known PHI patterns (MRN regex). | #175, #176, #199 |
| C53 | `internal/infra/storage/codec/codec.go` | Bind row primary key into AEAD AAD. | #113 |
| C54 | `internal/infra/lifecycle/sequencer.go` | Plumb caller `ctx` through `runShutdown` so `WaitForExit(ctx)` cancellation aborts in-progress shutdown. Register the runtime's components against `lifecycle.PhaseStopAccepting`/`PhaseDrainInFlight`/`PhaseCloseConnections` (in `cmd/fhir-subs/wiring.go::registerLifecycle`). | #160, #161, #180 |
| C55 | `internal/engine/scheduler/worker.go::recoverStuck` | Use `pg_advisory_xact_lock` or `FOR UPDATE SKIP LOCKED` to make the recovery sweep multi-pod safe. | #178, #179 |
| C56 | `internal/infra/storage/migrate/migrations/0001_init.sql` | Use `CREATE TABLE IF NOT EXISTS schema_migrations`. Idempotent migration bodies. | #158, #159, #187 |
| C57 | `cmd/fhir-subs/version.go` + `Dockerfile` + `.github/workflows/release.yml` | Fix ldflag (`main.Version` capitalized); add ldflag in Dockerfile; make `Version` and `Commit` consts (or read from `runtime/debug.BuildInfo`). | #110, #150 |
| C58 | `cmd/fhir-subs/main.go` | Add `dead-letters {list,replay,forget}` subcommands. | #40 |
| C59 | `cmd/fhir-subs/config.go::applySets` | Add `auth.access_token_ttl`, `auth.jwks_cache_ttl`, `auth.clock_skew`, `auth.jwks_allowed_hosts`, `auth.trusted_issuers`. | #110, #126 |
| C60 | Per-channel TLS floor | Standardize all channels (rest-hook activator, rest-hook delivery, email STARTTLS, message) on a single `MinTLSVersion` config knob. | #119, #185 |
| C61 | `internal/channel/channel.go` | Add `Close() error` to the `Channel` SPI interface so the scheduler can shut down channels gracefully. | #116 |
| C62 | `cmd/fhir-subs/wiring.go` | Mount `/.well-known/smart-configuration` and CapabilityStatement extensions for SMART discovery. | #30 |
| C63 | `internal/channel/websocket/websocket.go` | Pass `c.ctx` (not `r.Context()`) to bind handshake; honor channel-level Close. | #120 |
| C64 | `internal/channel/email/email.go` | Plumb `Now func() time.Time` into `buildMIME` Date header rendering. | #127 |
| C65 | Helm chart | Render topic catalog ConfigMap and mount it. Align containerPort + probe port (single listener model). Remove fictional metrics port 9090 OR add a second listener for it. | #51, #55, #56 |

**Total Section 6 items: 65.**

---

## Items genuinely impossible without a fake — flagged for user decision

After re-mapping every finding to real-software harnesses, the following items genuinely cannot be tested with real software alone. The user should decide whether to drop the test or accept the fake-shaped fixture.

1. **STARTTLS strip-attack scenario (#184).** Needs an SMTP relay that advertises STARTTLS in EHLO and then fails the upgrade. mitmproxy with a custom Python script does this. The script is a test artifact, but mitmproxy is real software. Recommendation: accept this as "real software with a test-only configuration" rather than a fake.

2. **Crash-during-migration recovery (#187).** Needs the migration runner to crash mid-DDL, leaving the DB in a half-state. We can drive this with `kill -9` against the binary mid-migrate (real signal against real process), but the *trigger timing* requires the binary to expose a config knob `Migrate.AbortAfterStatement=N` that exits non-zero after applying N statements. This is a test-specific knob with no operator value; flagged for user decision.

3. **JTI replay TOCTOU race (#114, #115).** Reproducible with two real concurrent HTTP requests using vegeta — no fake needed. *Cleared.*

4. **Encryption AAD-binding (#113).** Test needs to write a row, swap the `payload` column with another row's, and assert decrypt fails. Real Postgres + real `pgx` UPDATE; no fake. *Cleared.*

5. **Audit chain re-anchoring (#122).** Test needs to corrupt a `chain_hash` byte and assert the verifier reports every downstream row as broken. Real `pgx` UPDATE on real Postgres + real `audit-chain-walker` invocation; no fake. *Cleared.*

6. **Schema-snapshot test.** Real `pg_dump` against the real DB schema, compared against a checked-in snapshot. Real software. *Cleared.*

7. **Goroutine leak detection (#180).** Requires `go test -race` + `runtime.NumGoroutine()` deltas. Real Go runtime, real race detector. *Cleared.*

The **only** item where the user must explicitly approve a non-real fixture is item #2 above (the migration crash-after-N-statements knob). If the user rejects it, the only path to coverage of #187 is operator-side runbook-only ("if migration fails mid-flight, do X").

---
