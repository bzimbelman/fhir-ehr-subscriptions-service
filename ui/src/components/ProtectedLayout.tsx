import { redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";

/**
 * Server-component wrapper for protected pages. Wraps the children in an
 * auth check: if there's no session, the user is redirected to /signin.
 *
 * Usage:
 *   export default function MyPage() {
 *     return (
 *       <ProtectedLayout>
 *         <PageContents />
 *       </ProtectedLayout>
 *     );
 *   }
 */
export async function ProtectedLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }
  return <>{children}</>;
}
