import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SubscriptionsList } from "@/components/SubscriptionsList";

// Ticket #423: force-dynamic so OIDC env is read at REQUEST time, not
// build time. Next.js otherwise prerenders the unauthenticated redirect
// branch and the page never re-checks auth() at runtime.
export const dynamic = "force-dynamic";

export default async function SubscriptionsPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <SubscriptionsList />;
}
