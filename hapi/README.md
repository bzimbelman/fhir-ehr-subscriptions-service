# hapi

HAPI FHIR JPA server configuration. The image is the upstream `hapiproject/hapi`; this directory holds the `application.yaml` and the IG packages we load at startup (US Core 7.0, R5 Subscriptions Backport, etc.).

## Layout

```
hapi/
├── application.yaml     ← HAPI config (FHIR R4, Postgres, IG list, etc.)
└── igs/                 ← IG packages loaded at boot
    ├── hl7.fhir.us.core-7.0.0.tgz
    └── hl7.fhir.uv.subscriptions-backport-X.Y.Z.tgz
```
