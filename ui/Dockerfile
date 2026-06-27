# syntax=docker/dockerfile:1.7
#
# subscription-service operator UI -- Next.js 15 + NextAuth v5.
# Multi-stage:
#   1. deps  -- install pnpm + production node_modules (cached layer)
#   2. build -- compile the Next.js standalone output
#   3. run   -- minimal runtime image (Node 22 alpine, non-root)

# ---------- Stage 1: deps ----------
FROM node:22-alpine AS deps
WORKDIR /app

# pnpm via corepack (ships with Node 22). Pin to the same version that the
# repo's package.json declares so lockfile resolution is deterministic.
RUN corepack enable && corepack prepare pnpm@11.3.0 --activate

# Copy the pnpm config files alongside the manifest so the install picks up
# `onlyBuiltDependencies` / `allowBuilds` (otherwise pnpm v11 fails the build
# with ERR_PNPM_IGNORED_BUILDS on the native postinstalls).
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
RUN pnpm install --frozen-lockfile

# ---------- Stage 2: build ----------
FROM node:22-alpine AS build
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@11.3.0 --activate

COPY --from=deps /app/node_modules ./node_modules
COPY . .

ENV NEXT_TELEMETRY_DISABLED=1
RUN pnpm build

# ---------- Stage 3: runtime ----------
FROM node:22-alpine AS runtime
WORKDIR /app

# Non-root user for PSA restricted compliance (matches the convention from
# Epic #412). UID/GID 1001 because node:22-alpine ships with a `node` user
# already occupying UID/GID 1000. `wget` is built into BusyBox so we don't
# need an apk install for the HEALTHCHECK below.
RUN addgroup -g 1001 -S nodejs && \
    adduser  -u 1001 -S nextjs -G nodejs

ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000 \
    HOSTNAME=0.0.0.0

# Standalone output trims the runtime image down to just the files Next.js
# needs. The static assets live under .next/static; the public/ tree is for
# user-supplied assets (favicon, etc.) -- this scaffold has none, but we copy
# it conditionally so future tickets can drop assets in without revisiting
# the Dockerfile.
COPY --from=build --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=build --chown=nextjs:nodejs /app/.next/static ./.next/static

USER nextjs
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:3000/api/health | grep -q '"ok"' || exit 1

CMD ["node", "server.js"]
