# Athena Adapter — High-Level Design

**Summary.** athenahealth (athenaOne) is an API-first cloud EHR with a US Core FHIR R4 read API, a proprietary athenaNet REST API, and — as of late 2024 / 2025 — a limited-alpha **athenaOne FHIR Subscriptions** framework that implements the **HL7 Subscriptions R5 Backport STU 1.0.0** IG over R4 with `rest-hook` push delivery. The recommended primary CDC path for this adapter is the FHIR Subscription channel (push), with the athenaNet `/changed` polling family as the documented fallback for tenants that have not been granted the alpha scopes and for resource types not yet covered by a published `SubscriptionTopic`.

**Reader's prerequisites.** [overview.md](../../high-level-design/overview.md), [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md), [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md), [adapter-authoring-guide.md](../../adapter-authoring-guide.md).

---

## What changed in v2

v1 of this document concluded that **athenahealth does not offer FHIR Subscription / SubscriptionTopic / push delivery** and recommended polling the athenaNet `/v1/{practiceid}/{resource}/changed` family as the primary CDC path. **That conclusion was wrong.** athenahealth publishes an official sample webhook application and specification for the **athenaOne FHIR Subscriptions framework** at <https://github.com/athenahealth/aone-fhir-subscriptions> (last updated 2025-08-26, doc version 0.13). The framework:

- conforms to the **HL7 Subscriptions R5 Backport STU 1.0.0** IG (i.e., R4B-style topic-based subscriptions delivered on R4 wire),
- exposes `Subscription` and `SubscriptionTopic` REST endpoints at the same FHIR R4 base (`/fhir/r4/Subscription`, `/fhir/r4/SubscriptionTopic`),
- delivers via **`rest-hook`** with **`id-only`** payload, signed with **WebSub `X-Hub-Signature: sha256=<hmac>`**,
- publishes ~30 SubscriptionTopics across Patient, Encounter, Appointment, Order, Prescription, LabResult, ImagingResult, Claim, Provider, document events, and more,
- is **Alpha**: scopes (`system/Subscription.read`, `system/Subscription.write`, `system/SubscriptionTopic.read`) are turned on per-tenant by athena's API operations team and are **not surfaced in the public CapabilityStatement self-service UI** — which is why v1 missed it.

The CapabilityStatement at `api.platform.athenahealth.com/fhir/r4/metadata` and its Preview counterpart still do not list `Subscription` or `SubscriptionTopic` as resource entries (verified 2026-06-19), and a fresh-from-the-portal sandbox client cannot create a Subscription. The framework is real and athena-authoritative; access is gated. Treat the `/changed` polling path as a fallback that the adapter must still support, not as the primary path.

Source for everything in this re-verification: `https://github.com/athenahealth/aone-fhir-subscriptions/blob/main/README.md` (the official athena-published specification of the framework) plus the `samplecode/java` reference implementation in the same repo.

---

## 1. Vendor landscape

athenahealth ships a single integrated cloud platform — **athenaOne®** — that bundles EHR, revenue-cycle management, and patient engagement. The product names that historically shipped separately (athenaClinicals for the EHR, athenaCollector for RCM, athenaCommunicator for patient engagement) are now sub-modules of athenaOne; the EHR module is still informally referenced as "athenaClinicals" in API docs and capability statements. **athenaIDX** is a separate enterprise-RCM product line (formerly Centricity Business / GE) with its own interfaces — it is **out of scope for this adapter** and would warrant a separate `athena-idx` adapter id if support is added.

- **Market segment.** Ambulatory only. athena reports 170K+ providers serving 20%+ of the U.S. population, predominantly small-to-mid practices and ambulatory specialty groups; not used for inpatient. Source: <https://www.athenahealth.com/about>.
- **Hosting model.** Multi-tenant cloud, SaaS only. There is no on-prem deployment and no per-tenant interface engine (unlike Epic Bridges or Cerner CCL servers). All customer access is through athena-hosted REST endpoints. The adapter therefore only needs **outbound HTTPS** for athenaNet/FHIR pulls and **inbound HTTPS on a public, TLS-terminated webhook** for FHIR Subscription delivery; there is no private VPN or MLLP socket to operate.
- **Sandbox.** athena exposes a Preview environment at `api.preview.platform.athenahealth.com` and a Production environment at `api.platform.athenahealth.com`. Both speak the same protocols. Preview uses synthetic patient data and is the conformance target for the adapter's CI harness. The Subscriptions alpha is enabled on a per-tenant basis in **both** Preview and Production once athena's API operations team grants the scopes — there is no separate "subscriptions sandbox" host.

This shape — single cloud SaaS, ambulatory only, no operator-controlled HL7, but with a real (alpha) push channel — is materially different from Epic, Cerner, and Meditech. The adapter's design reflects that: **`Hl7MessageProcessor` is not built**; **`VendorAPIClient` carries the CDC load via either FHIR Subscription rest-hook delivery (preferred) or athenaNet `/changed` polling (fallback)**; **`FhirScanRunner` is the conformance backstop** for resources without either a published `SubscriptionTopic` or a `/changed` endpoint, and is also the bootstrap path for initial-state seeding.

## 2. API surface

Every CDC-relevant athena interface, with the channel and change-detection model the adapter would actually use.

| Name | Protocol | FHIR version | Change detection | Auth | GA status | Source |
|---|---|---|---|---|---|---|
| **athenaOne FHIR Subscriptions framework** | HTTPS, FHIR R4 + R5 Backport STU 1.0.0 IG | R4 wire, R5-Backport semantics | **Push.** `rest-hook` channel, `id-only` payload, signed `X-Hub-Signature: sha256=...`, at-least-once with 1h retry / 7d DLQ | OAuth2 client_credentials (2-legged), scopes `system/SubscriptionTopic.read`, `system/Subscription.read`, `system/Subscription.write` (granted out-of-band by athena API ops) | **Alpha** (limited rollout) — `aone-fhir-subscriptions` README v0.11 2024-11-19 launch, v0.12 2025-07-25 added filtering, v0.13 2025-08-26 added document event types | <https://github.com/athenahealth/aone-fhir-subscriptions/blob/main/README.md> |
| **athenahealth FHIR R4 Read API** | HTTPS, FHIR R4 (4.0.1) | R4, US Core 3.1.1 | Pull only — `_lastUpdated` supported with `eq/gt/lt/ge/le` comparators | OAuth2 / SMART on FHIR (`client_secret_*`, `private_key_jwt`) | GA | `GET https://api.platform.athenahealth.com/fhir/r4/metadata` (CapabilityStatement); `https://api.platform.athenahealth.com/fhir/r4/.well-known/smart-configuration` |
| **athenaNet "changed" REST endpoints** | HTTPS, JSON, athenaNet v1 | n/a (proprietary) | Pull (poll); server-side cursor via `leaveunprocessed=true` and `showprocessedstartdatetime` | OAuth2 client_credentials, scope `athena/service/Athenanet.MDP.*` | GA | `https://api.platform.athenahealth.com/v1/{practiceid}/{resource}/changed`; SDK reference: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b/athenahealth/patients.go>, `appointments.go`, `lab_results.go` |
| **athenaNet "subscription" REST endpoints** | HTTPS, JSON | n/a | Side-channel: registers interest in event types that *populate* the changed-feed (still pull) | Same OAuth2 client_credentials | GA | `GET/POST/DELETE /v1/{practiceid}/{resource}/changed/subscription`; `GET /v1/{practiceid}/{resource}/changed/subscription/events` |
| **athena Bulk FHIR (`Group/$export`)** | HTTPS, FHIR R4 NDJSON | R4 / Bulk Data IG 1.0 | Pull, async export — full state, no change deltas | SMART Backend Services (`private_key_jwt`, scope `system/*.read`) | GA (advertised in CapabilityStatement) | CapabilityStatement `rest.resource.operation` for `Group/$export` |
| **HL7 v2 over MLLP** | n/a — not offered as a customer-tenant interface | n/a | n/a | n/a | Not available | Confirmed by absence from athena public docs and the SaaS hosting model |

### Why the public CapabilityStatement does not list `Subscription`

The athena `aone-fhir-subscriptions` README is explicit:

> "the Subscription and SubscriptionTopic endpoints referenced in this document are only available in a limited alpha rollout at this time. … these scopes are not part of athenahealth's certified US Core FHIR R4 endpoints, therefore these scopes will not be listed in the self-service UI."

This explains the v1 misread: the authoritative `/fhir/r4/metadata` document on Production and Preview, fetched without a partner-assigned client, returns a CapabilityStatement that does **not** mention `Subscription`. That is consistent with the framework existing — it is alpha-gated and not advertised in the unauthenticated metadata. The adapter must therefore feature-detect the framework by attempting a `GET /fhir/r4/SubscriptionTopic` with the alpha scopes and falling back if 401/403/404 is returned.

### Three observations are load-bearing for the adapter design

1. **Push exists, gated.** athenaOne has a real FHIR Subscription `rest-hook` channel. When granted, it is the only sub-minute push path athena offers and should be the primary CDC source. Until granted, the adapter must operate on `/changed` polling as if push didn't exist.
2. **Payload is `id-only`.** Notification bundles carry resource references, not full resources. `HydrationService.Fetch` is **not optional** — every push event triggers a follow-up FHIR R4 read against the same base URL. Full-resource subscribers depend entirely on the hydration cache.
3. **`_lastUpdated` is honored on the FHIR API.** The CapabilityStatements at `api.platform.athenahealth.com/fhir/r4/metadata` and `api.preview.platform.athenahealth.com/fhir/r4/metadata` advertise `_lastUpdated` with full comparator support on Patient, Encounter, Observation, DiagnosticReport, ServiceRequest, MedicationRequest. This is the rare vendor where the project's general "do not trust `_lastUpdated`" guidance can be relaxed *as a hint* — but the framework's snapshot-and-diff is still the source of truth ([ehr-adapter.md §FHIR Scan Runner](../../high-level-design/domains/ehr-adapter.md#fhir-scan-runner)). `_lastUpdated` is a query optimization, not a change-detection mechanism.

## 3. Mapping FHIR Subscription requests to athena CDC

How each Stage 1 sub-component maps onto athena's API surface.

### `Hl7MessageProcessor` — **not built**

athena does not expose HL7 v2 over MLLP to customers or interface engines. The adapter declares `Capabilities.HL7Processor = false` and returns `nil` from `BuildHl7Processor`. The existing scaffold in [`adapters/athena/athena.go`](../../../adapters/athena/athena.go) wires a passthrough HL7 processor for "facilities that still emit v2 over MLLP to athena bridges" — that path is not part of the supported athenaOne integration and should be removed or moved behind a configuration flag before this adapter goes to production.

### `VendorAPIClient` — **primary CDC source (two modes)**

The client is **mode-aware**. At adapter startup, it probes for FHIR Subscription support and selects one of two implementations.

#### Mode A: FHIR Subscription rest-hook (preferred)

When `system/Subscription.write` is granted on the practice's OAuth client:

1. **At startup**, for each resource type in `vendor_api.resources`, the adapter `GET`s `/fhir/r4/SubscriptionTopic` to discover the published topic catalog (e.g., `Patient.create`, `Patient.update`, `Patient.merge`, `Patient.delete`, `Appointment.schedule`, `Appointment.check-in`, `Encounter.signoff`, `LabResult.create`, `Order.create`, `Prescription.create`, ...). It then `POST`s a `Subscription` resource per (topic, practice, department-filter) tuple. Body shape, per the athena README sample:

   ```json
   {
     "resourceType": "Subscription",
     "status": "requested",
     "reason": "fhir-subscriptions-foss adapter",
     "criteria": "https://api.platform.athenahealth.com/fhir/r4/SubscriptionTopic/Patient.update",
     "_criteria": {
       "extension": [
         { "url": "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-filter-criteria",
           "valueString": "ah-practice=Organization/a-1.Practice-195000" }
       ]
     },
     "channel": {
       "type": "rest-hook",
       "endpoint": "https://<adapter-public-host>/adapters/athena/webhook",
       "payload": "application/fhir+json",
       "_payload": {
         "extension": [
           { "url": "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-payload-content",
             "valueCode": "id-only" }
         ]
       },
       "header": [ "X-Hub-Secret: <hmac-secret-shared-with-athena>" ]
     }
   }
   ```

   Mandatory filter: `ah-practice`. Optional filter: `ah-department` (max 2000 values per filter; AND-combined with `ah-practice`). The adapter persists the returned Subscription `id` in `AdapterStateStore` keyed `subscription:athena:<topic>:<practice>` so it can be GC'd on shutdown.

2. **At runtime**, athena `POST`s notification Bundles to the adapter's webhook endpoint within a **2-second hard timeout** (athena's max round-trip per delivery). The adapter:
   - validates `X-Hub-Signature: sha256=<hmac>` against the shared secret using the WebSub validation algorithm (<https://www.w3.org/TR/websub/#signature-validation>),
   - parses the R5-Backport `notificationEvent` entries,
   - writes one `resource_changes` row per `notificationEvent.focus.reference`, with `event_code` set to the SubscriptionTopic id and `cursor` set to the notification's `eventNumber` (relative to the bundle, not absolute — see "delivery semantics" below),
   - returns `2xx` synchronously. Anything slower than 2s is treated by athena as a failure and retried.

3. **Delivery semantics.** **At-least-once.** Athena retries failed deliveries (non-2xx or timeout) for **up to 1 hour** before sending to a 7-day dead-letter queue. The adapter must therefore deduplicate at the `resource_changes` layer — duplicates are not pathological, they are normal. The README also notes athena's deviation from the R5 Backport IG: `eventsSinceSubscriptionStart`, `eventsInNotification`, and `notificationEvent.eventNumber` are **per-bundle**, not cumulative. The adapter must not attempt to detect gaps via these counters; it relies on the framework's snapshot-and-diff for completeness, and on `FhirScanRunner` as a backstop sweep.

4. **Translate(record VendorRecord)** is trivial in this mode: the `id-only` payload already carries the FHIR resource type and id, so `Translate` simply fans out to `HydrationService.Fetch` and copies the result into `VendorRecord.Resource`.

5. **Webhook endpoint hosting.** The adapter assembly defined in [`adapters/athena/athena.go`](../../../adapters/athena/athena.go) must register an HTTP route on the host's mux (per the SPI's `RegisterRoutes` extension point). The host is responsible for terminating TLS and exposing the route on a public DNS name; the adapter brings the handler.

#### Mode B: athenaNet `/changed` polling (fallback)

When the alpha scopes have not been granted, or when feature-detection (`GET /fhir/r4/SubscriptionTopic` returning 401/403/404 or empty bundle) fails:

1. **At startup**, for each resource type in `vendor_api.resources`, ensure the practice has a registered subscription by `GET /v1/{practiceid}/{resource}/changed/subscription`. If absent, `POST /v1/{practiceid}/{resource}/changed/subscription` with the desired `eventname` values (event taxonomy is per-resource; discovered via `GET /v1/{practiceid}/{resource}/changed/subscription/events`).
2. **In the loop**, poll `GET /v1/{practiceid}/{resource}/changed?leaveunprocessed=true&showprocessedstartdatetime=<cursor>` on a configurable cadence (default 30s, lower in Preview). `leaveunprocessed=true` means "do not advance athena's server-side cursor" — the adapter persists its *own* cursor (`showprocessedstartdatetime` value) in `AdapterStateStore` keyed `cursor:athena:changed:<resource>`, advancing it only after the resulting `resource_changes` rows commit. After successful drain, a final `GET ...?leaveunprocessed=false` (or the equivalent `processed=true` marker) acknowledges the batch on athena's side.
3. **Translate(record VendorRecord)** maps an athenaNet JSON record to a FHIR resource. Patient and Appointment changed records carry enough to synthesize the FHIR resource directly; encounter/lab/order records are reference-only and require a follow-up FHIR R4 read (delegated to `HydrationService.Fetch`).
4. **VendorRecord.Cursor** is set to the `showprocessedstartdatetime` of the next page. The framework persists it transactionally with the `resource_changes` insert; on restart the cursor is the only state needed to resume.
5. **VendorRecord.EventCode** is set to athena's event-name string (e.g., `appointmentbooked`, `appointmentcancelled`, `labresultreleased`). This flows to the engine for `SubscriptionTopic.eventTrigger` matching.

Resource-specific endpoint set, confirmed against the eleanorhealth/go-athenahealth SDK (commit `e08f0a979b`):

- `GET /v1/{practiceid}/patients/changed`
- `GET /v1/{practiceid}/appointments/changed`
- `GET /v1/{practiceid}/labresults/changed`
- `GET /v1/{practiceid}/prescriptions/changed`
- `GET /v1/{practiceid}/problems/changed`
- `GET /v1/{practiceid}/encounters/changed` (presence inferred from SDK pattern; **needs vendor confirmation**)

#### Cancel-and-replace

athena does not have Epic-style placeholder/filler order pairing; its model is "one record changes, one event fires." The framework's correlation-window machinery can stay at the default `correlation_hold_window = 30s` and the default unpaired emitters; `Classify().CorrelationKey` will typically be empty for athena records. Order edits are emitted as updates to the original athenaNet record id, not as a cancel/replace pair. The same is true under Mode A: athena's SubscriptionTopics are per-event (e.g., `Order.update` rather than `Order.cancel`+`Order.replace`).

### `FhirScanRunner` — **fallback and bootstrap**

Two roles:

1. **Conformance fallback** for resource types where neither the SubscriptionTopic catalog nor the `/changed` family has coverage (currently: Coverage, CareTeam, Goal, Provenance, Specimen, RelatedPerson, FamilyMemberHistory, Group, Questionnaire/QuestionnaireResponse, and most administrative-financial resources beyond Claim). For each such type, `scan_plan()` returns a `ScanTarget{ResourceType, Cadence, QueryParams}` with a low cadence (5–15 min) and uses `_lastUpdated` as a *hint* (`_lastUpdated=gt<since>&_count=200&cursor=...`) to keep page volumes down. The framework's snapshot-diff against `adapter_state` remains the change-detection source of truth.
2. **Initial-scan bootstrap.** When a subscriber registers a `Subscription` whose topic requires a resource type that has not yet been seeded, the framework calls `scan_plan()` and runs an initial scan to populate `adapter_state` ([ehr-adapter.md §FHIR Scan Runner](../../high-level-design/domains/ehr-adapter.md#fhir-scan-runner)). For push-covered or `/changed`-covered types, the scan runs once and then steps aside — `VendorAPIClient` carries the steady state.

`run_scan` handles athena-specific paging via the `cursor` opaque parameter (per CapabilityStatement) and emits the `ah-practice` filter implicitly from `adapter.config.practice_id`. `normalize` is the identity function — athena is US Core 3.1.1 and maps cleanly to the project's R5 representation without profile rewriting (FHIR version downgrade R4→R5 is the framework's responsibility, not the adapter's).

### `HydrationService` — **always built, mandatory under Mode A**

Required for `full-resource` subscribers (per [ehr-adapter.md §HydrationService](../../high-level-design/domains/ehr-adapter.md#hydration-service)) and structurally required under Mode A regardless of subscriber payload preference, because athena's `id-only` notifications have no resource body. Implementation: a single FHIR R4 `read` against `https://api.platform.athenahealth.com/fhir/r4/{ResourceType}/{id}`. Uses the same SMART Backend Services token pool as `FhirScanRunner`. `CacheTTL` defaults to 60s; for high-volume practices the operator may raise this to 300s with no clinical-staleness penalty because hydration is read-through and the underlying data has already been observed via the changed-feed or webhook.

## 4. Recommended architecture

**Primary CDC path.** Mode A — FHIR Subscription rest-hook delivery, when the alpha scopes have been granted to the practice's OAuth client. Push latency is sub-second under nominal athena load; this is the only path with that latency profile.

**Secondary CDC path.** Mode B — `VendorAPIClient` over the athenaNet `/changed` family, polling at 30 s, with the framework cursor in `adapter_state`. This is the production-default path until athena GAs the Subscription framework, since alpha access is per-tenant gated. The two modes are mutually exclusive per resource type — when Mode A is active for a resource, Mode B is suppressed for that resource to avoid double-emission.

**Fallback path.** `FhirScanRunner` over the FHIR R4 read API, with `_lastUpdated` as a hint and content-hash diff as the source of truth. Used for (a) resource types covered by neither Mode A nor Mode B; (b) initial seeding for new subscriptions; (c) re-sync if a webhook backlog exceeds the 7-day DLQ retention or a `/changed` cursor is suspected lost beyond athena's retention window.

**Mode selection (feature-detection).** At startup, after auth-bootstrap, the adapter runs `GET /fhir/r4/SubscriptionTopic`. Behaviors:

- `200` with a non-empty bundle → Mode A is available; per-topic capability flags drive per-resource selection.
- `401/403` → alpha scopes not granted; fall back to Mode B for all resources.
- `404` or empty bundle → framework not enabled on this tenant; fall back to Mode B.
- Network/5xx → retry with exponential backoff up to 3 attempts; on persistent failure, fall back to Mode B and emit a `mode_selection_degraded` metric.

The selection is recomputed every 24h so newly granted scopes promote a tenant from B to A without an adapter restart.

**Why this split.** athena's *documented*, *vendor-authored* push channel is the strongest possible signal that this is the real-time path. The README is updated through August 2025 and includes a Java reference implementation; this is not a forgotten experiment. Driving every notification off `/changed` polling alone, when push is available, would push avoidable latency into every clinical workflow and consume API quota the operator could otherwise spend on hydration. Mode B remains in the design because (a) Mode A is alpha and not all tenants will have it, and (b) athena's published `SubscriptionTopic` catalog does not yet cover every resource type the project supports.

**Auth.** Single OAuth2 client. Production token URL: `https://api.platform.athenahealth.com/oauth2/v1/token`. Preview token URL: `https://api.preview.platform.athenahealth.com/oauth2/v1/token`. Grant: `client_credentials` (2-legged) with scopes:

- `system/SubscriptionTopic.read`, `system/Subscription.read`, `system/Subscription.write` — Mode A; **must be requested out-of-band from athena's API operations team** ("not part of athenahealth's certified US Core FHIR R4 endpoints, therefore these scopes will not be listed in the self-service UI").
- `athena/service/Athenanet.MDP.*` — Mode B (athenaNet REST).
- `system/*.read` — `FhirScanRunner` and `HydrationService` (FHIR read API).

The `private_key_jwt` token-endpoint auth method is supported and is the recommended mode under SMART Backend Services for production. Source: `oauth2/v1/token` POST shape from the eleanorhealth/go-athenahealth `tokenprovider/default.go`; alpha scope list from `aone-fhir-subscriptions/README.md`.

**Webhook signing.** All Mode A deliveries carry `X-Hub-Signature: sha256=<hex-hmac-of-body-with-shared-secret>`. The shared secret is provided by the adapter at Subscription creation time via the `X-Hub-Secret` header on the `POST /Subscription` request. The adapter must validate the signature on every inbound notification before parsing the body; signature failures are logged as security events and return `401`, which intentionally triggers athena's retry loop.

**Rate limits and retry.** athena enforces per-practice quotas; the README does not publish numeric ceilings. The host-injected HTTP client should respect `Retry-After` and 429 responses, and the operator should set `vendor_api.poll_interval` (Mode B) and `fhir_scan.cadence` per practice to stay under the agreed quota. **Needs vendor confirmation** for documented rate-limit numbers. For Mode A, the inverse applies: the *adapter* is the rate-limited party because the webhook 2s timeout is hard. Slow downstream emit must not be inline with the webhook handler.

## 5. Per-resource-type mapping

Where each clinical resource the project cares about comes from, on athena. Mode A topics are listed where a published `SubscriptionTopic` is known to exist per the README v0.13 catalog; Mode B and FHIR-scan fallbacks are listed for completeness.

| Resource | Mode A SubscriptionTopic(s) (when alpha-enabled) | Mode B endpoint | Hydration call | Gotchas |
|---|---|---|---|---|
| Patient | `Patient.create`, `Patient.update`, `Patient.merge`, `Patient.delete` | `GET /v1/{practiceid}/patients/changed` | `GET /fhir/r4/Patient/{id}` | `Patient.merge` events surface a single notification on the surviving chart; the adapter must emit a delete for the merged-out chart by inspecting the FHIR `Patient.link` slice |
| Encounter | `Encounter.check-in`, `Encounter.reopen`, `Encounter.signoff` | `GET /v1/{practiceid}/encounters/changed` (presence per SDK; **needs confirmation**) | `GET /fhir/r4/Encounter/{id}` | Mode A distinguishes Encounter from Appointment (separate topic families) — disambiguation is unambiguous under Mode A. Under Mode B, only `/appointments/changed` is documented; clinical encounters without a scheduled appointment may not surface — see open question 3 |
| Appointment | `Appointment.schedule`, `Appointment.cancel`, `Appointment.check-in`, `Appointment.check-out`, `Appointment.reschedule`, `Appointment.update`, `Appointment.freeze`, `Appointment.unfreeze` | `GET /v1/{practiceid}/appointments/changed` | `GET /fhir/r4/Appointment/{id}` | 8 distinct event types in Mode A; the adapter passes `eventCode` straight through to `SubscriptionTopic.eventTrigger` matching |
| Observation (vitals, labs) | `LabResult.create`, `LabResult.update` (labs); no documented vitals topic | `GET /v1/{practiceid}/labresults/changed` (labs); FHIR scan with `category=vital-signs` (vitals) | `GET /fhir/r4/Observation/{id}` | Vitals are not in athena's SubscriptionTopic catalog as of v0.13; FHIR scan is the only reliable source. **Needs vendor confirmation** for a vitals topic on the roadmap |
| DiagnosticReport | `LabResult.create`, `LabResult.update`, `ImagingResult.create`, `ImagingResult.update` | `GET /v1/{practiceid}/labresults/changed` (carries the report skeleton; full report via FHIR read) | `GET /fhir/r4/DiagnosticReport/{id}` | The athenaNet "lab result" record packs Observation + DiagnosticReport into one event; under Mode B the adapter must split into two `resource_changes` rows. Under Mode A the topics already separate them |
| ServiceRequest (orders) | `Order.create`, `Order.update`, `Order.cancel` (per README v0.13 — 6 topics across order lifecycle) | `/labresults/changed`, `/prescriptions/changed` (split by class; general order `/changed` **needs confirmation**) | `GET /fhir/r4/ServiceRequest/{id}` | Mode A unifies order events under a single topic family; Mode B fragments by order class. Watch for athena reusing `ah-include-quality-codes` and other `ah-*` extensions on the FHIR side |
| MedicationRequest (Rx) | `Prescription.create`, `Prescription.update` (per README v0.13 — 7 topics) | `GET /v1/{practiceid}/prescriptions/changed` | `GET /fhir/r4/MedicationRequest/{id}` | — |
| Coverage / Claim | `Claim.create`, `Claim.update` (Mode A); no documented `/changed` for Coverage | (none for Coverage) | `GET /fhir/r4/Coverage/{id}`, `Claim/{id}` | Coverage falls through to FHIR scan in Mode B |
| Document events (admin/clinical/letter/medical-record/patient-info) | `AdminDocument.*`, `ClinicalDocument.*`, `Letter.*`, `MedicalRecord.*`, `PatientInfoOrder.*` (Alpha additions in v0.13, 2025-08-26) | n/a | `GET /fhir/r4/DocumentReference/{id}` | Document events are still flagged Alpha-within-Alpha; topic ids may rename before GA |
| Provider / ReferringProvider | `Provider.*`, `ReferringProvider.*` (per README v0.13) | (none) | `GET /fhir/r4/Practitioner/{id}` | — |

For any resource type the catalog and `/changed` family do not cover, fall through to the `FhirScanRunner` plan.

## 6. Open questions and risks

These need vendor confirmation or production validation before this adapter is considered complete.

1. **Mode A access timeline.** The Subscriptions framework has been Alpha since 2024-11-19 and the README has been updated through 2025-08-26 without a GA announcement. **Needs vendor confirmation** for (a) GA target, (b) the partner-engagement steps required to obtain alpha scopes for an integration like this, and (c) whether the alpha grant covers Production or Preview-only by default. Until confirmed, the adapter must default to Mode B and document Mode A as an opt-in for early-adopter practices.
2. **Webhook ingress hosting.** Mode A requires a public, TLS-terminated HTTPS endpoint with sub-2-second response latency. The `fhir-subscriptions-foss` deployment topology must guarantee the adapter handler runs hot (no cold-start) and is reachable from athena's egress IPs. **Needs operations confirmation** of the deployment pattern (sidecar in cluster ingress vs. dedicated webhook gateway) and any IP-allowlisting requirements. The README does not list athena egress IP ranges; this must be requested from athena's API operations team for environments that require allowlisting.
3. **Encounter vs. Appointment under Mode B.** athena models the scheduled visit (Appointment) and the clinical encounter as distinct objects; the FHIR R4 API exposes both as `Encounter`/`Appointment`, but Mode B's changed-feed only documents `/appointments/changed`. Whether a clinical encounter that occurred without a scheduled appointment fires a `/changed` event is **not documented**. Under Mode A this is moot — `Encounter.signoff` is its own topic. Mitigation under Mode B: combine `/appointments/changed` with a low-cadence `Encounter` FHIR scan.
4. **`/changed` cursor retention window.** athena's `showprocessedstartdatetime` cursor is server-side; if the adapter is offline longer than athena's retention horizon (likely 24–72 hours), the cursor lookup may return an error or silently truncate. **Needs vendor confirmation** of the documented retention window. Mitigation: on cursor-rejection error, the adapter falls back to a one-shot `FhirScanRunner` re-seed for affected types.
5. **Mode A DLQ handling.** athena retries failed deliveries for 1 hour and then queues to a 7-day DLQ. The README does not document a DLQ replay API. **Needs vendor confirmation** of the operator-facing tools for inspecting and replaying DLQ contents. Mitigation: if a webhook outage exceeds 7 days, the adapter triggers a `FhirScanRunner` re-seed for all topics covered under Mode A on that tenant.
6. **Alpha-instability of topic ids.** The README notes that the document-events topic family was added in v0.13 (2025-08-26). Topic identifier renames or removals during alpha are possible. The adapter should log unknown topic ids at WARN, emit a `topic_drift` metric, and store the topic catalog snapshot in `adapter_state` so the operator can diff catalogs across releases.
7. **Bulk FHIR for full resyncs.** `Group/$export` is advertised on the CapabilityStatement but the adapter does not currently use it. For very large practices an initial seed via `FhirScanRunner` may be too slow; a one-shot Bulk export to seed `adapter_state` would be the better path. This is a future enhancement, not a launch blocker.
8. **Rate limits.** The numeric ceilings (per-second, per-day, per-practice) are not documented in the public API reference for either Mode A or Mode B. **Needs vendor confirmation.** Until confirmed, Mode B defaults to a conservative poll cadence (≥30 s per resource type) and Mode A treats every webhook as best-effort with 429-aware backoff on the hydration follow-up reads.

## 7. References

All URLs verified 2026-06-19 unless noted.

- **athenaOne FHIR Subscriptions framework (the v2 correction):** <https://github.com/athenahealth/aone-fhir-subscriptions> — official athenahealth-published sample webhook application and specification. README v0.13 (2025-08-26) is the authoritative description of the rest-hook channel, payload format, signing, retry, scopes, and SubscriptionTopic catalog. The `samplecode/java` directory is a reference receiver implementation under Apache-2.0.
- HL7 FHIR Subscriptions R5 Backport STU 1.0.0 IG: <https://hl7.org/fhir/uv/subscriptions-backport/STU1/>
- WebSub signature validation: <https://www.w3.org/TR/websub/#signature-validation>
- athenahealth Document Portal (root): <https://docs.athenahealth.com/api/> (note: the public docs portal does **not** advertise the Subscriptions framework as of 2026-06-19; the GitHub repo is the canonical source)
- athenahealth FHIR R4 CapabilityStatement (Production): `GET https://api.platform.athenahealth.com/fhir/r4/metadata` — does **not** list `Subscription` in the unauthenticated metadata, consistent with the alpha-gating documented in the framework README
- athenahealth FHIR R4 CapabilityStatement (Preview): `GET https://api.preview.platform.athenahealth.com/fhir/r4/metadata`
- athenahealth SMART configuration (Preview): `GET https://api.preview.platform.athenahealth.com/fhir/r4/.well-known/smart-configuration`
- athenahealth product overview: <https://www.athenahealth.com/about>
- athenahealth Marketplace (redirected): <https://athenaconnect.athenahealth.com/marketplace/home>
- eleanorhealth/go-athenahealth (production-grade Go SDK; behavioral reference for Mode B athenaNet REST surface and OAuth2 flow):
  - HTTP client: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/httpclient.go>
  - Token provider: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/tokenprovider/default.go>
  - Subscriptions (Mode B `/changed/subscription`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/subscriptions.go>
  - Patients (`/changed`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/patients.go>
  - Appointments (`/changed`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/appointments.go>
  - Lab results (`/changed`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/lab_results.go>
- jmandel/ehi-export-analysis (mirrored athena API specs, including a Subscription-Example1.json that is consistent with the Mode A shape): <https://github.com/jmandel/ehi-export-analysis/tree/9b8271c8030030f9a9647bbc30209550d962c446/results/athenahealth-inc--athenaclinicals/downloads/api-specs>
- Existing scaffold in this repo: [`adapters/athena/athena.go`](../../../adapters/athena/athena.go)
