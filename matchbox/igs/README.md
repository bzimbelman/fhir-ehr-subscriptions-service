# Matchbox Implementation Guides

Matchbox loads any `*.tgz` packages it finds here at boot. The directory is
bind-mounted into the container at `/app/matchbox/igs` (see
`deploy/docker/docker-compose.yml`).

## How they get here

The tarballs are **not committed** to keep the repo small. Fetch them with:

```bash
./scripts/fetch-igs.sh
```

The script downloads from <https://packages.fhir.org> into both
`matchbox/igs/` and `hapi/igs/`. It is idempotent — files that already
exist are skipped.

## Expected packages

For the initial topology (HAPI v2-to-FHIR transforms via Matchbox), we
expect:

| Package | Version | Notes |
|---------|---------|-------|
| `hl7.fhir.uv.v2-to-fhir` | — | Added in a later ticket alongside the IPF app |

Until the IPF app lands, Matchbox starts with no IGs preloaded — it just
exposes its FHIR endpoint and waits.
