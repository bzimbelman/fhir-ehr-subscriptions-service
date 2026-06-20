# eClinicalWorks (eCW) Adapter — High-Level Design

**Summary.** eClinicalWorks publishes a FHIR R4 read/write API (US Core 3.1.1 / 6.1.0) and an HL7 v2 lab interface, but offers **no native push, webhook, or `Subscription` mechanism**. The eCW adapter will therefore be **HL7 v2 dominant for real-time signal** and **FHIR R4 polling (with bulk-data `$export` for large pulls) as the cold-path scanner**, with hydration via FHIR R4 read.

**Reader's prerequisites.** [overview.md](../../high-level-design/overview.md), [domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md), [contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md), [adapter-authoring-guide.md](../../adapter-authoring-guide.md). The Cerner and Athena scaffolds in [`adapters/cerner/cerner.go`](https://github.com/bzimbelman/fhir-ehr-subscriptions-service/blob/main/adapters/cerner/cerner.go) and [`adapters/athena/athena.go`](https://github.com/bzimbelman/fhir-ehr-subscriptions-service/blob/main/adapters/athena/athena.go) show the manifest shape this adapter will follow.

---

## 1. Vendor landscape

eClinicalWorks ("eCW") is the largest privately-held ambulatory EHR vendor in the U.S. (~150,000 providers, ~850,000 medical professionals as the company itself reports). Relevant for adapter scope:

- **Product lines.**
  - **eClinicalWorks EHR** — the core ambulatory EHR/practice-management product (the only product the adapter targets).
  - **healow** — patient engagement / patient-facing apps; has a separate FHIR developer portal for patient apps. Out of scope for an EHR-side adapter.
  - **PRISMA / PRISMANet** — eCW's TEFCA / health-information-exchange offering. Out of scope.
  - **EHI Export** — bulk export of all electronic health information for a patient or population. Adjacent to bulk-data FHIR `$export`, used for portability rather than CDC.
  - Source: <https://www.eclinicalworks.com/products-services/interoperability/>.

- **Market segment.** Ambulatory and small-to-mid-size practices, multi-specialty groups, urgent care, federally qualified health centers. Not significant in inpatient / large IDN markets where Epic and Oracle Health dominate.

- **Hosting models.** Predominantly **eCW Cloud** (vendor-hosted multi-tenant SaaS). On-prem and private-cloud installations exist but the FHIR API and developer programs target the cloud-hosted footprint. Per-practice base URLs imply tenant isolation inside a shared platform.

- **Reputation for openness.** Mixed. eCW has shipped a public FHIR developer portal (<https://fhir.eclinicalworks.com/ecwopendev>) to satisfy ONC Cures Act / 21st Century Cures requirements, and a self-serve sandbox is available without a partner agreement. Production access is gated by **developer registration, BAA, and a recurring per-practice fee** (commonly cited as **$50/month per practice** by integrators — see Section 7). Write APIs require **separate per-resource contracts**. Treat the adapter as a "FHIR-conformant but transactional-cost-aware" target.

---

## 2. API surface — change-data-capture inventory

| API / Interface | Protocol | FHIR version | Change-detection model | Auth | GA status | Source URL |
|---|---|---|---|---|---|---|
| **eCW FHIR R4 Read API (provider-facing)** | HTTPS REST | R4 / US Core 3.1.1 (USCDI v1) and US Core 6.1.0 (USCDI v3, eCW 11.52.305C+) | **Pull only** — synchronous read/search | SMART on FHIR (EHR Launch, Standalone Launch) with `launch`, `launch/patient`, `openid`, `fhirUser`, `offline_access`, `online_access` | GA (after registration + BAA) | <https://fhir.eclinicalworks.com/ecwopendev/documentation> |
| **eCW FHIR R4 Write API (Create/Update/Delete)** | HTTPS REST | R4 | n/a (subscriber-side writes; not used by the adapter) | SMART on FHIR | GA but **per-resource contracts required** | <https://fhir.eclinicalworks.com/ecwopendev/documentation> |
| **FHIR Bulk Data `$export` (Group-scoped)** | HTTPS REST async | R4 (HL7 Bulk Data IG) | Pull-with-`_since`; NDJSON delivery | SMART Backend Services (`client_credentials`, system scopes) | GA — requires explicit "Bulk Access" authorization on the developer platform plus eCW org grant | <https://fhir.eclinicalworks.com/ecwopendev/documentation/getting-started/backend/patient-access> |
| **Per-practice FHIR endpoints directory** | HTTPS (HTML/CSV download) | n/a | Discovery only | None for read; download offered by portal | GA | <https://fhir.eclinicalworks.com/ecwopendev/fhir-endpoints> |
| **HL7 v2 ORM (Order)** | MLLP (typically) | v2.x (commonly v2.5.1) | **Push** (real-time) | Network-level (private link / VPN); no application auth | GA — site-by-site interface engineering project | "eCW HL7 Reports Inbound Specifications ORU and MDM" (Aug 2022) PDF; "eCW HL7 Lab Results Specifications v2.3" (Aug 2022) — published by eClinicalWorks |
| **HL7 v2 ORU (Result)** | MLLP | v2.x | Push | Network-level | GA | as above |
| **HL7 v2 ADT (Patient demographic / encounter)** | MLLP | v2.x | Push | Network-level | GA but availability varies by deployment; some practices configure ADT only for downstream HIE / billing | Confirmed by integrator references; eCW does not publish a public ADT spec in the same way it does the lab specs — **needs vendor confirmation per facility** |
| **HL7 v2 MDM (Document)** | MLLP | v2.x | Push | Network-level | GA | "eCW HL7 Reports Inbound Specifications ORU and MDM" PDF |
| **`Subscription` / `SubscriptionTopic` (R4B / R5)** | — | — | — | — | **Not supported.** No reference in the public developer portal; no industry sources document support. | n/a |
| **Webhooks / event-driven push** | — | — | — | — | **Not supported.** Integrator references confirm "no webhooks or event-driven integration options available." | <https://www.patientlogistics.com/blog-posts/eclinicalworks-api-documentation> |
| **Proprietary change-feed / CDC stream** | — | — | — | — | **Not supported.** No public eCW SDK or change-feed equivalent to Epic Interconnect / Oracle Health Millennium events exists. | n/a |
| **eCW Marketplace integrations** | varies (vendor-to-vendor) | n/a | varies | Partner-only | Partner-only — not a public API surface | <https://www.eclinicalworks.com/products-services/interoperability/> |

The cells worth re-reading:

- The **only** real-time signals are HL7 v2 over MLLP. Everything else is pull-on-cadence.
- The **only** bulk pull mechanism is FHIR `$export` (Group-scoped). System-wide and Patient-scoped exports are not advertised.
- A `_lastUpdated` search parameter exists on the R4 API but its accuracy across resources has **not** been audited by us. Per the project's standing decision (`domains/ehr-adapter.md` — "We do not rely on `_lastUpdated`"), the FHIR Scan Runner will not depend on it.

---

## 3. Mapping FHIR Subscription requests to eCW CDC

### 3.1 `Hl7MessageProcessor` (primary real-time path)

**Inputs.** ADT, ORM, ORU, and MDM messages delivered over MLLP from the eCW interface engine to the deployment's MLLP listener.

**Trigger events expected.**

- ADT: `A01` (admit/visit notification), `A03` (discharge/end-visit), `A04` (register), `A08` (update), `A11` (cancel admit), `A13` (cancel discharge), `A28` / `A29` / `A31` (patient-info add/update/delete). eCW's ADT export configuration is per-deployment; the adapter should accept the full Common ADT trigger set and gate by `MSH-9` rather than hard-coding.
- ORM: `O01` (order). Cancel/replace patterns appear in `ORC-1` (`NW` new, `CA` cancel, `XO` change, `RO` replace).
- ORU: `R01` (unsolicited result). Status changes via `OBR-25` and `OBX-11`.
- MDM: `T02` / `T08` / `T11` (document notifications, replace, cancel) — useful for `DocumentReference` topics.

**Z-segments.** eCW publishes its lab specs (ORU/MDM, ORM) as PDFs; **these documents reportedly contain Z-segments specific to eCW's reporting needs**. The adapter's `lex` override must extend the standard tokenizer to walk the eCW Z-segments named in those PDFs (the spec PDFs are published by eCW but are not freely web-indexed; they are typically delivered to interface partners via the eCW project team — **needs vendor confirmation** to enumerate the exact Z-segment names and field positions).

**Cancel-and-replace.** `ORC-2` (placer order number) and `ORC-3` (filler order number) are the natural correlation key for ORM cancel/replace pairs, matching the framework's correlation-window state machine ([decisions/0005-cancel-and-replace-in-adapter.md](../../high-level-design/decisions/0005-cancel-and-replace-in-adapter.md)). For document replace (MDM `T08`/`T11`), `TXA-12` (parent document number) is the correlation key.

**Classify mapping.**

| HL7 condition | `change_kind` |
|---|---|
| ADT `A01`/`A04`/`A28` | `create` |
| ADT `A08`/`A29`/`A31` | `update` |
| ADT `A03`/`A11`/`A13` | `update` (encounter status change, not a delete) |
| ORM `ORC-1=NW` | `create` |
| ORM `ORC-1=CA` | `delete` (held; paired with subsequent `XO`/`RO` if any) |
| ORM `ORC-1=XO` / `RO` | `update` (paired with the prior `CA` via `ORC-2`/`ORC-3`) |
| ORU `OBX-11=F` | `create` or `update` (depending on whether a prior partial result was sent) |
| MDM `T02` | `create` |
| MDM `T08`/`T11` | `update` paired by `TXA-12` |

The base class owns the correlation state machine and dead-letter routing; vendor code only contributes the four `lex` / `classify` / `map_to_fhir` / `validate` overrides as documented in [adapter-authoring-guide.md](../../adapter-authoring-guide.md) Step 3.

### 3.2 `FhirScanRunner` (cold-path discovery and reconciliation)

**Source.** eCW FHIR R4 Read API. Per-practice base URLs of the form
`https://{regional_host}/fhir/r4/{practice_code}/`
(per the developer portal — practices download or register endpoints; **the URL pattern needs vendor confirmation per deployment** because regional hosts vary).

**Recommended `scan_plan()`.**

| Resource type | Cadence (default) | Query | Notes |
|---|---|---|---|
| `Patient` | 6 h | `?_count=200` (or per-practice patient panel size) | Demographic resync; backstop for missed ADT `A28`/`A29`/`A31` |
| `Encounter` | 15 m | `?status=in-progress,arrived,planned` | Real-time path is ADT; this is a backstop for facilities without ADT export |
| `Observation` | 30 m | `?category=laboratory&date=ge{NOW-7d}` | Backstop for missed ORU |
| `DiagnosticReport` | 30 m | `?category=LAB&date=ge{NOW-7d}` | Backstop for missed ORU |
| `MedicationRequest` | 1 h | `?status=active` | No HL7 path in most eCW deployments — pull-only |
| `AllergyIntolerance` | 6 h | `?clinical-status=active` | Pull-only |
| `Condition` | 1 h | `?clinical-status=active` | Pull-only |
| `ServiceRequest` | 30 m | `?status=active` | If subscriber needs orders not coming via ORM |

**Hash-and-diff.** Standard. Override `content_hash` to strip `meta.lastUpdated` and any vendor-volatile extensions before hashing — eCW resources may bump `meta.lastUpdated` without semantic change (this is the same gotcha called out in [decisions/0010-implementation-defaults.md](../../high-level-design/decisions/0010-implementation-defaults.md)).

**Bulk-data `$export` as an optional accelerator.** For large practices, the adapter can run a one-shot `Group/{group_id}/$export?_since={cursor}` via SMART Backend Services to reseed `adapter_state` faster than per-resource search. The Group ID must be obtained from the developer portal **and** the practice must explicitly authorize the app for bulk access ([eCW backend access docs](https://fhir.eclinicalworks.com/ecwopendev/documentation/getting-started/backend/patient-access)). Treat bulk export as a **bootstrap / nightly-reconcile** path, not a steady-state CDC mechanism — the polling engine's `_since` semantics still depend on `meta.lastUpdated` accuracy, so bulk results must still flow through the snapshot-and-diff pipeline; we accept the data and re-hash, we do not trust the cursor.

**Rate limits.** As of **Oct 7, 2025**, eCW limits per-base-URL FHIR calls to **250 calls/minute** with separate counters per practice code (per the eCW developer portal documentation). The framework's rate-limit budgeter must be configured with this cap. For large practices the `scan_plan()` cadences above are bounded by this cap before they are bounded by clinical staleness.

### 3.3 `VendorAPIClient` (capability declared but no real backend)

**eCW does not publish a vendor change feed, webhook channel, or proprietary streaming API.** The adapter declares `vendor_api_client: false` in the manifest and returns `nil` from `BuildVendorAPIClient`. If a future eCW release adds a SubscriptionTopic / R5-style channel (per ONC's emerging push-notification mandates under USCDI v4+), the capability gets flipped on then. See Section 8.

### 3.4 `HydrationService`

**Source.** Same FHIR R4 Read API (`GET {base}/{ResourceType}/{id}`). The Hydration Service performs single-resource reads on demand for full-resource Bundle hydration in Stage 4. Uses the framework-injected HTTP client (the adapter does not handle SMART auth itself — it is wired via `AdapterContext.HTTP`).

Override `cache_ttl()` to **60s default**, but consider lowering to **15s** for `Encounter` and `Observation` (clinical staleness is most acute for in-progress encounters and pending labs). Higher TTL is fine for `Patient`, `AllergyIntolerance`, `Practitioner`, `Organization`.

---

## 4. Recommended architecture

**Primary CDC path: HL7 v2 over MLLP** for ADT, ORM, ORU, and MDM. Real-time, well-understood, eCW publishes the lab/MDM specs. The 250-call/minute rate cap on the FHIR API makes a polling-only design fragile for any meaningful patient population.

**Fallback CDC path: FHIR R4 polling on cadence**, snapshot-and-diff, with **Group `$export` bootstrap** for large practices. Used as:

1. Cold start / catch-up after an outage.
2. Backstop for facilities that decline to wire HL7 v2 (some smaller eCW Cloud practices do not enable an outbound HL7 interface).
3. Discovery for resource types that do not flow over HL7 (`AllergyIntolerance`, `Condition`, `MedicationRequest`).

**Hydration: FHIR R4 read.** Always available for full-resource Bundles. This is the pragmatic reason every eCW deployment must purchase at least the read FHIR API access.

**Justification.** eCW's API surface is bimodal: real-time only via HL7 v2, everything else via pull. There is no third path. Mirroring that bimodality directly into the adapter is the simplest, most honest design.

---

## 5. Per-resource-type mapping table

| FHIR resource | Primary source | Trigger / scan | Gotchas |
|---|---|---|---|
| `Patient` | HL7 ADT (`A01`/`A04`/`A08`/`A28`–`A31`) → `Hl7MessageProcessor`; backstop FHIR scan 6 h | ADT trigger or scan pull | ADT export not always enabled by the practice. eCW patient ID vs MRN distinction must be preserved in `Patient.identifier`. **Needs vendor confirmation** which OID/system eCW uses for the practice's MRN. |
| `Encounter` | HL7 ADT (`A01`/`A03`/`A04`/`A08`/`A11`/`A13`) → `Hl7MessageProcessor`; backstop scan 15 m | ADT trigger or `Encounter?status=in-progress…` scan | eCW encounters are appointment-driven; `A03` ≠ `delete` — it is a status update to `finished`. The classifier maps it to `update`, not `delete`. |
| `Observation` (lab) | HL7 ORU `R01` → `Hl7MessageProcessor`; backstop scan 30 m | ORU trigger or `Observation?category=laboratory&date=ge…` | ORU panels carry many `OBX` rows; `map_to_fhir` produces one `Observation` per `OBX` plus a `DiagnosticReport` parent. Z-segments may carry result-author annotations. |
| `Observation` (vital) | FHIR scan only | scan | No HL7 vitals path in most eCW deployments. |
| `DiagnosticReport` | HL7 ORU `R01`; backstop scan 30 m | ORU trigger or scan | `OBR-25` (result status `F`/`P`/`C`) drives `DiagnosticReport.status`. Preliminary→Final is `update`, paired by `OBR-3` filler order number. |
| `MedicationRequest` | FHIR scan 1 h | scan | **No HL7 path** in most eCW deployments — pull-only. The `_lastUpdated` warning applies; rely on snapshot-and-diff. |
| `AllergyIntolerance` | FHIR scan 6 h | scan | Pull-only. |
| `Condition` | FHIR scan 1 h | scan | Pull-only. |
| `DocumentReference` | HL7 MDM (`T02`/`T08`/`T11`); FHIR scan 1 h | MDM trigger or scan | `TXA-12` is the cancel-and-replace correlation key. Document body retrieval uses FHIR `Binary` read (hydration). |
| `ServiceRequest` (Order) | HL7 ORM `O01`; backstop scan 30 m | ORM trigger or scan | `ORC-1=CA` + subsequent `RO`/`XO` is the canonical cancel-and-replace pattern; correlation by `ORC-2`/`ORC-3`. |
| `Coverage`, `Practitioner`, `Organization`, `Location` | Hydration only (no scan, no HL7 trigger) | none — read on demand | Reference data; cached aggressively. |

---

## 6. Open questions / risks

1. **Z-segment specifications are not freely web-indexed.** eCW's "HL7 Lab Results Specifications v2.3" and "HL7 Reports Inbound Specifications ORU and MDM" PDFs are published by eCW but typically delivered through the interface-engineering channel rather than the developer portal. Without those PDFs we cannot enumerate the Z-segments the `lex` override must walk. **Action: request the current versions from an eCW interface engineer or partner.**
2. **eCW does not support FHIR `Subscription`.** Confirmed by absence in the developer portal and explicit integrator statements ("no webhooks or event-driven integration options available"). Any roadmap for `SubscriptionTopic` support would land via ONC's USCDI v4+ subscription mandates, not eCW's own roadmap. **Action: track ONC USCDI v4 push-notification certification updates.**
3. **`_lastUpdated` accuracy is unaudited.** Project standing policy is to not rely on it. The snapshot-and-diff approach is correct regardless, but if an integrator finds eCW honors `_lastUpdated` consistently, an optimization pass could add `?_lastUpdated=gt{cursor}` to scan queries to shrink response sizes. **Needs vendor confirmation** at the resource-type level.
4. **Per-practice fee model.** Integrator references cite **$50/month per practice** for FHIR API access charged by eCW, **plus separate per-resource contracts for write APIs**. The adapter is read-only for CDC purposes (writes are subscriber-side), so the write-contract issue does not block the adapter — but operators need to know that *every* practice they bring online incurs an ongoing eCW fee. **Action: surface this in operator documentation.**
5. **Sandbox availability.** Self-serve sandbox is documented at <https://fhir.eclinicalworks.com/ecwopendev/documentation/getting-started>. Sandbox covers FHIR read APIs; bulk export sandbox availability **needs vendor confirmation**. HL7 v2 cannot be tested against eCW's sandbox — only against a real practice's interface engine.
6. **BAA and partner status for production.** Production access requires a signed BAA. Whether the developer or the practice signs the BAA depends on the integration model (vendor-on-behalf vs practice-direct). For a hosted `fhir-ehr-subscriptions-service` deployment delivered to a practice, the deployment is practice-direct and the BAA is between the practice and the developer/operator.
7. **Group ID provisioning for bulk export.** Each practice must grant the app permission to a specific `Group`; `Group` IDs are obtained via the developer portal after EMR-side authorization. This is a manual step per-practice. **Action: document the operator runbook for activating bulk export against a new practice.**
8. **Endpoint discovery.** eCW publishes a downloadable per-practice endpoint list at <https://fhir.eclinicalworks.com/ecwopendev/fhir-endpoints>. There is no machine-discoverable directory comparable to Epic's open-data EHR endpoint list at the level of the ONC-defined `.well-known/smart-configuration`. Operators provide the base URL via deployment configuration; the adapter does not auto-discover.
9. **Interface engine variability.** Many eCW practices use **Mirth Connect** (or other mid-market engines) between eCW and downstream systems. The MLLP feed our listener receives may be transformed before it reaches us, including site-specific Z-segment renaming. **Action: the adapter's `lex` override must be configurable enough to accommodate site-level transforms.**
10. **No equivalent to Epic Interconnect / Cerner CCL / Oracle Health change-feeds exists.** Do not budget for a `VendorAPIClient` until eCW publishes one.

---

## 7. References

- eClinicalWorks FHIR developer portal — <https://fhir.eclinicalworks.com/ecwopendev>
- eCW FHIR API documentation — <https://fhir.eclinicalworks.com/ecwopendev/documentation>
- eCW FHIR endpoints directory — <https://fhir.eclinicalworks.com/ecwopendev/fhir-endpoints>
- eCW SMART Backend Services / Bulk Data getting-started — <https://fhir.eclinicalworks.com/ecwopendev/documentation/getting-started/backend/patient-access>
- eCW interoperability landing page — <https://www.eclinicalworks.com/products-services/interoperability/>
- HL7 Bulk Data IG (R4) — <https://build.fhir.org/ig/HL7/bulk-data/export.html>
- HL7 SMART App Launch — <https://build.fhir.org/ig/HL7/smart-app-launch/app-launch.html>
- Patient Logistics on eCW API ("solid FHIR R4 read coverage… No webhooks or event-driven integration options") — <https://www.patientlogistics.com/blog-posts/eclinicalworks-api-documentation>
- ONC Certified Health IT Product List (eCW certified API entries) — <https://chpl.healthit.gov/#/search/eclinicalworks>
- HL7 v2-to-FHIR Implementation Guide (used by the framework) — <https://hl7.org/fhir/uv/v2mappings/>
- Project: domain doc — [docs/high-level-design/domains/ehr-adapter.md](../../high-level-design/domains/ehr-adapter.md)
- Project: SPI contract — [docs/high-level-design/contracts/adapter-spi.md](../../high-level-design/contracts/adapter-spi.md)
- Project: adapter authoring guide — [docs/adapter-authoring-guide.md](../../adapter-authoring-guide.md)
- Project: cancel-and-replace ADR — [docs/high-level-design/decisions/0005-cancel-and-replace-in-adapter.md](../../high-level-design/decisions/0005-cancel-and-replace-in-adapter.md)
- Project: implementation defaults ADR — [docs/high-level-design/decisions/0010-implementation-defaults.md](../../high-level-design/decisions/0010-implementation-defaults.md)
