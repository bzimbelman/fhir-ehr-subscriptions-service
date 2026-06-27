import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { SettingsView } from "@/components/SettingsView";

/**
 * Operator Settings view (Epic #398, ticket #406).
 *
 * Read-only display of how this deployment is configured: feature toggles,
 * downstream URLs, schema versions, build info. Configuration changes go
 * through env vars + redeploy -- there are no edit controls on this page.
 *
 * Auth: gated by NextAuth session, same as the rest of the operator UI.
 * Admin-API bearer token is server-side only; calls go through the
 * /api/admin/[...path] proxy.
 */
export default async function SettingsPage() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <SettingsView />;
}
