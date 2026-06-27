import type { DefaultSession } from "next-auth";

declare module "next-auth" {
  /**
   * Session shape augmented to carry the OIDC artifacts we need to call the
   * admin API on behalf of the user. The OIDC tokens are server-side only --
   * they are never exposed to the browser; this type just lets server code
   * read them off the session without casting.
   */
  interface Session {
    accessToken?: string;
    idToken?: string;
    user: {
      username?: string;
    } & DefaultSession["user"];
  }
}

declare module "next-auth/jwt" {
  interface JWT {
    accessToken?: string;
    idToken?: string;
    username?: string;
  }
}
