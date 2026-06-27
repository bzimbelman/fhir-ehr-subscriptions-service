import Link from "next/link";

/**
 * Left-nav shell for the operator console. The links point at placeholder
 * routes today; subsequent UI tickets land the actual screens at the URLs
 * listed here. Renaming any of these requires a coordinated change with the
 * ticket that owns the page.
 */

interface NavLink {
  href: string;
  label: string;
  /** The ticket that lands the real content at this route. */
  ticket: string;
}

export const NAV_LINKS: readonly NavLink[] = [
  { href: "/dashboard", label: "Dashboard", ticket: "#400" },
  { href: "/messages", label: "Messages", ticket: "#401" },
  { href: "/subscriptions", label: "Subscriptions", ticket: "#404" },
  { href: "/matchbox", label: "Matchbox", ticket: "#404" },
  { href: "/settings", label: "Settings", ticket: "#405" },
  { href: "/audit", label: "Audit", ticket: "#406" },
] as const;

export function Navigation() {
  return (
    <nav
      aria-label="Primary"
      className="w-56 shrink-0 border-r border-gray-200 bg-gray-50 p-4"
    >
      <div className="mb-6 text-sm font-semibold uppercase tracking-wide text-gray-500">
        subscription-service
      </div>
      <ul className="space-y-1">
        {NAV_LINKS.map((link) => (
          <li key={link.href}>
            <Link
              href={link.href}
              className="block rounded px-3 py-2 text-sm text-gray-800 hover:bg-gray-200"
            >
              {link.label}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
