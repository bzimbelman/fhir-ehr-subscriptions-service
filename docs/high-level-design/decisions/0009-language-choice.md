# 0009 — Language Choice: Go

**Status:** Accepted

## Context

The architecture deferred the language choice to a follow-up ADR ([architecture.md "Open Questions"](../../architecture.md#open-questions)). The candidates were Rust, Go, and the JVM (Kotlin or Java). Every component LLD was written language-neutral so this decision could be made on its own merits.

The design's load-bearing requirements that bear on language choice:

- **Single container, fast startup, low memory baseline, stable under sustained load** ([high-level-concept.md](../../high-level-concept.md)). This rules out the JVM in JIT mode (cold starts of seconds, ~250–500 MB baseline). GraalVM native-image would solve cold-start and footprint but breaks several mature JVM libraries we'd otherwise rely on (notably parts of the HAPI FHIR validation stack).
- **Async I/O all the way through** with multi-task concurrency in one process ([0002-single-instance-no-leader-election](0002-single-instance-no-leader-election.md), every LLD's pseudo-code is `async`).
- **Postgres-native with `SELECT FOR UPDATE SKIP LOCKED`** as the queue-claim primitive ([0001-postgres-only](0001-postgres-only.md), [low-level-design/storage.md](../../low-level-design/storage.md)).
- **Healthcare integration libraries**: HL7 v2 parser, FHIR R5/R4B handling, FHIRPath sandbox with timeout, JWT/JWKS validation, OpenTelemetry, Prometheus, S/MIME for the email channel.
- **Pluggable adapter and channel SPIs** as base classes with REQUIRED and OPTIONAL overrides ([contracts/adapter-spi.md](../contracts/adapter-spi.md), [contracts/channel-spi.md](../contracts/channel-spi.md)).
- **Crash-resume from durable rows** is correctness-critical ([contracts/internal-tables.md](../contracts/internal-tables.md)).
- **PHI handling**: encryption at rest, redaction, audit-log hash chain.
- **Free and open source** with a low contributor barrier so healthcare integrators outside the original team can write vendor adapters ([architecture.md "Pluggable EHR adapters"](../../architecture.md#operational-simplicity)).

## Decision

**Go.** All net-new code is written in Go. The deployable artifact is a single statically-linked Go binary inside a small OCI container. The minimum supported Go version is **1.22** (selected for `slog`, generics maturity, and `for range over int`).

### Library shortlist (project standard, locked here)

These are the libraries the implementation uses unless a future ADR overrides one. Adapter and channel authors may pull in additional libraries inside their own modules, but the core depends only on the list below.

| Concern | Library |
|---|---|
| Postgres driver and pool | `github.com/jackc/pgx/v5` (and `pgx/v5/pgxpool`) |
| SQL composition | hand-written SQL with `pgx`; no ORM. Repository pattern per [storage LLD](../../low-level-design/storage.md). |
| HTTP server (Subscriptions API + probes) | `net/http` with `github.com/go-chi/chi/v5` for routing |
| HTTP client (adapter outbound, hydration, channels/rest-hook) | `net/http` with hardened `http.Client` |
| WebSocket server | `nhooyr.io/websocket` (or its post-rename successor `github.com/coder/websocket`) |
| MLLP framing | hand-rolled in `mllp-listener` module; no third-party MLLP library is mature enough to depend on |
| HL7 v2 parsing | hand-rolled lexer producing a typed segment tree. Existing Go HL7 v2 libraries do not meet our vendor-Z-segment-extension shape. The base `Hl7MessageProcessor` ships the lexer; adapters extend it. |
| FHIR resource model | hand-rolled minimal Go types covering the resources we use (`Subscription`, `SubscriptionTopic`, `SubscriptionStatus`, `Bundle`, `MessageHeader`, plus the resource types our built-in topics fire on). Generated from the spec's StructureDefinitions. **Not** using `samply/golang-fhir-models` directly — too broad — but its codegen approach is the reference. |
| FHIR JSON / XML serialization | `encoding/json` with explicit FHIR canonical-JSON shaping where needed (see [decisions/0008](0008-resolved-design-questions.md) for canonicalization). XML is via `encoding/xml`. |
| FHIRPath evaluator | hand-rolled, sandboxed, supports the subset documented in [topic-matcher LLD](../../low-level-design/topic-matcher.md). No mature Go FHIRPath library implements wall-clock timeout + traversal limit + deny-list for non-deterministic functions. |
| FHIR profile validation | `github.com/google/fhir` Go bindings if they cover R5; otherwise hand-rolled minimum-viable validator. (Validation strictness scope per [decisions/0008](0008-resolved-design-questions.md).) |
| JWT / JWKS | `github.com/golang-jwt/jwt/v5` (verify only) plus `github.com/MicahParks/keyfunc/v3` for JWKS fetch + cache |
| YAML / TOML config | `gopkg.in/yaml.v3` and `github.com/BurntSushi/toml`; loader is hand-rolled per [configuration LLD](../../low-level-design/configuration.md) |
| JSON Schema for adapter / channel manifest validation | `github.com/santhosh-tekuri/jsonschema/v5` |
| Structured logging | `log/slog` (stdlib, Go 1.21+). Redaction wrapper is hand-rolled per [observability LLD](../../low-level-design/observability.md). |
| Metrics | `github.com/prometheus/client_golang/prometheus` and `prometheus/promhttp` |
| Tracing | `go.opentelemetry.io/otel` with `otlptracehttp` exporter |
| SMTP client | `net/smtp` (stdlib) plus a STARTTLS wrapper if `net/smtp` is insufficient |
| S/MIME | `github.com/github/smimesign` or equivalent. **If S/MIME library quality is not adequate at implementation time, ship plain SMTP for v1 and defer Direct/S/MIME to v2.** Decision deferred until prototype. |
| AES-GCM (column-level encryption at rest) | `crypto/aes` + `crypto/cipher` (stdlib) |
| UUID / ULID generation | `github.com/google/uuid` for UUIDv4. Format choice for `correlation_id` is captured in a separate ADR (Group A item from the readiness review). |
| HTTP testing / mocking | `net/http/httptest` (stdlib) |
| Property-based testing | `github.com/leanovate/gopter` or `pgregory.net/rapid` |
| HTTP middleware (TLS, request-id, panic-recover, auth) | `chi` middlewares plus hand-rolled |
| Signal handling, context propagation | `os/signal`, `context` (stdlib) |

### Tooling

- Build: standard `go build`. No bazel, no make wrapper.
- Module layout: a single Go module at the repo root (`go.mod`). Internal packages organized by component (one Go package per LLD module).
- Linting: `golangci-lint` with a curated config (errcheck, govet, staticcheck, ineffassign, unused, gosec, gocritic).
- Formatting: `gofmt` and `goimports`. Enforced in CI.
- Test runner: `go test ./...` with race detector on in CI (`-race`).
- Coverage: `go test -coverprofile`. Target is meaningful coverage on the engine, topic matcher, channel SPI, and adapter SPI base classes; less on glue.
- Container base: `gcr.io/distroless/static-debian12` for the runtime stage; `golang:1.22-alpine` for build. Multi-stage Dockerfile producing a single-binary image targeting <30 MB compressed.
- CI: GitHub Actions baseline (build, test with race, lint, container build, SBOM via `syft`, image signing via `cosign`).

## Why Go

**Container fit.** Statically-linked Go binaries land at 20–60 MB depending on dependencies. Cold-start is well under 100 ms. Memory baseline at idle is 30–80 MB. This matches the concept doc's "small, fast startup, low memory" without a special build pipeline.

**Async model.** The pseudo-code in every LLD maps almost 1:1 onto Go's goroutine + channel + `context.Context` model. The design's pattern of "claim a row with `SELECT FOR UPDATE SKIP LOCKED`, process, commit, signal next stage" is canonical Go.

**Postgres-native.** `pgx/v5` is the gold-standard Postgres driver in any language. `SELECT FOR UPDATE SKIP LOCKED`, advisory locks (we don't use them but if we ever did), `LISTEN`/`NOTIFY`, prepared statements, batch operations, and transactional outbox are all idiomatic.

**Library coverage.** Go has acceptable libraries for every concern except FHIR and HL7 v2 — and for those we are committing to hand-rolled minimal implementations that cover the subset our design uses. This is a deliberate trade-off: the FHIR ecosystem in any language other than the JVM forces some hand-rolling, and Go's maintainability tax for hand-rolled libraries is lower than Rust's. The architecture review acknowledged the FHIRPath evaluator and HL7 v2 parser as project-built; this ADR formalizes that.

**Contributor onboarding.** A healthcare integrator who knows HAPI but does not know `Tokio` / `Pin` / `'static` lifetimes can still read Go and contribute a vendor adapter. Lower barrier to contribution is critical for the FOSS commitment in [high-level-concept.md](../../high-level-concept.md).

**Operational maturity.** Go's runtime in production at the scale we target (single-facility throughput, 10s–low 1000s of events/min) is well understood. The garbage collector's tail latency is not a concern at our SLAs (event-to-delivery measured in 10s of ms, not microseconds).

## Why not Rust

- HL7 v2 and FHIR ecosystems are too thin in 2026 for a healthcare-integration server. We would write the missing libraries ourselves, doubling the project. Rust's `fhir-rs`, `rusty_hl7`, and FHIRPath crates are not production-ready for the resource set we use.
- Contributor barrier is high. Async Rust is a meaningful filter — most healthcare integrators don't have it as a working skill. The FOSS commitment expects external adapters.
- The memory-safety guarantees Rust offers are not paid for here: we are single-tenant, the deployment is one process, and Go's race detector + careful design covers our actual exposure.

## Why not JVM

- Container size and cold-start are the wrong shape for the architecture. JIT JVM hits multi-second cold starts and 250+ MB baseline. SnapStart and CDS help but don't fix the magnitude.
- GraalVM native-image fixes both, at the cost of breaking compatibility with parts of HAPI FHIR's validation stack and adding significant build-pipeline complexity.
- HAPI FHIR is the single most compelling JVM advantage. Bringing in HAPI as a runtime library means accepting JIT JVM (and its size/startup cost) or fighting Graal compatibility. Neither matches the design's commitments.
- Operationally JVM is fine, but the lift over Go is small and the cost over Go is large.

## Consequences

**Positive.**
- Container size and startup time match the concept doc's commitments without a special build pipeline.
- Library choices (per the table above) become concrete and the implementation can begin once the remaining "Group A" decisions in the readiness review are pinned (correlation_id format, JSON canonicalization, locale, etc.).
- Repo layout and CI baseline are mechanical from here.
- The contributor barrier is among the lowest of the candidate languages.

**Negative.**
- We accept the cost of hand-rolling subsets of: HL7 v2 parsing (lexer + segment tree), FHIR resource types (codegen from StructureDefinitions for the resources we use), and FHIRPath evaluation (sandboxed, with timeout). Each is bounded — the spec subset our design needs is small — but each is real engineering work.
- We do not get HAPI FHIR's full validation surface for free. The `adapters/default` adapter and the topic matcher both need a profile-validation strategy that does not depend on HAPI. Captured in [decisions/0008](0008-resolved-design-questions.md) as a deferred profile-validator scope decision.
- S/MIME for the email channel is the weakest library area. If the prototype shows the available Go S/MIME libraries are inadequate, the email channel ships plain SMTP for v1 and Direct / S/MIME defers to v2. Already aligned with the email channel's existing scoping in [low-level-design/channels.md](../../low-level-design/channels.md).
- We pay a small per-deployment GC tail cost. Acceptable at our latency targets.

**Neutral.**
- Go 1.22 minimum version is a pin we can revisit; the project commits to the latest two minor versions.
- The library shortlist is a starting point. Individual swaps (e.g., `chi` → `echo`, `gopter` → `rapid`) are not architectural changes and do not require a new ADR.

## Touched documents

After this ADR is accepted, three forward-looking references in existing docs need to be updated:

- `docs/architecture.md` — the "Open Questions" entry that says "deferred to a follow-up `adr/0001-language-choice.md`" should point at this ADR (`docs/high-level-design/decisions/0009-language-choice.md`).
- `docs/high-level-concept.md` — the "final language choice will be made in a follow-up architecture document" sentence should be replaced with a link to this ADR.
- `docs/low-level-design/README.md` — the language-neutral phrasing "regardless of the eventual language choice (Rust + Tokio, Go, JVM with virtual threads)" should be replaced with the concrete Go statement.

A separate cleanup pass — not part of this ADR — applies the locked library choices into LLD pseudo-code where the LLD currently uses generic placeholder names.
