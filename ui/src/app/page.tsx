import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";

/**
 * Root route -- redirects to the dashboard (ticket #400) when authenticated,
 * otherwise to /signin. Keeping this as a thin redirect lets us change the
 * default landing page in one place if a future ticket adds (for example)
 * an Interfaces index ahead of the dashboard.
 */
export default async function Page() {
  if (!isOidcConfigured()) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  redirect("/dashboard");
}
