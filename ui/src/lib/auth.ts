import NextAuth, { type NextAuthConfig } from "next-auth";
import { isOidcConfigured, readOidcEnv } from "@/lib/oidc-env";

/**
 * NextAuth v5 (Auth.js) configuration for subscription-service operator UI.
 *
 * The UI is provider-agnostic: it consumes whatever OIDC IdP the rest of
 * the platform already uses (Keycloak, Auth0, Okta, Authentik, etc.). See
 * docs/auth.md in the repo root for per-provider recipes.
 *
 * IMPORTANT (ticket #423): the OIDC client config MUST be read at request
 * time, not at module evaluation. NextAuth v5 accepts a function form
 * whose body runs once per request, which is exactly what we want. See:
 *   https://authjs.dev/reference/nextjs#with-request-object-and-config
 * Reading env at module scope would cause Next.js's standalone production
 * build to inline the (build-time-empty) values into the bundle and
 * ignore the runtime container env.
 *
 * `isOidcConfigured` and `readOidcEnv` are extracted into
 * `@/lib/oidc-env` so the env-reading helpers can be tested in isolation
 * AND so the build's static analysis sees them as opaque function calls.
 */

export { isOidcConfigured } from "@/lib/oidc-env";

function buildConfig(): NextAuthConfig {
  const env = readOidcEnv();
  return {
    // When OIDC is not configured, we still register the route handlers
    // so the /api/auth/* endpoints don't 404 -- but every sign-in
    // attempt is short-circuited by the /signin page before it gets
    // here.
    providers: env.configured
      ? [
          {
            id: "oidc",
            name: "OIDC",
            type: "oidc",
            issuer: env.issuer,
            clientId: env.clientId,
            clientSecret: env.clientSecret,
            authorization: { params: { scope: "openid profile email" } },
          },
        ]
      : [],
    pages: { signIn: "/signin" },
    session: { strategy: "jwt" },
    callbacks: {
      async jwt({ token, account, profile }) {
        // Stash OIDC artifacts on the JWT so server-side admin-API proxy
        // routes can use the access_token to call the backend on behalf
        // of the user.
        if (account) {
          token.accessToken = account.access_token;
          token.idToken = account.id_token;
        }
        if (profile) {
          token.username =
            (profile.preferred_username as string | undefined) ??
            (profile.email as string | undefined) ??
            undefined;
        }
        return token;
      },
      async session({ session, token }) {
        session.accessToken = token.accessToken as string | undefined;
        session.idToken = token.idToken as string | undefined;
        session.user = {
          ...session.user,
          username: token.username as string | undefined,
        };
        return session;
      },
    },
  };
}

// NextAuth v5 supports passing a function so the config is rebuilt per
// request. This is the canonical pattern for runtime-derived config and
// is what defeats Next.js's build-time env inlining.
export const { handlers, signIn, signOut, auth } = NextAuth(() =>
  buildConfig(),
);

// Suppress "isOidcConfigured imported but unused" if a future refactor
// drops the dependency in this file -- it is intentionally re-exported.
void isOidcConfigured;
