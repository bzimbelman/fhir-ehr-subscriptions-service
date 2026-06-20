# Admin UI PRD — Critical Review

**Reviewing:** `docs/admin-tool.md` v1 (2026-06-18)
**Source ticket:** OpenProject #38
**Lens:** the user explicitly asked for "a simple app that we later add features to" over "an overly complex app that has features no one needs or uses." This review applies that lens hard.

The PRD is well-written and disciplined in many places (read-only first, alerting punted to Alertmanager, write actions deferred to v2). But it has scoped six screens where the ticket describes four use cases, it invents back-end capability the codebase doesn't have, and it leans on at least one CLI subcommand that doesn't exist. The biggest single risk: the Trace screen is the most expensive thing to build *and* requires schema additions the PRD doesn't acknowledge.

Cuts and risks lead. Gaps last.

---

## Part 2 — Over-build (cut, defer, or replace)

### O1. Five-tile dashboard with green/yellow/red traffic-light logic (§4.1)

The PRD spends a full table defining green/yellow/red thresholds for five tiles (HL7 Ingress, EHR Adapters, Matcher, Delivery Channels, DLQ). Every one of those thresholds is a 2–5 line PromQL rule that the operator's existing Grafana already renders better than we will, with history, with annotations, with their own alert routing. We're building Grafana, badly, on top of the same metric series.

**Recommendation: cut to a single status panel** — lifecycle state (`startup`/`running`/`draining`/`shutdown`), version/commit, last config reload, and a "see Grafana for trends" link. Ship a Grafana dashboard JSON in `deploy/` instead of recreating it in a custom UI. The §6 Alertmanager rules are already the right answer; the dashboard is redundant with them.

### O2. Logs Viewer screen (§4.6, §3.5)

This is a re-implementation of Loki / CloudWatch / Splunk inside our binary. The §5.2 plan calls for a 50–200 MB in-memory ring buffer of structured log records, a tail endpoint, a search endpoint with five filter widgets, virtualized rendering for "100k+ rows," NDJSON export with a 100 MB cap, and tail mode (polling / SSE / WS — open question §10.6). All of that to do something the operator already has via `kubectl logs -f` and their existing log aggregator.

The persona section (§2) literally says "comfortable with web consoles, log viewers" — they already own one.

**Recommendation: cut entirely from MVP.** Replace with a one-line affordance on every other screen: **"Copy `kubectl logs` command pre-filled with this `correlationId`"**. That is what the operator will actually do on Day 1, and it costs us zero new endpoints, zero new memory budget, zero new transport debate.

If we're worried about non-Kubernetes deployments, expose `GET /admin/logs/tail` as a thin wrapper around the existing log writer with `?correlationId=` and `?tail=N` only. Five filter widgets and downloads are v2.

### O3. "Trace empty-path explainer" (§4.3, journey 3.2 step 5)

The drawer's "no match" branch is supposed to detect three failure modes (no resource_change, no topic match, no active subscription) and link the operator to the right config. Pretty UX, but each branch is a separate query, and the cardinality of "why didn't this fire" is much larger than three (filter rejected event, FHIRPath timeout, subscription off, channel disabled, `eventsSinceSubscriptionStart` capped, key version mismatch, encrypted-at-rest decode fail, DLQ row pre-existed, etc.). At MVP this is worth a **single line of "no events found in window — common causes: …"** plus links to Subscriptions list, Topics list, DLQ. The conditional logic is gold-plating.

**Recommendation: defer the conditional explainer to v2.** Ship a static "common causes" panel.

### O4. Status history strip on Subscription Detail (§4.2)

A horizontal timeline derived from `audit_log` rows joined by `target_id` to answer "was this active at 14:32?" The same answer comes from rendering `created_at`, `updated_at`, `status`, and `lastError` as four fields on the page. Subscriptions don't flip status often; in steady state most rows have one or two audit entries. Building a hover-rich timeline strip for a 1-row history is theatre.

**Recommendation: replace with a plain table** — last N audit entries for this subscription, columns `at | actor | action | from → to | reason`. Ten lines of JSX. The "horizontal timeline" UX is v2.

### O5. Per-adapter throughput sparkline + per-resource-kind 24h counts (§4.5)

These widgets exist in Prometheus already (`fhir_subs_resource_changes_total{adapter_id, resource_type}` or whichever the rollup is). Re-implementing sparklines in the UI duplicates Grafana for the second time and pulls a charting dependency into a tool that should be HTML tables. The operator's "is this adapter dead?" question is answered by **state badge + last heartbeat age + last error**, which the PRD already lists.

**Recommendation: cut sparklines and 24h breakdowns from MVP.** Keep state, heartbeat age, restart count, last error. Defer charts to v2 or just link to Grafana.

### O6. Substring search across `id`, `clientId`, `topicUrl`, `endpoint` for subscriptions (§3.4, §4.2)

The existing `GET /admin/subscriptions?clientId=X` is per-client by design — the audit comment in `admin.go:131` is "the query parameter is required so operators cannot accidentally page the entire fleet." The PRD's §5.2 cross-client search endpoint reverses that decision and lifts the **S-2.8 deferral** ("searchSubscriptions no pagination — DEFERRED" in `docs/status.md:72`) without explaining why we're unblocking it now. **The repo doesn't have paginated search yet. The PRD treats it as if it does.**

For a hospital with one EHR and a known set of subscribers (the v1 deployment shape), the operator already knows the `clientId`. They don't need substring search.

**Recommendation: ship the existing per-client list as-is.** If the operator doesn't know `clientId`, give them a `GET /admin/clients` endpoint that lists the distinct `clientId`s in the table — that is a 30-line query. Defer fuzzy substring search to v2 once we have actual paging in the repo (S-2.8).

### O7. "Copy CLI command" pre-filling `fhir-subs dead-letters list|replay|forget` (§3.3, §4.4, §5.2)

The CLI doesn't exist. The runbook (`docs/operations/dead-letters-runbook.md:109`) says explicitly: "A `fhir-subs dead-letters list|replay|forget` admin CLI is deferred (see future-work.md P1.6). Until then, the SQL above is the operator surface." The PRD is shipping a "copy" affordance that pastes a command the operator can't run. That's worse than no affordance — it's a confidence trap.

**Recommendation: replace with "Copy SQL" or "Copy DLQ row JSON for ticket attachment".** The runbook already has the SQL. Or: this is a strong argument that the **CLI subcommand should ship before or alongside the UI**, since both surfaces want it. Either way, do not ship a UI button for a non-existent CLI.

### O8. Logs `Download` NDJSON / text export with 100 MB cap (§4.6)

A 100 MB NDJSON download from a hospital IT person's browser, served by a Go process with the data in a ring buffer, is going to be a memory spike at best and an OOM at worst. The use case is "attach logs to a ticket." The use case is solved by `kubectl logs ... > file.ndjson` or by their existing log aggregator's export. We do not need to be in the log-export business.

**Recommendation: cut entirely.** If we keep any log endpoint at all (per O2), cap the response at 1k records and let the operator's external store handle exports.

### O9. Audit-log row written for every read (§5.2, §7)

"All admin endpoints emit an audit_log entry per call." In v1 every actor is `"admin-token"`. The audit signal is zero (one bearer token = one identity = no differentiation), the write amplification is real (a quiet operator clicking around the dashboard for 10 minutes generates tens of audit rows per minute given 15s auto-refresh on five tiles plus background polling on Logs tail mode). With auto-refresh enabled, the audit table will be dominated by Dashboard polls. **This makes the audit table less useful for actual incident investigation, not more.**

**Recommendation: only audit *write* actions and explicit read actions a human triggered (search submissions, drilldowns).** Skip audit on auto-refresh polls and dashboard tile fetches. Revisit when SSO lands and `actor_id` becomes meaningful.

---

## Part 3 — Recommended cuts to MVP scope

If shipping in two weeks, build this:

**Survives intact:**
- **Subscriptions list + detail (§4.2)** — the existing per-client list page plus a detail drawer that renders the full subscription row + last 100 deliveries + last 20 audit entries (as a plain table, not a timeline strip — see O4). This answers "was X active at 14:32?" and "what is X subscribed to?"
- **Dead-Letter Queue list + detail (§4.4)** — the existing `/admin/dead_letters` endpoint with a UI on top. Filter by `reason`. Drawer shows full row. Replace "Copy CLI" with "Copy SQL" until the CLI exists (see O7).

**Reduced:**
- **Adapter Health (§4.5)** — keep state badge, heartbeat age, restart count, last error. Cut sparklines and per-resource counts (see O5). One screen, three columns of text. **NEW:** add `fhir_subs_adapter_last_heartbeat_seconds` gauge (PRD already calls this out in §10.5; needs to land first, not after).
- **Dashboard (§4.1)** — collapse from five traffic-light tiles to a single status panel: lifecycle state, version, last config reload, links to Grafana for trends and Alertmanager for alerts (see O1). One screen, four fields, two links.
- **Trace / Events (§4.3)** — search by `correlationId` and `subscriptionId` only for MVP. Skip patient-ID search until the schema work lands (see G1, R1). Skip the conditional empty-state explainer (see O3). Keep the row drawer because it's the actual triage surface.

**Cut:**
- **Logs Viewer (§4.6)** — replaced by "Copy `kubectl logs` command" affordance on every screen (see O2).

**Not yet built (precondition):**
- `fhir_subs_adapter_last_heartbeat_seconds` gauge — PRD §10.5 acknowledges this is NEW; it has to land for §6's `SidecarAdapterDown` alert and for the Adapter Health card heartbeat-age, so it is a hard precursor to MVP.
- `patient_id` indexed column or separate `patient_index` table on `ehr_events` / `resource_changes` if patient-ID Trace is in scope (see G1, R1) — otherwise patient search has to be cut from MVP entirely.

**Endpoints actually needed for the cut MVP:**
- `GET /admin/subscriptions/{id}` — NEW, trivial
- `GET /admin/subscriptions/{id}/deliveries?limit=N&before=...` — NEW, indexed read
- `GET /admin/subscriptions/{id}/audit?limit=N` — NEW, indexed read
- `GET /admin/events/search?correlationId=...&subscriptionId=...&from=...&to=...&limit=...` — NEW, narrowed scope (no patient/order yet)
- `GET /admin/events/{eventNumber}` — NEW
- `GET /admin/adapters` — NEW, in-memory snapshot

That's six new endpoints, not nine. Logs tail/download cut. `subscriptions/search` cut (use the existing `?clientId=` plus a new `GET /admin/clients`).

---

## Part 4 — Risks the PRD didn't surface

### R1. Patient-ID search doesn't have a column (§4.3, §3.2)

`internal/infra/storage/repos/models.go` shows `ResourceChangeRow` and `EhrEventRow` have no `patient_id` field. The patient identifier lives inside the encrypted `Resource []byte` payload. The PRD's centerpiece feature ("search by patient ID + time window") cannot be served by the existing schema; it requires either (a) extracting and indexing patient IDs at write time on `resource_changes` / `ehr_events`, or (b) decrypting payloads at query time — which is architecturally wrong (encryption-at-rest is enforced precisely to keep PHI off the read path). The PRD assumes (a) but doesn't list it under §5.2 NEW work or §10 Open Questions. **This is the biggest unstated piece of the build.**

### R2. PHI in the URL bar (§4.3, §3.2 step 2)

The Trace screen searches by patient identifiers, MRNs, system|value, and `Patient.id`. If those go in query strings — which they do, per the §5.2 endpoint signature `?patient=...` — they end up in the operator's browser history, the proxy access log, the Kubernetes ingress log, the L7 load balancer log, and probably their corporate web filter. **PHI is now in five log stores none of which were in scope for the encryption-at-rest work.** Mitigations: POST the search body, use opaque server-side query IDs, or hash the patient identifier client-side. Pick one and document it. The PRD's PHI section (§5.2) only addresses payload bodies, not query strings.

### R3. Dashboard auto-refresh × five tiles × Prometheus client × audit log = noisy floor (§4.1, §5.2 audit clause)

15s auto-refresh on the dashboard with five tiles, each pulling a Prom range query and writing an audit_log row, gives ~20 audit writes per minute per open tab. Two operators leave the dashboard open over lunch, the audit table grows by ~24k rows/day from idle browsers. Combined with the hash-chained audit log's append cost, this is a measurable write amplification. See O9 for mitigation.

### R4. "UI runs inside or outside the cluster" is unanswered and changes the auth model (§7, §10.2)

PRD §10.2 lists this as an open question. It shouldn't be open by MVP — it determines whether the bearer token lives in the browser (XSS-exposed), in a same-origin proxy (extra component to deploy), or only in `kubectl port-forward` flows (locks the operator to k8s). Decide before shipping; the answer drives whether `/admin/ui/*` is bundled into the sidecar binary (simplest, safest) or a separate image with its own auth proxy.

### R5. List endpoints unbounded above ~10k rows (§4.4 acceptance "10,000 rows without browser jank"; S-2.8 paging deferral)

The DLQ acceptance criterion targets "10,000 rows without browser jank" — that's a client-side concern, but the server side caps at `MaxAdminDeadLetterLimit = 500` per the existing code (`admin.go:31`). To paginate to 10k the operator pages 20 times, each request scanning the whole table because the existing handler is `ListRecent` not a cursor. At 50k subscriptions or DLQ rows this falls over. The PRD acknowledges S-2.8 only for `subscriptions` (§5.2 row 1); it does not acknowledge the same gap for `dead_letters`, `audit_log`, `deliveries`, or `events.search`. **All five list endpoints need cursor pagination defined before shipping; MVP should explicitly cap each one to 500 and not promise 10k.**

### R6. Correlation-ID format and propagation across HL7 boundary unspecified (§3.2, §4.3)

The Trace screen and the Logs filter both pivot on `correlationId`. The `audit_log` row writer, the channel attempt logger, the matcher's `MetricsEmitter`, and the inbound HL7 processor all need to emit the **same** UUID for the same logical event chain. The PRD assumes this works; it doesn't define which component mints the ID, when it's propagated across `resource_changes → ehr_events → deliveries → dead_letters`, or what happens when an HL7 message produces N `resource_changes` (one parent ID with N children? N independent IDs? a tree?). Without this contract written down, the Trace screen will surface inconsistent ID joins. Worth a 10-line section in the PRD.

---

## Part 1 — Gaps (real, not invented)

### G1. No back-end indexing for patient-ID Trace search

The PRD's flagship feature in §3.2 cannot be served by the existing schema (see R1). MVP-blocker if patient-ID search is in scope; otherwise cut from MVP and ship correlation-ID search first.

**Treatment:** add a `patient_id_hash` column (or a `patient_index` table) populated by the adapter at write-time, with an index on `(patient_id_hash, occurred_at)`.

### G2. No "where does the operator find the bearer token?" path

PRD §7 says the token is "configured at deploy time, stored in the operator's secret manager, and injected into the UI as a config value." For a hospital IT operator who didn't deploy the sidecar, **how do they get to the UI on Day 1?** No URL of record, no "operator setup" doc referenced, no `kubectl port-forward` recipe. They will get blocked at the login screen.

**Treatment:** add an "Operator first-run" appendix (or a deploy/README link) — three commands that produce a working URL + token.

### G3. No "things changed since I last looked" affordance

Steady-state operator opens the UI once a day to confirm nothing's broken. Today the dashboard is a snapshot — there's no "what's new since 09:00?" pane. Concrete scenario: operator was on PTO Mon–Wed, comes in Thursday, wants to know what happened in those three days without scrolling through every screen.

**MVP-blocking?** No, v2-fine.

**Treatment:** v2 — a single "Activity since X" panel on the dashboard counting DLQ inserts, adapter restarts, subscription state flips since a chosen timestamp.

### G4. No "I'm getting paged, what do I do?" landing path

When Alertmanager fires `SidecarDeadLetterRateHigh`, the page links to the runbook. The runbook tells the operator to look at the DLQ. **The UI does not link to the runbook.** Concrete scenario: paged at 02:00, half-asleep, opens the UI — there is no "open runbook" link from the DLQ screen.

**MVP-blocking?** Borderline; trivial fix.

**Treatment:** every screen that maps to a runbook gets a small "Runbook ↗" link in the header. DLQ → dead-letters runbook, Adapter Health → adapter runbook, etc.

### G5. No "tell me what version is deployed and what's in it" affordance for upgrade verification

Operators upgrade the sidecar and want to confirm the new version is up. PRD §4.1 includes "Sidecar version + commit + build time," good — but it's buried in the dashboard. Concrete scenario: an SRE is rolling a hotfix and wants the version visible on every screen header.

**MVP-blocking?** No, v2-fine.

**Treatment:** put `version@commit` in the global header. One line.

### G6. No "how big is my fleet right now?" counters

A hospital with 50 EHR clients each with 10 subscriptions = 500 subscription rows. Operator wants to know the totals at a glance: active subscriptions, errored subscriptions, off subscriptions, DLQ depth, in-flight deliveries. PRD §4.1 traffic-light tiles are pass/fail, not counts. Concrete scenario: a vendor asks "how many active subscriptions are you running?" — the operator should answer in two clicks.

**MVP-blocking?** No.

**Treatment:** add `GET /admin/stats` returning four integers. Render as four numbers on the dashboard. Replaces three of the five traffic-light tiles (see O1).

### G7. No "redirect from a Prometheus/Grafana alert" deep link

Alertmanager labels can carry URLs. A `runbook_url` is in the §6 example. A *deep link into the UI* (e.g., `/admin/ui/dlq?reason=subscriber_5xx&from=now-1h`) is not. Concrete scenario: alert fires for `subscriber_5xx`, on-call clicks the alert, lands on a generic dashboard, has to filter manually.

**MVP-blocking?** No, but cheap.

**Treatment:** define and document URL parameters for DLQ filter, Trace filter, Subscription detail. Wire them into the Alertmanager rule templates in `deploy/`.

### G8. No "this is a sandbox / non-prod" banner

PRD §1 calls this "a single deployed sidecar" but doesn't tell the UI to render an environment badge. Operators run dev / staging / prod side-by-side; without a visual cue, the wrong-tab risk is real. Concrete scenario: operator triages a prod incident on a staging tab open in the next window over.

**MVP-blocking?** No, but cheap and high-leverage.

**Treatment:** environment string in config, rendered as a colored top-bar banner. Five lines of code.

---

## Closing read

The PRD does the hard things right: read-only first, no in-app alerting, write actions deferred, PHI redaction inherited from the existing service, audit-log integration, sample Alertmanager rules. If the cuts above land, the surface is roughly half what's drafted, with the same "is it broken / why didn't X fire / what's stuck" coverage and a credible two-week delivery. The Trace screen is the one place where ambition outran the schema; G1/R1 should be answered before the PRD goes from Draft to Approved.
