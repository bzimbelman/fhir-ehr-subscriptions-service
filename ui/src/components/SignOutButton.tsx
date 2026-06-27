import { signOut } from "@/lib/auth";

/**
 * Sign-out button. Wraps the NextAuth v5 server action so the OIDC session
 * cookie is cleared and the user is redirected back to /signin.
 */
export function SignOutButton() {
  return (
    <form
      action={async () => {
        "use server";
        await signOut({ redirectTo: "/signin" });
      }}
    >
      <button
        type="submit"
        className="rounded border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-800 hover:bg-gray-100"
      >
        Sign out
      </button>
    </form>
  );
}
