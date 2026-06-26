# Keycloak — IdP recipe

This page documents the Keycloak-specific tooling that ships with
subscription-service: a turn-key realm export and a provisioning script.
It's the deep-dive companion to [`docs/auth.md`](../auth.md), which holds
the provider-agnostic auth contract.

If you're standing up subscription-service against a different IdP
(Auth0, Okta, Authentik, etc.), this page is irrelevant — see the
"Provider recipes" section of `docs/auth.md` for those.

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

The full realm definition lives at
[`idp/keycloak/realms/subscription-service.json`](../../idp/keycloak/realms/subscription-service.json)
and is intended to be imported via the Keycloak admin UI (Realm settings ->
Action -> Partial import) or via
[`scripts/idp/keycloak/provision-realm.sh`](../../scripts/idp/keycloak/provision-realm.sh)
(see [Provisioning the realm](#provisioning-the-realm) below).

The example confidential client shipped in the export is
`subscription-service-cli`. It is granted `system/Subscription.crus` and
`system/Patient.r` as *default* client scopes; the other SMART scopes
defined in [`docs/auth.md`](../auth.md) are *optional* and can be
requested explicitly via the `scope` parameter on the token request.

## Provisioning the realm

`scripts/idp/keycloak/provision-realm.sh` is idempotent — it logs in, GETs
the realm, and only POSTs the import if the realm doesn't already exist.

Modern (Quarkus) Keycloak, with secret automation:

```bash
KEYCLOAK_URL=https://your-keycloak.example.com \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD='<admin-password>' \
KEYCLOAK_CLIENT_SECRET=$(openssl rand -hex 32) \
  scripts/idp/keycloak/provision-realm.sh
```

Legacy WildFly Keycloak (`/auth/` path prefix):

```bash
KEYCLOAK_URL=https://your-keycloak.example.com \
KEYCLOAK_PATH_PREFIX=/auth \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD='<admin-password>' \
KEYCLOAK_CLIENT_SECRET=$(openssl rand -hex 32) \
  scripts/idp/keycloak/provision-realm.sh
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

If you used `KEYCLOAK_CLIENT_SECRET` during provisioning, use that value
below; otherwise rotate the `CHANGE-ME-IN-DEPLOYMENT` placeholder in the
admin UI (Clients -> `subscription-service-cli` -> Credentials -> Regenerate)
first.

```bash
TOKEN=$(curl -sS -X POST \
  https://your-keycloak.example.com/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d "client_secret=${CLIENT_SECRET}" \
  -d 'scope=system/Subscription.crus system/Patient.r' \
  | jq -r .access_token)

curl -sS -H "Authorization: Bearer ${TOKEN}" \
  https://your-subscription-service.example.com/fhir/metadata
```

Tokens expire after 15 minutes; re-request rather than refreshing (the M2M
flow does not issue refresh tokens).

## Onboarding additional clients

In the Keycloak admin UI for the `subscription-service` realm:

- **Client ID**: `subscription-service-<integrator-slug>`
- **Client authenticator**: `Client Id and Secret`
- **Service accounts**: enabled
- **Standard flow / Direct access grants**: disabled (M2M only)
- **Default client scopes**: the approved minimum-privilege set
- Regenerate the client secret and share it out-of-band

See [`docs/auth.md`](../auth.md) "Onboarding an external system" for the
operator process around this (review, scope choice, secret rotation cadence).

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
[`docs/keycloak-quickstart.md`](../keycloak-quickstart.md).
