import { isOidcConfigured, signIn } from "@/lib/auth";

/**
 * Custom sign-in page. NextAuth v5 ships a built-in one, but operators get a
 * much clearer error when OIDC is unconfigured if we render a dedicated page.
 *
 * The "Sign in" action is a server action that calls `signIn("oidc")` --
 * NextAuth handles the redirect to the IdP.
 */
export default function SignInPage() {
  if (!isOidcConfigured) {
    return (
      <div className="mx-auto max-w-2xl space-y-4">
        <h1 className="text-2xl font-semibold">OIDC is not configured</h1>
        <p className="text-sm text-gray-700">
          The operator UI requires an OIDC identity provider. Set the following
          environment variables and restart:
        </p>
        <ul className="list-disc pl-6 text-sm font-mono">
          <li>OIDC_ISSUER</li>
          <li>OIDC_CLIENT_ID</li>
          <li>OIDC_CLIENT_SECRET</li>
          <li>NEXTAUTH_URL</li>
          <li>AUTH_SECRET</li>
        </ul>
        <p className="text-sm text-gray-700">
          See <code>docs/auth.md</code> in the repo root for per-provider
          recipes (Keycloak, Auth0, Okta, Authentik, etc.).
        </p>
        <p className="text-sm text-gray-700">
          The dashboard also needs <code>ADMIN_API_BASE_URL</code> and{" "}
          <code>ADMIN_API_BEARER_TOKEN</code> to call the interface-engine
          admin endpoints -- without those, the dashboard cards will render
          per-section errors but the page itself stays up.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-md space-y-4">
      <h1 className="text-2xl font-semibold">Sign in</h1>
      <p className="text-sm text-gray-700">
        Authenticate with your organization&apos;s OIDC provider.
      </p>
      <form
        action={async () => {
          "use server";
          await signIn("oidc", { redirectTo: "/dashboard" });
        }}
      >
        <button
          type="submit"
          className="rounded bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
        >
          Sign in with OIDC
        </button>
      </form>
    </div>
  );
}
