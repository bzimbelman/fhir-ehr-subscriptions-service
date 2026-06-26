# Authentication & authorization

> Status: Provider-agnostic. Tickets #359 (JWT enforcement), #370/#376
> (provider-agnostic configuration), and #372 (rename interceptor +
> docs to make the OIDC genericity explicit). This document defines the
> *enforcement* layer inside HAPI itself (JWT validation + SMART scope
> mapping to HAPI auth rules) and points at IdP-specific recipes for
> the *identity* side.

## Overview

subscription-service authenticates FHIR API callers via OIDC bearer tokens.
Any OpenID Connect provider that exposes a JWKS endpoint works — including
Keycloak, Auth0, Okta, Azure AD, AWS Cognito, Authentik, etc. The HAPI
interceptor (`OidcJwtAuthenticationInterceptor`) consumes only the standard
OIDC artifacts (issuer URL, JWKS, the `scope` claim); it has no
provider-specific code paths.

Pick whichever IdP you already operate. The realm/tenant provisioning is
on you (this repo ships a turn-key Keycloak realm export under
`idp/keycloak/` because that's the IdP the maintainer runs locally), but
the FHIR API itself doesn't care which IdP signed the token — it just
validates the JWS against the JWKS at the configured issuer and reads the
`scope` claim.

Everything below uses placeholder hostnames (`your-idp.example.com`,
`your-subscription-service.example.com`). Substitute your own. The
reference deployment that this project's maintainer runs is documented in
a single callout at the bottom of this page.

## Configuration

The HAPI auth layer is configured by two environment variables (Spring Boot
relaxed binding maps `SUBSCRIPTION_SERVICE_AUTH_*` env vars onto the
`subscription-service.auth.*` property tree):

| Property                                            | Env var                                | Default                                                                                  |
| --------------------------------------------------- | -------------------------------------- | ---------------------------------------------------------------------------------------- |
| `subscription-service.auth.enabled`                 | `SUBSCRIPTION_SERVICE_AUTH_ENABLED`    | `true`                                                                                   |
| `subscription-service.auth.issuer`                  | `SUBSCRIPTION_SERVICE_AUTH_ISSUER`     | **none — required when auth is enabled** (ticket #370). Container fails fast at startup. |
| `subscription-service.auth.jwks-url`                | `SUBSCRIPTION_SERVICE_AUTH_JWKS_URL`   | derived from issuer: `${issuer}/protocol/openid-connect/certs` (Keycloak shape).         |
| `subscription-service.auth.allow-anonymous-paths`   | (yaml list only)                       | `[/metadata, /.well-known/smart-configuration]`                                          |

For IdPs whose JWKS URL isn't `${issuer}/protocol/openid-connect/certs`
(everything except Keycloak), set `SUBSCRIPTION_SERVICE_AUTH_JWKS_URL`
explicitly to the value advertised under the `jwks_uri` key in the
provider's `.well-known/openid-configuration` document. The recipes below
spell that out per IdP.

When `enabled=true` and `issuer` is empty, the Spring context refresh
fails fast with:

```
subscription-service.auth.issuer is required when auth is enabled.
Set SUBSCRIPTION_SERVICE_AUTH_ISSUER to your OIDC provider's issuer URL
(e.g., https://your-idp.example.com/realms/<realm> for Keycloak,
https://<tenant>.us.auth0.com/ for Auth0,
https://<org>.okta.com/oauth2/default for Okta) or set
SUBSCRIPTION_SERVICE_AUTH_ENABLED=false for local dev.
```

The HAPI container exits non-zero and restarts in a loop — the
docker-compose default for `restart:` is `unless-stopped`, so look for the
message in `docker logs subscription-service-hapi`.

### Disabling for local development

Set `SUBSCRIPTION_SERVICE_AUTH_ENABLED=false` in `.env`. The whole
auto-configuration is gated by `@ConditionalOnProperty`, so disabling it
makes HAPI behave exactly like the upstream image — useful when running
the docker-compose stack without any IdP available.

```bash
# In deploy/docker/.env
SUBSCRIPTION_SERVICE_AUTH_ENABLED=false
```

## Provider recipes

Each recipe gives the operator the 3–5 lines of config needed to point
subscription-service at the named IdP. The HAPI side is the same for all
of them; only the env-var values change.

Verify any recipe by GETing the IdP's discovery document and confirming
that the `issuer` and `jwks_uri` values match what you're setting:

```bash
curl -sS https://your-idp.example.com/path-to-discovery/.well-known/openid-configuration \
  | jq '{issuer, jwks_uri, token_endpoint}'
```

The `issuer` value the IdP advertises is exactly what
`SUBSCRIPTION_SERVICE_AUTH_ISSUER` must equal (string compare, no trailing
slash gymnastics — Nimbus is strict).

### Keycloak (modern, 17+)

Quarkus-based Keycloak, released April 2022, dropped the `/auth/` path
prefix that all earlier releases had. Most new Keycloak deployments are on
this shape.

```bash
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=https://your-keycloak.example.com/realms/subscription-service
# JWKS URL is derived automatically:
# https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/certs
```

Realm provisioning: turn-key script at `scripts/idp/keycloak/provision-realm.sh`
plus the JSON realm export at `idp/keycloak/realms/subscription-service.json`.
See [`docs/idp/keycloak.md`](idp/keycloak.md) for the full setup walkthrough.

### Keycloak (legacy WildFly, <17)

Keycloak releases before 17 (the WildFly-based distribution) mount under
`/auth/`. The realm shape is identical; only the URL prefix differs.

```bash
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=https://your-keycloak.example.com/auth/realms/subscription-service
# JWKS URL is derived automatically:
# https://your-keycloak.example.com/auth/realms/subscription-service/protocol/openid-connect/certs
```

The provisioning script accepts `KEYCLOAK_PATH_PREFIX=/auth` for this
case — see [`docs/idp/keycloak.md`](idp/keycloak.md).

### Auth0

Auth0 issuers always end in a trailing slash (a long-standing Auth0
quirk). Match it exactly. The JWKS path is `.well-known/jwks.json`, not
`protocol/openid-connect/certs`, so you must set it explicitly:

```bash
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=https://your-tenant.us.auth0.com/
SUBSCRIPTION_SERVICE_AUTH_JWKS_URL=https://your-tenant.us.auth0.com/.well-known/jwks.json
```

Provisioning (out of band): in the Auth0 dashboard, create an "API" with
an identifier (audience) of your choice and "Machine to Machine"
applications with the `system/Subscription.crus` etc. scopes added as
permissions on that API. Auth0 puts custom scopes in the `scope` claim of
the issued access token, which is exactly what
`OidcJwtAuthenticationInterceptor` reads.

### Okta

Okta defaults to a "default" authorization server under the path
`/oauth2/default`. Custom auth servers are at `/oauth2/<server-id>`. The
JWKS endpoint is under the auth server, not the org root:

```bash
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=https://your-org.okta.com/oauth2/default
SUBSCRIPTION_SERVICE_AUTH_JWKS_URL=https://your-org.okta.com/oauth2/default/v1/keys
```

Provisioning (out of band): in the Okta admin console, add the SMART
scope catalog (`system/Subscription.crus` etc.) to the authorization
server's scopes, create a service-account-style "API Services
Integration" app, and grant it those scopes. Okta surfaces them in the
`scope` claim on client_credentials access tokens.

### Authentik

Authentik exposes each OAuth2/OIDC provider under
`/application/o/<app-slug>/`. The issuer is the application's base URL;
the JWKS endpoint is under the same path:

```bash
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=https://your-authentik.example.com/application/o/subscription-service/
SUBSCRIPTION_SERVICE_AUTH_JWKS_URL=https://your-authentik.example.com/application/o/subscription-service/jwks/
```

Provisioning (out of band): create an OAuth2/OIDC provider in Authentik
named e.g. `subscription-service`, set the application slug to match the
issuer URL above, add the SMART scopes (`system/Subscription.crus`,
`system/Patient.r`, etc.) as scope mappings, then bind one or more
service-account applications to the provider.

### Other IdPs

Azure AD, AWS Cognito, Google Identity, Ping Identity, ForgeRock, and any
other OIDC-conformant provider follow the same shape. Fetch the
discovery document at
`https://<your-idp>/<path-to-issuer>/.well-known/openid-configuration`,
copy the `issuer` value into `SUBSCRIPTION_SERVICE_AUTH_ISSUER`, and the
`jwks_uri` value into `SUBSCRIPTION_SERVICE_AUTH_JWKS_URL`. The HAPI side
does not need to know which IdP is on the other end.

## Scope catalog

Scopes follow the SMART on FHIR `system/<Resource>.<crud-flags>` naming
convention. They are the same regardless of which IdP issues the token;
each IdP just needs to be configured to put them in the `scope` claim.

| Scope                       | What it grants                                                                                                                                     |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `system/Subscription.crus`  | Create, Read, Update, Search FHIR `Subscription` resources. The base scope an external system needs to register webhook subscriptions.             |
| `system/Subscription.r`     | Read-only access to `Subscription` resources (for monitoring/audit clients that should not create or modify subscriptions).                        |
| `system/Patient.r`          | Read FHIR `Patient` resources. Required for any subscriber that reads patient context after a notification fires.                                  |
| `system/Patient.cruds`      | Full lifecycle (Create, Read, Update, Delete, Search) of `Patient`. Used by trusted ingestion-side services, not typical external subscribers.     |
| `system/Observation.r`      | Read FHIR `Observation` resources. Used by subscribers that consume lab/vitals data delivered through subscription notifications.                  |

New clients onboarded for external systems should follow least-privilege:
assign only the scopes the integration actually needs.

## Obtaining a token (client_credentials)

Every OIDC IdP supports the OAuth2 `client_credentials` grant for M2M
flows. The exact endpoint is in the IdP's discovery document under
`token_endpoint`. Generic shape:

```bash
TOKEN=$(curl -sS -X POST \
  "${TOKEN_ENDPOINT}" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d "client_id=${CLIENT_ID}" \
  -d "client_secret=${CLIENT_SECRET}" \
  -d 'scope=system/Subscription.crus system/Patient.r' \
  | jq -r .access_token)

curl -sS -H "Authorization: Bearer ${TOKEN}" \
  https://your-subscription-service.example.com/fhir/metadata
```

> Auth0 requires an additional `audience` parameter on the token request
> (the identifier of the API you registered).  Okta and Keycloak do not.
> See your IdP's docs for grant-specific knobs.

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
3. Operator creates a new confidential / M2M client in the configured
   IdP:
   - **Client ID**: `subscription-service-<integrator-slug>`
   - **Client authenticator**: client_id + secret (or mTLS, or private_key_jwt
     if the IdP supports it and the integrator can hold a key).
   - **Service accounts / client_credentials grant**: enabled.
   - **Authorization-code / interactive flows**: disabled (M2M only).
   - **Scopes**: the approved set from step 2.
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

The IdP we trust only controls *inbound* access to our FHIR API. When a
`Subscription` fires and we POST to the subscriber's REST-hook endpoint, we
include the `Subscription.header` values verbatim. The subscriber is
responsible for verifying that header (usually a bearer token they generated
and registered with the `Subscription` resource at creation time). See
"Subscription channel security" in `docs/architecture.md` for the policy
controlling what we require on the subscriber side.

## How the FHIR API enforces tokens (ticket #359)

HAPI itself doesn't know anything about the IdP. The enforcement layer is
a small Spring Boot auto-configuration JAR built from `hapi/auth/` and
layered onto the upstream HAPI image (see `hapi/Dockerfile`). At runtime
the auto-configuration registers two HAPI server interceptors:

1. **`OidcJwtAuthenticationInterceptor`** —
   `@Hook(SERVER_INCOMING_REQUEST_POST_PROCESSED)`. For every request:
   - If the path is on the anonymous allow-list (`/metadata`,
     `/.well-known/smart-configuration` by default), pass through.
   - Otherwise, require an `Authorization: Bearer <jwt>` header.
   - Parse and verify the JWS signature against the IdP's JWKS using
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
│       │   ├── OidcJwtAuthenticationInterceptor.java
│       │   ├── ScopeAuthorizationInterceptor.java
│       │   └── SmartScope.java                ← SMART scope parser
│       ├── main/resources/META-INF/spring/
│       │   └── org.springframework.boot.autoconfigure.AutoConfiguration.imports
│       └── test/...        ← JUnit 5 tests; mock JWKS via Wiremock
```

### What ticket #359 does NOT do (deferred)

- **Multi-tenancy partition mapping** — HAPI's partition context is set
  from the `tenant` claim, but tenant claim → partition wiring is its own
  ticket (#369, merged).
- **Audit logging** of authentication failures beyond Spring INFO logs.
- **SMART user/ and patient/ scopes** — only `system/` is recognized in
  v1 (matches the realm catalog above).
- **JWT introspection** as a fallback for opaque tokens — every modern
  IdP issues JWTs natively so no introspection round-trip is needed.

## Legacy WildFly-based Keycloak

> If you point this service at a legacy WildFly-based Keycloak distribution
> (versions before the Quarkus rewrite), set `KEYCLOAK_PATH_PREFIX=/auth`
> when running the provisioning script — older Keycloak serves under `/auth`
> by default while the modern distribution serves at root. The rest of this
> document is provider-agnostic and works with any OIDC IdP that exposes
> JWKS.
