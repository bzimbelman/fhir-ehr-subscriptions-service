import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SignOutButton } from "@/components/SignOutButton";
import { DashboardView } from "@/components/DashboardView";

// Ticket #423: force-dynamic so OIDC env is read at REQUEST time, not
// build time. Next.js otherwise prerenders the unauthenticated redirect
// branch and the page never re-checks auth() at runtime.
export const dynamic = "force-dynamic";

/**
 * Operator dashboard -- the default landing page after sign-in (Epic #398,
 * ticket #400).
 *
 * Information architecture is taken from
 * docs/ui-design/reference-screens/01-dashboard.png (Mirth Connect) -- we
 * adopt the data shown, NOT the visual style.
 *
 * Page layout (top to bottom):
 *   1. Top bar: deployment name + system status pill + user / sign-out
 *   2. Stats row: 6 cards (today / week / month / success rate / DLQ /
 *      active subscriptions) with a manual Refresh button
 *   3. Two-column section: component health (left), recent activity (right)
 *   4. Footer with last-updated timestamp
 *
 * Auth: this page is gated by the same `auth()` check as the rest of the
 * UI. The actual admin-API data is fetched client-side from
 * `/api/admin/[...path]` -- see `apiClient.ts` and the proxy route for
 * why the bearer token never reaches the browser.
 *
 * Operational note: if OIDC is unconfigured the page redirects to
 * /signin, which renders a clear configuration banner. The dashboard
 * itself doesn't try to render with mock data -- "configure OIDC first"
 * is the more useful message for an operator setting up the deployment.
 */
export default async function DashboardPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }

  const username =
    session.user?.username ?? session.user?.email ?? session.user?.name ?? null;
  const deploymentName =
    process.env.NEXT_PUBLIC_DEPLOYMENT_NAME ??
    process.env.DEPLOYMENT_NAME ??
    undefined;

  return (
    <DashboardView
      username={username}
      deploymentName={deploymentName}
      signOutSlot={<SignOutButton />}
    />
  );
}
