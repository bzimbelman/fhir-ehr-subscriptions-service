import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SubscriptionDetail } from "@/components/SubscriptionDetail";

/**
 * Subscription detail page (Epic #398, ticket #404).
 *
 * URL is `/subscriptions/[id]` with the bare HAPI id (no
 * "Subscription/" prefix) so the path doesn't have to encode a slash.
 *
 * Same OIDC + session gating as the list view.
 */
interface PageProps {
  params: Promise<{ id: string }>;
}

export default async function SubscriptionDetailPage({ params }: PageProps) {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  const { id } = await params;
  return <SubscriptionDetail id={id} />;
}
