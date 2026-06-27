import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SubscriptionsList } from "@/components/SubscriptionsList";

/**
 * Subscriptions list page (Epic #398, ticket #404).
 *
 * Same auth gating as the dashboard (#400): if OIDC is unconfigured
 * we redirect to /signin (which renders a "configure OIDC first"
 * banner); otherwise we require an authenticated session. The
 * SubscriptionsList component fetches data client-side via the
 * /api/admin proxy.
 */
export default async function SubscriptionsPage() {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <SubscriptionsList />;
}
