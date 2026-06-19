# MEDITECH Adapter — High-Level Design

**Summary.** MEDITECH is a multi-platform inpatient EHR vendor whose flagship modern product is **Expanse** (web-based, 2018+), with two predecessor platforms (**Magic**, **Client/Server 6.x**) still in active production. Expanse exposes a **FHIR R4 read-only REST API** aligned with US Core for ONC certification, but ships **no native `Subscription` / `SubscriptionTopic`, no webhooks, and no public change-data-capture stream**. The MEDITECH adapter must therefore be **HL7 v2 dominant for real-time signal** (the integration model every MEDITECH site already runs through their interface engine), with **FHIR R4 polling** as the reconciliation fallback. **Bulk Data `$export` and Traverse Exchange are not third-party-consumable change feeds** as of 2026.

**Reader's prerequisites.** [overview.md](../../high-level-design/overview.md), [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md), [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md), [adapter-authoring-guide.md](../../adapter-authoring-guide.md). The Cerner, Athena, and eClinicalWorks scaffolds in [`adapters/cerner/cerner.go`](../../../adapters/cerner/cerner.go), [`adapters/athena/athena.go`](../../../adapters/athena/athena.go), and [`adapters/eclinicalworks/eclinicalworks.go`](../../../adapters/eclinicalworks/eclinicalworks.go) show the manifest shape this adapter will follow. Current scaffold: [`adapters/meditech/meditech.go`](../../../adapters/meditech/meditech.go).

---

## 1. Vendor landscape

MEDITECH (Medical Information Technology, Inc., Westwood MA, founded 1969) is the third-largest U.S. inpatient EHR by acute-hospital share, dominant in **community hospitals, critical-access hospitals, and mid-size IDNs**. 2019 revenue was $493.8M (per Wikipedia); the company is privately held and does not publish certified-customer counts. Three software platforms are simultaneously in production, and that fact materially shapes adapter design — a single `meditech` adapter ID has to deal with all three even though the FHIR API only exists on one.

- **MAGIC** — adopted 1982. Server-centric architecture, character-mode "dumb terminal" clients, MUMPS-style scripting language. **No FHIR API.** HL7 v2 over MLLP is the only structured outbound channel. Still in production at a long tail of small/critical-access sites; MEDITECH continues to support it but is migrating customers off. Source: <https://en.wikipedia.org/wiki/MEDITECH>.
- **Client/Server 6.x ("C/S 6.x")** — introduced 1994, version 6.0 in 2006. Windows-only fat-client architecture. **Limited FHIR API** — some C/S sites have ONC-certified FHIR R4 endpoints retrofit; coverage is narrower than Expanse. HL7 v2 is the dominant integration channel. Many community hospitals are still on this platform. Source: <https://en.wikipedia.org/wiki/MEDITECH>.
- **Expanse** — released February 27, 2018; "mobile, web-based EHR." Modern web-tier architecture, browser/mobile clients, the platform MEDITECH actively sells and certifies for ONC. **Full FHIR R4 read API** (per developer portal). HL7 v2 is still the lingua franca for real-time integration with downstream systems.
- **Expanse Patient Care** — January 2020, nursing-focused module of Expanse (not a separate platform).
- **Traverse Exchange** — MEDITECH's TEFCA / health-information-exchange product (Carequality / Commonwell-adjacent). It is a **HIE/document-query gateway**, not a third-party-consumable change feed. The public marketing pages were not reachable for this brief (404 redirect on home.meditech.com); confirm scope per deployment. **Not a CDC source for this adapter.**
- **Greenfield** — MEDITECH's developer-facing brand and sandbox program. The current scaffold's TODO references "MEDITECH Greenfield FHIR API," but as of 2026 the production developer portal has been folded into `home.meditech.com/en/d/restapiresources/` ("RESTful API Resources"). "Greenfield" historically referred to the legacy sandbox and SDK program.

**Hosting models.** MEDITECH supports three:
1. **Customer-hosted on-prem** — historically the dominant model; many community hospitals run MEDITECH on their own metal.
2. **MaaS — MEDITECH-as-a-Service** — private-cloud hosted by MEDITECH (Foundation MaaS, the modern hosted offering). Expanse is the typical MaaS target.
3. **Public cloud (AWS / Google Cloud)** — Expanse-on-AWS and Expanse-on-Google-Cloud are MEDITECH-managed deployments on the customer's hyperscaler tenancy. Same FHIR API surface, same HL7 capabilities.

The hosting model **does not change the API surface** but does change connectivity: on-prem requires a site-to-site VPN / MLLP listener inside the hospital network; MaaS / cloud-hosted sites typically expose HTTPS FHIR endpoints publicly (with OAuth) and route HL7 over a customer-managed VPN to a MEDITECH-side interface engine. The adapter cares about the difference for HL7 transport configuration, not for translation logic.

Sources: <https://en.wikipedia.org/wiki/MEDITECH>, <https://home.meditech.com/en/d/restapiresources/>, MEDITECH press releases on Expanse-on-AWS and Expanse-on-Google-Cloud (URLs were 404 at brief time; **needs vendor confirmation** for current marketing pages).

---

## 2. API surface — change-data-capture inventory

Every CDC-relevant interface MEDITECH exposes to a third-party integrator. The framing matters: MEDITECH is, like every other major U.S. EHR, **HL7 v2 first** for site-to-site integration; FHIR is a 21st Century Cures / ONC-certification compliance surface, not an event delivery surface.

| API / Interface | Protocol | FHIR version | Change-detection model | Auth | GA status | Source |
|---|---|---|---|---|---|---|
| **Expanse FHIR R4 Read API** | HTTPS REST | R4 (4.0.1), US Core 3.1.1 (USCDI v1) and **US Core 6.1.0 (USCDI v3)** for ONC-certified Expanse releases | Pull only (REST search) | SMART on FHIR (EHR Launch / Standalone Launch / Backend Services) | GA on Expanse; partial on C/S 6.x; absent on Magic | <https://home.meditech.com/en/d/restapiresources/> |
| **HL7 v2 over MLLP (Expanse / C/S / Magic)** | TCP/MLLP (often TLS) | n/a (HL7 v2.x — typically v2.5.1, sites also run v2.3 / v2.4) | Push (real-time triggers) | Network-level (VPN + IP allow-list) | GA on all three platforms — **the only universal CDC surface** | MEDITECH-published interface specs delivered per-site; "NPR Toolbox" historical reference |
| **MEDITECH Data Repository (DR)** | Microsoft SQL Server (replicated) | n/a | Pull (SQL queries against a near-real-time replica of the operational DB) | SQL Server auth (network-bound) | GA — **operational reporting use, not a third-party event stream** | MEDITECH "Data Repository" (DR) product. **Not used by this adapter.** |
| **NPR Reports** | MUMPS / NPR scripting | n/a | Pull (batch report extracts) | Internal/MEDITECH-side | Internal scripting language; not a third-party integration surface | Historical — used for one-off extract jobs by MEDITECH consultants |
| **Traverse Exchange** | HTTPS (Carequality / Commonwell IHE-XCA / FHIR query gateway) | R4 (for FHIR portions) | Document-query / on-demand | Federated (TEFCA QHIN / IHE) | GA in HIE federation context | <https://ehr.meditech.com/> (page reachable was 404 at brief time; **needs vendor confirmation**) |
| **FHIR Bulk Data `$export`** | HTTPS REST async (Bulk Data IG) | R4 NDJSON | Pull (`_since` if supported) | SMART Backend Services | **Needs vendor confirmation** — MEDITECH's ONC certification artifact for Cures Act g(10) implies a Bulk Export endpoint, but the endpoint pattern, supported `_since` semantics, and tenant access process are not publicly documented | ONC g(10) / Cures Act certification (general); MEDITECH-specific docs gated behind developer registration |
| **`Subscription` / `SubscriptionTopic` (R4B / R5)** | — | — | — | — | **Not supported.** Absent from MEDITECH's published Capability Statement and developer portal as of 2026. No public roadmap announcement. | Confirmed by absence in <https://home.meditech.com/en/d/restapiresources/>; **needs vendor confirmation** for any non-public roadmap. |
| **Webhooks / SSE / Kafka / event streams** | — | — | — | — | **Not offered** as a third-party-consumable product. | (no public doc — confirmed by absence in API index) |
| **Greenfield Sandbox** | HTTPS REST (R4) | R4 | Pull only | SMART | GA — developer/sandbox tier | <https://home.meditech.com/en/d/restapiresources/> (developer registration required) |

Two observations are load-bearing for the adapter design:

1. **HL7 v2 is the only universal real-time signal across all three MEDITECH platforms.** Expanse adds FHIR R4 read for compliance, but MEDITECH sites uniformly operate with an HL7 v2 outbound feed for downstream integration (lab systems, registration, billing, ancillary apps). The adapter's HL7 path is therefore not optional — it is the production CDC mechanism.
2. **There is no MEDITECH-supplied push CDC product.** No webhooks, no SSE, no Kafka, no native FHIR `Subscription`. The realistic non-HL7 path is FHIR REST polling with snapshot-and-diff, and (where available) Bulk Data `$export` for incremental batch reconciliation.

A note on `_lastUpdated`: MEDITECH's published Capability Statement (gated behind developer registration) is reported by integrators to advertise `_lastUpdated` on US Core resources, but accuracy across resource types and editing pathways has not been audited by us. Per the project's standing decision (`domains/ehr-adapter.md` — "We do not rely on `_lastUpdated`"), the FHIR Scan Runner uses snapshot-and-diff as the source of truth and treats `_lastUpdated` only as a query-narrowing hint.

---

## 3. Mapping FHIR Subscription requests to MEDITECH CDC

How each Stage 1 sub-component maps onto MEDITECH's API surface.

### 3.1 `Hl7MessageProcessor` — primary real-time path

**Capability declared:** `HL7Processor: true`.

Every MEDITECH platform (Magic, C/S 6.x, Expanse) ships HL7 v2 outbound feeds through MEDITECH's own interface engine (Expanse Integration Engine / older "NPR Toolbox" interface scripts on Magic and C/S). The site provisions an outbound MLLP feed to the host's MLLP listener; the host writes raw bytes to `hl7_message_queue` and ACKs MEDITECH; the adapter's processor claims rows and translates.

**Trigger events the MEDITECH adapter must handle** (HL7 v2.5.1 unless otherwise noted; MEDITECH sites often run v2.3 or v2.4):

- **ADT** — `A01` admit, `A02` transfer, `A03` discharge, `A04` register outpatient, `A05` pre-admit, `A08` update patient info, `A11` cancel admit, `A13` cancel discharge, `A28`/`A29`/`A31` add/update/delete person, `A40` merge patients. Map to `Patient` and `Encounter`.
- **ORM** — `O01` orders. Cancel/replace via `ORC-1` (`NW`/`CA`/`XO`/`RP`). Map to `ServiceRequest` (and `MedicationRequest` for pharmacy via `RDE`).
- **ORU** — `R01` results (lab/radiology/cardiology). Map to `Observation` and `DiagnosticReport`. ORU corrections via `OBR-25=C` and `OBX-11`.
- **MDM** — `T02` document notification, `T08`/`T11` replace/cancel. Map to `DocumentReference`.
- **SIU** — scheduling, when sites use MEDITECH Scheduling. `S12`/`S13`/`S14`/`S15`/`S26` etc. Map to `Appointment`.
- **DFT** — `P03` financial transaction (less commonly subscribed; gated by config).

**Z-segments.** MEDITECH HL7 v2 specs are **per-platform and per-site**. The historical "NPR Toolbox" specs (Magic / C/S) and the modern "Expanse Integration Engine" specs are delivered to interface partners through a MEDITECH professional-services engagement; **they are not freely indexed on the public web**. Z-segment names commonly observed at MEDITECH sites include:

- `ZPV` — provider visit information (similar role to Cerner's `ZPV`)
- `ZBL` — billing-related segments
- `ZIN` — insurance/coverage
- `ZGP` — provider-group identifiers
- Site-customized Z-segments for facility-specific extensions

**The adapter must therefore be configurable.** `lex` extends the standard tokenizer with Z-segment shapes, but the *meaning* is per-site. The `config_schema.json` for the `meditech` adapter must expose a `z_segment_map` so operators can declare which Z-segment fields populate which FHIR extensions. The `meditech.go` scaffold already calls this out in its TODO comment ("Lex must branch on MSH-3/MSH-4 sender app/facility before parsing Z-segments") — this is correct: a single `meditech` adapter binary serves Magic, C/S, and Expanse sites, and the lex must dispatch on MSH-3 (sending application) / MSH-4 (sending facility) to apply the right Z-segment map. **Needs vendor confirmation per deployment** for the exact Z-segment names and field positions in use at any given site.

**Cancel-and-replace.** MEDITECH's pattern follows the HL7 v2 standard: an `ORM^O01` with `ORC-1=CA` followed by a fresh `ORM^O01` with `ORC-1=NW` and a new `ORC-3` filler order number. The correlation key the adapter returns from `Classify` should be `ORC-2` (placer order number) when stable, falling back to a composite of `(PID-3 patient ID, ORC-4 order group, OBR-3 universal service ID timestamp)`. Default `CorrelationHoldWindow()` of 30s is appropriate; MEDITECH sites with slow interface engine queues may need a larger window. For document replace (MDM `T08`/`T11`), `TXA-12` (parent document number) is the correlation key.

`MapToFHIR` produces the post-translation R4 resource using the [HL7 v2-to-FHIR Implementation Guide](https://hl7.org/fhir/uv/v2mappings/) plus MEDITECH-specific overrides for Z-segments and PID identifier types (MEDITECH MRNs typically live in `PID-3` with assigning-authority `MR`). The framework's `Validate` default is sufficient at first; over time, validate against [US Core 6.1](https://hl7.org/fhir/us/core/STU6.1/) (Expanse's ONC-certified profile target).

### 3.2 `FhirScanRunner` — fallback / Expanse-only or compliance-driven sites

**Capability declared:** `FhirScanRunner: true` (configurable — operators with HL7 v2 may disable it; Magic-only sites cannot use it at all).

The framework calls `RunScan` on cadence; the adapter authenticates via SMART Backend Services (JWT bearer; client credentials grant against MEDITECH's tenant token endpoint, the URL pattern of which is per-deployment and gated behind developer registration — **needs vendor confirmation per deployment**). Per the project's standing rule, snapshot-and-diff against `adapter_state` is the change-detection mechanism; `_lastUpdated` is only a narrowing hint.

**`scan_plan()` returns `(resource_type, cadence, query_params)`** for resources active subscriptions need. Cadences must respect MEDITECH's per-tenant rate limits (numeric ceilings are not publicly documented — **needs vendor confirmation**). Suggested defaults:

| Resource type | Cadence (default) | Query | Notes |
|---|---|---|---|
| `Patient` | 6 h | `?_count=200&_lastUpdated=gt{since}` | Demographic resync; backstop for missed ADT `A28`/`A29`/`A31`. Patient endpoint typically requires a search filter; cannot enumerate all patients. |
| `Encounter` | 15 m | `?status=in-progress,arrived,planned` | Real-time path is ADT; this is a backstop for facilities without ADT export. |
| `Observation` (vitals) | 5 m | `?category=vital-signs&date=ge{NOW-1d}` | Vitals don't always emit ORU at MEDITECH sites — FHIR scan is the most reliable path. |
| `Observation` (labs) | 30 m | `?category=laboratory&date=ge{NOW-7d}` | Backstop for missed ORU. |
| `DiagnosticReport` | 30 m | `?category=LAB&date=ge{NOW-7d}` | Backstop for missed ORU. |
| `MedicationRequest` | 1 h | `?status=active` | Pharmacy interface availability varies — pull is the safe default. |
| `Condition`, `Procedure`, `AllergyIntolerance` | 30 m | by patient (when subscription scope is patient-bound) | Problem list and allergy edits often don't emit HL7. |
| `DocumentReference` | 1 h | `?date=ge{NOW-7d}` | Backstop for MDM. |

`Normalize` is mostly identity for R4. `ContentHash` should strip `meta.lastUpdated`, `meta.versionId`, and any MEDITECH-specific `meta.tag` entries before hashing — MEDITECH may refresh these without changing the resource body (**needs vendor confirmation** that this is the case; behavior matches the conservative pattern observed at Cerner).

### 3.3 `VendorAPIClient` — inert at launch, reserved for future Bulk Data

**Capability declared:** `VendorAPIClient: false` at launch.

MEDITECH offers no proprietary push CDC product. The closest thing to a vendor-managed batch feed is **FHIR Bulk Data `$export`**, which is required for ONC g(10) certification on Expanse, but: (a) the public documentation does not confirm `_since` support, `_typeFilter` semantics, or per-tenant concurrency limits; (b) tenant access is gated through a partner / developer registration process that is not self-serve; (c) the endpoint URL pattern is per-tenant and not published.

**Recommendation:** ship the adapter with `VendorAPIClient: false` and `BuildVendorAPIClient` returning `nil`. After production validation against an Expanse tenant confirms Bulk Data semantics, add a `VendorAPIClient` implementation that polls `Group/$export` with `_since`, parallel to the Cerner adapter's design (see [`adapters/cerner/cerner.go`](../../../adapters/cerner/cerner.go) and the Cerner HLD §3.3). This is a future enhancement, not a launch blocker.

The Data Repository (SQL replica) is **not** suitable for this adapter — it is a customer-side reporting database, requires SQL Server credentials inside the hospital network, and is not part of MEDITECH's developer-program API surface. Sites that want to drive CDC off DR-replicated tables typically build their own ETL outside the FHIR-subscriptions service.

### 3.4 `HydrationService` — required for `full-resource` subscriptions

**Capability declared:** `HydrationService: true`.

`Fetch(reference)` issues `GET /{ResourceType}/{id}` against the Expanse FHIR R4 API using the same SMART Backend Services token as the scan runner. The framework caches per-replica with default TTL 60s and coalesces concurrent calls. Override `CacheTTL()` to 30s for `Encounter` and `Observation` in clinical-staleness-sensitive deployments.

For Magic-only sites (no FHIR API), `HydrationService` returns `not-implemented` — those deployments cannot serve `full-resource` subscriptions and the operator's subscription topic must be limited to `id-only` notifications. **Document this constraint in the operator-facing setup guide.**

---

## 4. Recommended architecture — primary path + fallback

**Primary CDC path: HL7 v2 over MLLP** at every MEDITECH site, regardless of platform (Magic, C/S, or Expanse). It is the only universal real-time signal MEDITECH offers, the trigger events map cleanly to FHIR resource changes, the framework's MLLP listener handles persistence-then-ACK durability, and every MEDITECH site already operates an HL7 outbound feed for downstream integration. Provisioning a feed for the subscription service is an interface-engineering project, not a new product purchase.

**Fallback CDC path: FHIR R4 polling via `FhirScanRunner`** on Expanse (and on C/S 6.x where the FHIR retrofit is enabled). Catches resources that don't emit HL7 trigger events (problem-list edits, allergy edits, vitals at sites with no ORU vital-signs path) and acts as a daily reconciliation. Cadence is per-resource and tunable by the operator; default cadences in §3.2 are conservative.

**Future enhancement: Bulk Data `$export`** on Expanse once the tenant access process and `_since` semantics are confirmed in production. Until then, `VendorAPIClient` is inert.

**Why not push?** MEDITECH has no public webhook / Kafka / SSE / `Subscription` product. Building "fake push" via aggressive FHIR REST polling hits per-tenant rate limits and stresses MEDITECH's database; it is not a viable substitute for HL7 v2.

**Magic-only sites** run HL7 v2 only. The adapter declares `FhirScanRunner: false` and `HydrationService: false` in the runtime manifest when the operator's `meditech.platform` config is `magic`. **id-only subscriptions only** at those sites.

---

## 5. Per-resource-type mapping

| FHIR resource | Primary source (HL7 site) | Trigger / scan | Fallback (Expanse, FHIR-only) | Notes / gotchas |
|---|---|---|---|---|
| `Patient` | ADT `A01/A04/A08/A28/A29/A31/A40` | `Hl7MessageProcessor` create/update; `A40` is merge — emit two changes (delete absorbed + update survivor) | FHIR scan `Patient?_lastUpdated=gt{since}` | Patient endpoint requires a search filter; cannot enumerate all patients. MEDITECH MRN is in `PID-3` with assigning-authority `MR`. |
| `Encounter` | ADT `A01/A02/A03/A11/A13` | `Hl7MessageProcessor`; cancel-and-replace possible on `A11` (cancel admit) followed by re-admit | FHIR scan `Encounter?status=...` | Encounter status changes (ED triage, transfer to inpatient) don't always emit ADT — FHIR reconciliation catches these. |
| `Observation` (vitals) | Often **not** in HL7 stream at MEDITECH sites | rarely ORU | FHIR scan `Observation?category=vital-signs` (5 min cadence) | MEDITECH sites commonly do not interface vitals via HL7. Plan for FHIR-only acquisition. |
| `Observation` (labs) | ORU `R01` | `Hl7MessageProcessor`; ORU corrections via `OBR-25=C` map to update with `previous_resource` | FHIR scan `Observation?category=laboratory` (30 min) | MEDITECH ORU may carry multiple OBR/OBX groups → multiple `Observation` resources per HL7 message. The `MapToFHIR` step must fan out and the framework's transactional outbox writes one `resource_changes` row per produced resource. |
| `DiagnosticReport` | ORU `R01` (final report rollup) | `Hl7MessageProcessor` | FHIR scan `DiagnosticReport?category=LAB` (30 min) | One DiagnosticReport often corresponds to many Observations — emit both, the engine handles `_include` later. |
| `ServiceRequest` (orders) | ORM `O01` | `Hl7MessageProcessor`; **cancel-and-replace is the canonical case** — `ORC-1=CA` followed by `ORC-1=NW` with new `ORC-3` | FHIR scan `ServiceRequest?status=active` (1 h) | The Expanse FHIR API exposes `ServiceRequest` GET only (no POST in the public API). REST cannot create, only read. |
| `MedicationRequest` (RDE/RDS pharmacy) | RDE `O11` (new pharmacy order) | `Hl7MessageProcessor` | FHIR scan `MedicationRequest?status=active` (1 h) | Pharmacy interface is often separately provisioned at the site — capability may be off even when ADT/ORM are on. Make the RDE handler optional via config. |
| `AllergyIntolerance` | Custom Z-segment in ADT (site-specific) or no HL7 emission at all | Often not in HL7 stream | FHIR scan `AllergyIntolerance?patient={id}` (30 min) | Many MEDITECH sites do not interface allergies via HL7. FHIR-only acquisition is the realistic path. |
| `Condition` | PV1 / DG1 in ADT | partial | FHIR scan `Condition?patient={id}` (30 min) | Problem-list edits in Expanse PCS often don't emit ADT — FHIR reconciliation is the realistic path. |
| `DocumentReference` | MDM `T02` / `T08` / `T11` | `Hl7MessageProcessor` (paired by `TXA-12`) | FHIR scan `DocumentReference?date=ge{NOW-7d}` (1 h) | MEDITECH document interfaces are widely deployed; expect this path to be dominant. |
| `Procedure` | ORM (procedure orders), MDM (op notes) | partial | FHIR scan `Procedure?patient={id}` (30 min) | Procedure data is fragmented across HL7 streams — FHIR reconciliation matters. |
| `Appointment` | SIU `S12`/`S13`/`S14`/`S15`/`S26` | `Hl7MessageProcessor` (when site uses MEDITECH Scheduling) | FHIR scan `Appointment?date=ge{NOW}` (15 min) | Sites using third-party scheduling won't emit SIU; FHIR scan is the only path. |

---

## 6. Configuration shape (preview)

The `config_schema.json` for the `meditech` adapter will require:

```jsonc
{
  "platform": "expanse",                    // "magic" | "cs6" | "expanse"
  "fhir_base_url": "https://{tenant}.meditech.com/fhir/r4",  // pattern needs vendor confirmation per tenant
  "auth": {
    "kind": "smart-backend-services",
    "client_id": "...",
    "private_key_file": "/run/secrets/meditech_jwt_key.pem",
    "token_url": "https://{tenant}.meditech.com/oauth2/token",  // per-tenant; needs vendor confirmation
    "scope": "system/Patient.read system/Encounter.read system/Observation.read system/DiagnosticReport.read system/Condition.read system/Procedure.read system/MedicationRequest.read system/AllergyIntolerance.read system/DocumentReference.read system/ServiceRequest.read system/Appointment.read"
  },
  "hl7": {
    "enabled": true,
    "version": "2.5.1",
    "sender_app_dispatch": {
      // dispatch on MSH-3 / MSH-4 to apply the right Z-segment map
      "MEDITECH-EXPANSE-IE": "expanse_zmap",
      "MEDITECH-MAGIC-NPR":  "magic_zmap",
      "MEDITECH-CS-NPR":     "cs6_zmap"
    },
    "z_segment_maps": {
      "expanse_zmap": { /* per-site mapping */ },
      "magic_zmap":   { /* per-site mapping */ },
      "cs6_zmap":     { /* per-site mapping */ }
    },
    "correlation_hold_window_seconds": 30
  },
  "scan": {
    "enabled": true,
    "targets": [
      { "resource_type": "Observation",     "cadence_seconds": 300,  "query_params": "category=vital-signs" },
      { "resource_type": "Observation",     "cadence_seconds": 1800, "query_params": "category=laboratory" },
      { "resource_type": "Encounter",       "cadence_seconds": 900,  "query_params": "status=in-progress,arrived,planned" },
      { "resource_type": "DocumentReference","cadence_seconds": 3600,"query_params": "date=ge{NOW-7d}" }
    ]
  }
}
```

When `platform: "magic"`, the runtime should refuse to instantiate `FhirScanRunner` and `HydrationService` (they cannot exist without a FHIR API), and the operator's subscription topics are limited to `id-only` shapes.

---

## 7. Open questions / risks

1. **HL7 v2 site spec is non-public and platform-divergent.** MEDITECH does not publish a canonical HL7 v2 implementation guide; each customer receives interface specifications through MEDITECH professional services. Z-segment names and field positions differ across Magic, C/S 6.x, and Expanse, and across customer customizations. **Mitigation:** Make `z_segment_maps` config-driven; ship starter maps for the most common segments (`ZPV`, `ZBL`, `ZIN`, `ZGP`); dispatch on `MSH-3`/`MSH-4` to pick the right map; require operator-confirmed site overrides before go-live. **Needs vendor confirmation per deployment.**

2. **FHIR API access is gated.** MEDITECH's developer portal (<https://home.meditech.com/en/d/restapiresources/>) requires registration; sandbox is available, but production access requires the customer hospital to grant the integrating app access through a MEDITECH-side process. The **endpoint URL pattern, token URL, and tenant ID format are per-deployment** — there is no canonical `https://fhir.meditech.com/` style URL. **Document this in the operator-facing setup guide.**

3. **Bulk Data `$export` semantics are not publicly documented.** Required for ONC g(10) certification, so it must exist on Expanse, but `_since` accuracy, `_typeFilter` support, per-tenant concurrency, and operational frequency are not in the public docs. **Mitigation:** ship the adapter without `VendorAPIClient` at launch; revisit after a production tenant validates Bulk Data behavior.

4. **No FHIR Subscription / SubscriptionTopic on the roadmap (as of brief time).** Confirmed by absence in the public Capability Statement and developer portal. ONC's HTI-2 / TEFCA expectations may eventually push MEDITECH to add native Subscription support; if so, the adapter should detect it via capability discovery and prefer it. **Needs vendor confirmation for roadmap.**

5. **`_lastUpdated` accuracy is unaudited.** Reportedly advertised on Expanse's Capability Statement, but per-resource accuracy across editing pathways (chart correction, version increment, profile re-tag) has not been tested. **Mitigation:** snapshot-and-diff in the framework remains the source of truth — `_lastUpdated` is only a query-narrowing hint.

6. **Magic-only sites are functionally limited.** No FHIR API → no `FhirScanRunner` and no `HydrationService` → only `id-only` subscriptions. The adapter must refuse to start in `expanse` mode at a Magic site; operators must select `platform: "magic"` explicitly. The deployment's subscription-topic catalog must reflect this constraint.

7. **C/S 6.x FHIR coverage is partial.** Some C/S 6.x sites have ONC-certified FHIR endpoints, some do not. The `meditech` adapter cannot assume a uniform FHIR surface across all C/S customers. **Mitigation:** when `platform: "cs6"`, the operator must declare which resources have FHIR endpoints; `scan_plan()` filters to those.

8. **Traverse Exchange is not a CDC source.** It is a TEFCA / IHE document-query gateway, used for cross-organization HIE document retrieval — not a real-time event feed for the subscribing organization's own patients. **Document explicitly** so operators do not propose Traverse as a substitute for HL7 or FHIR scan.

9. **Data Repository (DR) is out of scope.** It is a customer-side SQL Server replica, not a third-party API surface. Sites that want to drive CDC off DR-replicated tables must build their own ETL outside this service. **Document explicitly** so operators do not propose DR as a CDC source.

10. **HL7 transport hardening varies by hosting model.** On-prem MEDITECH deployments require the host's MLLP listener to live inside the hospital network or behind a site-to-site VPN; MaaS / cloud-hosted deployments route HL7 over a customer-managed VPN to MEDITECH's interface engine. Both must be handled by the host's MLLP listener (vendor-neutral), not the adapter — but the operator setup guide must document both options.

11. **No FHIR `Subscription` test fixtures.** Because MEDITECH does not implement the resource, there are no MEDITECH-flavored subscription request fixtures we can pin against. Conformance testing is limited to `adapters/default`'s suite plus MEDITECH-specific HL7 v2 fixtures the operator's interface team supplies.

---

## 8. References

MEDITECH developer / API surface:

- RESTful API Resources (developer portal root): <https://home.meditech.com/en/d/restapiresources/>
- Expanse product page: <https://ehr.meditech.com/> (specific pages 404'd at brief time — **needs vendor confirmation** for current marketing URLs)
- MEDITECH on Wikipedia: <https://en.wikipedia.org/wiki/MEDITECH>

Standards:

- HL7 v2-to-FHIR Implementation Guide: <https://hl7.org/fhir/uv/v2mappings/>
- US Core 3.1.1 (USCDI v1): <https://hl7.org/fhir/us/core/STU3.1.1/>
- US Core 6.1.0 (USCDI v3): <https://hl7.org/fhir/us/core/STU6.1/>
- FHIR Bulk Data Access IG: <https://hl7.org/fhir/uv/bulkdata/>
- SMART App Launch Framework: <https://hl7.org/fhir/smart-app-launch/>
- ONC Cures Act Final Rule g(10) certification criteria: <https://www.healthit.gov/topic/certification-ehrs/2015-edition-cures-update>

In-repo:

- [overview.md](../../high-level-design/overview.md)
- [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md)
- [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md)
- [adapter-authoring-guide.md](../../adapter-authoring-guide.md)
- Peer adapter HLDs:
  - [cerner/high-level-design.md](../cerner/high-level-design.md)
  - [athena/high-level-design.md](../athena/high-level-design.md)
  - [eclinicalworks/high-level-design.md](../eclinicalworks/high-level-design.md)
- Current scaffold: [`adapters/meditech/meditech.go`](../../../adapters/meditech/meditech.go)
