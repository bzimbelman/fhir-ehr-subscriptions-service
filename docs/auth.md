# Authentication & authorization

> Status: Updated, tickets #359 (JWT enforcement landed) and #370/#376
> (provider-agnostic configuration). This document defines both the
> *identity* infrastructure (Keycloak realm, clients, scopes) AND the
> *enforcement* layer inside HAPI itself (JWT validation + SMART scope
> mapping to HAPI auth rules).

## Overview

External systems (and our own internal services) call the subscription-service
FHIR API using OAuth2 bearer tokens. Tokens are issued by a Keycloak realm
named `subscription-service` that the operator provisions on whichever
Keycloak instance the deployment runs against. The realm is self-contained
and can sit alongside other realms on a shared Keycloak server.

Everything below uses placeholder hostnames (`your-keycloak.example.com`,
`your-subscription-service.example.com`). Substitute your own. The reference
deployment that this project's maintainer runs is documented in a single
callout at the bottom of this page.

## Modern vs. legacy Keycloak path prefix

Keycloak 17 (released April 2022) replaced the WildFly-based distribution
with a Quarkus-based one and **dropped the `/auth/` path prefix** that was
present in every earlier release. Both shapes show up in the wild, so the
provisioning script and the auth-layer config both accept either:

| Keycloak version       | Base URL shape                                          | `KEYCLOAK_PATH_PREFIX` |
| ---------------------- | ------------------------------------------------------- | ---------------------- |
| **>= 17** (Quarkus)    | `https://your-keycloak.example.com/realms/<name>`       | empty (default)        |
| < 17 (WildFly, legacy) | `https://your-keycloak.example.com/auth/realms/<name>`  | `/auth`                |

Every example in this document uses the modern (no-prefix) form. If you're
on a legacy WildFly Keycloak, prepend `/auth` to every Keycloak URL — e.g.
`https://your-keycloak.example.com/auth/realms/subscription-service/...`.
The provisioning script accepts `KEYCLOAK_PATH_PREFIX=/auth` for that case.

## The realm

| Property              | Value                                                                                          |
| --------------------- | ---------------------------------------------------------------------------------------------- |
| Realm name            | `subscription-service`                                                                         |
| Issuer URL            | `https://your-keycloak.example.com/realms/subscription-service`                                |
| Discovery document    | `https://your-keycloak.example.com/realms/subscription-service/.well-known/openid-configuration` |
| JWKS URL              | `https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/certs` |
| Token endpoint        | `https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/token` |
| Access token lifespan | 15 minutes                                                                                     |
| Refresh token lifespan| 30 minutes (M2M clients are configured not to use refresh tokens)                              |
| SSL required          | `external` (HTTPS required for all non-localhost traffic)                                      |

The full realm definition lives at `keycloak/realms/subscription-service.json`
and is intended to be imported via the Keycloak admin UI (Realm settings ->
Action -> Partial import) or via `scripts/keycloak/provision-realm.sh` (see
[Provisioning the realm](#provisioning-the-realm) below).

## Scope catalog

Scopes follow the SMART on FHIR `system/<Resource>.<crud-flags>` naming
convention. The first iteration covers Subscription, Patient, and Observation;
additional resources will be added as the API surface grows.

| Scope                       | What it grants                                                                                                                                     |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `system/Subscription.crus`  | Create, Read, Update, Search FHIR `Subscription` resources. The base scope an external system needs to register webhook subscriptions.             |
| `system/Subscription.r`     | Read-only access to `Subscription` resources (for monitoring/audit clients that should not create or modify subscriptions).                        |
| `system/Patient.r`          | Read FHIR `Patient` resources. Required for any subscriber that reads patient context after a notification fires.                                  |
| `system/Patient.cruds`      | Full lifecycle (Create, Read, Update, Delete, Search) of `Patient`. Used by trusted ingestion-side services, not typical external subscribers.     |
| `system/Observation.r`      | Read FHIR `Observation` resources. Used by subscribers that consume lab/vitals data delivered through subscription notifications.                  |

The example client (`subscription-service-cli`) is granted
`system/Subscription.crus` and `system/Patient.r` as *default* client scopes;
the others are *optional* and can be requested explicitly via the `scope`
parameter on the token request. New clients onboarded for external systems
should follow least-privilege: assign only the scopes the integration actually
needs.

## Provisioning the realm

`scripts/keycloak/provision-realm.sh` is idempotent — it logs in, GETs the
realm, and only POSTs the import if the realm doesn't already exist.

Modern (Quarkus) Keycloak, with secret automation:

```bash
KEYCLOAK_URL=https://your-keycloak.example.com \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD='<admin-password>' \
KEYCLOAK_CLIENT_SECRET=$(openssl rand -hex 32) \
  scripts/keycloak/provision-realm.sh
```

Legacy WildFly Keycloak (`/auth/` path prefix):

```bash
KEYCLOAK_URL=https://your-keycloak.example.com \
KEYCLOAK_PATH_PREFIX=/auth \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD='<admin-password>' \
KEYCLOAK_CLIENT_SECRET=$(openssl rand -hex 32) \
  scripts/keycloak/provision-realm.sh
```

Dry-run mode (`--dry-run`) authenticates and probes the realm endpoint but
skips the import POST — useful for confirming connectivity, credentials, and
that `KEYCLOAK_PATH_PREFIX` is set correctly against a Keycloak you don't
want to mutate.

When `KEYCLOAK_CLIENT_SECRET` is set, the script substitutes the
`CHANGE-ME-IN-DEPLOYMENT` placeholder onto a temp copy of the realm JSON
before import. The committed JSON is never modified. When it is not set, the
placeholder imports as-is and a warning is logged; rotate the secret in the
admin UI before the realm sees any traffic.

## Obtaining a token (client_credentials)

The example confidential client is `subscription-service-cli`. If you used
`KEYCLOAK_CLIENT_SECRET` during provisioning, use that value below;
otherwise rotate the `CHANGE-ME-IN-DEPLOYMENT` placeholder in the admin UI
(Clients -> `subscription-service-cli` -> Credentials -> Regenerate) first.

To obtain a bearer token via the OAuth2 `client_credentials` grant:

```bash
curl -sS -X POST \
  https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d "client_secret=${CLIENT_SECRET}" \
  -d 'scope=system/Subscription.crus system/Patient.r' \
  | jq -r .access_token
```

That JWT goes in the `Authorization: Bearer <token>` header on every call to
`https://your-subscription-service.example.com/fhir/*`. Tokens expire after
15 minutes; re-request rather than refreshing (M2M flow does not issue
refresh tokens).

A useful one-liner for capturing the token into a shell variable:

```bash
TOKEN=$(curl -sS -X POST \
  https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d "client_secret=${CLIENT_SECRET}" \
  | jq -r .access_token)

curl -sS -H "Authorization: Bearer ${TOKEN}" \
  https://your-subscription-service.example.com/fhir/metadata
```

You can inspect the token at <https://jwt.io> or with `jq -R 'split(".")[1] |
@base64d | fromjson'` to see the `iss`, `azp`, `scope`, and `exp` claims.

## Onboarding an external system

External integrators do not self-register. The operator process is:

1. Integrator opens an onboarding ticket describing:
   - The integrating system (name, owner, contact for security incidents).
   - The intended use (which FHIR resources will they read/write, do they need
     to register Subscriptions, what callback URL will they use).
   - Their expected request volume and traffic pattern.
2. Operator reviews the request and chooses the minimum scope set required.
3. Operator creates a new confidential client in the `subscription-service`
   realm via the Keycloak admin UI:
   - **Client ID**: `subscription-service-<integrator-slug>`
   - **Client authenticator**: `Client Id and Secret`
   - **Service accounts**: enabled
   - **Standard flow / Direct access grants**: disabled (M2M only)
   - **Default client scopes**: the approved set from step 2
4. Operator regenerates the client secret and shares the `client_id` +
   `client_secret` + the assigned scopes with the integrator out-of-band
   (encrypted email, password manager share, etc. — never in a ticket
   comment or chat).
5. Operator records the onboarding in the integrator inventory (location TBD —
   for now, keep an entry in the onboarding ticket).

Rotation: client secrets are rotated annually, or immediately if a
compromise is suspected. The integrator is given 14 days notice of a planned
rotation and a new secret is provisioned in parallel before the old one is
revoked.

## Webhook callbacks (subscriber side)

The Keycloak realm here only controls *inbound* access to our FHIR API. When a
`Subscription` fires and we POST to the subscriber's REST-hook endpoint, we
include the `Subscription.header` values verbatim. The subscriber is
responsible for verifying that header (usually a bearer token they generated
and registered with the `Subscription` resource at creation time). See
"Subscription channel security" in `docs/architecture.md` for the policy
controlling what we require on the subscriber side.

## Smoke-testing Keycloak reachability

The Keycloak server should be reachable from any machine that will run the
provisioning script. Quick sanity check against the master realm (which is
always present):

```bash
# Modern (Quarkus) Keycloak — no /auth/ prefix
curl -sS https://your-keycloak.example.com/realms/master/.well-known/openid-configuration \
  | jq -r .issuer
# Expected: https://your-keycloak.example.com/realms/master

# Legacy WildFly Keycloak — prepend /auth
curl -sS https://your-keycloak.example.com/auth/realms/master/.well-known/openid-configuration \
  | jq -r .issuer
# Expected: https://your-keycloak.example.com/auth/realms/master
```

If that does not return JSON, the Keycloak server is unreachable — fix
networking or the server itself before proceeding.

For a self-contained smoke test that spins up Keycloak in a container and
runs the full provision -> token -> verify loop, see
[`keycloak-quickstart.md`](./keycloak-quickstart.md).

## How the FHIR API enforces tokens (ticket #359)

HAPI itself doesn't know anything about Keycloak. The enforcement layer is
a small Spring Boot auto-configuration JAR built from `hapi/auth/` and
layered onto the upstream HAPI image (see `hapi/Dockerfile`). At runtime
the auto-configuration registers two HAPI server interceptors:

1. **`KeycloakJwtAuthenticationInterceptor`** —
   `@Hook(SERVER_INCOMING_REQUEST_POST_PROCESSED)`. For every request:
   - If the path is on the anonymous allow-list (`/metadata`,
     `/.well-known/smart-configuration` by default), pass through.
   - Otherwise, require an `Authorization: Bearer <jwt>` header.
   - Parse and verify the JWS signature against the realm's JWKS using
     Nimbus JOSE+JWT (already shipped inside the HAPI image — no new
     transitive deps). Only RS256 / RS384 / RS512 are accepted; HS\*
     ("none" + symmetric) tokens are refused.
   - Verify `iss` matches the configured issuer; verify `exp` is in the
     future and `nbf` (if present) is in the past.
   - On success, stash the verified `JWTClaimsSet` and the parsed
     `Set<SmartScope>` on `RequestDetails.userData` so downstream
     interceptors can read them without re-parsing.
   - On any failure, throw `AuthenticationException` → HTTP 401 +
     `OperationOutcome`. The message describes the failure (`Token
     rejected: Expired JWT` etc.) but never leaks the token contents.

2. **`ScopeAuthorizationInterceptor`** — extends HAPI's
   `AuthorizationInterceptor` with default policy `DENY`. `buildRuleList`
   reads the stashed scopes and produces a HAPI `IAuthRule` list. The
   catalog above maps to rules as follows:

   | SMART scope                  | HAPI rules produced                                                            |
   | ---------------------------- | ------------------------------------------------------------------------------ |
   | `system/Subscription.crus`   | `create`, `read`, `write` (update), `search` on `Subscription`                 |
   | `system/Subscription.r`      | `read`, `search` on `Subscription`                                             |
   | `system/Patient.r`           | `read`, `search` on `Patient`                                                  |
   | `system/Patient.cruds`       | `create`, `read`, `write` (update), `delete`, `search` on `Patient`            |
   | `system/Observation.r`       | `read`, `search` on `Observation`                                              |

   Plus an always-allow rule for `/metadata` and a terminating deny-all
   ("operation not permitted by SMART scopes") that turns any
   unrecognized request into a 403.

### Configuration

All knobs live under `subscription-service.auth.*` and are bindable via
either `application.yaml` or environment variables (Spring Boot's relaxed
binding maps `SUBSCRIPTION_SERVICE_AUTH_*` env vars onto the property
tree).

| Property                                            | Env var                                | Default                                                                                  |
| --------------------------------------------------- | -------------------------------------- | ---------------------------------------------------------------------------------------- |
| `subscription-service.auth.enabled`                 | `SUBSCRIPTION_SERVICE_AUTH_ENABLED`    | `true`                                                                                   |
| `subscription-service.auth.issuer`                  | `SUBSCRIPTION_SERVICE_AUTH_ISSUER`     | **none — required when auth is enabled** (ticket #370). Container fails fast at startup. |
| `subscription-service.auth.jwks-url`                | `SUBSCRIPTION_SERVICE_AUTH_JWKS_URL`   | derived from issuer: `${issuer}/protocol/openid-connect/certs`                           |
| `subscription-service.auth.allow-anonymous-paths`   | (yaml list only)                       | `[/metadata, /.well-known/smart-configuration]`                                          |

When `enabled=true` and `issuer` is empty, the Spring context refresh fails
with:

```
subscription-service.auth.issuer is required when auth is enabled.
Set SUBSCRIPTION_SERVICE_AUTH_ISSUER (e.g.,
https://your-keycloak.example.com/realms/subscription-service) or set
SUBSCRIPTION_SERVICE_AUTH_ENABLED=false for local dev.
```

The HAPI container exits non-zero and restarts in a loop — the docker-compose
default for `restart:` is `unless-stopped`, so look for the message in
`docker logs subscription-service-hapi`.

### Disabling for local development

Set `SUBSCRIPTION_SERVICE_AUTH_ENABLED=false` in `.env`. The whole
auto-configuration is gated by `@ConditionalOnProperty`, so disabling it
makes HAPI behave exactly like the upstream image — useful when running
the docker-compose stack without a Keycloak instance available.

```bash
# In deploy/docker/.env
SUBSCRIPTION_SERVICE_AUTH_ENABLED=false
```

### Source layout

```
hapi/
├── Dockerfile              ← multi-stage; builds auth JAR + layers it onto upstream
├── auth/                   ← Maven project; produces the JAR
│   ├── pom.xml
│   └── src/
│       ├── main/java/com/bzonfhir/subscriptionservice/auth/
│       │   ├── AuthAutoConfiguration.java     ← Spring Boot @AutoConfiguration entry
│       │   ├── AuthProperties.java            ← @ConfigurationProperties bound from yaml/env
│       │   ├── JwtValidator.java              ← Nimbus-backed JWT validation
│       │   ├── KeycloakJwtAuthenticationInterceptor.java
│       │   ├── ScopeAuthorizationInterceptor.java
│       │   └── SmartScope.java                ← SMART scope parser
│       ├── main/resources/META-INF/spring/
│       │   └── org.springframework.boot.autoconfigure.AutoConfiguration.imports
│       └── test/...        ← JUnit 5 tests; mock JWKS via Wiremock
```

### What ticket #359 does NOT do (deferred)

- **Multi-tenancy partition mapping** — HAPI's partition context is set
  from the `tenant` claim, but tenant claim → partition wiring is its own
  ticket (#369).
- **Audit logging** of authentication failures beyond Spring INFO logs.
- **SMART user/ and patient/ scopes** — only `system/` is recognized in
  v1 (matches the realm catalog above).
- **JWT introspection** as a fallback for opaque tokens — Keycloak issues
  JWTs natively so no introspection round-trip is needed.

## Reference deployment

> The maintainer's instance runs at `https://keycloak.bzonfhir.com` (a
> Cloudflare-tunneled, legacy WildFly-based Keycloak — set
> `KEYCLOAK_PATH_PREFIX=/auth` when running the provisioning script
> against it). The subscription-service deployment in front of it is at
> `https://subscription-service.bzonfhir.com`. Everything else in this
> document is provider-agnostic; this callout exists only so a reader who
> stumbles into the maintainer's repo knows what the real URLs look like.
