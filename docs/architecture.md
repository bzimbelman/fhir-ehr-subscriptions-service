# Subscription Service — Architecture & Design Notes

> Status: Draft, 2026-06-25. This document captures the initial design discussion for the `subscription-service` project. Code does not yet exist; this is the shared starting point.

## Goal

Provide a FOSS, self-hostable pipeline that:

1. Listens for HL7 v2 message feeds from EHRs and labs (MLLP/TCP).
2. Converts them to FHIR R4 resources using the HL7 v2-to-FHIR Implementation Guide and project-specific mappings.
3. Persists them to a FHIR server.
4. Exposes a FHIR R4 API at `https://subscription-service.bzonfhir.com/fhir` where external systems can register Subscriptions and read resources.
5. Fires those Subscriptions when matching resources change.

The system is designed to be deployable as either a Docker Compose stack or a Kubernetes (Helm) release. The same container images are used in both targets.

---

## Topology

```
   ┌─────────────────────────────────────────────────────────────┐
   │  IPF Spring Boot app (one JVM, possibly multiple replicas)  │
   │                                                             │
   │  MLLP listener :2575  ─┐                                    │
   │  MLLP listener :2576  ─┼─► HL7v2 parser ─► v2 message POJO  │
   │  MLLP listener :2577  ─┘   (HAPI HL7v2)                     │
   │                        │                                    │
   │                        ▼                                    │
   │            Camel route: enrich, normalize, route by MSH-9   │
   │                        │                                    │
   │                        ▼                                    │
   │          Matchbox HTTP call: $transform with                │
   │            StructureMap = "ADT_A01" (etc.)                  │
   │                        │                                    │
   │                        ▼                                    │
   │                 FHIR Bundle (transaction)                   │
   │                        │                                    │
   │                        ▼                                    │
   │             HAPI FHIR client: POST /fhir (Bundle)           │
   │                                                             │
   │  ◄── ACK back to sender (AA / AE / AR)                      │
   └─────────────────────────────────────────────────────────────┘
                           │
                           ▼
              ┌────────────────────────┐
              │ HAPI FHIR JPA server   │
              │  + Subscription engine │
              │  + Postgres            │
              └─────────┬──────────────┘
                        │ matching topic
                        ▼
                  REST-hook / WebSocket / message subscribers
```

The IPF app and Matchbox/HAPI run side by side. Matchbox is just another FHIR endpoint that exposes `$transform`; IPF calls it like any other FHIR server.

---

## FHIR version & US conformance

**Decision: FHIR R4, with US Core 7.0 and the R5 Subscriptions Backport IG.**

Reasoning:

- USCDI v4 conformance under ONC's HTI-1 rule targets **R4 + US Core**. Every EHR that matters for our use cases (Epic, Cerner/Oracle, Athena, etc.) exposes an R4 API and the US Core profiles. US Core has not migrated to R5.
- R4's *native* Subscription resource is the legacy criteria-based REST-hook model. R5 introduced a much better Topic-based model (`SubscriptionTopic` + `Subscription` + `SubscriptionStatus`). The **R5 Subscriptions Backport IG** brings that model *back* to R4, so we get the future-shaped subscription API on the FHIR version EHRs actually speak.
- HAPI FHIR supports R4 + the Backport IG + the legacy criteria-based model on the same server; subscribers can use whichever they prefer.

We will NOT run on R5 today. We will revisit when US Core 8.x publishes on R5 and the EHRs follow.

---

## Authentication & authorization

**Decision: Reuse the existing Keycloak instance at `keycloak.bzonfhir.com`.** (v1, will use any oauth provider in future).

Approach:

- Add a new realm (or new clients in an existing realm) for the subscription service.
- HAPI FHIR validates bearer tokens via JWKS against the Keycloak realm's `.well-known/openid-configuration`.
- Token modes:
  - **Client credentials** (machine-to-machine) for systems that register Subscriptions, post bundles, or read resources programmatically. Each external system gets its own client + secret.
  - **SMART on FHIR / authorization code** (user-attributed) for any UI that needs to act on behalf of a clinician or patient. Optional; not required for the first cut.
- Scopes follow SMART scope conventions (`system/Subscription.crus`, `system/Patient.r`, etc.). HAPI maps scopes to authorization rules via its AuthorizationInterceptor.
- Webhook callbacks (REST-hook Subscriptions) include a bearer token configured on the Subscription itself (`Subscription.endpoint` + `Subscription.header`). Subscribers are responsible for verifying the token they receive — typical pattern is for the subscriber to register the secret value with us and check for it on incoming notifications.

What we explicitly do NOT want:

- Wide-open FHIR endpoints in any environment.
- HAPI's built-in basic auth — Keycloak owns identity.
- Per-service ad-hoc auth (e.g., a static API key just for this service).

---

## Persistence

**Decision: HAPI on Postgres, backed by a persistent volume.**

- HAPI's reference deployment uses H2 in-memory; we override to Postgres in both deployment targets.
- Docker Compose: bind-mount a host directory (e.g., `/home/zman/subscription-data/postgres`) to `/var/lib/postgresql/data` so the data survives container recreation.
- Kubernetes: PVC backed by the cluster's default StorageClass.
- Backups are out of scope for the initial deployment; the first iteration is development-grade. A backup strategy (logical dumps on a cron, or `pg_basebackup` to object storage) will be added before any production use.

Matchbox is stateless. The IPF app is stateless. Only Postgres holds durable state.

---

## Profile validation (US Core)

**Decision: Validation against US Core profiles is configurable per deployment, off by default.**

- Environment variable `SUBSCRIPTION_SERVICE_VALIDATION_MODE` controls behavior: `off` (default, no profile validation), `warn` (validate, surface findings in the response `OperationOutcome`, accept anyway), `enforce` (reject non-conforming bundles with HTTP `422 Unprocessable Entity` and an `OperationOutcome`).
- HAPI's `RequestValidatingInterceptor` is wired with the IGs already installed at boot under `hapi.fhir.implementationguides` (US Core 7.0 + Subscriptions Backport R4); the interceptor's failure mode is what the env var actually switches between. The validator picks up the IGs by injecting the JPA starter's `IInstanceValidatorModule` bean — no re-loading of the package tarballs.
- Default `off` because the v2-to-FHIR IG StructureMaps don't always produce strictly US Core–conformant output for every field; a brand-new deployment shouldn't reject real-world traffic. Operators dial up to `warn` once they see what their feeds actually produce, then to `enforce` once their custom maps fill the gaps.

**Implemented in #367** — see `hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/validation/`:

- `ValidationProperties` — binds `SUBSCRIPTION_SERVICE_VALIDATION_MODE` to the `ValidationMode` enum (`OFF | WARN | ENFORCE`).
- `ProfileValidationAutoConfiguration` — Spring Boot auto-configuration. Skipped entirely when `mode=off`. When `mode=warn|enforce` it builds a `RequestValidatingInterceptor` configured with `addValidationResultsToResponseOperationOutcome=true`; in `enforce` mode it also sets `failOnSeverity=ERROR`. Registers the interceptor on the HAPI `RestfulServer` via a `SmartInitializingSingleton` (same pattern the auth layer uses).
- Note on the HTTP response code: HAPI's `BaseValidatingInterceptor.fail(...)` hard-codes `UnprocessableEntityException` (HTTP 422), which is the FHIR-standard response for failed resource validation. Earlier drafts of this section said "400 Bad Request"; 422 is the correct shape and what the e2e suite asserts.

---

## Subscription channel security

**Decision: Channel security policy is configurable per deployment, secure-by-default.**

- Environment variable `SUBSCRIPTION_SERVICE_CHANNEL_SECURITY` controls how strict the server is when accepting `Subscription` resources:
  - `strict` (default) — REST-hook `endpoint` URLs MUST be HTTPS; `Subscription.header` MUST contain at least one `Authorization` header (bearer token or similar); WebSocket subscriptions require an authenticated session.
  - `relaxed` — HTTPS still required but no header mandate. Useful for development or for subscribers that authenticate the *callback* via mutual TLS / IP allowlist instead of a header.
  - `permissive` — HTTP and missing auth headers are allowed. **Only intended for the sandbox/testing model (3)** and local dev (model 4).
- Whatever policy is in effect, the server records the configured `Subscription.header` and includes it on every outbound notification — the subscriber is always responsible for verifying that header.

**Implementation (ticket #368, merged):** a HAPI server interceptor that hooks `STORAGE_PRESTORAGE_RESOURCE_CREATED` and `STORAGE_PRESTORAGE_RESOURCE_UPDATED`. Violations short-circuit the write with `HTTP 422 Unprocessable Entity` plus an `OperationOutcome` whose issues list each specific failure ("HTTPS required", "Authorization header required", etc.). Classes live under `com.bzonfhir.subscriptionservice.channelsecurity`:

- [`ChannelSecurityProperties`](../hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/channelsecurity/ChannelSecurityProperties.java) — binds `SUBSCRIPTION_SERVICE_CHANNEL_SECURITY` to a `ChannelSecurityMode` enum; default `STRICT`.
- [`ChannelSecurityInterceptor`](../hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/channelsecurity/ChannelSecurityInterceptor.java) — the interceptor itself. Permissive mode logs a startup WARN documenting that the policy is relaxed for sandbox/dev.
- [`ChannelSecurityAutoConfiguration`](../hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/channelsecurity/ChannelSecurityAutoConfiguration.java) — Spring Boot `@AutoConfiguration`; always loaded (no `@ConditionalOnProperty`) so a misconfigured env var can never silently disable the interceptor.

---

## Multi-tenancy

**Decision: Build HAPI partition-based multi-tenancy in from day one, configurable per deployment.**

**Status (ticket #369): IMPLEMENTED.** The interceptor, properties, and HAPI `partitioning` block are merged. See [`multi-tenancy.md`](multi-tenancy.md) for the operator workflow.

The cost of building this in up-front is small (a config toggle, a small auth interceptor that maps a JWT claim to a HAPI partition, ~3 HAPI config flags). The cost of retrofitting it later — migrating existing resources into partitions, changing URLs subscribers depend on — is large enough to rule it out.

Approach:

- Environment variable `SUBSCRIPTION_SERVICE_MULTITENANCY` switches between `disabled` (default) and `enabled`.
- **Disabled mode** — all requests resolve to HAPI's `DEFAULT` partition. URLs look like `/fhir/Patient/123`. From a subscriber's perspective the server is single-tenant and partitions are invisible. This is the model (1) and (4) shape.
- **Enabled mode** — a small `Interceptor` (`TenantPartitionInterceptor` in `com.bzonfhir.subscriptionservice.multitenancy`) reads a `tenant` claim from the validated JWT and sets the HAPI partition context for the request. Resources, Subscriptions, and notifications are partition-scoped automatically — HAPI does the work. Tenants cannot see each other's resources, period. This is the model (2) and (3) shape.
- The FHIR base URL shape is the same in both modes (`/fhir/Patient/123`). The starter's URL-based tenant strategy (`/fhir/{tenant}/Patient/...`) is explicitly unregistered at boot; the partition comes from the JWT, never from the URL path.
- Tenant provisioning is operator-driven: a new tenant means a new HAPI partition (one API call) plus a Keycloak client that issues tokens with the matching `tenant` claim. No DDL, no schema changes.
- The Postgres schema gets HAPI's `partition_id` column whether multi-tenancy is enabled or not — `hapi.fhir.partitioning` is configured in `application.yaml` regardless of mode. This is what makes retrofitting unnecessary later — if a single-tenant deployment later wants to become multi-tenant, every existing resource is already in `DEFAULT` and stays there while new tenants get their own partitions.

What this explicitly does NOT include in the first cut:

- Per-tenant resource quotas, rate limits, or storage limits. Those are managed-cloud (model 2) concerns and belong in the operational tooling repo.
- A tenant-management UI. CLI/API only for v1.
- Cross-tenant queries. Tenants are isolated; there is no "see across all tenants" mode for non-admin users.

---

## Mapping strategy

**Decision: Start with the public HL7 v2-to-FHIR IG StructureMaps; layer custom maps on top.**

- Matchbox loads the published `hl7.fhir.uv.v2mappings` IG package at boot. This covers the common message types (ADT, ORU, ORM, MDM, SIU, VXU) with maps the HL7 community maintains.
- For project-specific concerns (Z-segments, customer-specific fields, special routing rules), we add custom FML files under `matchbox/maps/`. Matchbox loads them alongside the IG; routes in IPF select by `source=` URL.
- The mapping layer is *configurable per deployment*: an operator can drop in a different IG version or a different set of custom maps without rebuilding the IPF app or HAPI. This is one reason Matchbox is a separate service rather than an embedded library.

We will eventually want a way to:

- pin specific IG/StructureMap versions per deployment,
- test custom maps against canonical v2 fixtures in CI,
- promote tested maps from dev → prod alongside their fixtures.

These are deferred until after the first end-to-end slice works.

---

## Public API surface

`https://subscription-service.bzonfhir.com/fhir/*` exposes the standard FHIR REST API. External systems can:

- `POST /fhir/Subscription` (or `/fhir/SubscriptionTopic` + `/fhir/Subscription` under the Backport IG) to register for notifications.
- `GET /fhir/{Resource}/{id}` to read resources after a notification fires.
- `GET /fhir/{Resource}?...` to search.
- Open a WebSocket connection for `websocket` channel Subscriptions.

This is the *primary* product of the service from a consumer's perspective. The HL7 ingestion side is invisible to them — resources just appear.

---

## Repository scope

This repository contains **the tool itself** and nothing else: source code, configuration, container images, Docker Compose, Helm chart, design docs. Companion concerns live in their own repos (to be created later):

- Product website / marketing site
- End-user documentation portal
- Managed-cloud operational tooling, SLAs, support runbooks
- Premium / add-on features outside the base FOSS offering

This keeps self-hosters consuming a clean, focused artifact while letting any managed offerings layered on top evolve independently.

## Hosting models (informational)

The tool is intended to support four distribution shapes. The first two are *production*, the second two are *non-production*. All four ship the **same artifacts** out of this repo (container images + Helm chart + Compose); support, operations, and lifecycle differ.

1. **Self-hosted in the facility's network** (production). Facility deploys on their own hardware or in their own cloud, with their own networking. Best fit for facilities with internal IT capacity.
2. **Managed cloud (we host)** (production). We operate an instance per facility. Facility runs a VPN to our environment and points HL7 feeds at the VPN endpoint. Configuration surface looks identical to self-hosted.
3. **Public sandbox / testing cloud** (non-production). Turn-key default configuration anyone can sign up for. Customizations wiped on a fixed cadence so it remains a trial environment.
4. **Local download for personal use** (non-production). Mechanically the same as (1), but with no support, no SLA, no managed updates.

This repo's deployment targets — Docker Compose and Helm — are designed so that **the same image and chart serve all four models**. Per-customer or per-environment configuration is values-driven; nothing about the tool itself needs to change between models.

## Deployment targets

### Docker Compose on zdock (development)

Fastest path for our own development. zdock already runs the Cloudflare tunnel and the Meld stack; adding one compose project is straightforward. The tunnel's ingress config gets a new entry pointing `subscription-service.bzonfhir.com` at the local HAPI port. Also serves as the reference deployment for hosting model (4) — self-installers point at the same `deploy/docker/docker-compose.yml`.

Layout:

```
deploy/docker/
├── docker-compose.yml
├── .env.example
├── postgres/                ← bind-mount target (gitignored data)
└── README.md
```

### Kubernetes (Helm)

Production-shaped target. Mirrors the rest of the CDS Tools platform. Each component is its own Deployment + Service:

```
deploy/k8s/
└── charts/
    └── subscription-service/
        ├── Chart.yaml
        ├── values.yaml
        ├── values-dev.yaml
        ├── values-prod.yaml
        └── templates/
            ├── hapi-{deployment,service,configmap}.yaml
            ├── matchbox-{deployment,service,configmap}.yaml
            ├── ipf-{deployment,service}.yaml      ← LoadBalancer for MLLP
            ├── postgres-{statefulset,service,pvc}.yaml
            └── ingress.yaml                       ← FHIR HTTPS ingress
```

The chart will be installable into Rancher Desktop, our own cloud cluster, and a facility's cluster (EKS / GKE / AKS / OpenShift / bare-metal) with just a values overlay. This is the same chart used by hosting models (1), (2), and (3) — only the values file differs.

### Single-machine all-in-one

Deferred. There are enough moving parts (Postgres, Matchbox, HAPI, IPF, possibly cloudflared) that a meaningfully ergonomic single-binary deploy is non-trivial. Revisit after both Docker and Kubernetes targets are working.

---

## HL7 MLLP ingress (deferred)

The MLLP/TCP side of the system is out of scope for the first network design pass — Cloudflare's free HTTP tunnel cannot carry plain TCP, so MLLP ingress needs its own strategy (LAN-only, Cloudflare Spectrum, dedicated VPN, etc.). Decisions to be made later:

- Where do MLLP listeners bind? (zdock LAN IP, k8s LoadBalancer, ngrok TCP, …)
- What's the auth/encryption story? (`mllps://` TLS, client certs, IP allowlist)
- How do EHRs reach us? (LAN, VPN, Cloudflare Spectrum, …)

For now the IPF app is designed to expose MLLP ports — they just won't be publicly reachable through `subscription-service.bzonfhir.com`.

---

## Open questions

These have been **resolved** since the first draft and are now decisions in the sections above; listed here so the resolution trail is visible:

1. ~~License~~ → Apache 2.0 for the tool. See `README.md` "License".
2. ~~US Core profile validation~~ → configurable, off by default. See "Profile validation".
3. ~~Subscription channel security~~ → configurable, secure-by-default. See "Subscription channel security".
4. ~~Multi-tenancy~~ → HAPI partitions, build in from day one, configurable per deployment. See "Multi-tenancy".

Remaining items will be worked through as the implementation progresses.

---

## Next steps

1. Stand up Docker Compose target with HAPI + Postgres + Matchbox (no IPF yet), verify `/fhir/metadata` returns, register a test Subscription against an external webhook.
2. Wire HAPI to Keycloak (JWKS validation, scope-based authorization).
3. Add the IPF Spring Boot app with one MLLP listener and ADT^A01 transform via Matchbox. End-to-end smoke test: nc-piped v2 in, webhook fires.
4. Add additional message types (ORU, ORM, SIU, MDM) once the first slice works.
5. Mirror the deployment into a Helm chart and validate on Rancher Desktop.
6. Resolve MLLP ingress strategy.
