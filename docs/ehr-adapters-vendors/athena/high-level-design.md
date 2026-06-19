# Athena Adapter — High-Level Design

**Summary.** athenahealth (athenaOne) is an API-first cloud EHR with a US Core FHIR R4 read API and a proprietary athenaNet REST API. Neither honors the FHIR `Subscription` resource; the adapter must synthesize subscription semantics from a poll-based "changed-feed" (athenaNet REST) plus a FHIR R4 hydration path, with HL7 v2 generally not in scope.

**Reader's prerequisites.** [overview.md](../../high-level-design/overview.md), [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md), [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md), [adapter-authoring-guide.md](../../adapter-authoring-guide.md).

---

## 1. Vendor landscape

athenahealth ships a single integrated cloud platform — **athenaOne®** — that bundles EHR, revenue-cycle management, and patient engagement. The product names that historically shipped separately (athenaClinicals for the EHR, athenaCollector for RCM, athenaCommunicator for patient engagement) are now sub-modules of athenaOne; the EHR module is still informally referenced as "athenaClinicals" in API docs and capability statements. **athenaIDX** is a separate enterprise-RCM product line (formerly Centricity Business / GE) with its own interfaces — it is **out of scope for this adapter** and would warrant a separate `athena-idx` adapter id if support is added.

- **Market segment.** Ambulatory only. athena reports 170K+ providers serving 20%+ of the U.S. population, predominantly small-to-mid practices and ambulatory specialty groups; not used for inpatient. Source: <https://www.athenahealth.com/about>.
- **Hosting model.** Multi-tenant cloud, SaaS only. There is no on-prem deployment and no per-tenant interface engine (unlike Epic Bridges or Cerner CCL servers). All customer access is through athena-hosted REST endpoints. The adapter therefore only needs **outbound HTTPS** to athena; there is no private VPN or MLLP socket to operate.
- **Sandbox.** athena exposes a Preview environment at `api.preview.platform.athenahealth.com` and a Production environment at `api.platform.athenahealth.com`. Both speak the same protocols. The Preview environment uses synthetic patient data and is the conformance target for the adapter's CI harness.

This shape — single cloud SaaS, ambulatory only, no operator-controlled HL7 — is materially different from Epic, Cerner, and Meditech. The adapter's design reflects that: **`Hl7MessageProcessor` is not built**; **`VendorAPIClient` carries the full CDC load** against a polled change-feed; **`FhirScanRunner` is the conformance fallback** for resources without changed-feed coverage.

## 2. API surface

Every CDC-relevant athena interface, with the channel and change-detection model the adapter would actually use.

| Name | Protocol | FHIR version | Change detection | Auth | GA status | Source |
|---|---|---|---|---|---|---|
| **athenahealth FHIR R4 Read API** | HTTPS, FHIR R4 (4.0.1) | R4, US Core 3.1.1 | Pull only — `_lastUpdated` supported with `eq/gt/lt/ge/le` comparators | OAuth2 / SMART on FHIR (`client_secret_*`, `private_key_jwt`) | GA | `GET https://api.platform.athenahealth.com/fhir/r4/metadata` (CapabilityStatement); `https://api.platform.athenahealth.com/fhir/r4/.well-known/smart-configuration` |
| **athenaNet "changed" REST endpoints** | HTTPS, JSON, athenaNet v1 | n/a (proprietary) | Pull (poll); server-side cursor via `leaveunprocessed=true` and `showprocessedstartdatetime` | OAuth2 client_credentials, scope `athena/service/Athenanet.MDP.*` | GA | `https://api.platform.athenahealth.com/v1/{practiceid}/{resource}/changed`; SDK reference: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b/athenahealth/patients.go>, `appointments.go`, `lab_results.go` |
| **athenaNet "subscription" REST endpoints** | HTTPS, JSON | n/a | Side-channel: registers interest in event types that *populate* the changed-feed (still pull) | Same OAuth2 client_credentials | GA | `GET/POST/DELETE /v1/{practiceid}/{resource}/changed/subscription`; `GET /v1/{practiceid}/{resource}/changed/subscription/events` |
| **athena Bulk FHIR (`Group/$export`)** | HTTPS, FHIR R4 NDJSON | R4 / Bulk Data IG 1.0 | Pull, async export — full state, no change deltas | SMART Backend Services (`private_key_jwt`, scope `system/*.read`) | GA (advertised in CapabilityStatement) | CapabilityStatement `rest.resource.operation` for `Group/$export` |
| **HL7 v2 over MLLP** | n/a — not offered as a customer-tenant interface | n/a | n/a | n/a | Not available | Confirmed by absence from athena public docs and the SaaS hosting model |
| **FHIR `Subscription` / `SubscriptionTopic`** | n/a — not in CapabilityStatement | n/a | n/a | n/a | Not implemented | `metadata` endpoint above; no entry for either resource on Preview or Production |

Two observations are load-bearing for the adapter design:

1. **There is no push channel from athena.** Neither the FHIR API nor athenaNet exposes webhooks, server-sent events, or a streaming change feed. The only "subscription" surface — `/v1/{practiceid}/{resource}/changed/subscription` — is a *registration of interest*, after which the event still surfaces only when the client polls the matching `/changed` endpoint. The athenaNet "subscription" name is a misnomer relative to FHIR; mechanically it is a server-side filter on a polled cursor, not a delivery contract.
2. **`_lastUpdated` is honored on the FHIR API.** The CapabilityStatements at `api.platform.athenahealth.com/fhir/r4/metadata` and `api.preview.platform.athenahealth.com/fhir/r4/metadata` advertise `_lastUpdated` with full comparator support on Patient, Encounter, Observation, DiagnosticReport, ServiceRequest, MedicationRequest. This is the rare vendor where the project's general "do not trust `_lastUpdated`" guidance can be relaxed *as a hint* — but the framework's snapshot-and-diff is still the source of truth ([ehr-adapter.md §FHIR Scan Runner](../../high-level-design/domains/ehr-adapter.md#fhir-scan-runner)). `_lastUpdated` is a query optimization, not a change-detection mechanism.

## 3. Mapping FHIR Subscription requests to athena CDC

How each Stage 1 sub-component maps onto athena's API surface.

### `Hl7MessageProcessor` — **not built**

athena does not expose HL7 v2 over MLLP to customers or interface engines. The adapter declares `Capabilities.HL7Processor = false` and returns `nil` from `BuildHl7Processor`. The existing scaffold in [`adapters/athena/athena.go`](../../../adapters/athena/athena.go) wires a passthrough HL7 processor for "facilities that still emit v2 over MLLP to athena bridges" — that path is not part of the supported athenaOne integration and should be removed or moved behind a configuration flag before this adapter goes to production.

### `VendorAPIClient` — **primary CDC source**

Wraps the athenaNet `/changed` family. One `Consume` worker per subscribed resource type. The flow is:

1. **At startup**, for each resource type in `vendor_api.resources`, ensure the practice has a registered subscription by `GET /v1/{practiceid}/{resource}/changed/subscription`. If absent, `POST /v1/{practiceid}/{resource}/changed/subscription` with the desired `eventname` values (event taxonomy is per-resource; discovered via `GET /v1/{practiceid}/{resource}/changed/subscription/events`).
2. **In the loop**, poll `GET /v1/{practiceid}/{resource}/changed?leaveunprocessed=true&showprocessedstartdatetime=<cursor>` on a configurable cadence (default 30s, lower in Preview). `leaveunprocessed=true` means "do not advance athena's server-side cursor" — the adapter persists its *own* cursor (`showprocessedstartdatetime` value) in `AdapterStateStore` keyed `cursor:athena:changed:<resource>`, advancing it only after the resulting `resource_changes` rows commit. After successful drain, a final `GET ...?leaveunprocessed=false` (or the equivalent `processed=true` marker) acknowledges the batch on athena's side.
3. **`Translate(record VendorRecord)`** maps an athenaNet JSON record to a FHIR resource. Patient and Appointment changed records carry enough to synthesize the FHIR resource directly; encounter/lab/order records are reference-only and require a follow-up FHIR R4 read (delegated to `HydrationService`'s `Fetch`).
4. **`VendorRecord.Cursor`** is set to the `showprocessedstartdatetime` of the next page. The framework persists it transactionally with the `resource_changes` insert; on restart the cursor is the only state needed to resume.
5. **`VendorRecord.EventCode`** is set to athena's event-name string (e.g., `appointmentbooked`, `appointmentcancelled`, `labresultreleased`). This flows to the engine for `SubscriptionTopic.eventTrigger` matching.

Resource-specific endpoint set, confirmed against the eleanorhealth/go-athenahealth SDK (commit `e08f0a979b`):

- `GET /v1/{practiceid}/patients/changed`
- `GET /v1/{practiceid}/appointments/changed`
- `GET /v1/{practiceid}/labresults/changed`
- `GET /v1/{practiceid}/prescriptions/changed`
- `GET /v1/{practiceid}/problems/changed`
- `GET /v1/{practiceid}/encounters/changed` (presence inferred from SDK pattern; **needs vendor confirmation**)

Each `/changed` endpoint has its own `/subscription` companion; not all event taxonomies are documented publicly and must be enumerated at startup via `/changed/subscription/events`.

**Cancel-and-replace.** athena does not have Epic-style placeholder/filler order pairing; its model is "one record changes, one event fires." The framework's correlation-window machinery can stay at the default `correlation_hold_window = 30s` and the default unpaired emitters; `Classify().CorrelationKey` will typically be empty for athena records. Order edits are emitted as updates to the original athenaNet record id, not as a cancel/replace pair.

### `FhirScanRunner` — **fallback and bootstrap**

Two roles:

1. **Conformance fallback** for resource types where the changed-feed has no native coverage (currently: Coverage, CareTeam, Goal, Provenance, Specimen, RelatedPerson, FamilyMemberHistory, Group, Questionnaire/QuestionnaireResponse). For each such type, `scan_plan()` returns a `ScanTarget{ResourceType, Cadence, QueryParams}` with a low cadence (5–15 min) and uses `_lastUpdated` as a *hint* (`_lastUpdated=gt<since>&_count=200&cursor=...`) to keep page volumes down. The framework's snapshot-diff against `adapter_state` remains the change-detection source of truth.
2. **Initial-scan bootstrap.** When a subscriber registers a `Subscription` whose topic requires a resource type that has not yet been seeded, the framework calls `scan_plan()` and runs an initial scan to populate `adapter_state` ([ehr-adapter.md §FHIR Scan Runner](../../high-level-design/domains/ehr-adapter.md#fhir-scan-runner)). For changed-feed-covered types, the scan runs once and then steps aside — the `VendorAPIClient` carries the steady state.

`run_scan` handles athena-specific paging via the `cursor` opaque parameter (per CapabilityStatement) and emits the `ah-practice` filter implicitly from `adapter.config.practice_id`. `normalize` is the identity function — athena is US Core 3.1.1 and maps cleanly to the project's R5 representation without profile rewriting (FHIR version downgrade R4→R5 is the framework's responsibility, not the adapter's).

### `HydrationService` — **always built**

Required for `full-resource` subscribers (per [ehr-adapter.md §HydrationService](../../high-level-design/domains/ehr-adapter.md#hydration-service)). Implementation: a single FHIR R4 `read` against `https://api.platform.athenahealth.com/fhir/r4/{ResourceType}/{id}`. Uses the same SMART Backend Services token pool as `FhirScanRunner`. `CacheTTL` defaults to 60s; for high-volume practices the operator may raise this to 300s with no clinical-staleness penalty because hydration is read-through and the underlying data has already been observed via the changed-feed.

## 4. Recommended architecture

**Primary CDC path.** `VendorAPIClient` over the athenaNet `/changed` family, polling at 30 s, with the framework cursor in `adapter_state`. This is the only path with sub-minute latency on athena.

**Fallback path.** `FhirScanRunner` over the FHIR R4 read API, with `_lastUpdated` as a hint and content-hash diff as the source of truth. Used for (a) resource types not in the changed-feed; (b) initial seeding for new subscriptions; (c) re-sync if the cursor is suspected lost beyond athena's retention window.

**Why this split.** athena's `/changed` endpoints are documented as the supported real-time signal — the SDK literature, athena's API reference (the `/changed/subscription` URL family), and the existence of per-event taxonomies all point to this as the production CDC mechanism. The FHIR read API is the system of record for resource shapes, but it is not optimized for change discovery — `_search?_lastUpdated=gt...` is a polling fallback, not a primary path. Driving every notification off FHIR scans alone would push polling cadence into the 5–15 min range to stay under the rate limit, which violates the project's general expectation of sub-minute notification latency on resources where the EHR is capable of it.

**Auth.** Single OAuth2 client. Production token URL: `https://api.platform.athenahealth.com/oauth2/v1/token`. Preview token URL: `https://api.preview.platform.athenahealth.com/oauth2/v1/token`. Grant: `client_credentials` with scope `athena/service/Athenanet.MDP.*` for athenaNet REST and `system/*.read` for FHIR (the FHIR scope set is per-resource and listed in the SMART configuration). The `private_key_jwt` token-endpoint auth method is supported and is the recommended mode under SMART Backend Services for production. Source: `oauth2/v1/token` POST shape from the eleanorhealth/go-athenahealth `tokenprovider/default.go`.

**Rate limits.** athena enforces per-practice quotas; the SDK ships a pluggable rate limiter but does not hardcode the numeric ceilings. The host-injected HTTP client should respect `Retry-After` and 429 responses, and the operator should set `vendor_api.poll_interval` and `fhir_scan.cadence` per practice to stay under the agreed quota. **Needs vendor confirmation** for documented rate-limit numbers.

## 5. Per-resource-type mapping

Where each clinical resource the project cares about comes from, on athena.

| Resource | Primary source | Endpoint | Event codes (sample) | Hydration call | Gotchas |
|---|---|---|---|---|---|
| Patient | Vendor API Client | `GET /v1/{practiceid}/patients/changed` | `patientcreated`, `patientupdated`, `patientdeleted` (deletion semantics: athena soft-deletes via status flags; **needs vendor confirmation**) | `GET /fhir/r4/Patient/{id}` | Deleted patients may resurface on chart-merge; merge events are reported as updates to the surviving chart |
| Encounter | Vendor API Client + FHIR fallback | `GET /v1/{practiceid}/appointments/changed` (encounter is athena's term for the *clinical* visit; appointment is its scheduled face) | `appointmentbooked`, `appointmentcheckin`, `appointmentcheckout`, `appointmentcancelled` | `GET /fhir/r4/Encounter/{id}` | "Encounter" vs "Appointment" disambiguation is non-trivial; see open question 3 |
| Observation (vitals, labs) | Vendor API Client (lab results); FHIR scan (vitals) | `GET /v1/{practiceid}/labresults/changed` for labs; FHIR scan with `category=vital-signs` for vitals | `labresultreleased`, `labresultupdated` | `GET /fhir/r4/Observation/{id}` | Vitals do not have a documented changed-feed endpoint as of this writing; FHIR scan is the only reliable source. **Needs vendor confirmation** for a vitals `/changed` feed |
| DiagnosticReport | Vendor API Client + FHIR fallback | `GET /v1/{practiceid}/labresults/changed` (carries the report skeleton; full report via FHIR read) | `labresultreleased` | `GET /fhir/r4/DiagnosticReport/{id}` | The athenaNet "lab result" record packs Observation + DiagnosticReport into one event; the adapter must split into two `resource_changes` rows |
| ServiceRequest (orders) | Vendor API Client | Documented `/changed` family for orders is partial; lab and prescription have explicit endpoints (`/labresults/changed`, `/prescriptions/changed`). General order `/changed` — **needs vendor confirmation** | `labordercreated`, `prescriptioncreated` (per-resource feeds, not a unified ServiceRequest feed) | `GET /fhir/r4/ServiceRequest/{id}` | Different order classes flow through different `/changed` endpoints; the adapter normalizes them all to FHIR `ServiceRequest` rows. Watch for athena reusing `ah-include-quality-codes` and other `ah-*` extensions on the FHIR side |

For any resource type the changed-feed does not cover, fall through to the `FhirScanRunner` plan.

## 6. Open questions and risks

These need vendor confirmation or production validation before this adapter is considered complete.

1. **Rate limits.** The numeric ceilings (per-second, per-day, per-practice) are not documented in the public API reference. The SDK exposes a pluggable rate limiter but ships a no-op default. **Needs vendor confirmation.** Until confirmed, the adapter must default to a conservative poll cadence (≥30 s per resource type) and back off aggressively on 429.
2. **Marketplace partnership.** athena's developer access is gated through the **athena Marketplace** (now redirected to `athenaconnect.athenahealth.com/marketplace`). Production credentials and per-practice authorization typically require a signed partner agreement; Preview credentials are easier to obtain via the developer portal. **Needs vendor confirmation** that an `fhir-ehr-subscriptions-service` deployment qualifies as a Marketplace integration partner; without that, only Preview is accessible.
3. **Encounter vs. Appointment disambiguation.** athena models the scheduled visit (Appointment) and the clinical encounter as distinct objects; the FHIR R4 API exposes both as `Encounter`/`Appointment`, but the changed-feed only has `/appointments/changed`. Whether a clinical encounter that occurred without a scheduled appointment fires a changed-feed event is **not documented**. The adapter may need to combine `/appointments/changed` with a low-cadence `Encounter` FHIR scan to be complete.
4. **Cursor retention window.** athena's `showprocessedstartdatetime` cursor is server-side; if the adapter is offline longer than athena's retention horizon (likely 24–72 hours), the cursor lookup may return an error or silently truncate. **Needs vendor confirmation** of the documented retention window. Mitigation: on cursor-rejection error, the adapter falls back to a one-shot `FhirScanRunner` re-seed for affected types.
5. **`Subscription` resource roadmap.** The CapabilityStatement does not list `Subscription` or `SubscriptionTopic`. athena has not announced an R4B Topic-Based Subscription rollout. If they do (driven by ONC Final Rule expectations), this adapter's primary CDC path could shift from `VendorAPIClient` to `FhirScanRunner` with topic-aware filtering, or to `VendorAPIClient` over a future webhook channel. The adapter design must remain capability-driven so a future migration is additive.
6. **Bulk FHIR for full resyncs.** `Group/$export` is advertised on the CapabilityStatement but the adapter does not currently use it. For very large practices an initial seed via `FhirScanRunner` may be too slow; a one-shot Bulk export to seed `adapter_state` would be the better path. This is a future enhancement, not a launch blocker.
7. **Event-code taxonomy stability.** The event names returned by `/changed/subscription/events` are not versioned in the documentation. If athena adds or renames events, `SubscriptionTopic.eventTrigger` matching may silently miss or double-fire. The adapter should log unknown event codes at WARN and emit a metric so operators detect drift.

## 7. References

All URLs verified 2026-06-19 unless noted.

- athenahealth Document Portal (root): <https://docs.athenahealth.com/api/>
- athenahealth FHIR R4 CapabilityStatement (Production): `GET https://api.platform.athenahealth.com/fhir/r4/metadata`
- athenahealth FHIR R4 CapabilityStatement (Preview): `GET https://api.preview.platform.athenahealth.com/fhir/r4/metadata`
- athenahealth SMART configuration (Preview): `GET https://api.preview.platform.athenahealth.com/fhir/r4/.well-known/smart-configuration`
- athenahealth product overview: <https://www.athenahealth.com/about>
- athenahealth Marketplace (redirected): <https://athenaconnect.athenahealth.com/marketplace/home>
- eleanorhealth/go-athenahealth (production-grade Go SDK, used here as a behavioral reference for the athenaNet REST surface and OAuth2 flow):
  - HTTP client: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/httpclient.go>
  - Token provider: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/tokenprovider/default.go>
  - Subscriptions: <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/subscriptions.go>
  - Patients (`/changed` endpoint): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/patients.go>
  - Appointments (`/changed`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/appointments.go>
  - Lab results (`/changed`): <https://github.com/eleanorhealth/go-athenahealth/blob/e08f0a979b3e13fd37c9ea328d190b1c279ec1d1/athenahealth/lab_results.go>
- jmandel/ehi-export-analysis (mirrored athena API specs): <https://github.com/jmandel/ehi-export-analysis/tree/9b8271c8030030f9a9647bbc30209550d962c446/results/athenahealth-inc--athenaclinicals/downloads/api-specs>
- Existing scaffold in this repo: [`adapters/athena/athena.go`](../../../adapters/athena/athena.go)
