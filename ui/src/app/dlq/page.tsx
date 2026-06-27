import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { DlqView } from "@/components/DlqView";

// Ticket #423: force-dynamic so OIDC env is read at REQUEST time, not
// build time. Next.js otherwise prerenders the unauthenticated redirect
// branch and the page never re-checks auth() at runtime.
export const dynamic = "force-dynamic";

/**
 * Operator DLQ viewer (Epic #398, ticket #403).
 *
 * Mirth doesn't have a dedicated DLQ screen -- it's a filter on the
 * message browser. Our equivalent is a first-class page with:
 *   - bulk replay / discard with confirmation
 *   - client-side error-pattern fingerprinting ("23 rows are failing the
 *     same way")
 *   - inline row expansion to inspect raw_message without leaving the page
 *
 * Auth: gated by NextAuth session, same as the rest of the operator UI.
 * The admin-API bearer token never reaches the browser; calls are
 * proxied through /api/admin/[...path].
 */
export default async function DlqPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <DlqView />;
}
