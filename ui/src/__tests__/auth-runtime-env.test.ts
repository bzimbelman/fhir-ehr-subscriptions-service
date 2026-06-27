import { describe, it, expect, beforeEach, afterAll } from "vitest";

/**
 * Regression tests for the build-time env baking bug (ticket #423).
 *
 * Next.js's standalone production build evaluates module-level
 * `process.env.X` expressions at build time, baking the (empty) values into
 * the bundle -- so runtime container env was ignored when
 * `isOidcConfigured` was a top-level `const`.
 *
 * The fix splits the env-reading helper out into `@/lib/oidc-env` (a plain
 * module that does not import `next-auth`) so it can be tested in isolation
 * here AND so the production build cannot inline the values. `auth.ts`
 * delegates to these helpers from inside a request-time NextAuth callback.
 *
 * The first failure mode that regression-tests this bug is the
 * module-load const; if anyone re-introduces one, the second test will
 * fail because `isOidcConfigured` won't pick up the post-import env
 * mutation.
 */
describe("isOidcConfigured() / readOidcEnv()", () => {
  const original = {
    OIDC_ISSUER: process.env.OIDC_ISSUER,
    OIDC_CLIENT_ID: process.env.OIDC_CLIENT_ID,
    OIDC_CLIENT_SECRET: process.env.OIDC_CLIENT_SECRET,
  };

  beforeEach(() => {
    delete process.env.OIDC_ISSUER;
    delete process.env.OIDC_CLIENT_ID;
    delete process.env.OIDC_CLIENT_SECRET;
  });

  afterAll(() => {
    if (original.OIDC_ISSUER !== undefined)
      process.env.OIDC_ISSUER = original.OIDC_ISSUER;
    if (original.OIDC_CLIENT_ID !== undefined)
      process.env.OIDC_CLIENT_ID = original.OIDC_CLIENT_ID;
    if (original.OIDC_CLIENT_SECRET !== undefined)
      process.env.OIDC_CLIENT_SECRET = original.OIDC_CLIENT_SECRET;
  });

  it("is false when no OIDC vars are set", async () => {
    const { isOidcConfigured } = await import("@/lib/oidc-env");
    expect(isOidcConfigured()).toBe(false);
  });

  it("becomes true after env is set, without re-importing the module", async () => {
    const { isOidcConfigured } = await import("@/lib/oidc-env");
    expect(isOidcConfigured()).toBe(false);
    process.env.OIDC_ISSUER = "https://example.com";
    process.env.OIDC_CLIENT_ID = "test";
    process.env.OIDC_CLIENT_SECRET = "test";
    expect(isOidcConfigured()).toBe(true);
  });

  it("is false again if any one var is unset after being true", async () => {
    const { isOidcConfigured } = await import("@/lib/oidc-env");
    process.env.OIDC_ISSUER = "https://example.com";
    process.env.OIDC_CLIENT_ID = "test";
    process.env.OIDC_CLIENT_SECRET = "test";
    expect(isOidcConfigured()).toBe(true);
    delete process.env.OIDC_CLIENT_SECRET;
    expect(isOidcConfigured()).toBe(false);
  });

  it("readOidcEnv() reflects the current process.env on each call", async () => {
    const { readOidcEnv } = await import("@/lib/oidc-env");
    expect(readOidcEnv()).toEqual({
      issuer: "",
      clientId: "",
      clientSecret: "",
      configured: false,
    });
    process.env.OIDC_ISSUER = "https://example.com";
    process.env.OIDC_CLIENT_ID = "id";
    process.env.OIDC_CLIENT_SECRET = "shh";
    expect(readOidcEnv()).toEqual({
      issuer: "https://example.com",
      clientId: "id",
      clientSecret: "shh",
      configured: true,
    });
  });

  // Note: we deliberately do NOT import @/lib/auth in this file because
  // next-auth's transitive `import "next/server"` doesn't resolve under
  // vitest's ESM resolver (next has no `exports` field for that path).
  // The application callers all do `!isOidcConfigured()` with parens, so
  // any future re-introduction of a boolean re-export would fail tsc;
  // and `pnpm build` itself is the integration test for end-to-end
  // wiring.
});
