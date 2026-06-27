import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { MatchboxView } from "@/components/MatchboxView";

// Ticket #423: force-dynamic so OIDC env is read at REQUEST time, not
// build time. Next.js otherwise prerenders the unauthenticated redirect
// branch and the page never re-checks auth() at runtime.
export const dynamic = "force-dynamic";

/**
 * Operator Matchbox transform inspector (Epic #398, ticket #405).
 *
 * The mapping engine sits behind the interface engine; operators
 * occasionally need to (a) confirm Matchbox is healthy, (b) see which
 * StructureMaps are loaded, and (c) paste a problem message to see what
 * Matchbox makes of it. This page is that surface.
 *
 * Auth: gated by NextAuth session, same as the rest of the operator UI.
 * The admin-API bearer token never reaches the browser; calls are
 * proxied through /api/admin/[...path].
 */
export default async function MatchboxPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <MatchboxView />;
}
