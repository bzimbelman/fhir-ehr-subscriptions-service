"use client";

import Link from "next/link";
import { useExtensions } from "@/extensions/useExtensions";

/**
 * Left-nav shell for the operator console. Reads the navigation
 * entries from the `UiExtensionRegistry` -- this used to be a
 * hard-coded `NAV_LINKS` constant; ticket #437 moved it into the
 * extension registry so commercial bundles can register additional
 * nav links beside the FOSS builtins without forking this file.
 *
 * The registry filters by entitlement, so a commercial nav link
 * without a matching license simply doesn't appear here. Links are
 * sorted by `order` (low first) with ties broken alphabetically by
 * displayName.
 */
export function Navigation() {
  const { navLinks } = useExtensions();

  return (
    <nav
      aria-label="Primary"
      className="w-56 shrink-0 border-r border-gray-200 bg-gray-50 p-4"
    >
      <div className="mb-6 text-sm font-semibold uppercase tracking-wide text-gray-500">
        subscription-service
      </div>
      <ul className="space-y-1">
        {navLinks.map((link) => (
          <li key={link.id}>
            <Link
              href={link.href}
              className="block rounded px-3 py-2 text-sm text-gray-800 hover:bg-gray-200"
            >
              {link.displayName}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}
