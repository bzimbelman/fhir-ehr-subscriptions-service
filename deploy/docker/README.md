# Docker Compose deployment

Reference Compose stack for the subscription-service. Used for our own development on zdock and as the artifact self-installers (hosting model 4) consume.

## Layout

```
deploy/docker/
├── docker-compose.yml   ← all services (HAPI, Postgres, Matchbox, IPF)
├── .env.example         ← required environment variables
├── postgres/            ← bind-mount target for HAPI's Postgres data (gitignored)
└── README.md            ← you are here
```

## Stand up

```bash
cp .env.example .env   # then edit values
docker compose up -d
docker compose ps      # confirm all services healthy
```

## Tear down

```bash
docker compose down              # stops containers, keeps data
docker compose down --volumes    # also removes named volumes
```

Persistent data (Postgres) lives in `./postgres/` via bind-mount, so a plain `down` never destroys it.
