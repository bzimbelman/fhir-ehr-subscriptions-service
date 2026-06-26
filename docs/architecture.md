# Subscription Service — Architecture & Design Notes

> Status: Draft, 2026-06-25. This document captures the initial design discussion for the `subscription-service` project. Code does not yet exist; this is the shared starting point.

## Goal

Provide a FOSS, self-hostable pipeline that:

1. Listens for HL7 v2 message feeds from EHRs and labs (MLLP/TCP).
2. Converts them to FHIR R4 resources using the HL7 v2-to-FHIR Implementation Guide and project-specific mappings.
3. Persists them to a FHIR server.
4. Exposes a FHIR R4 API where external systems can register Subscriptions and read resources.
5. Fires those Subscriptions when matching resources change.

The system is designed to be deployable as either a Docker Compose stack or a Kubernetes (Helm) release. The same container images are used in both targets.

---

## Topology

```
   ┌─────────────────────────────────────────────────────────────┐
   │  Interface engine (Spring Boot + IPF, one JVM, possibly    │
   │  multiple replicas)                                         │
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

The interface engine and Matchbox/HAPI run side by side. Matchbox is just another FHIR endpoint that exposes `$transform`; the interface engine calls it like any other FHIR server.

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

**Decision: Any OIDC-conformant identity provider.** (Ticket #372 generalized
this from the original Keycloak-only design.) The HAPI auth layer
(`OidcJwtAuthenticationInterceptor`) is a pure JWT/JWKS validator with no
provider-specific code paths.

Approach:

- The operator picks whichever OIDC IdP they already operate — Keycloak,
  Auth0, Okta, Azure AD, AWS Cognito, Authentik, etc. — and points HAPI
  at it via `SUBSCRIPTION_SERVICE_AUTH_ISSUER` (and, where the JWKS path
  isn't Keycloak-shaped, `SUBSCRIPTION_SERVICE_AUTH_JWKS_URL`).
- HAPI FHIR validates bearer tokens via JWKS against the IdP's
  `.well-known/openid-configuration` (resolved by Nimbus JOSE+JWT, which
  caches and refreshes the keys on its own schedule).
- Token modes:
  - **Client credentials** (machine-to-machine) for systems that register Subscriptions, post bundles, or read resources programmatically. Each external system gets its own client + secret.
  - **SMART on FHIR / authorization code** (user-attributed) for any UI that needs to act on behalf of a clinician or patient. Optional; not required for the first cut.
- Scopes follow SMART scope conventions (`system/Subscription.crus`, `system/Patient.r`, etc.). HAPI maps scopes to authorization rules via its AuthorizationInterceptor.
- Webhook callbacks (REST-hook Subscriptions) include a bearer token configured on the Subscription itself (`Subscription.endpoint` + `Subscription.header`). Subscribers are responsible for verifying the token they receive — typical pattern is for the subscriber to register the secret value with us and check for it on incoming notifications.

What we explicitly do NOT want:

- Wide-open FHIR endpoints in any environment.
- HAPI's built-in basic auth — the IdP owns identity.
- Per-service ad-hoc auth (e.g., a static API key just for this service).

See [`docs/auth.md`](auth.md) for the full provider-agnostic contract and
per-IdP recipes (Keycloak, Auth0, Okta, Authentik).

---

## Persistence

**Decision: HAPI and the interface engine each get their own Postgres database, on the same Postgres server, backed by a persistent volume.**

- HAPI's reference deployment uses H2 in-memory; we override to Postgres in both deployment targets.
- The interface engine has its own Postgres database (default name `ipf`) for the durable inbound message store (Epic #378). Same Postgres *server* as HAPI; separate database, separate user, separate Flyway migration history. Keeps HAPI's schema changes from breaking the interface engine and vice versa, and lets us split to a separate Postgres server later without app changes.
- Docker Compose: bind-mount a host directory to `/var/lib/postgresql/data` so the data survives container recreation. The default in `.env.example` is `./postgres-data` (inside the repo, fine for dev); production deployments should point at a path outside the repo (e.g., `/var/lib/subscription-service/postgres`).
- Kubernetes: PVC backed by the cluster's default StorageClass.
- Backups are out of scope for the initial deployment; the first iteration is development-grade. A backup strategy (logical dumps on a cron, or `pg_basebackup` to object storage) will be added before any production use.

Matchbox is stateless. Postgres holds all durable state (HAPI's FHIR resources, the interface engine's inbound message store).

---

## Interface engine durability, retries, and DLQ

**Decision: every inbound HL7 v2 message is persisted to the interface engine's `ingested_messages` table BEFORE the sender is ACKed. Transform + push to HAPI happens asynchronously. Failed transforms retry with exponential backoff; after the configured max attempts they move to a DEAD_LETTER state for operator triage.**

This closes the "messages lost on container restart or downstream failure" gap that the earlier (pre-#378) inline-transform path had.

### Pipeline shape

```
MLLP receive → parse v2 → INSERT into ingested_messages (status=RECEIVED) → ACK AA
                                       ↓
                              (async worker, every IPF_WORKER_POLL_MS)
                                       ↓
                  SELECT FOR UPDATE SKIP LOCKED  rows where
                       status = RECEIVED
                    OR (status = FAILED AND next_attempt_at <= now())
                                       ↓
                  UPDATE status = TRANSFORMING  (committed before I/O)
                                       ↓
                  POST to Matchbox $transform → POST FHIR Bundle to HAPI
                                       ↓
       ┌───────────────────────────────┼───────────────────────────────┐
       ▼                               ▼                               ▼
   success                       transient failure              attempts exhausted
       │                               │                               │
   status=DELIVERED             status=FAILED                    status=DEAD_LETTER
   delivered_at=now()           attempt_count++                  next_attempt_at=NULL
   last_error=NULL              next_attempt_at=now()+backoff    log event=dlq (WARN)
                                log event=retry_scheduled (INFO)
```

### Schema

The `ingested_messages` table (V002 Flyway migration in `interface-engine/src/main/resources/db/migration/`) is multi-protocol-ready from day one:

- `source_protocol` ENUM: `HL7V2_MLLP` (today), `FHIR_REST` (future), `EHR_NATIVE_API` (future, e.g. Athena), `OTHER`
- `source_system` + `source_id` — UNIQUE constraint gives idempotency for free: replays from the same EHR control-id return AA without creating a duplicate row
- `status` ENUM: `RECEIVED` / `TRANSFORMING` / `DELIVERED` / `FAILED` / `DEAD_LETTER`
- `raw_message` + `raw_content_type` — the original payload, preserved for replay and audit
- `attempt_count`, `last_attempt_at`, `next_attempt_at`, `last_error` — retry bookkeeping
- `delivered_at` — terminal-success timestamp

Three indexes back the worker's poll query and the admin endpoint's list/filter pattern.

### Retry policy

Configurable per deployment via env vars (defaults shown):

| Variable | Default | Meaning |
|---|---|---|
| `IPF_MAX_ATTEMPTS` | 5 | After this many failed attempts, move to DEAD_LETTER |
| `IPF_BACKOFF_BASE_MS` | 1000 | Base delay before retry attempt 2 |
| `IPF_BACKOFF_FACTOR` | 2.0 | Exponent: delay = BASE * FACTOR^(N-1) |
| `IPF_BACKOFF_MAX_MS` | 300000 | Cap on the per-attempt delay (5 min) |
| `IPF_DLQ_LOG_LEVEL` | WARN | Log level for the `event=dlq` transition |

With defaults: attempt 1 fails → wait 1s; attempt 2 → wait 2s; 3 → 4s; 4 → 8s; 5 → DEAD_LETTER. Total wall-clock waiting between retries: ~15s.

### Failure modes the design accepts

| Scenario | What happens |
|---|---|
| EHR sends duplicate of an already-received message | UNIQUE constraint trips on insert; we ACK AA with the original control id. No duplicate row, no retry storm. |
| Interface engine container restarts mid-process | Any rows in `status=TRANSFORMING` older than `IPF_WORKER_TRANSFORMING_STALE_SECONDS` (default 60s) are reset to `FAILED` on startup so the worker picks them back up. |
| Matchbox is down when a message arrives | Receive path persists + ACKs AA (no AE!). Worker hits Matchbox, gets a connection error, schedules retry. Once Matchbox returns, the next poll picks up the row and succeeds. |
| Matchbox is permanently broken for a message | After `IPF_MAX_ATTEMPTS` failures the row hits DEAD_LETTER. An operator queries `GET /admin/messages?status=DEAD_LETTER`, fixes the upstream issue (e.g., updates the StructureMap), then `POST /admin/messages/{id}/retry` resets the row to RECEIVED with attempt_count=0. |
| HAPI is down | Same as Matchbox-down: persist + ACK AA, retry path picks it back up when HAPI returns. |
| Postgres is unreachable | Only legitimate AE-ACK case. The interface engine genuinely can't take ownership of the message; honest AE tells the sender to retry. |
| Multiple interface-engine replicas competing for work | The worker's poll uses `SELECT FOR UPDATE SKIP LOCKED`, so concurrent replicas claim disjoint batches. Single-replica today; horizontal scale is a values bump. |

### Operator surface

`GET /admin/messages?status=DEAD_LETTER` lists the rows that need human attention. `GET /admin/messages/{id}` returns the full row including the raw v2 payload for inspection. `POST /admin/messages/{id}/retry` resets to RECEIVED, attempt_count=0. `DELETE /admin/messages/{id}` (only on DEAD_LETTER rows) purges known-bad messages.

Full docs in [`docs/admin-api.md`](admin-api.md). Bearer-token auth via `IPF_ADMIN_AUTH_TOKEN` env var (empty = unauthenticated, fine for dev; required for production).

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
- Tenant provisioning is operator-driven: a new tenant means a new HAPI partition (one API call) plus an IdP client/application that issues tokens with the matching `tenant` claim. No DDL, no schema changes.
- The Postgres schema gets HAPI's `partition_id` column whether multi-tenancy is enabled or not — `hapi.fhir.partitioning` is configured in `application.yaml` regardless of mode. This is what makes retrofitting unnecessary later — if a single-tenant deployment later wants to become multi-tenant, every existing resource is already in `DEFAULT` and stays there while new tenants get their own partitions.

What this explicitly does NOT include in the first cut:

- Per-tenant resource quotas, rate limits, or storage limits. Those are managed-cloud (model 2) concerns and belong in the operational tooling repo.
- A tenant-management UI. CLI/API only for v1.
- Cross-tenant queries. Tenants are isolated; there is no "see across all tenants" mode for non-admin users.

---

## FHIR AuditEvent generation

**Decision: Every interesting FHIR REST operation produces a FHIR `AuditEvent` resource persisted back into HAPI, so the audit trail is itself FHIR-queryable.**

**Status (ticket #391, Epic #387): IMPLEMENTED.** See `hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/audit/`.

Compliance frameworks (HIPAA, SOC 2, ONC) require a tamper-evident audit trail of who accessed/modified what. JSON access logs (ticket #388) are good for operational debugging but unstructured for compliance review and disappear on container restart. By writing AuditEvent rows INTO HAPI itself, we get a standards-shaped trail that's queryable via `GET /fhir/AuditEvent?_count=10&_sort=-_lastUpdated` and retained alongside the data it audits.

Approach:

- A HAPI server interceptor (`AuditEventInterceptor`) hooks `SERVER_OUTGOING_RESPONSE` and `SERVER_HANDLE_EXCEPTION`. After every interesting operation, it builds an AuditEvent (type=DICOM `rest`, subtype=`create|read|update|delete|search|...`, action `C|R|U|D|E`, agent from JWT claims, entity referencing the affected resource, period covering request start→end) and persists it through HAPI's `DaoRegistry`.
- **Skip-list** — `metadata` (capability statement), `AuditEvent` (otherwise GET on the audit log creates infinite recursion), `actuator/*`, `admin/*`. These never produce AuditEvents on either success or failure.
- **Writes always audited; reads/searches opt-in.** Read traffic dwarfs write traffic; the env vars `SUBSCRIPTION_SERVICE_AUDIT_CAPTURE_READS` and `SUBSCRIPTION_SERVICE_AUDIT_CAPTURE_SEARCH` (both `false` by default) flip read/search auditing on per deployment when compliance requires it. Writes (CREATE/UPDATE/PATCH/DELETE/TRANSACTION/BATCH) are audited regardless.
- **Failures always audited** — including failed reads, because a failed read is itself a security signal. The outcome code distinguishes minor failure (`4`, 4xx) from serious failure (`8`, 401/5xx).
- **Master toggle** — `SUBSCRIPTION_SERVICE_AUDIT_ENABLED=false` disables the whole subsystem (auto-config-gated via `@ConditionalOnProperty`). Default `true` — production paranoia.
- **Audit-write failures are swallowed and logged.** The audit trail records what already happened; failing the caller's request because an audit row couldn't be written would be worse than missing one row. `DaoRegistryAuditEventPersister.persist()` catches every exception and logs at WARN.
- **Persistence path** — internal `SystemRequestDetails` is used when writing AuditEvent rows so the scope-authorization interceptor doesn't reject the audit-write (otherwise an external caller without `system/AuditEvent.c` would also be unable to GENERATE their own audit row).

Operator surface — env vars (all also Helm `featureToggles.audit.*`):

- `SUBSCRIPTION_SERVICE_AUDIT_ENABLED` (default `true`)
- `SUBSCRIPTION_SERVICE_AUDIT_CAPTURE_READS` (default `false`)
- `SUBSCRIPTION_SERVICE_AUDIT_CAPTURE_SEARCH` (default `false`)
- `SUBSCRIPTION_SERVICE_AUDIT_RETENTION_DAYS` (default `365`, informational only — purging is a separate scheduled job, not yet implemented)

What this explicitly does NOT include yet:

- A scheduled purger for old AuditEvent rows. `retention-days` is recorded today but unused until that job lands.
- Streaming AuditEvents to an external SIEM. The rows are in HAPI; downstream pipelines can poll `GET /fhir/AuditEvent?_lastUpdated=gt...` if they want them.

---

## Mapping strategy

**Decision: Start with the public HL7 v2-to-FHIR IG StructureMaps; layer custom maps on top.**

- Matchbox loads the published `hl7.fhir.uv.v2mappings` IG package at boot. This covers the common message types (ADT, ORU, ORM, MDM, SIU, VXU) with maps the HL7 community maintains.
- For project-specific concerns (Z-segments, customer-specific fields, special routing rules), we add custom FML files under `matchbox/maps/`. Matchbox loads them alongside the IG; routes in the interface engine select by `source=` URL.
- The mapping layer is *configurable per deployment*: an operator can drop in a different IG version or a different set of custom maps without rebuilding the interface engine or HAPI. This is one reason Matchbox is a separate service rather than an embedded library.

We will eventually want a way to:

- pin specific IG/StructureMap versions per deployment,
- test custom maps against canonical v2 fixtures in CI,
- promote tested maps from dev → prod alongside their fixtures.

These are deferred until after the first end-to-end slice works.

---

## Public API surface

The FHIR REST API is exposed at whatever hostname the operator puts in front of HAPI's HTTP port — `https://your-deployment.example.com/fhir/*` or similar. External systems can:

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

### Docker Compose

Fastest path to running. Single host, four containers (Postgres, HAPI, Matchbox, interface engine). Good for development, demos, single-node deployments, and self-installers who want to run the system in their own Docker environment.

See [`../deploy/docker/`](../deploy/docker/) for the compose stack and [`smoke-test.md`](smoke-test.md) for the end-to-end verification recipe.

### Kubernetes (Helm)

Production-shaped target. Each component is its own Deployment + Service:

```
deploy/k8s/charts/subscription-service/
├── Chart.yaml
├── values.yaml                # defaults
├── values-dev.yaml            # dev cluster overrides
├── values-rancher.yaml        # local Rancher Desktop validation overrides
└── templates/
    ├── _helpers.tpl
    ├── configmap-{hapi,healthcheck}.yaml
    ├── secret-{postgres,auth}.yaml
    ├── statefulset-postgres.yaml
    ├── deployment-{hapi,matchbox,interface-engine}.yaml
    ├── service-{postgres,hapi,matchbox,interface-engine,interface-engine-mllp}.yaml
    ├── ingress.yaml              # FHIR HTTPS via the cluster's IngressClass
    └── networkpolicy.yaml        # optional, off by default
```

The chart is installable into Rancher Desktop, a cloud cluster, or a facility's cluster (EKS / GKE / AKS / OpenShift / bare-metal) with just a values overlay. See [`k8s-deployment.md`](k8s-deployment.md) for the operator workflow.

### Single-machine all-in-one

Deferred. There are enough moving parts (Postgres, Matchbox, HAPI, interface engine) that a meaningfully ergonomic single-binary deploy is non-trivial. Revisit after both Docker and Kubernetes targets have shipped to enough users to justify the consolidation.

### Exposing the FHIR endpoint publicly

The compose stack and the Helm chart both put HAPI on an HTTP port; making that reachable from the outside is a deployment-specific decision. Any reverse proxy or tunnel works. See [`deployment-recipes/`](deployment-recipes/) for concrete recipes:

- Cloudflare tunnel
- Caddy reverse proxy
- Traefik
- nginx
- Kubernetes Ingress
- Direct port-forward (dev only)

---

## HL7 MLLP ingress

MLLP is plain TCP, not HTTP. Most HTTP-only proxies and tunnels (including Cloudflare's free tier) cannot carry it. For the first version of the system MLLP ingress is intentionally LAN/VPN-only — sources connect to the interface engine's MLLP port over a trusted network path.

Decisions still to be made (depending on the deployment shape):

- Where do MLLP listeners bind? (host LAN IP, k8s LoadBalancer, ngrok TCP, etc.)
- What's the auth/encryption story? (`mllps://` TLS, client certs, IP allowlist)
- How do sources reach the listener? (LAN, site-to-site VPN, IPsec tunnel, Cloudflare Spectrum if paid)

For now the interface engine exposes MLLP ports on the host; operators decide how to route inbound traffic.

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

1. Stand up Docker Compose target with HAPI + Postgres + Matchbox (no interface engine yet), verify `/fhir/metadata` returns, register a test Subscription against an external webhook.
2. Wire HAPI to an OIDC IdP (JWKS validation, scope-based authorization).
3. Add the interface engine (Spring Boot + IPF) with one MLLP listener and ADT^A01 transform via Matchbox. End-to-end smoke test: nc-piped v2 in, webhook fires.
4. Add additional message types (ORU, ORM, SIU, MDM) once the first slice works.
5. Mirror the deployment into a Helm chart and validate on Rancher Desktop.
6. Resolve MLLP ingress strategy.

---

## Observability

The full operational observability story lives under Epic #387. Two
contracts are load-bearing for downstream agents and dashboards and are
documented out of this file's scope so they can be versioned and CI-gated
independently:

- [`observability/log-schema.md`](observability/log-schema.md) — JSON log
  field-stability matrix, versioning policy, worked examples. Read this
  before depending on any log field in an automation.
- [`observability/metric-catalog.md`](observability/metric-catalog.md) —
  Prometheus metric catalog with the same stability tiers, plus naming
  conventions and label-cardinality rules.
- [`observability/schema-stability-contract.md`](observability/schema-stability-contract.md)
  — what the future CI gate will enforce against both docs.
