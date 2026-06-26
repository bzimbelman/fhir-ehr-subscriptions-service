# Keycloak quickstart (smoke test)

> Keycloak-specific. See [`docs/auth.md`](./auth.md) for the
> provider-agnostic auth contract and [`docs/idp/keycloak.md`](./idp/keycloak.md)
> for the full Keycloak setup walkthrough.

A self-contained recipe for spinning up a modern (Quarkus) Keycloak in a
container, provisioning the `subscription-service` realm via
[`scripts/idp/keycloak/provision-realm.sh`](../scripts/idp/keycloak/provision-realm.sh),
and confirming end-to-end that the realm issues usable bearer tokens.

The whole loop fits in a single shell session and runs in ~30 seconds on a
laptop. It's the canonical way to validate that the provisioning script
works against a stranger's Keycloak — i.e. the portability assertion that
ticket #376 makes.

## 1. Start Keycloak in a container

```bash
docker run -d --name kc-smoketest \
  -e KEYCLOAK_ADMIN=admin \
  -e KEYCLOAK_ADMIN_PASSWORD=admin \
  -p 8888:8080 \
  quay.io/keycloak/keycloak:24.0 start-dev
```

That gets you a Quarkus Keycloak on `http://localhost:8888` with master
realm admin credentials `admin / admin`. The `start-dev` profile disables
the TLS-required check so plain HTTP is fine for the smoke test.

Wait for it to come up:

```bash
until curl -sf http://localhost:8888/realms/master/.well-known/openid-configuration >/dev/null; do
  sleep 1
done
echo "Keycloak ready."
```

## 2. Provision the realm

From the repo root:

```bash
export CLIENT_SECRET=$(openssl rand -hex 32)

KEYCLOAK_URL=http://localhost:8888 \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD=admin \
KEYCLOAK_CLIENT_SECRET="${CLIENT_SECRET}" \
  scripts/idp/keycloak/provision-realm.sh
```

Expected output:

```
==> Substituted client secret on temp file (/tmp/provision-realm.<rand>.json).
==> Keycloak base URL:  http://localhost:8888
==> Realm to provision: subscription-service
...
==> Realm 'subscription-service' imported successfully (HTTP 201).
    Issuer URL: http://localhost:8888/realms/subscription-service
    JWKS:       http://localhost:8888/realms/subscription-service/protocol/openid-connect/certs
    Token URL:  http://localhost:8888/realms/subscription-service/protocol/openid-connect/token
```

Re-run the same command — the second invocation should exit 0 with
`Realm 'subscription-service' already exists. No changes made.`. That's
the idempotency guarantee.

## 3. Confirm the discovery document

```bash
curl -sS http://localhost:8888/realms/subscription-service/.well-known/openid-configuration \
  | jq '{issuer, token_endpoint, jwks_uri}'
```

Expected:

```json
{
  "issuer": "http://localhost:8888/realms/subscription-service",
  "token_endpoint": "http://localhost:8888/realms/subscription-service/protocol/openid-connect/token",
  "jwks_uri": "http://localhost:8888/realms/subscription-service/protocol/openid-connect/certs"
}
```

## 4. Grab a token via client_credentials

```bash
TOKEN=$(curl -sS -X POST \
  http://localhost:8888/realms/subscription-service/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials' \
  -d 'client_id=subscription-service-cli' \
  -d "client_secret=${CLIENT_SECRET}" \
  | jq -r .access_token)

echo "${TOKEN}" | cut -d. -f2 | base64 -d 2>/dev/null | jq '{iss, azp, scope, exp}'
```

You should see `iss` matching the realm issuer URL, `azp` =
`subscription-service-cli`, and `scope` including `system/Subscription.crus
system/Patient.r` (the default scopes assigned to the example client in the
realm export).

## 5. (Optional) Point HAPI at it

To verify HAPI accepts the tokens, wire the docker-compose stack to this
Keycloak. In `deploy/docker/.env`:

```
SUBSCRIPTION_SERVICE_AUTH_ENABLED=true
SUBSCRIPTION_SERVICE_AUTH_ISSUER=http://host.docker.internal:8888/realms/subscription-service
```

> The `host.docker.internal` hostname is what containers use to reach the
> host's `localhost`. On Linux you may need to add
> `--add-host=host.docker.internal:host-gateway` to the HAPI service or use
> the host's LAN IP instead.

Then `docker compose up -d hapi` and:

```bash
curl -sS -H "Authorization: Bearer ${TOKEN}" http://localhost:18080/fhir/metadata \
  | jq '.resourceType, .fhirVersion'
```

`200 OK` with a `CapabilityStatement` confirms the round-trip works.

## 6. Tear down

```bash
docker rm -f kc-smoketest
```

## Legacy WildFly Keycloak

The recipe above uses a modern Quarkus image. For a legacy WildFly-based
Keycloak (Keycloak < 17), substitute the image and add the path-prefix env
var to the provisioning step:

```bash
docker run -d --name kc-smoketest-legacy \
  -e KEYCLOAK_USER=admin -e KEYCLOAK_PASSWORD=admin \
  -p 8889:8080 jboss/keycloak:16.1.1

# ... wait for it ...

KEYCLOAK_URL=http://localhost:8889 \
KEYCLOAK_PATH_PREFIX=/auth \
KEYCLOAK_ADMIN_USER=admin \
KEYCLOAK_ADMIN_PASSWORD=admin \
KEYCLOAK_CLIENT_SECRET=$(openssl rand -hex 32) \
  scripts/idp/keycloak/provision-realm.sh
```

All the discovery / token URLs gain the `/auth/` segment correspondingly.
The realm JSON itself is identical — the path-prefix is purely a server-side
mount, not a property of the realm.
