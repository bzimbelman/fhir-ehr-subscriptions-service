import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { AuditView } from "@/components/AuditView";

/**
 * Operator audit log view (Epic #398, ticket #407).
 *
 * Browses the FHIR AuditEvent rows HAPI persists (from #391). Read-only;
 * the actual emission is server-side, this is a window onto the data.
 *
 * Auth: gated by NextAuth session, same as the rest of the operator UI.
 * Admin-API bearer token is server-side only; calls go through the
 * /api/admin/[...path] proxy (which forwards to `/admin/audit*`).
 */
export default async function AuditPage() {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <AuditView />;
}
