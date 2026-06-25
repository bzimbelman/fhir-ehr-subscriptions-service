# Initial Design Conversation

> Captured 2026-06-25. This is the verbatim shape of the conversation that led to the project being created, preserved so the reasoning behind the early decisions is not lost.

## Context

The CDS Tools platform needs a way to ingest HL7 v2 feeds from EHRs and labs, convert them to FHIR, and let downstream systems subscribe to changes. We explored the FOSS landscape, picked HAPI FHIR as the server, picked IPF + Matchbox as the HL7-to-FHIR pipeline, and decided to host the result at `subscription-service.bzonfhir.com` on existing infrastructure.

## Server choice — HAPI FHIR

We surveyed the FOSS FHIR servers that support Subscriptions:

| Server | License | R4 legacy | R4B Backport | R5 Topic-based |
|---|---|---|---|---|
| HAPI FHIR (JPA Server) | Apache 2.0 | ✅ | ✅ | ✅ |
| Medplum | Apache 2.0 | ✅ | partial | partial |
| Blaze | Apache 2.0 | ✅ | partial | in progress |
| Microsoft FHIR (OSS) | MIT | ❌ (removed) | ❌ | ❌ |
| LinuxForHealth FHIR | Apache 2.0 | via "Notification" | ❌ | ❌ |
| Firely Server (Vonk) | proprietary | ✅ | ✅ | ✅ |
| Aidbox | proprietary | ✅ | ✅ | ✅ |

**HAPI FHIR** chosen — broadest support, reference implementation for the Subscriptions Backport IG, mature, well-known, Apache 2.0.

## Pipeline choice — IPF + Matchbox

We surveyed FOSS options for HL7 v2 → FHIR:

| Tool | Type | Notes |
|---|---|---|
| NextGen Connect (Mirth) | Integration engine | Battle-tested, but GUI-driven and v2→FHIR mapping is DIY |
| Open eHealth Integration Platform (IPF) | Camel extension | Code-first, JVM, production-grade |
| LinuxForHealth hl7v2-fhir-converter | Java library | YAML templates for common message types |
| Microsoft FHIR Converter | CLI/library | Liquid templates, R4 target |
| HL7 v2-to-FHIR IG | Spec + StructureMaps | Authoritative mapping artifacts; needs a runtime |
| Matchbox | Transform engine | Executes FHIR StructureMaps (FML), built on HAPI |
| HAPI HL7v2 module | Library | Canonical v2 parser; mapping to FHIR is DIY |
| Apache Camel (`camel-hl7` + `camel-fhir`) | Framework | Most flexible, most DIY |

**IPF + Matchbox** chosen — code-first pipeline, official HL7 v2-to-FHIR StructureMaps executed by Matchbox, IPF handles MLLP/parsing/routing/retries. Single JVM app (IPF) calls Matchbox over HTTP for the transform, then posts to HAPI.

## Hosting & distribution models to investigate

Four target shapes for how `subscription-service` reaches users — two production, two non-production:

**Production**

1. **Self-hosted in the facility's network.** The facility deploys the tool on their own hardware (Docker or Kubernetes) or in their own cloud account with their own networking. We publish the images and the Helm chart; they own the operational surface. Best fit for facilities with mature internal IT.

2. **Managed cloud (we host).** We run the tool in our cloud on the facility's behalf. The facility stands up a VPN from their network to our environment and points their HL7 feeds at the VPN endpoint. From their perspective the service "looks like" it's on-prem — same FHIR endpoint, same Subscription API — but they don't operate it. They configure it much the same way they would on-prem; only the network plumbing differs.

**Non-production**

3. **Public testing/sandbox cloud.** A turn-key, default-configured instance anyone can sign up for and try without downloading or installing anything. Customizations get wiped on a fixed cadence (e.g., every 30 days) so it remains a trial environment rather than a free production tier. Funnels evaluators toward one of the two production models.

4. **Local download for personal use.** Mechanically identical to (1), but the user is on their own — no support, no SLA, no managed updates. The "developer takes it home" path.

Models (1) and (4) ship the **same artifacts** out of this repo (images + charts); the only difference between them is the support relationship around them. Model (2) layers managed-service operations on top of those artifacts. Model (3) is a single shared instance of model (2) with a lifecycle policy attached.

The take-two epic targets the **infrastructure that makes all four possible**: a runnable Docker Compose stack and a Helm chart for Kubernetes. Hosting business decisions — pricing, sandbox lifecycle, VPN onboarding, support tiers — are explicitly *not* part of this repo.

## Repository scope

This repository is **the tool itself**: IPF app, Matchbox configuration, HAPI configuration, Docker Compose, Helm chart, design docs. Everything else lives in companion repos to be created later:

- Product website / marketing site
- End-user documentation portal (configuration guides per hosting model)
- Support runbooks, SLA definitions, managed-cloud operational tooling
- Premium / add-on features that aren't part of the base FOSS offering

Keeping the tool repo focused on the buildable artifact lets self-hosters (models 1 and 4) consume it cleanly without inheriting commercial concerns, and lets the managed-cloud and sandbox offerings (models 2 and 3) evolve without churning the tool's source tree.

## Decisions made in this conversation

- **FHIR version**: R4 with US Core 7.0 and the R5 Subscriptions Backport IG. Rationale: USCDI-conformant EHRs are on R4 + US Core; the Backport gives us R5's Topic-based subscription model without forcing the rest of the stack to R5.
- **Auth (v1)**: Reuse Keycloak at `keycloak.bzonfhir.com`. New realm/clients; client-credentials for machine-to-machine, SMART on FHIR optional for user-attributed flows. Any OAuth2 provider that exposes JWKS will be supported in a later iteration.
- **Persistence**: Postgres for HAPI, on a persistent volume (bind mount on zdock, PVC on Kubernetes).
- **Mappings**: Start with the public HL7 v2-to-FHIR IG; layer custom FML files on top per deployment.
- **Deployment targets**: Docker Compose and Kubernetes (Helm) — both first class. Same images in both. Single-machine all-in-one deferred.
- **MLLP ingress strategy**: Deferred. The IPF app exposes MLLP listeners but the public hostname only covers HTTPS for the FHIR API.
- **External subscription registration**: Yes — `subscription-service.bzonfhir.com/fhir` is the public FHIR endpoint where external systems register Subscriptions and receive notifications.
- **License**: Apache 2.0 for the tool. Companion repos (managed-cloud ops, support, premium add-ons) may carry different licenses.
- **US Core profile validation**: Configurable per deployment via `SUBSCRIPTION_SERVICE_VALIDATION_MODE` (`off` / `warn` / `enforce`); off by default.
- **Subscription channel security**: Configurable per deployment via `SUBSCRIPTION_SERVICE_CHANNEL_SECURITY` (`strict` / `relaxed` / `permissive`); secure-by-default (`strict`).
- **Multi-tenancy**: Build HAPI partition-based multi-tenancy in from day one, configurable per deployment via `SUBSCRIPTION_SERVICE_MULTITENANCY` (`disabled` / `enabled`). Up-front cost is small; retrofit cost would be large. Disabled mode runs in HAPI's `DEFAULT` partition and looks single-tenant to subscribers.

## Out of scope for this repo

- **CDS Hooks integration.** Whether the existing CDS Hooks backend becomes a subscriber to this service, or how it might, is not a decision this repo makes. This repo is the tool; downstream integrations live in their own repos.

## Open items carried into the project

See [architecture.md](./architecture.md) "Open questions" — most have been resolved; remaining ones will be worked through as implementation progresses.
