# Operator UI end-to-end tests

Playwright tests for the subscription-service operator console (Epic #398,
ticket #424).

The same test bodies run against:

- **Local** (default): the docker-compose stack at `http://localhost:3000`.
- **Public** (`PLAYWRIGHT_BASE_URL=https://subscription-service-ui.bzonfhir.com`):
  the maintainer's hosted reference deployment landed by ticket #423.

## What's covered

- **8 per-page smoke tests** (`pages/*.spec.ts`): one per primary nav route
  (`/dashboard`, `/interfaces`, `/messages`, `/dlq`, `/subscriptions`,
  `/matchbox`, `/settings`, `/audit`). Each asserts the page renders its
  identifying header plus the primary nav and main landmarks.
- **1 multi-user session-isolation test**
  (`multi-user/session-isolation.spec.ts`): two browser contexts, two
  distinct Keycloak users, signs A out, asserts B remains authenticated.

Per-page tests reuse a shared NextAuth cookie jar built by `helpers/auth.setup.ts`,
which signs in once via Keycloak and persists `storageState` to
`playwright/.auth/opsa.json`. The multi-user test does NOT reuse storage state
-- it creates fresh contexts so the isolation guarantee is provable.

## Prerequisites

1. The full subscription-service stack must be reachable at
   `PLAYWRIGHT_BASE_URL` (default `http://localhost:3000`). For local runs:

   ```bash
   cd deploy/docker
   cp .env.example .env       # if you don't have one yet; then fill OIDC_* + AUTH_SECRET
   docker compose up -d
   docker compose ps          # wait for hapi, interface-engine, ui to be (healthy)
   ```

2. Two distinct Keycloak users must exist in the configured realm (default
   realm: `Development`):

   - `TEST_USER_A_USERNAME` / `TEST_USER_A_PASSWORD`
   - `TEST_USER_B_USERNAME` / `TEST_USER_B_PASSWORD`

   These are read from process env. The Epic-398 dev convention is `opsa` and
   `opsb`; if those don't exist yet in your realm, provision them via
   `kcadm.sh` or the Keycloak admin UI before running this suite.

3. The OIDC client registered with the IdP must include
   `${PLAYWRIGHT_BASE_URL}/api/auth/callback/oidc` in its redirect URIs.

## Running

```bash
cd ui
pnpm install
pnpm e2e:install            # one-time: downloads the chromium browser
export TEST_USER_A_USERNAME=opsa
export TEST_USER_A_PASSWORD='<opsa password>'
export TEST_USER_B_USERNAME=opsb
export TEST_USER_B_PASSWORD='<opsb password>'
pnpm e2e                    # default: http://localhost:3000
```

For the public host:

```bash
PLAYWRIGHT_BASE_URL=https://subscription-service-ui.bzonfhir.com pnpm e2e
```

Other useful commands:

```bash
pnpm e2e:headed             # watch the browser drive itself
pnpm e2e:ui                 # Playwright's interactive test runner
```

## Layout

```
ui/e2e/
├── playwright.config.ts             # base URL, projects, storage-state wiring
├── helpers/
│   ├── auth.setup.ts                # signs in once per user; writes storageState
│   └── keycloak-login.ts            # programmatic Keycloak login helper
├── pages/                           # 1 test per UI page
│   ├── audit.spec.ts
│   ├── dashboard.spec.ts
│   ├── dlq.spec.ts
│   ├── interfaces.spec.ts
│   ├── matchbox.spec.ts
│   ├── messages.spec.ts
│   ├── settings.spec.ts
│   └── subscriptions.spec.ts
├── multi-user/
│   └── session-isolation.spec.ts    # two users in two contexts
└── README.md                        # this file
```

Storage state lands under `ui/playwright/.auth/` (gitignored).

## Design notes

- **Single worker, no retries.** Tests are written to be deterministic; if
  one flakes, fix the test rather than papering it over.
- **No `getByText`-only assertions for page identity.** Each per-page test
  uses `getByRole('heading', { ... })` so the test fails loudly if the page's
  semantic structure regresses.
- **The dashboard is the exception.** That page has no `<h1>`; its identity
  is asserted via `data-testid="dashboard-top-bar"` plus the `<h2>At a
  glance</h2>` heading.
- **Username surfaces via `data-testid="topbar-username"`.** The multi-user
  test pins per-user identity on that element rather than relying on the
  Sign-out button (which is identical across users).
