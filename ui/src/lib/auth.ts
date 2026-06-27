import NextAuth, { type NextAuthConfig } from "next-auth";

/**
 * NextAuth v5 (Auth.js) configuration for subscription-service operator UI.
 *
 * The UI is provider-agnostic: it consumes whatever OIDC IdP the rest of the
 * platform already uses (Keycloak, Auth0, Okta, Authentik, etc.). See
 * docs/auth.md in the repo root for per-provider recipes.
 *
 * The OIDC client config is read from environment variables at request time,
 * not at module evaluation, so the app can boot without an OIDC config (with
 * a documented "please configure OIDC" landing page).
 */

const issuer = process.env.OIDC_ISSUER ?? "";
const clientId = process.env.OIDC_CLIENT_ID ?? "";
const clientSecret = process.env.OIDC_CLIENT_SECRET ?? "";

export const isOidcConfigured = Boolean(issuer && clientId && clientSecret);

const config: NextAuthConfig = {
  // When OIDC is not configured, we still register the route handlers so the
  // /api/auth/* endpoints don't 404 -- but every sign-in attempt is short-
  // circuited by the /signin page before it gets here.
  providers: isOidcConfigured
    ? [
        {
          id: "oidc",
          name: "OIDC",
          type: "oidc",
          issuer,
          clientId,
          clientSecret,
          authorization: { params: { scope: "openid profile email" } },
        },
      ]
    : [],
  pages: { signIn: "/signin" },
  session: { strategy: "jwt" },
  callbacks: {
    async jwt({ token, account, profile }) {
      // Stash OIDC artifacts on the JWT so server-side admin-API proxy routes
      // can use the access_token to call the backend on behalf of the user.
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

export const { handlers, signIn, signOut, auth } = NextAuth(config);
