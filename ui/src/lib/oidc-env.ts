/**
 * Runtime OIDC environment reader (ticket #423).
 *
 * Why this file exists as its own module:
 *
 * Next.js's standalone production build performs static analysis on every
 * module and substitutes literal values for `process.env.X` references at
 * BUILD time. When the operator UI is built in CI without OIDC env vars
 * (which is the only sane way to build the image -- the image is generic),
 * any module-level `const issuer = process.env.OIDC_ISSUER` is inlined as
 * `const issuer = undefined` in the compiled bundle. At runtime, the
 * container can have `OIDC_ISSUER` set in its env, but the bundle no
 * longer reads it.
 *
 * The fix is to read `process.env.OIDC_*` inside a FUNCTION body. Next.js
 * does not inline env reads inside functions (because function execution
 * is deferred to runtime), so the container env is honored. This module
 * encapsulates that pattern and is the single point of truth for "is
 * OIDC configured right now."
 *
 * Note: do NOT add a top-level `const` here that caches the result.
 *
 * Regression-tested in src/__tests__/auth-runtime-env.test.ts.
 */

export interface OidcEnv {
  issuer: string;
  clientId: string;
  clientSecret: string;
  configured: boolean;
}

/**
 * Reads the current OIDC env vars on every call. Never cache the result
 * at module scope.
 */
export function readOidcEnv(): OidcEnv {
  const issuer = process.env.OIDC_ISSUER ?? "";
  const clientId = process.env.OIDC_CLIENT_ID ?? "";
  const clientSecret = process.env.OIDC_CLIENT_SECRET ?? "";
  return {
    issuer,
    clientId,
    clientSecret,
    configured: Boolean(issuer && clientId && clientSecret),
  };
}

/**
 * True iff all three required OIDC env vars are non-empty *right now*.
 * Always call as a function -- `isOidcConfigured()`, not
 * `isOidcConfigured`.
 */
export function isOidcConfigured(): boolean {
  return readOidcEnv().configured;
}
