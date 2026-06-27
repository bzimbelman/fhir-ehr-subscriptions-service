# subscription-service operator UI

Next.js 15 + NextAuth.js v5 operator console for `subscription-service`. This
directory ships the scaffold from ticket #399 — directory layout, OIDC login,
health probe, Dockerfile, and placeholder pages where subsequent tickets
(#400–#408) land the actual operator screens.

## Tech stack

| Concern           | Choice                                |
| ----------------- | ------------------------------------- |
| Framework         | Next.js 15 (App Router)               |
| Runtime           | Node 22                               |
| UI                | React 19                              |
| Auth              | NextAuth.js v5 (Auth.js) — OIDC       |
| Styling           | Tailwind CSS 4                        |
| Tests             | Vitest + Testing Library              |
| Language          | TypeScript 5.7 (strict)               |
| Package manager   | pnpm (`packageManager` is pinned)     |

Minor versions are pinned explicitly in `package.json`; no `^`/`~` ranges. The
intent is reproducible builds; we'll bump deliberately.

## Local development

```bash
pnpm install
cp .env.example .env.local       # fill in OIDC_*, NEXTAUTH_URL, AUTH_SECRET
pnpm dev                         # http://localhost:3000
```

With OIDC unconfigured (empty env vars), the app boots and lands every route
on `/signin`, which renders a "please configure OIDC" message rather than
crashing.

## Environment variables

| Variable                  | Required        | Notes                                                                                          |
| ------------------------- | --------------- | ---------------------------------------------------------------------------------------------- |
| `OIDC_ISSUER`             | yes (for auth)  | The OIDC provider's issuer URL. See `docs/auth.md` for per-provider recipes.                   |
| `OIDC_CLIENT_ID`          | yes             | Confidential client registered with the IdP. Default: `subscription-service-ui`.               |
| `OIDC_CLIENT_SECRET`      | yes             | Client secret. Treat as a secret.                                                              |
| `NEXTAUTH_URL`            | yes             | Externally-visible URL of this UI. Used for OIDC redirect URI construction.                    |
| `AUTH_SECRET`             | prod yes        | `openssl rand -base64 32`. NextAuth v5 refuses to start without it in production.              |
| `ADMIN_API_BASE_URL`      | yes             | Base URL of the interface-engine admin API (e.g. `http://interface-engine:8090`).              |
| `ADMIN_API_BEARER_TOKEN`  | yes (if backend gated) | Must match `IPF_ADMIN_AUTH_TOKEN` on the interface-engine. Server-side only — never leaked. |

See `.env.example` for the canonical template.

## OIDC provider setup

The UI is provider-agnostic. It works with any OIDC-conformant IdP:

- **Keycloak (modern)** — `OIDC_ISSUER=https://kc.example.com/realms/subscription-service`
- **Keycloak (legacy /auth/)** — same, but with `/auth/realms/...`
- **Auth0** — `OIDC_ISSUER=https://tenant.us.auth0.com/` (trailing slash matters)
- **Okta** — `OIDC_ISSUER=https://org.okta.com/oauth2/default`
- **Authentik** — `OIDC_ISSUER=https://authentik.example.com/application/o/subscription-service/`

Register a confidential client in your IdP with the redirect URI
`${NEXTAUTH_URL}/api/auth/callback/oidc`. Scopes: `openid profile email`.

Full per-provider recipes (including out-of-band scope/role provisioning) are
in `docs/auth.md` at the repo root.

## Backend admin API proxy pattern

The bearer token for the interface-engine admin API stays server-side. The
browser never sees it. Every admin API call from a UI page or component goes
through a Next.js API route that:

1. Reads the user's session via `auth()` — 401 if missing.
2. (Future) Enforces a role / scope check from the session.
3. Calls the admin API via `serverSideAdminFetch()` from `src/lib/apiClient.ts`,
   which injects the server-side bearer token.
4. Streams the response back to the browser.

`serverSideAdminFetch` is the building block. Subsequent UI tickets
(#400–#408) wire concrete admin endpoints on top of it. No real admin
endpoints are wired in this scaffold.

## Directory layout

```
ui/
├── Dockerfile             # multi-stage; non-root runtime
├── package.json           # pinned versions; pnpm packageManager
├── tsconfig.json          # strict mode + noUncheckedIndexedAccess
├── next.config.js         # standalone output for Docker
├── postcss.config.js      # @tailwindcss/postcss
├── tailwind.config.ts     # content globs for Tailwind 4
├── vitest.config.ts       # jsdom env; vite-react plugin
├── .env.example
├── .eslintrc.json         # next/core-web-vitals + next/typescript
└── src/
    ├── app/
    │   ├── layout.tsx                    # root layout with left-nav shell
    │   ├── page.tsx                      # post-login landing
    │   ├── signin/page.tsx               # custom sign-in page
    │   ├── api/auth/[...nextauth]/route.ts
    │   ├── api/health/route.ts           # k8s liveness/readiness
    │   ├── globals.css                   # @import "tailwindcss"
    │   └── (placeholder pages)           # dashboard, messages, subscriptions, ...
    ├── components/
    │   ├── Navigation.tsx                # left-nav with six links
    │   ├── SignOutButton.tsx
    │   └── ProtectedLayout.tsx
    ├── lib/
    │   ├── auth.ts                       # NextAuth v5 config
    │   └── apiClient.ts                  # serverSideAdminFetch helper
    ├── types/
    │   └── next-auth.d.ts                # session augmentation
    └── __tests__/
        ├── health.test.ts
        ├── navigation.test.tsx
        └── session.test-d.ts
```

## Scripts

| Script              | What it does                                |
| ------------------- | ------------------------------------------- |
| `pnpm dev`          | Start the Next.js dev server on :3000.      |
| `pnpm build`        | Production build (standalone output).       |
| `pnpm start`        | Run the built app.                          |
| `pnpm lint`         | ESLint (Next config + TS).                  |
| `pnpm typecheck`    | `tsc --noEmit` against `tsconfig.json`.     |
| `pnpm test`         | Vitest (component + API route tests).       |

## Docker

```bash
# From the repo root:
docker build -t subscription-service/ui:dev ui/
docker run --rm -p 13000:3000 subscription-service/ui:dev
curl -fsS http://localhost:13000/api/health     # {"status":"ok"}
```

The image:

- Runs as UID 1001 (non-root, named `nextjs`) for PSA restricted compliance. UID 1000 is occupied by the upstream `node:22-alpine` image's `node` user.
- Has a HEALTHCHECK against `/api/health`.
- Uses Next.js standalone output for a minimal runtime footprint.

## What this ticket does NOT do

- No real admin endpoints are wired. `src/lib/apiClient.ts` is a helper, not a
  client for any specific endpoint. Tickets #400–#408 land those.
- No compose / Helm wiring. Ticket #408 owns the deployment integration.
- No end-to-end OIDC verification against a live IdP. The OIDC config is
  exercised by unit tests + type-level checks; the live login flow is
  verified once we have a provisioned realm.
