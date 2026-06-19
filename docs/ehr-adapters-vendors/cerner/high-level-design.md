# Oracle Health (Cerner) Adapter — High-Level Design

**Summary.** This document is the vendor research and architecture brief for the `cerner` (Oracle Health Millennium) adapter that plugs into [`fhir-ehr-subscriptions-service`](../../../README.md) via the [Adapter SPI](../../high-level-design/contracts/adapter-spi.md). It maps Cerner's real, current 2026 API surface onto the four adapter sub-components — `Hl7MessageProcessor`, `FhirScanRunner`, `VendorAPIClient`, `HydrationService` — and recommends a primary CDC path plus a fallback.

---

## 1. Vendor landscape

Oracle acquired Cerner Corporation in June 2022 (deal announced December 2021, ~$28.3B) and rebranded the EHR product line as **Oracle Health**. The Cerner brand still appears on legacy interfaces (`fhir.cerner.com` redirects to `docs.oracle.com/en/industries/health/...`, the `cerner.com` developer portal is being migrated, and customer-facing tools still say "Cerner Central"), but new docs are under `docs.oracle.com/en/industries/health/`.

Product lines relevant to this adapter:

- **Cerner Millennium** — the flagship EHR. Runs on a single shared Oracle/HP-UX-derived backend. ~9.5M users globally. *This is the platform the adapter targets.*
- **CommunityWorks** — multi-tenant managed Millennium for community/critical-access hospitals. Runs on the same Millennium codebase but Oracle hosts it (CernerWorks). The FHIR R4 API surface is the same.
- **Soarian** — acquired from Siemens in 2015; Cerner has been migrating Soarian customers onto Millennium for years. Soarian's API surface is *not* the Millennium FHIR API. **This adapter does not target Soarian.** A separate `oracle-health-soarian` adapter ID would be appropriate if needed.

Hosting models:

- **CernerWorks (Oracle-hosted)** — the dominant model. The FHIR endpoints live at Oracle-managed hostnames per tenant (e.g., `https://fhir-myrecord.cerner.com/r4/{tenant-id}/`).
- **Client-hosted ("Self-Hosted Solutions")** — a shrinking minority. Same APIs, hosted on customer infrastructure. **HL7 v2 connectivity terms differ** between hosting models — see §7.

Sources: [docs.oracle.com/en/industries/health/](https://docs.oracle.com/en/industries/health/), [en.wikipedia.org/wiki/Cerner](https://en.wikipedia.org/wiki/Cerner), Oracle press releases.

---

## 2. API surface

Every CDC-capable interface Cerner exposes to a third-party integrator. **Important framing:** Cerner has *no* native FHIR `Subscription` or `SubscriptionTopic` support — confirmed against the published Capability Statement and the API product index. The realistic CDC paths are HL7 v2 (legacy site interface), FHIR Bulk Data with `_since` (modern incremental polling), and FHIR REST polling (per-resource fallback).

| API / Interface | Protocol | FHIR version | Change-detection model | Auth | GA status | Source |
|---|---|---|---|---|---|---|
| **Millennium FHIR R4 API** | HTTPS REST | R4 (4.0.1) | Pull (REST search) | SMART on FHIR (v1/v2): Auth Code, Client Credentials, JWT Bearer | GA | [mfrap/r4_overview](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfrap/r4_overview.html) |
| **Millennium FHIR R4 Bulk Data ($export)** | HTTPS REST (async kick-off / poll / NDJSON) | R4 + Bulk Data IG v1.0.1, with v2.0.0 experimental params | Pull (incremental via `_since`) | SMART Backend Services (JWT) | GA | [mfbda/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/index.html) |
| **R4 Public Endpoints (Patient app / Provider app)** | HTTPS REST | R4 | Pull | SMART Patient/Provider scopes (user-context) | GA | [mfpea/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfpea/index.html), [mfpeb/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfpeb/index.html) |
| **Millennium EHR APIs (proprietary REST)** | HTTPS REST | n/a (Cerner-proprietary JSON) | Pull only | SMART/OAuth2 client credentials | GA | [mcfap/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mcfap/index.html) |
| **FHIR DSTU 2 API** | HTTPS REST | DSTU2 | Pull | SMART | **End of Support** — do not use | [mfdap/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfdap/index.html) |
| **HL7 v2 over MLLP ("Cerner Open Engine" / OPENLink)** | TCP/MLLP | n/a (HL7 v2.x) | Push (real-time triggers) | TLS + IP allow-list (typically) | GA but site-specific | Cerner site-specific interface specs (not publicly published; ordered through CernerWorks Connectivity / professional services) |
| **FHIR R4/R5 `Subscription` / `SubscriptionTopic`** | — | — | — | — | **NOT SUPPORTED** (absent from Capability Statement) | [mfrap/r4_overview](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfrap/r4_overview.html) |
| **Webhooks / Kafka / push CDC** | — | — | — | — | **NOT OFFERED** as a third-party-consumable product | (no public doc; confirmed by absence in API index) |
| **CCL / Discern / MPages** | Internal scripting | n/a | Internal-only | Cerner-internal | Not a third-party integration surface | Cerner architecture (legacy) |

Notes on the negative findings:

- The FHIR R4 Capability Statement explicitly does not list `Subscription` or `SubscriptionTopic`. Cerner has not announced a roadmap for either as of 2026.
- The proprietary "EHR APIs" product (Allergies, Conditions, Chief Complaints, Personnel, Locations, Messages, Recipients) is **synchronous REST only** — no event streams, no webhooks, no SSE. ([mcfap rest-endpoints](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mcfap/rest-endpoints.html))
- "Cerner Ignite APIs" is historical marketing for the FHIR product line — it is the same FHIR R4 API documented above, not a separate event/streaming product.
- "CareAware" is Cerner's medical-device interoperability platform (smart pumps, monitors); it is not a third-party-accessible event bus.

---

## 3. The four adapter sub-components — what the Cerner adapter does

### 3.1 `Hl7MessageProcessor` — primary real-time path

**Capability declared:** `HL7Processor: true`.

The site provisions a Cerner Open Engine (or OPENLink) outbound interface that ships HL7 v2 messages over MLLP to the host's MLLP listener. The host writes raw bytes to `hl7_message_queue` and ACKs Cerner; the adapter's processor then claims rows and translates.

Trigger events the Cerner adapter must handle (HL7 v2.5.1 unless otherwise noted; Cerner sites often run v2.3 or v2.5):

- **ADT** — `A01` admit, `A02` transfer, `A03` discharge, `A04` register outpatient, `A05` pre-admit, `A08` update patient info, `A11` cancel admit, `A13` cancel discharge, `A28`/`A31` add/update person, `A40` merge patients. Map to `Patient` and `Encounter`.
- **ORM/OMG/OML** — orders. `NW` new, `XO` change, `CA` cancel, `OC` cancelled by service, `RP` replace. Map to `ServiceRequest` (and `MedicationRequest` for pharmacy via `RDE`).
- **ORU** — `R01` results. Map to `Observation` and `DiagnosticReport`.
- **SIU** — scheduling, when sites use Cerner Scheduling. Map to `Appointment`.
- **MDM** — document/`T02`. Map to `DocumentReference`.

**Z-segments.** Cerner sites routinely add `ZPV` (provider visit), `ZCD` (clinical data), `ZAL` (allergy), and site-customized Z-segments. **There is no single public "Cerner HL7 v2 spec"** — each site/customer receives an interface specification through CernerWorks Connectivity / professional services that documents the Z-segments configured for that site. The adapter must therefore be configurable: `lex` extends the standard tokenizer with vendor-known Z-segment shapes, but the *meaning* is per-site. The adapter's config schema should expose a `z_segment_map` so operators can declare which Z-segment fields populate which FHIR extensions.

**Cancel-and-replace.** Cerner's cancel/replace pattern is similar to Epic's: a `ORM^O01` with `ORC-1=CA` is followed by a fresh `ORM^O01` with `ORC-1=NW` and a new `ORC-3` filler order number. The correlation key the adapter returns from `Classify` should be `ORC-2` (placer order number) when stable, falling back to a composite of `(PID-3 patient ID, ORC-4 order group / placer parent, OBR-3 universal service ID timestamp)`. Default `CorrelationHoldWindow()` of 30s is appropriate; some Cerner sites configure longer queueing on the interface engine and may need a larger window.

`MapToFHIR` produces the post-translation R4 resource using the [HL7 v2-to-FHIR Implementation Guide](https://hl7.org/fhir/uv/v2mappings/) plus Cerner-specific overrides for Z-segments. The framework's `Validate` default is sufficient at first; over time, validate against [US Core 6.1](https://hl7.org/fhir/us/core/STU6.1/) (which Cerner aligns with for ONC certification).

### 3.2 `FhirScanRunner` — fallback / FHIR-only sites

**Capability declared:** `FhirScanRunner: true` (configurable — operators with HL7 v2 may disable it).

The framework calls `RunScan` on cadence; the adapter authenticates via SMART Backend Services (JWT bearer; client credentials grant against `/tenants/{tenant-id}/protocols/oauth2/profiles/smart-v1/token` at `authorization.cerner.com`) and fetches per-resource. **Critical:** Cerner does not expose a reliable `_lastUpdated` search parameter — the published Capability Statement omits it from the documented search params for `Patient`, `Encounter`, `Condition`, `Procedure`. The system-wide doc on general search-parameter support does not promise it elsewhere. The adapter must rely on the framework's snapshot-and-diff (canonical-JSON SHA-256), which is the documented project behavior. ([overview.md §FHIR Scan Runner](../../high-level-design/domains/ehr-adapter.md#fhir-scan-runner))

**`scan_plan()`** returns `(resource_type, cadence, query_params)` for the resources the active subscriptions need. Cadences must respect Cerner's rate budget (Cerner returns 429 on overrun; specific limits are not publicly documented and are CernerWorks tenant-specific). Suggested defaults:

- `Patient` — narrow scan only; do not enumerate all patients. The Patient endpoint requires a search filter (`identifier`, `name+birthdate`, etc.). Scan only patients referenced by an active subscription's filter.
- `Encounter` — by patient reference, every 5-15 min.
- `Observation` — by patient + category, every 5 min for vitals/labs.
- `DiagnosticReport`, `Condition`, `Procedure`, `MedicationRequest`, `AllergyIntolerance`, `DocumentReference` — by patient, every 15 min.

Vendor profile normalization (`Normalize`) is mostly identity for R4. `ContentHash` should strip `meta.lastUpdated`, `meta.versionId`, and Cerner-specific `meta.tag` entries before hashing — Cerner sometimes refreshes these without changing the resource body.

### 3.3 `VendorAPIClient` — Bulk Data $export, **not** a real change feed

**Capability declared:** `VendorAPIClient: true` (the FHIR Bulk Data product is the closest thing Cerner offers to a vendor-managed change feed). **Not a true streaming feed** — it's an asynchronous batch export that the adapter polls on a long cadence.

The `Consume(sink, cursor)` loop:

1. Read `cursor` (last successful `_since` watermark). If absent, initialize to "now − 7 days" (or operator-configured backfill window).
2. Kick off `POST /Patient/$export` (with the configured patient list — typically the operator-defined "subscribed cohort") or `GET /Group/{Group_ID}/$export` (preferred — Cerner Group export accepts `_since`, `_type`, `_typeFilter`). Set `_since=<cursor>`, `_type=` to the resources subscriptions need, and (where applicable) `_typeFilter=` for status filtering.
3. Poll the `Content-Location` URL until ready (the framework's retry/backoff supervises the loop).
4. Download each NDJSON file, push each FHIR resource onto the sink as a `VendorRecord` with `Cursor` set to the *next* `_since` value (i.e., the kick-off timestamp; Cerner advances `_since` semantics by transaction time).
5. The framework computes content-hash diffs against `adapter_state` and emits `resource_changes` rows.

`Translate(record)` is mostly identity — Cerner returns valid R4 resources. Set `EventCode` to `"bulk-data-since"` so the framework can tag the change with its origin.

**Frequency.** Cerner explicitly warns that bulk export "runs against the organization's database and uses the same services running other applications" ([mfbda/](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/index.html)). One in-flight export per client/tenant. A reasonable cadence is **every 15-60 minutes**, configurable. This is *not* near-real-time — it's the modern incremental-polling baseline.

**Exportable resource types:** `AllergyIntolerance, CarePlan, CareTeam, Condition, Coverage, Device, DiagnosticReport, DocumentReference, Encounter, Goal, Immunization, Location, MedicationDispense, MedicationRequest, Patient, Procedure, Observation, Organization, Practitioner, Provenance, RelatedPerson, ServiceRequest, Specimen` ([op-group-group_id-export-get](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/op-group-group_id-export-get.html)). `MedicationStatement` and several admin resources are not in the export.

### 3.4 `HydrationService` — required for `full-resource` subscriptions

**Capability declared:** `HydrationService: true`.

`Fetch(reference)` issues `GET /{ResourceType}/{id}` against the Millennium FHIR R4 API using the same SMART Backend Services token as the scan runner. The framework caches per-replica with default TTL 60s and coalesces concurrent calls. Override `CacheTTL()` to 30s for `Encounter` and `Observation` in clinical-staleness-sensitive deployments.

---

## 4. Recommended architecture — primary path + fallback

**Primary CDC path: HL7 v2 / Open Engine** when the site has an interface engine and is willing to provision an outbound feed (the typical inpatient/large-system case). It is the only near-real-time option Cerner offers, the trigger events map cleanly to FHIR resource changes, and the framework's MLLP listener already handles persistence-then-ACK durability.

**Fallback CDC path: FHIR Bulk Data `$export` with `_since`** for FHIR-only sites, sites where HL7 v2 provisioning is gated by procurement, or as a parallel reconciliation feed even when HL7 v2 is primary. It catches resources not emitted as HL7 trigger events (e.g., `CarePlan`, `Goal`, `Coverage`) and acts as a daily re-sync.

**Single-resource FHIR REST polling** (the `FhirScanRunner`) stays available for: (a) operator-configured high-frequency targets (a 30-second `Observation` scan for an ICU vitals topic), (b) on-demand seeding when a new subscription's resource type is not in the existing scan plan, (c) hydration backfill when Bulk Data has not produced a fresh export yet.

**Why not push?** Cerner has no public webhook/Kafka/SSE product. Building a "fake push" via aggressive polling against the FHIR REST API quickly hits rate limits and is harder on Cerner's database than Bulk Data's batched approach.

---

## 5. Per-resource-type mapping

| FHIR resource | Primary source (HL7 site) | Trigger / scan | Fallback (FHIR-only site) | Notes / gotchas |
|---|---|---|---|---|
| `Patient` | ADT `A01/A04/A08/A28/A31/A40` | `Hl7MessageProcessor` create/update; `A40` is merge — emit two changes (delete absorbed + update survivor) | Bulk Data `_since` + targeted `Patient` REST scan | Patient endpoint requires search filter; cannot enumerate all patients via REST. Cerner MRN is in `PID-3` (system-specific). |
| `Encounter` | ADT `A01/A02/A03/A11/A13` | `Hl7MessageProcessor`; cancel-and-replace possible on `A11` (cancel admit) followed by re-admit | Bulk Data `_since` `_type=Encounter` | Encounter status changes don't always emit ADT — bulk reconciliation catches these. |
| `Observation` (vitals, labs, results) | ORU `R01` | `Hl7MessageProcessor`; ORU corrections via `OBR-25=C` map to update with `previous_resource` | Bulk Data; if real-time required, FHIR REST scan by `category=vital-signs` or `category=laboratory` + patient | Cerner ORU may carry multiple OBR/OBX groups → multiple `Observation` resources per HL7 message. The `MapToFHIR` step must fan out and the framework's transactional outbox writes one `resource_changes` row per produced resource. |
| `DiagnosticReport` | ORU `R01` (final report rollup) | `Hl7MessageProcessor` | Bulk Data | One DiagnosticReport often corresponds to many Observations — emit both, the engine handles `_include` later. |
| `ServiceRequest` (orders) | ORM/OMG/OML `O01` | `Hl7MessageProcessor`; **cancel-and-replace is the canonical case** — `ORC-1=CA` followed by `ORC-1=NW` with new `ORC-3` | Bulk Data | The Millennium FHIR R4 API exposes `ServiceRequest` GET only (no POST in the public API) — REST cannot create, only read. |
| `MedicationRequest` (RDE/RDS pharmacy) | RDE `O11` (new pharmacy order) | `Hl7MessageProcessor` | Bulk Data | Pharmacy interface is often separately provisioned at the site — capability may be off even when ADT/ORM are on. Make the RDE handler optional via config. |
| `AllergyIntolerance` | Custom Z-segment in ADT (site-specific `ZAL`) or no HL7 emission at all | Often not in HL7 stream | Bulk Data + FHIR REST scan | Many sites do not interface allergies via HL7. Plan for FHIR-only acquisition. |
| `Condition` | PV1 / DG1 in ADT | `Hl7MessageProcessor` for problem list updates that come through DG1 | Bulk Data + FHIR REST scan | Problem list edits in PowerChart often don't emit ADT — bulk reconciliation is the realistic path. |
| `DocumentReference` | MDM `T02` | `Hl7MessageProcessor` | Bulk Data + `GET /DocumentReference/$docref` | The `$docref` operation is Cerner-supported and useful for hydration of recent notes. |
| `Procedure` | ORM (procedure orders), MDM (op notes) | partial | Bulk Data | Procedure data is fragmented across HL7 streams — bulk reconciliation is important. |

---

## 6. Configuration shape (preview)

The `config_schema.json` for the `cerner` adapter will require:

```jsonc
{
  "fhir_base_url": "https://fhir-myrecord.cerner.com/r4/{tenant-id}",
  "auth": {
    "kind": "smart-backend-services",
    "client_id": "...",
    "private_key_file": "/run/secrets/cerner_jwt_key.pem",
    "token_url": "https://authorization.cerner.com/tenants/{tenant-id}/protocols/oauth2/profiles/smart-v1/token",
    "scope": "system/Patient.read system/Encounter.read system/Observation.read system/DiagnosticReport.read system/Condition.read system/Procedure.read system/MedicationRequest.read system/AllergyIntolerance.read system/DocumentReference.read system/ServiceRequest.read"
  },
  "bulk_data": {
    "enabled": true,
    "mode": "group",                  // "group" | "patient"
    "group_id": "...",                // when mode=group
    "patient_list_source": "...",     // when mode=patient
    "cadence_seconds": 1800,
    "types": ["Patient","Encounter","Observation","DiagnosticReport","Condition","Procedure","MedicationRequest","AllergyIntolerance","DocumentReference","ServiceRequest"]
  },
  "hl7": {
    "enabled": true,
    "version": "2.5.1",
    "z_segment_map": { /* per-site mapping */ },
    "correlation_hold_window_seconds": 30
  },
  "scan": {
    "targets": [
      { "resource_type": "Observation", "cadence_seconds": 300, "query_params": "category=vital-signs" }
    ]
  }
}
```

---

## 7. Open questions / risks

1. **HL7 v2 site spec is non-public.** There is no canonical "Cerner HL7 v2 implementation guide" we can ship Z-segment lex against. Each customer receives a CernerWorks Connectivity interface specification through their professional-services engagement. **Mitigation:** Make `z_segment_map` config-driven; ship a starter map for the most common segments (`ZPV`, `ZCD`, `ZAL`) with conservative mapping; require the operator to confirm site-specific overrides before go-live. **Needs vendor confirmation per deployment.**

2. **Bulk Data rate limits / job concurrency are not publicly documented.** Cerner caps to **one in-flight export per client/tenant**, but per-day limits and per-export size limits are tenant-specific. **Mitigation:** Make the Bulk Data cadence configurable, surface a `cdc_lag_seconds` metric, and start at 30 min.

3. **`_since` semantics across resource types.** Cerner says `_since` filters resources updated after the timestamp; what counts as "updated" (chart correction? versioning? simple re-sign?) is not documented per resource. **Mitigation:** Snapshot-and-diff in the framework still catches false positives — `_since` is an optimization for cheaper exports, not a substitute for diffing.

4. **Sandbox vs. production access.** The public sandbox at `https://fhir-open.cerner.com/r4/ec2458f2-1e24-41c8-b71b-0e701af7583d` is read-only and unauthenticated; SMART Backend Services and Bulk Data require **app registration in Cerner Code Console** and per-tenant authorization through Cerner Central. **Production deployment requires an Oracle Health partnership / contract** — this is *not* something a third party can self-serve to a live tenant. Document this in the operator-facing setup guide.

5. **No FHIR Subscription / SubscriptionTopic on the roadmap.** Confirmed by absence in the 2026 Capability Statement. If Oracle adds it, the adapter should detect it (capability discovery) and prefer it; until then we own the CDC path entirely. **Needs vendor confirmation for roadmap.**

6. **Patient-level export limit.** `POST /Patient/$export` accepts at most **20,000 patient references per kick-off**. Tenants with larger subscribed cohorts must use Group export or chunk the request. **Mitigation:** Adapter config `bulk_data.mode = "group"` is the recommended default.

7. **Soarian customers are out of scope.** The adapter targets Millennium only. Operators on Soarian need a different adapter ID. Document this in the adapter README.

8. **`_typeFilter` exclusions.** `_typeFilter` is *not* supported for `Location, Organization, Patient, Practitioner, Provenance, RelatedPerson, Specimen`, and a long list of common FHIR search parameters (`_lastUpdated`, `_id`, `_include`, `_sort`, `encounter`, `identifier`, `patient`, `subject`, `date`) are unsupported inside `_typeFilter`. The adapter must validate operator-configured filters against this allowlist or runtime errors will appear far from the source. ([op-group-group_id-export-get](https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/op-group-group_id-export-get.html))

9. **DSTU2 endpoints are End of Support.** Operators still on DSTU2 must migrate before this adapter is useful. The adapter only targets R4.

10. **HL7 transport hardening.** Cerner Open Engine outbound feeds are typically TLS over MLLP with IP allow-listing; some sites still run plain MLLP behind a VPN. Both must be handled by the host's MLLP listener (vendor-neutral), not the adapter — but the operator setup guide must document the requirement.

---

## 8. References

FHIR R4 / Bulk Data:

- Millennium FHIR R4 overview: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfrap/r4_overview.html
- Millennium FHIR R4 REST endpoints (resource list): https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfrap/rest-endpoints.html
- Bulk Data Access overview: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/index.html
- Bulk Data REST endpoints: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/rest-endpoints.html
- Group `$export` parameters: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/op-group-group_id-export-get.html
- Patient `$export` parameters: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfbda/op-patient-export-post.html

Authorization:

- Cerner / Oracle Health Authorization Framework: https://docs.oracle.com/en/industries/health/millennium-platform-apis/authorization-framework/
- SMART Application Developer Overview: https://docs.oracle.com/en/industries/health/millennium-platform-apis/smart-developer-overview/

Other product surfaces:

- EHR (proprietary) APIs: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mcfap/index.html
- EHR REST endpoints catalog: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mcfap/rest-endpoints.html
- Public Endpoints — Patient apps: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfpea/index.html
- Public Endpoints — Provider apps: https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfpeb/index.html
- FHIR DSTU 2 (deprecated): https://docs.oracle.com/en/industries/health/millennium-platform-apis/mfdap/index.html
- Oracle Health docs root: https://docs.oracle.com/en/industries/health/index.html
- API product index: https://docs.oracle.com/en/industries/health/millennium-platform-apis/index.html

Standards:

- HL7 v2-to-FHIR Implementation Guide: https://hl7.org/fhir/uv/v2mappings/
- US Core 6.1: https://hl7.org/fhir/us/core/STU6.1/
- FHIR Bulk Data Access IG: https://hl7.org/fhir/uv/bulkdata/

In-repo:

- [overview.md](../../high-level-design/overview.md)
- [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md)
- [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md)
- [adapter-authoring-guide.md](../../adapter-authoring-guide.md)
- [adapters/cerner/cerner.go](../../../adapters/cerner/cerner.go) (current scaffold)
