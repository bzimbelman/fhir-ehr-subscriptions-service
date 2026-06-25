# hapi

HAPI FHIR JPA server configuration plus the derived image that layers our
auth interceptors onto the upstream `hapiproject/hapi` image. This
directory holds the `application.yaml`, the IG packages we load at startup
(US Core 7.0, R5 Subscriptions Backport, etc.), and the auth JAR
sub-project.

## Layout

```
hapi/
├── Dockerfile           ← derived image: upstream HAPI + auth JAR (#359)
├── .dockerignore        ← keeps build context minimal
├── application.yaml     ← HAPI config (FHIR R4, Postgres, IG list, etc.)
├── auth/                ← Maven project producing the auth interceptor JAR
│   ├── pom.xml
│   ├── src/             ← Java source + JUnit tests
│   └── README.md
├── ca-cert/             ← optional corporate CA drop-in (gitignored)
└── igs/                 ← IG packages loaded at boot
    ├── hl7.fhir.us.core-7.0.0.tgz
    └── hl7.fhir.uv.subscriptions-backport.r4-1.1.0.tgz
```

## Building the derived image

```bash
docker build -t subscription-service/hapi:dev hapi/
```

The compose stack at `deploy/docker/docker-compose.yml` does this for you
on `docker compose up --build`. See `docs/auth.md` ("How the FHIR API
enforces tokens") for what the auth layer actually does and how to disable
it for local development.
