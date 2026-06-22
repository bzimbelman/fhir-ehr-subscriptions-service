# Realstack test harness — operator quick-start

<!-- docs-lint:ignore-port=5433 -->
<!-- 5433 above is the recommended SSH-tunnel port on the operator's
     laptop when forwarding zdock's Postgres to localhost. The binary
     itself never opens 5433 — it only consumes the tunneled URL. -->


The realstack harness boots the production `cmd/fhir-subs` binary
against a stack of real-software dependencies (Postgres, Keycloak,
HAPI FHIR, Mailpit, plus a pair of subscriber binaries) and runs the
e2e tests under build tag `e2e_realstack`. Two modes are supported,
selected at boot time by environment variables.

> **CI gate (OP #347):** the `realstack` job in
> `.github/workflows/integration.yml` runs this harness on every push
> to `main`, on `workflow_dispatch`, and on PRs that carry the
> `full-e2e` label. Local-containers mode is used in CI (no
> `FHIR_SUBS_TEST_*` env vars are set on the runner).

This document is the operator-facing walkthrough for both modes. The
internal architecture is documented in
[`e2e/realstack/doc.go`](../e2e/realstack/doc.go); the per-finding
strategy is documented in
[`docs/e2e-coverage-strategy.md`](e2e-coverage-strategy.md) §3.H1.

## Mode 1 — local containers (default)

Use this when you have a fresh laptop and want to run the harness
without any extra setup. The harness brings up Postgres, Keycloak, and
HAPI FHIR locally as part of its docker-compose stack.

```bash
# From the repo root.
go test -tags e2e_realstack -count=1 ./e2e/realstack/...
```

What happens internally:

- The harness picks a unique compose project name per test (`realstack-<hex>`).
- Activates the `external-local` compose profile so the
  `postgres`, `keycloak`, and `hapi-fhir` services come up alongside
  Mailpit and the receivers.
- Polls each service's healthcheck until it reports ready.
- Provisions a Keycloak realm + client via Keycloak's REST API.
- Builds and launches `cmd/fhir-subs` as a real child process pointing
  at the locally-spawned dependencies.
- Tears the whole stack down on test exit (`docker compose down -v`).

Trade-off: each test boot is ~30-60s of container startup. Running the
full suite end-to-end takes minutes.

## Mode 2 — externally-managed dependencies (e.g. zdock)

Use this when you have shared infrastructure already running — a
zdock laptop, a dev cluster, or any other long-lived deployment of
the three services. The harness skips spinning up its own
postgres/keycloak/hapi containers and points the production binary at
the URLs you supply.

Set all three env vars before invoking `go test`:

```bash
export FHIR_SUBS_TEST_DB_URL=postgres://fhirsubs:fhirsubs@localhost:5433/fhirsubs?sslmode=disable
export FHIR_SUBS_TEST_FHIR_URL=https://hapi.bzonfhir.com/fhir
export FHIR_SUBS_TEST_OIDC_ISSUER_URL=https://keycloak.bzonfhir.com/realms/fhir-subs

go test -tags e2e_realstack -count=1 ./e2e/realstack/...
```

What changes:

- The harness does NOT activate the `external-local` compose profile.
  `docker compose ps` against the harness project will show only
  Mailpit + the receiver containers + (when `EnableMLLP` is set) the
  MLLP control plane.
- `Stack.Postgres.URL`, `Stack.HAPIFHIR.BaseURL`,
  `Stack.Keycloak.IssuerURL` are populated directly from the env
  values.
- `provisionKeycloak` runs against the externally-managed Keycloak's
  admin API and creates the `fhir-subs` realm + `fhir-subs-test`
  client there. Re-running the harness against the same Keycloak is
  idempotent (409 "realm exists" is treated as success).
- `provisionKeycloakAuthClient` upserts an `auth_clients` row in the
  externally-managed Postgres so the binary's verifier accepts tokens
  the harness mints via the Keycloak client_credentials grant.

### Walkthrough — pointing at zdock

zdock runs a long-lived MELD/Keycloak/HAPI stack accessible via
Cloudflare tunnels. To run the harness against zdock:

1. **Start the tunnel watchdog** (auto-heals on disconnect, see
   [`docs/operations/local_watchdog.md`](operations/local_watchdog.md)).

   ```bash
   ~/cz/claude-helper-cli/scripts/local-watchdog.sh --status
   ```

   If the tunnel is green you'll see `keycloak.bzonfhir.com` reachable
   on `https`. If not, start it via
   `~/cz/claude-helper-cli/scripts/local-watchdog.sh --start` (or
   directly from zdock with
   `systemctl --user start cloudflared-meld.service`).

2. **Open the Postgres tunnel** so the harness can run migrations and
   seed `auth_clients`. The exact form depends on your zdock setup —
   if you expose Postgres via a port-forward (e.g. `localhost:5433`):

   ```bash
   ssh -L 5433:localhost:5432 zman@zdock
   ```

   Then point `FHIR_SUBS_TEST_DB_URL` at `localhost:5433` with the
   credentials zdock's compose file uses.

3. **Confirm HAPI is reachable**:

   ```bash
   curl -s https://hapi.bzonfhir.com/fhir/metadata | head -c 200
   ```

4. **Confirm Keycloak's issuer endpoint is reachable**:

   ```bash
   curl -s https://keycloak.bzonfhir.com/realms/fhir-subs/.well-known/openid-configuration | head -c 200
   ```

   (If the realm doesn't exist yet, `provisionKeycloak` will create
   it on first run.)

5. **Export the env vars and run the harness**:

   ```bash
   export FHIR_SUBS_TEST_DB_URL=postgres://fhirsubs:fhirsubs@localhost:5433/fhirsubs?sslmode=disable
   export FHIR_SUBS_TEST_FHIR_URL=https://hapi.bzonfhir.com/fhir
   export FHIR_SUBS_TEST_OIDC_ISSUER_URL=https://keycloak.bzonfhir.com/realms/fhir-subs

   go test -tags e2e_realstack -count=1 -run TestRealStack_BootsFullStackUnder90s ./e2e/realstack/...
   ```

   The first run will be faster than the local-containers path because
   Keycloak + HAPI + Postgres are already running.

### Partial mixes are not supported

`ParseExternalSystemConfig` rejects any environment where one or two
of the three vars are set:

```text
realstack: external-system env vars must be all-set or all-unset;
set=FHIR_SUBS_TEST_DB_URL, missing=FHIR_SUBS_TEST_FHIR_URL,FHIR_SUBS_TEST_OIDC_ISSUER_URL.
v1 of the env-gate does not support partial mixes (file a follow-up
story if you need it)
```

If you hit this and need a real partial-mix path (e.g. external
Keycloak but local Postgres), file a follow-up story rather than
papering over it with a manual workaround. The constraint is
deliberate — it keeps the matrix of "what does the harness actually
talk to" small enough to reason about per test run.

## Verifying the env-gate

A test in
[`e2e/realstack/external_systems_handles_test.go`](../e2e/realstack/external_systems_handles_test.go)
(`TestRealStack_ExternalSystemsModeUsesEnvSuppliedURLs`) drives
exactly the external-mode path end-to-end: it brings up a side-stack
under the `external-local` profile in a dedicated compose project,
points the harness at it via env vars, and asserts:

- `Stack.UsesExternalSystems()` returns `true`.
- The rendered binary config references the external Postgres URL.
- The harness's own compose project has NO postgres/keycloak/hapi
  containers (the profile was skipped).
- The binary's `/readyz` reports 200, proving migrations and Keycloak
  provisioning ran end-to-end against the external dependencies.

## Compose-only operator workflows

For interactive exploration outside Go tests:

```bash
# Bring up everything (default profile only — receivers + mailpit + binary helpers).
make e2e-realstack-up

# Bring up the full local stack including postgres/keycloak/hapi.
docker compose -f e2e/realstack/docker-compose.yml -p realstack \
  --profile external-local up -d --wait

# Tear down.
make e2e-realstack-down
```

## Troubleshooting

**Symptom:** `realstack: external-system env vars must be all-set or all-unset`

- One or two of the three env vars are set. Check
  `env | grep FHIR_SUBS_TEST_` and either set all three or unset the
  ones that leaked from a parent shell.

**Symptom:** `dial external Keycloak keycloak.bzonfhir.com:443: ...`

- The Cloudflare tunnel is down. Run
  `~/cz/claude-helper-cli/scripts/local-watchdog.sh --status` and let
  the watchdog heal it, or restart manually (see
  [`docs/operations/local_watchdog.md`](operations/local_watchdog.md)).

**Symptom:** `keycloak admin token: 401`

- The externally-managed Keycloak's admin user/password is not
  `admin/admin`. The harness assumes the dev-mode default; if your
  external Keycloak runs with a different admin password you'll need
  to extend `provisionKeycloak` to read it from an env var (file a
  follow-up story).

**Symptom:** `pgx connect ... no such host`

- The Postgres URL points at a host the test process can't resolve.
  If you're using zdock's Postgres via SSH tunnel, confirm the tunnel
  is alive: `lsof -i :5433` should show the ssh process listening.
