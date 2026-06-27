import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { MessageDetailView } from "@/components/MessageDetailView";

/**
 * Single-message detail page (Epic #398, ticket #402).
 *
 * URL is `/messages/[id]` where `id` is the numeric primary key from
 * `ingested_messages`. The dashboard, interfaces detail, and DLQ row
 * expansions all link here.
 *
 * Same OIDC + session gating as the rest of the operator UI.
 */
interface PageProps {
  params: Promise<{ id: string }>;
}

export default async function MessageDetailPage({ params }: PageProps) {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  const { id } = await params;
  return <MessageDetailView id={id} />;
}
