import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SubscriptionsList } from "@/components/SubscriptionsList";

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
