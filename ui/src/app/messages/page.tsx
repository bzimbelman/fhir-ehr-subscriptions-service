import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { MessagesListView } from "@/components/MessagesListView";

// Ticket #423: force-dynamic so OIDC env is read at REQUEST time, not
// build time. Next.js otherwise prerenders the unauthenticated redirect
// branch and the page never re-checks auth() at runtime.
export const dynamic = "force-dynamic";

/**
 * Operator message browser (Epic #398, ticket #402).
 *
 * Lists every message that flowed through the engine with filters +
 * pagination. Each row links to `/messages/{id}` for the deep-dive view.
 *
 * Auth: gated by NextAuth session, same as the rest of the operator UI.
 * The admin-API bearer token never reaches the browser; calls are
 * proxied through /api/admin/[...path].
 */
export default async function MessagesPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <MessagesListView />;
}
