import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { InterfacesListView } from "@/components/InterfacesListView";

/**
 * Per-interface list (Epic #398, ticket #401).
 *
 * Information architecture mirrors Mirth Connect's channel grid (see
 * `docs/ui-design/reference-screens/01-dashboard.png`) but scoped to the
 * "list of interfaces" rather than "everything the dashboard shows".
 *
 * Auth gate is identical to the dashboard: any authenticated NextAuth
 * session passes. The admin bearer token is never sent to the browser --
 * the per-interface data is fetched client-side from `/api/admin/...`
 * which is the same proxy route the dashboard uses.
 */
export default async function InterfacesPage() {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <InterfacesListView />;
}
