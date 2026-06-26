# Docker Compose deployment

Reference Compose stack for the subscription-service. Used for our own
development on the-deploy-host and as the artifact self-installers (hosting model 4)
consume.

## Layout

```
deploy/docker/
├── docker-compose.yml   ← HAPI, Postgres, Matchbox (IPF comes later)
├── .env.example         ← required environment variables
├── postgres-data/       ← bind-mount target for Postgres (gitignored data)
└── README.md            ← you are here
```

The Compose file references config files outside this directory:

```
hapi/application.yaml    ← mounted into the HAPI container
hapi/igs/*.tgz           ← IG packages loaded at boot (not committed)
matchbox/igs/*.tgz       ← Matchbox IG packages (not committed)
```

`scripts/fetch-igs.sh` at the repo root downloads the IG tarballs from
<https://packages.fhir.org>. Run it once after cloning.

## Stand up (local dev)

```bash
cp .env.example .env             # then edit if you want non-defaults
../../scripts/fetch-igs.sh       # fetch IG packages into hapi/igs/, matchbox/igs/
docker compose up -d
docker compose ps                # confirm all services healthy

# smoke checks
curl -fsS http://localhost:${HAPI_HOST_PORT:-18080}/fhir/metadata | head -c 400
curl -fsS http://localhost:${MATCHBOX_HOST_PORT:-18081}/matchboxv3/fhir/metadata | head -c 400
```

`HAPI_HOST_PORT` defaults to **18080**; Matchbox to **18081**. Both run on
8080 inside their containers — Matchbox v3 exposes FHIR under the
`/matchboxv3/fhir` context path, NOT `/fhir`.

## Stand up (the-deploy-host)

the-deploy-host keeps Postgres data under `${WORKDIR}/subscription-data/postgres` so
it survives worktree rebuilds. Override the bind-mount in `.env`:

```bash
POSTGRES_DATA_DIR=${WORKDIR}/subscription-data/postgres
```

The directory must exist and be writable by the Postgres container user
(uid 999 on Alpine). On the-deploy-host:

```bash
sudo mkdir -p ${WORKDIR}/subscription-data/postgres
sudo chown -R 999:999 ${WORKDIR}/subscription-data/postgres
```

## Tear down

```bash
docker compose down              # stops containers, keeps data
docker compose down --volumes    # also removes named volumes (we use bind-mounts, so this is a no-op)
```

Persistent data lives in the directory pointed at by `POSTGRES_DATA_DIR`
via bind-mount, so a plain `down` never destroys it. To wipe local state,
`rm -rf postgres-data/` (after `down`).

## What's not here yet

- **IPF Spring Boot app** (HL7 v2 MLLP ingestion) — separate ticket.
- **OIDC IdP wiring** (JWT auth on `/fhir/*`) — separate ticket (#359, merged).
- **Feature toggles** referenced in `.env.example`
  (`SUBSCRIPTION_SERVICE_VALIDATION_MODE` etc.) are stubs today; they get
  wired into the HAPI interceptors in tickets #367-#369.
