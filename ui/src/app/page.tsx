import { redirect } from "next/navigation";
import { auth } from "@/lib/auth";
import { isOidcConfigured } from "@/lib/auth";
import { SignOutButton } from "@/components/SignOutButton";

/**
 * Post-login landing page. Placeholder for the dashboard that ticket #400
 * will ship -- this scaffold only proves that auth works and routing lands
 * the user here.
 */
export default async function Page() {
  if (!isOidcConfigured) {
    // Documented "please configure OIDC" landing -- prevents crash when the
    // app boots without env vars (e.g., during local docker build smoke).
    redirect("/signin");
  }

  const session = await auth();
  if (!session) {
    redirect("/signin");
  }

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">subscription-service</h1>
      <p className="text-sm text-gray-600">
        Signed in as {session.user?.username ?? session.user?.email ?? "..."}
      </p>
      <SignOutButton />
      <p className="text-sm text-gray-500">
        Dashboard and other pages land in subsequent tickets (#400-#408).
      </p>
    </div>
  );
}
