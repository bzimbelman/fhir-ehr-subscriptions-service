# HAPI Implementation Guides

HAPI loads any `*.tgz` IG packages it finds here at boot. The mount is
configured in `deploy/docker/docker-compose.yml` (`/app/igs` inside the
container), and the specific packages to install are declared in
`hapi/application.yaml` under `hapi.fhir.implementationguides`.

## Expected packages

| Package | Version | Source |
|---------|---------|--------|
| `hl7.fhir.us.core` | 7.0.0 | <https://packages.fhir.org/hl7.fhir.us.core/7.0.0> |
| `hl7.fhir.uv.subscriptions-backport.r4` | 1.1.0 | <https://packages.fhir.org/hl7.fhir.uv.subscriptions-backport.r4/1.1.0> |

The R4 variant of the Backport IG (`.r4` suffix) is what we use; the
unsuffixed `hl7.fhir.uv.subscriptions-backport` package declares
`fhirVersions: ["4.3.0"]` (R4B) and HAPI's package installer rejects it
against this server's R4 (4.0.1) context. Same content, different
metadata.

## How they get here

The tarballs themselves are **not committed** — they're typically a couple of
megabytes each, version-pinned externally, and easy to refetch:

```bash
./scripts/fetch-igs.sh
```

The script writes to both `hapi/igs/` and `matchbox/igs/`, is idempotent
(skips files that already exist), and uses the FHIR package registry
(<https://packages.fhir.org>).

## Adding a new IG

1. Drop the `.tgz` into `hapi/igs/`.
2. Add a stanza to `hapi.fhir.implementationguides` in `hapi/application.yaml`
   with the package name, version, and `packageUrl: file:///app/igs/<file>.tgz`.
3. Add the URL to `scripts/fetch-igs.sh` so future fresh checkouts get it.
4. Restart the `hapi` service: `docker compose restart hapi`.
