# Multi-tenancy

> Implementation of the multi-tenancy section in [`architecture.md`](architecture.md).
> Ticket: #369. Status: merged 2026-06-26.

The subscription service can run in two modes, controlled by the
`SUBSCRIPTION_SERVICE_MULTITENANCY` environment variable. Both modes use the same
container image, the same Helm chart, and the same Postgres schema — only the runtime
behaviour differs.

## Modes

| Mode       | Env var value         | URL shape           | Tenant comes from        | Schema           |
|------------|-----------------------|---------------------|--------------------------|------------------|
| `disabled` | `disabled` (default)  | `/fhir/Patient/123` | n/a (always `DEFAULT`)   | `partition_id` column present, all rows = `DEFAULT` |
| `enabled`  | `enabled`             | `/fhir/Patient/123` | `tenant` claim on JWT    | `partition_id` column populated per tenant |

The URL shape is deliberately identical in both modes. Tenant identity NEVER appears in the
URL path. The starter's URL-based tenant strategy (`/fhir/{tenant}/Patient/...`) is
unregistered at boot — see `MultitenancyAutoConfiguration`.

### Disabled mode (default)

Every request resolves to HAPI's reserved `DEFAULT` partition. From a subscriber's
perspective the server is single-tenant and partitions are invisible. The `partition_id`
column in Postgres exists but only holds the DEFAULT partition's id.

Use this for:

- Hosting model (1) — self-hosted, one facility per deployment.
- Hosting model (4) — local download for personal use.

### Enabled mode

Every request must carry a JWT with a non-empty `tenant` claim (or whatever claim
`subscription-service.multitenancy.tenant-claim` is configured to read). The interceptor
maps that claim value to a HAPI partition of the same name. Resources, Subscriptions, and
notifications are scoped to that partition automatically.

Missing or blank `tenant` claim => HTTP **403 Forbidden** with message
`"tenant claim required when multitenancy enabled"`.

Use this for:

- Hosting model (2) — managed cloud, many tenants on one deployment.
- Hosting model (3) — public sandbox, one partition per signup.

## JWT claim shape

The default claim name is `tenant`. It MUST be a non-empty string. Recommended shape:

```jsonc
{
  "iss": "https://keycloak.bzonfhir.com/auth/realms/subscription-service",
  "sub": "service-account-acme-hospital",
  "azp": "acme-hospital",
  "scope": "system/Patient.cruds system/Subscription.crus",
  "tenant": "acme-hospital",          // <-- the partition name
  "iat": 1750000000,
  "exp": 1750003600
}
```

The claim value becomes the partition name verbatim (after trimming whitespace). Keep it
DNS-label-friendly: ASCII letters, digits, hyphens. Examples: `acme-hospital`, `globex`,
`memorial-east`. Avoid spaces, dots, slashes, or anything an IdP or HTTP layer might
URL-encode en route.

To change the claim name (e.g. to align with an existing claim mapping in your IdP), set:

```yaml
subscription-service:
  multitenancy:
    mode: enabled
    tenant-claim: org_id   # default is "tenant"
```

or `SUBSCRIPTION_SERVICE_MULTITENANCY_TENANT_CLAIM=org_id`.

## Operator workflow: provisioning a new tenant

Adding a new tenant in `enabled` mode is two steps and **no schema changes**.

### 1. Create the HAPI partition

HAPI exposes the partition management operation at `$partition-management-create-partition`
on the system level. With an admin/operator token:

```bash
TENANT=acme-hospital
TOKEN=...   # operator-scoped JWT

curl -X POST "https://subscription-service.bzonfhir.com/fhir/\$partition-management-create-partition" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      { "name": "id",   "valueInteger": 42 },
      { "name": "name", "valueCode": "'"${TENANT}"'" }
    ]
  }'
```

The `id` is an internal integer key (HAPI assigns it; you choose any unused value above 0).
The `name` is what the JWT claim must match.

### 2. Create the IdP client + claim mapper

Configure your OIDC IdP to issue a `tenant` claim with the partition name on the access
tokens it mints for this tenant's client. The exact mechanism is IdP-specific; the
intent is the same across providers.

- **Keycloak**: create a client `subscription-service-${TENANT}` (Client Credentials
  flow), add a "Hardcoded claim" client mapper with Token claim name `tenant` and Claim
  value `${TENANT}`, Claim JSON Type String, Add to access token ON. Assign the SMART
  scopes (`system/Patient.cruds`, `system/Subscription.crus`, etc.) that this tenant
  should have.
- **Auth0**: create a Machine-to-Machine application, then use an Action on the
  Client Credentials Exchange flow to call
  `api.accessToken.setCustomClaim('tenant', '${TENANT}')`. Grant the scopes via the
  API's permissions.
- **Okta**: create an API Services Integration, add a token-claim customization on the
  authorization server with a literal value for `${TENANT}`, and grant the SMART scopes.
- **Authentik / others**: use the IdP's equivalent of a "static claim mapper".

The client's tokens will now include `"tenant": "acme-hospital"` automatically, and every
FHIR request signed with one of those tokens will land in the `acme-hospital` partition.

## Worked example: tenant isolation

Suppose two tenants `acme` and `globex` are provisioned per the steps above. Each has its
own IdP client/application and matching `tenant` claim.

```bash
# Token A: tenant=acme
TOKEN_ACME=$(curl ... | jq -r .access_token)
# Token B: tenant=globex
TOKEN_GLOBEX=$(curl ... | jq -r .access_token)

# 1. Acme creates a Patient. Lands in partition `acme`.
curl -X POST https://subscription-service.bzonfhir.com/fhir/Patient \
  -H "Authorization: Bearer ${TOKEN_ACME}" \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Patient","name":[{"family":"Smith"}]}'
# => 201 Created, Patient/<acme-id>

# 2. Globex tries to read the Acme patient.
curl https://subscription-service.bzonfhir.com/fhir/Patient/<acme-id> \
  -H "Authorization: Bearer ${TOKEN_GLOBEX}"
# => 404 Not Found  (HAPI doesn't leak existence across partitions)

# 3. Acme reads its own patient.
curl https://subscription-service.bzonfhir.com/fhir/Patient/<acme-id> \
  -H "Authorization: Bearer ${TOKEN_ACME}"
# => 200 OK
```

Tenants cannot see each other's resources. Subscription notifications are also
partition-scoped: an `acme` subscription only fires on `acme` events.

## Configuration reference

`subscription-service.multitenancy.*` properties bound from `application.yaml` / env vars:

| Property                   | Env var                                       | Default    | Description                                              |
|----------------------------|-----------------------------------------------|------------|----------------------------------------------------------|
| `mode`                     | `SUBSCRIPTION_SERVICE_MULTITENANCY`           | `disabled` | `disabled` or `enabled`.                                 |
| `tenant-claim`             | `SUBSCRIPTION_SERVICE_MULTITENANCY_TENANT_CLAIM` | `tenant`   | JWT claim name to read in `enabled` mode.                |
| `test-mode`                | `SUBSCRIPTION_SERVICE_MULTITENANCY_TEST_MODE` | `false`    | **TEST ONLY** — see below.                               |

And in `hapi/application.yaml`, the structural toggle that makes the schema partition-aware
even in `disabled` mode:

```yaml
hapi:
  fhir:
    partitioning:
      partitioning_include_in_search_hashes: false
      allow_references_across_partitions: false
      conditional_create_duplicate_identifiers_enabled: false
```

These are intentionally always on. Removing them would drop `partition_id` from the schema
and turn a "switch from disabled to enabled" into a data migration.

## TEST ONLY: header-based tenant override

`SUBSCRIPTION_SERVICE_MULTITENANCY_TEST_MODE=true` (or `subscription-service.multitenancy.test-mode: true`)
makes the interceptor read the tenant from the `X-Test-Tenant` HTTP header instead of the
JWT. This exists exclusively so the e2e test suite can demonstrate tenant isolation
without spinning up a full OIDC IdP; setting it on a production deployment lets any client
choose its own tenant by sending a header.

**NEVER enable this in production.**

The interceptor logs a loud `WARN` on every startup when this flag is set:

```
*** SUBSCRIPTION_SERVICE_MULTITENANCY_TEST_MODE IS ENABLED *** Tenant will be read from the 'X-Test-Tenant' request header, BYPASSING JWT validation.
```

## What this implementation does NOT include

Deferred to follow-up tickets (see `architecture.md` for the rationale):

- Per-tenant resource quotas, rate limits, or storage limits.
- A tenant-management UI.
- Cross-tenant queries / admin-wide views.
- Automated tenant provisioning. (Both steps above are manual / scripted today.)
