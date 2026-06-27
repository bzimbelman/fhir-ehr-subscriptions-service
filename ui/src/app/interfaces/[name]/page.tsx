import { notFound, redirect } from "next/navigation";
import { auth, isOidcConfigured } from "@/lib/auth";
import { InterfaceDetailView } from "@/components/InterfaceDetailView";
import { parseInterfaceSlug } from "@/lib/interfaces";

/**
 * Per-interface drill-down (Epic #398, ticket #401). Reference:
 * docs/ui-design/reference-screens/03-channel-detail.md
 *
 * The `[name]` route segment is the slug from `interfaceSlug()` --
 * URL-encoded `${source_system}__${source_protocol}`. We parse it
 * server-side so a malformed link is a 404 rather than a confusing
 * blank UI.
 */
interface PageProps {
  params: Promise<{ name: string }>;
}

export default async function InterfaceDetailPage({ params }: PageProps) {
  if (!isOidcConfigured) {
    redirect("/signin");
  }
  const session = await auth();
  if (!session) {
    redirect("/signin");
  }

  const { name } = await params;
  const parsed = parseInterfaceSlug(name);
  if (!parsed) {
    notFound();
  }

  return (
    <InterfaceDetailView
      sourceSystem={parsed.sourceSystem}
      sourceProtocol={parsed.sourceProtocol}
    />
  );
}
