# Demo compose — one-command bridge spin-up

Closes Demo gap-5 from `docs/subscription-sidecar-demo.md`. The
operator-facing walkthrough lives in [`README.md`](README.md); this file
is a focused reference for the compose stack itself.

## One-liner

```sh
cd demo && docker compose up -d
curl -fsS --retry 30 --retry-delay 2 --retry-connrefused http://localhost:8443/readyz
```

`/readyz` returns 200 within ~60s (dominated by Postgres warm-up + bridge
migrations). Tear down with `docker compose down -v`.

## Services

| Service | Image | Host ports | Purpose |
|---|---|---|---|
| `postgres` | `postgres:16-alpine` | `5433` → `5432` | Bridge's storage backend. `pgdata` volume holds `hl7_message_queue`, `resource_changes`, `ehr_events`, `deliveries`, `subscriptions`, audit, and migration tables. |
| `bridge` | `fhir-subs:demo` (built from `../Dockerfile`) | `8443` (API/probes), `2575` (MLLP) | The `cmd/fhir-subs` binary, configured by `demo/config.yaml`. |

The bridge runs migrations against Postgres on boot via
`internal/infra/storage/migrate.Up`, advisory-locked so concurrent
starts don't race (B-33).

## Volume mounts (bridge)

| Host path | Container path | Notes |
|---|---|---|
| `./config.yaml` | `/etc/fhir-subs/config.yaml` | Read-only. Replace to change demo behavior. |
| `./topics` | `/etc/fhir-subs/topics` | Read-only. SubscriptionTopic JSON files seeded by Demo gap-6 (#83). The bridge hot-reloads on `SIGHUP` (B-35). |

## What the demo intentionally does NOT show

(Repeated here so a reader of `docker-compose.yml` doesn't get surprised by `config.yaml`.)

- **Auth.** `auth.audience` is unset, so `cmd/fhir-subs/wiring.go::buildProductionRuntime` mounts a no-op auth middleware. Production needs SMART Backend Services + JWKS.
- **TLS.** `server.http.insecure: true` and `mllp.listeners[0].tls: false`. Production needs TLS / mTLS (audit B-20).
- **Real codec key management.** `config.yaml` pins a single AES-GCM key in cleartext. Production loads keys from a secret store; rotation is a day-one operator task (see `docs/low-level-design/storage.md`).

## Platform notes

- `postgres:16-alpine` is multi-arch (linux/amd64 + linux/arm64), and the bridge image is built from source via `../Dockerfile`, so this compose works on Apple Silicon and amd64 Linux/Mac without a `platform:` override.
- `docker compose build bridge` rebuilds the bridge image after source changes; otherwise compose reuses the cached `fhir-subs:demo` tag.
- The bridge container has no in-container healthcheck (the `gcr.io/distroless/static-debian12:nonroot` base ships no shell, curl, or wget). Operators verify readiness from the host with the `/readyz` curl above.
