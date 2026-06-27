# Docker Compose deployment

Reference Compose stack for subscription-service. Use this for local development, demos, single-node deployments, and as a starting point for self-installers who want to run the system in their own Docker environment.

For Kubernetes deployments, see [`../k8s/`](../k8s/) instead.

## Layout

```
deploy/docker/
├── docker-compose.yml   ← five services (Postgres, HAPI, Matchbox, interface-engine, operator UI)
├── .env.example         ← environment variables
├── postgres-data/       ← bind-mount target for Postgres data (gitignored)
└── README.md            ← you are here
```

The Compose file references config files outside this directory:

```
hapi/application.yaml    ← mounted into the HAPI container
hapi/igs/*.tgz           ← FHIR IG packages loaded at boot (not committed)
matchbox/igs/*.tgz       ← Matchbox IG packages (not committed)
```

`scripts/fetch-igs.sh` at the repo root downloads the IG tarballs from <https://packages.fhir.org>. Run it once after cloning, or after bumping a pinned IG version.

## Stand up

```bash
cp .env.example .env             # edit if you want non-defaults
../../scripts/fetch-igs.sh       # fetch IG packages into hapi/igs/, matchbox/igs/
docker compose up -d
docker compose ps                # confirm all services are healthy
```

Smoke checks:

```bash
curl -fsS http://localhost:${HAPI_HOST_PORT:-18080}/fhir/metadata | head -c 400
curl -fsS http://localhost:${MATCHBOX_HOST_PORT:-18081}/matchboxv3/fhir/metadata | head -c 400
```

Default host ports:

- HAPI FHIR: `18080`
- Matchbox: `18081`
- Interface engine HTTP (actuator): `18090`
- Interface engine MLLP: `2575`
- Operator UI: `3000`

Matchbox v3 exposes FHIR under the `/matchboxv3/fhir` context path, NOT `/fhir`.

The operator UI (Next.js + NextAuth) is built locally from `../../ui/` and
proxies all admin API calls through `/api/admin/*` server routes so the
bearer token stays out of the browser. Set `OIDC_ISSUER`, `OIDC_CLIENT_ID`,
`OIDC_CLIENT_SECRET`, and `AUTH_SECRET` in `.env` to wire real sign-in;
until then the UI boots and `/signin` shows a "configure OIDC first" banner.

## Persistent data

Postgres data lives in the host directory pointed at by `POSTGRES_DATA_DIR` (default `./postgres-data` — relative to this directory). For longer-lived deployments, point this at a directory outside the repo:

```bash
# in your .env
POSTGRES_DATA_DIR=${HOME}/subscription-service-data/postgres
```

Whatever path you pick:

- The directory must exist before `docker compose up`
- It must be writable by the Postgres container's user (uid 999 on the alpine image — `sudo chown -R 999:999 <path>` if you hit a permission error)

## Tear down

```bash
docker compose down              # stops containers; bind-mounted data survives
docker compose down --volumes    # we use bind-mounts not named volumes, so this is the same
rm -rf postgres-data/            # wipe the local Postgres data (after down)
```

## Exposing the FHIR endpoint to the outside

The Compose stack listens on the host ports above. To make the FHIR API reachable from outside the host — for subscribers across a VPN, the internet, etc. — put a reverse proxy in front of HAPI's port. See [`../../docs/deployment-recipes/`](../../docs/deployment-recipes/) for concrete recipes (Cloudflare tunnel, Caddy, Traefik, nginx).

MLLP is plain TCP — most HTTP-only proxies won't carry it. MLLP ingress is intentionally LAN/VPN-only for the first cut.

## Configuration toggles

All feature toggles are environment variables in `.env`. See `.env.example` for the catalogue:

- `SUBSCRIPTION_SERVICE_AUTH_ENABLED` — OIDC JWT enforcement on `/fhir/*`. Defaults to `false` for quickstart; flip to `true` once you have an OIDC issuer configured.
- `SUBSCRIPTION_SERVICE_VALIDATION_MODE` — US Core profile validation: `off` / `warn` / `enforce`.
- `SUBSCRIPTION_SERVICE_CHANNEL_SECURITY` — policy for Subscription channels: `strict` / `relaxed` / `permissive`.
- `SUBSCRIPTION_SERVICE_MULTITENANCY` — HAPI partition-based isolation: `disabled` / `enabled`.

See [`../../docs/architecture.md`](../../docs/architecture.md) for the full design rationale of each toggle, and [`../../docs/auth.md`](../../docs/auth.md) for OIDC provider setup.
