# FHIR Subscriptions FOSS — High-Level Concept

## Overview

`fhir-subscriptions-foss` is a free and open source server that bridges **FHIR Subscriptions** on one side and **Electronic Health Record (EHR) systems** on the other. It allows external applications, registries, and analytics platforms to subscribe to clinical events in an EHR using the standard FHIR Subscriptions API, even when the underlying EHR does not natively expose FHIR Subscriptions, or only supports them in a limited way.

The server speaks the FHIR Subscriptions protocol upstream and translates downstream into whatever the EHR actually provides — typically HL7 v2 messaging, FHIR REST APIs, and/or vendor-proprietary APIs. It watches for record changes on the EHR side and fires the appropriate subscription notifications on the FHIR side.

## Goals

- Provide a **drop-in FHIR Subscriptions endpoint** for clients that need event notifications about clinical data.
- Be **EHR-agnostic** — pluggable adapters for different EHRs (Epic, Cerner/Oracle Health, Meditech, Allscripts, athenahealth, NextGen, etc.).
- Run as a **single, fast, stable container** that can be deployed either inside a facility's network or in the cloud over a VPN connection to the facility.
- Be **single-tenant by design** — one server instance per facility. No multi-tenant complexity, no cross-facility data co-mingling.
- Be **free and open source** so that any facility, integrator, or vendor can deploy it without licensing friction.

## Deployment Model

- **One deployment per facility.** A deployment is bound to a single facility's EHR and a single facility's clinical data domain.
- **One container, one Postgres.** State (subscription registrations, delivery state, event cursors) lives in Postgres. Restart resumes from durable state without losing in-flight events.
- **One EHR adapter per deployment, many adapters in the codebase.** A running instance connects to exactly one EHR (e.g., Epic 2025, Meditech Expanse, Oracle Health Millennium). The codebase supports many vendor adapters through a stable plugin interface so third parties can ship adapters for their own EHR without forking the core.
- **Two supported topologies:**
  1. **On-premises** — runs inside the facility's network alongside the EHR, with direct LAN access to HL7 interfaces and EHR APIs.
  2. **Cloud + VPN** — runs in the cloud and connects to the facility's network via a site-to-site VPN, IPsec tunnel, or equivalent (e.g., AWS Site-to-Site VPN, Tailscale, WireGuard).
- **Container-first.** Distributed as an OCI container image. Configuration via environment variables, mounted config files, and/or a configuration API.

## Two Sides of the Bridge

### 1. Subscriptions side (toward subscribers)

This side implements the FHIR Subscriptions specification (FHIR R4 Subscriptions and FHIR R4B/R5 Subscription Topics / Backport). Responsibilities include:

- **Client registration and management** — create, read, update, delete `Subscription` resources.
- **Subscription Topic catalog** — publish the set of supported topics (e.g., new lab result, encounter admit/discharge, order placed, results finalized, patient demographics changed).
- **Channel support** — at minimum REST-hook (webhooks); ideally also `websocket`, `email`, and `message` (FHIR messaging).
- **Filtering and criteria evaluation** — honor the FHIR-defined filter criteria on subscriptions and only deliver matching events.
- **Notification delivery** — generate and deliver `Bundle`s of type `subscription-notification` (or R4 backport equivalents) with full or id-only payloads, per the subscriber's requested payload type.
- **Delivery guarantees** — durable retry with backoff, dead-letter handling, and per-subscriber heartbeat / handshake notifications.
- **Security** — TLS, SMART on FHIR token validation, and per-subscriber credentials.

### 2. EHR side (toward the EHR)

This side connects to the EHR using whatever interfaces the EHR provides. It is responsible for detecting record changes and translating them into FHIR Subscription events.

- **HL7 v2 messaging** — accept inbound HL7 v2 feeds (ADT, ORU, ORM, SIU, MDM, etc.) over MLLP/TCP. This is often the most reliable signal of EHR change events.
- **FHIR REST API** — poll or query the EHR's FHIR API for additional context, resolution of references, and resource hydration.
- **Vendor-proprietary APIs** — pluggable adapters for Epic (Interconnect, App Orchard / Vendor Services APIs), Cerner / Oracle Health (Millennium APIs), Meditech, Allscripts, etc.
- **Change detection strategies (per adapter):**
  - HL7 v2 message ingestion as the primary trigger.
  - Periodic FHIR resource scans with snapshot-and-diff change detection as a fallback. (`_lastUpdated` filters are unreliable across EHRs — many vendors don't index or honor them correctly — so the adapter must read the relevant resources and compare against the prior snapshot.)
  - CDC-style hooks where available.
  - Native EHR event/notification mechanisms when present.
- **Translation layer** — map EHR-side data into the FHIR resources required by the subscription topic and filter criteria.

## Data Flow (Typical Event)

1. The EHR generates a clinical event — e.g., a new lab result is finalized.
2. The EHR emits an HL7 v2 ORU message (or surfaces the change via its API).
3. The server's EHR adapter receives the event.
4. The adapter translates / hydrates it into the FHIR resources covered by the subscription topic (e.g., `Observation`, `DiagnosticReport`, `Patient`).
5. The subscription engine evaluates the event against all active subscriptions and their filter criteria.
6. Matching subscriptions produce a `Bundle` of type `subscription-notification`.
7. The notification dispatcher delivers it via the subscriber's chosen channel (REST-hook, websocket, etc.) with retry and durability.
8. The dispatcher records delivery status, updates the subscription's `status`, and emits heartbeats as required.

## Technology Choices

The server is intended to be:

- **Fast** — low-latency event processing, suitable for near-real-time clinical workflows.
- **Stable** — long-running, memory-safe, predictable under load.
- **Small** — minimal container image, minimal runtime dependencies.
- **Operable** — first-class observability (structured logs, metrics, traces), graceful shutdown, health/readiness probes.

Strong candidates for the implementation language and runtime:

- **Rust** — memory safety, excellent async story (Tokio), small static binaries, strong HL7 and FHIR libraries emerging in the ecosystem.
- **Go** — fast compile, good concurrency, very small containers, mature HL7 libraries (e.g., HAPI-equivalents), straightforward operational model.
- **Java/Kotlin (JVM)** — most mature FHIR ecosystem (HAPI FHIR), but heavier container footprint; viable if HAPI's depth outweighs container size concerns.

Decided: Go. See [high-level-design/decisions/0009-language-choice.md](high-level-design/decisions/0009-language-choice.md). The non-negotiables (containerizable, fast startup, low memory baseline, stable under sustained load) are all satisfied.

## Non-Goals

- **Multi-tenancy.** One server, one facility. Operators run multiple instances for multiple facilities.
- **EHR data warehousing.** The server is an event bridge, not a long-term clinical data store. It keeps only what is needed for subscription state, delivery guarantees, and short-term event correlation.
- **Acting as a primary FHIR server.** It exposes a FHIR Subscriptions interface, not a general-purpose FHIR API server. Subscribers wanting full FHIR REST should be pointed at the EHR's own FHIR endpoint or a dedicated FHIR facade.
- **Re-implementing EHR business logic.** The server reflects what the EHR says happened; it does not enforce clinical rules.

## Open Questions (to be resolved in design docs)

- Which FHIR version(s) to target first — R4, R4B, R5 — and how to handle topic-based subscriptions (R5) vs. R4 backport.
- Implementation language and core libraries (Rust vs. Go vs. JVM).
- Postgres schema design and migration tooling (Postgres is the chosen — and only — backend).
- Adapter SDK design — how third parties build new EHR adapters without forking the core.
- Configuration and secret management strategy across on-prem and cloud-VPN deployments.
