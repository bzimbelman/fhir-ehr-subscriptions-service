# Authentication & authorization

> Status: Draft, ticket #358. This document defines the *identity* infrastructure
> for the subscription-service FHIR API. JWT validation and scope-based
> authorization inside HAPI itself are deferred to ticket #359.

## Overview

External systems (and our own internal services) call the subscription-service
FHIR API at `https://subscription-service.bzonfhir.com/fhir/*` using OAuth2
bearer tokens. Tokens are issued by the shared Keycloak instance at
`keycloak.bzonfhir.com`. A dedicated Keycloak realm — `subscription-service` —
houses the clients and scopes for this API; it is isolated from the realms
used by other tools on the same Keycloak server.

This ticket (#358) sets up the realm. Ticket #359 wires HAPI to validate the
tokens this realm issues and to enforce the scopes carried in them.

## The realm

| Property              | Value                                                                              |
| --------------------- | ---------------------------------------------------------------------------------- |
| Realm name            | `subscription-service`                                                             |
| Issuer URL            | `https://keycloak.bzonfhir.com/realms/subscription-service`                        |
| Discovery document    | `https://keycloak.bzonfhir.com/realms/subscription-service/.well-known/openid-configuration` |
| JWKS URL              | `https://keycloak.bzonfhir.com/realms/subscription-service/protocol/openid-connect/certs` |
| Token endpoint        | `https://keycloak.bzonfhir.com/realms/subscription-service/protocol/openid-connect/token` |
| Access token lifespan | 15 minutes                                                                         |
| Refresh token lifespan| 30 minutes (M2M clients are configured not to use refresh tokens)                  |
| SSL required          | `external` (HTTPS required for all non-localhost traffic)                          |

> Note on path prefix: the Cloudflare-tunneled Keycloak instance currently
> exposes its API under `/auth/` (legacy WildFly mount). The URLs above assume
> the v23 path-style (no `/auth/`). If the realm is imported into the legacy
> instance, prepend `/auth` to every Keycloak URL — e.g.
> `https://keycloak.bzonfhir.com/auth/realms/subscription-service/...`. The
> provisioning script (`scripts/keycloak/provision-realm.sh`) accepts a
> `KEYCLOAK_PATH_PREFIX=/auth` env var for that case.

The full realm definition lives at `keycloak/realms/subscription-service.json`
and is intended to be imported via the Keycloak admin UI (Realm settings ->
Action -> Partial import) or via `scripts/keycloak/provision-realm.sh`.

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

## Obtaining a token (client_credentials)

The example confidential client is `subscription-service-cli` with the
placeholder secret `CHANGE-ME-IN-DEPLOYMENT`. After importing the realm, the
operator rotates the secret to a real value (Admin UI -> Clients ->
`subscription-service-cli` -> Credentials -> Regenerate).

To obtain a bearer token via the OAuth2 `client_credentials` grant:

```bash
curl -sS -X POST \
  https://keycloak.bzonfhir.com/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d 'client_secret=CHANGE-ME-IN-DEPLOYMENT' \
  -d 'scope=system/Subscription.crus system/Patient.r' \
  | jq -r .access_token
```

That JWT goes in the `Authorization: Bearer <token>` header on every call to
`https://subscription-service.bzonfhir.com/fhir/*`. Tokens expire after 15
minutes; re-request rather than refreshing (M2M flow does not issue refresh
tokens).

A useful one-liner for capturing the token into a shell variable:

```bash
TOKEN=$(curl -sS -X POST \
  https://keycloak.bzonfhir.com/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d 'client_secret=CHANGE-ME-IN-DEPLOYMENT' \
  | jq -r .access_token)

curl -sS -H "Authorization: Bearer ${TOKEN}" \
  https://subscription-service.bzonfhir.com/fhir/metadata
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

The shared Keycloak server should be reachable from any machine that will run
the provisioning script. Quick sanity check against the master realm (which is
always present):

```bash
curl -sS https://keycloak.bzonfhir.com/auth/realms/master/.well-known/openid-configuration \
  | jq -r .issuer
# Expected: http://keycloak.bzonfhir.com/auth/realms/master
```

(Use `https://keycloak.bzonfhir.com/realms/master/.well-known/...` if the
deployed Keycloak is the v23 path-style instance.)

If that does not return JSON, the Cloudflare tunnel to zdock is down — see
`~/.claude/projects/-Users-bzimbelman-cz/memory/infrastructure.md` for the
tunnel troubleshooting runbook.

## What ticket #358 does NOT do (deferred to #359)

This ticket delivers the identity *infrastructure*: a realm, scopes, an
example client, a provisioning script, and documentation. It does **not**:

- Wire HAPI's `AuthorizationInterceptor` to validate JWTs against this realm's
  JWKS.
- Map SMART scopes (`system/Subscription.crus`, etc.) to HAPI authorization
  rules.
- Reject unauthenticated traffic at the FHIR endpoint.
- Configure HAPI's `tenant` partition mapping from JWT claims (multi-tenancy).
- Set up audit logging of token introspection failures.

Until #359 lands, the FHIR endpoint will still serve unauthenticated requests
(or whatever HAPI's default is). Treat the gap as a temporary state: import
the realm now so the issuer URL is stable, then wire HAPI in #359 with
confidence that the tokens already exist.
