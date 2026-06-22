# Open Story Audit

A running log of "what changed in the open story list and why," kept alongside
the OpenProject board so the carved-up tickets and the codebase don't drift
out of sync.

The board is the source of truth. This doc explains the rationale behind
status changes that span multiple tickets so a reviewer can see the whole
shape at a glance without walking 20 OP comments.

Sections are append-only. Each section is dated and references the merge
commit / PR / OP story that drove the change.

---

## 2026-06-22 — Realstack simplification (#344) audit close-out

Driving change: PR #93, merge commit `8c27619`, OP story #344 ("Realstack
simplification A — DELETE bloat services") landed on origin/main.

PR #93 deleted the following from `e2e/realstack/docker-compose.yml`:

- `prometheus` container + `e2e/realstack/fixtures/prometheus/`
- `otel-collector` container + `otel-spans` sidecar + `e2e/realstack/fixtures/otel/`
- `coredns` container + `e2e/realstack/fixtures/coredns/`
- `mitmproxy` container + `e2e/realstack/fixtures/mitmproxy/`
- `nginx` container + `e2e/realstack/fixtures/nginx/`
- `cmd/test-token-mint/` binary + `Dockerfile.tokenmint`
- realstack tests scoped around the deleted services
  (`prod_binary_observability_red_test.go`, `tokenmint_red_test.go`,
  `prod_binary_smart_auth_required_red_test.go`)

The replacement assertions for the same behaviors landed in proper
unit/integration tests:

- `/metrics` surfacing named counters → `cmd/fhir-subs/wiring_observability_test.go`
  (boots the real binary, GETs `/metrics`, asserts on the prom-format response).
- Tracing pipeline span emission → `internal/infra/observability/tracing/tracing_test.go`
  (in-process OTLP receiver via the otel SDK test exporter).
- SSRF / DNS-rebinding / IDN / CG-NAT / IPv6 site-local / IPv4 broadcast →
  `internal/api/handlers/url_validator_test.go` against the existing
  `handlers.Resolver` seam.
- STARTTLS-strip → `internal/channel/email/email_test.go` against the
  in-process `testrelay` that can opt out of advertising STARTTLS.

The simplified realstack now boots 7 services (postgres, keycloak, hapi-fhir,
mailpit, test-resthook-subscriber, test-ws-subscriber, test-mllp-control-plane).

OP story #348 ("Close obsolete H5/H6/H7 stories from auditOpen") drove this
audit-doc section.

### Stories closed-as-rejected — superseded by #344

These were carved 100% around components #344 deleted. Each ticket carries an
OP comment naming PR #93 / commit 8c27619 / OP #344 as the supersession.

| OP | Title | Reason |
|----|-------|--------|
| #260 | H5 CertFixture — mint real X.509 chains via cfssl for TLS / mTLS | Drove TLS scenarios via the deleted `nginx` reverse proxy + cfssl-minted certs feeding it. The binary's own TLS listeners (`ServeTLS`, MLLP TLS) remain in scope under #267 / W3 + per-channel hardening tickets, not a separate cfssl harness. |
| #261 | H6 AdversarialDNS — drive DNS-rebinding / SSRF cases via real CoreDNS | Drove DNS adversarial cases via the deleted `coredns` container + per-scenario Corefile rewrites. Replaced by `internal/api/handlers/url_validator_test.go` against the existing `handlers.Resolver` seam. C42 / C43 wiring stays under #267 / W3. |
| #262 | H7 ObservabilityRig — assert real Prometheus / OTel Collector telemetry | Drove observability assertions via the deleted `prometheus` + `otel-collector` containers. Replaced by `cmd/fhir-subs/wiring_observability_test.go` and `internal/infra/observability/tracing/tracing_test.go`. C16 / C17 / C36 / C52 wiring stays under #267 / W3. |

### Stories edited with new scope — partially still relevant

These were not 100% scoped around deleted components. Each carries an OP
comment describing what changed in scope after #344. Acceptance criteria are
unchanged unless noted.

| OP | Title | Scope change |
|----|-------|--------------|
| #265 | W1 — Convert e2e suite from harness fakes to real-software harnesses | Drop `mitmproxy` / `nginx` / `coredns` / `prometheus` / `otel-collector` from the depended-on stack. Item 7 (`channels_email_starttls_required_test.go::stubRelay`) targets Mailpit-without-STARTTLS or the in-process `testrelay`, not deleted mitmproxy. All other deletions (`e2e/harness/*`, `e2e/mocksub/smtp.go`, `AuthAudience: ""` shortcuts, raw `INSERT INTO subscriptions`) stand. |
| #266 | W2 — Replace 11 t.Skip merge-gate stubs with real prod-binary e2e | Drop H7 (ObservabilityRig) from the dependency list — it is closed as obsolete. Scenarios that previously asserted via real Prometheus / OTel now assert via the binary's `/metrics` endpoint and the in-process OTLP receiver. The 11 scenarios themselves are unchanged. |
| #267 | W3 — Section 6 binary code-changes (65 items) | No C-items removed or descoped. C16 / C17 / C36 / C52 / C42 / C43 are now verified at the unit/integration layer instead of via deleted containers. C14 / C15 / C37 / C60 (TLS path) keep verifying the binary's own TLS listeners directly, no nginx layer. |

### Stories left as-is — independent of #344

Confirmed against the deletion list and left untouched.

| OP | Title | Note |
|----|-------|------|
| #244 / #256 | H1 RealStackHarness | Already Closed. The realstack still IS the H1 harness — #344 simplified its surface, not its purpose. |
| #287 | H10 (closed dup) | Already Closed. |
| #289 | H10 — Migrate e2e/orchestrator off legacy harness | Open; scope-confirm comment posted. Targets `e2e/harness/{api,pipeline,scripted_adapter,tls,topic_seed}.go` deletion + per-test migrations. None of those files were touched by #344. |
| #293 / #294 / #295 | H10a / H10b / H10c | Already Closed. The control-plane / programmable rest-hook / JWT-mint primitives they delivered are part of the simplified stack. |
| #296 | H10d — Activation-race bypass | Open. Independent of #344 (about the legacy `hpipe.MarkSubscriptionActive` shortcut, not about deleted containers). |
| #297 | H10e — Scripted adapter for per-message FHIR shapes | Open. Independent of #344 (about replacing legacy `hpipe.ScriptedAdapter`, not about deleted containers). |

### Strategy doc note

`docs/e2e-coverage-strategy.md` Section 3 still enumerates H1-H9 harness
components; the H5 / H6 / H7 sub-sections are now historical (they describe
the harness shape pre-#344). They are kept in place for audit-trail
continuity rather than rewritten — readers should consult the closed
OP tickets and this audit section for the current state.

### Independent verification

Spot-check expected by #348's pre-merge gate:

- #260 / #261 / #262 status = Rejected (status_id 14) on op.bzonfhir.com.
- Each closed ticket carries a comment naming PR #93 / commit `8c27619` /
  OP #344 as the supersession.
- #265 / #266 / #267 carry a "Scope update" comment describing the
  post-#344 dependency changes.
- #289 carries a "Scope check" comment confirming no scope change.
- This file (`docs/open-story-audit.md`) is consistent with the OP board
  state above.
