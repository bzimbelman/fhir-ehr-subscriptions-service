/**
 * Builtin (FOSS) UI extensions for subscription-service.
 *
 * Today's 8 nav-linked routes (dashboard, interfaces, messages, dlq,
 * subscriptions, matchbox, settings, audit) plus the three drill-down
 * detail routes (`/interfaces/[name]`, `/messages/[id]`,
 * `/subscriptions/[id]`) are registered through the same SPI a
 * commercial bundle would use. None of these carry a
 * `requiredEntitlement` -- they are unconditionally FOSS.
 *
 * Why register builtins through the SPI?
 *
 *   1. Single source of truth: `Navigation.tsx` reads from the
 *      registry, not a separate `NAV_LINKS` constant. Adding a new
 *      builtin nav link means adding an entry here -- nowhere else.
 *   2. The SPI is exercised on every page load, so we can't quietly
 *      break it.
 *   3. When commercial bundles register additional manifests at boot,
 *      their NavLinks land beside ours and the registry sorts the
 *      combined list. No special-casing.
 *
 * Routing note: Next.js still uses filesystem-based App Router for
 * the actual page resolution. The `PageRouteExtension.component`
 * lazy-import is METADATA -- it lets the registry tell consumers
 * "yes, the host knows about this route." The runtime render is
 * still Next.js's job.
 */

import {
  SPI_SCHEMA_VERSION,
  type Extension,
  type NavLinkExtension,
  type PageRouteExtension,
  type UiExtensionManifest,
} from "@bzonfhir/ui-extensions";

const SUPPLIER = "first-party" as const;

/**
 * The 8 nav-linked top-level routes. Order matches the historical
 * `NAV_LINKS` constant in `Navigation.tsx` so the rendered nav is
 * pixel-identical to today.
 */
const navLinks: readonly NavLinkExtension[] = [
  {
    kind: "nav-link",
    id: "builtin.nav.dashboard",
    displayName: "Dashboard",
    href: "/dashboard",
    order: 10,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.interfaces",
    displayName: "Interfaces",
    href: "/interfaces",
    order: 20,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.messages",
    displayName: "Messages",
    href: "/messages",
    order: 30,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.dlq",
    displayName: "DLQ",
    href: "/dlq",
    order: 40,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.subscriptions",
    displayName: "Subscriptions",
    href: "/subscriptions",
    order: 50,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.matchbox",
    displayName: "Matchbox",
    href: "/matchbox",
    order: 60,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.settings",
    displayName: "Settings",
    href: "/settings",
    order: 70,
  },
  {
    kind: "nav-link",
    id: "builtin.nav.audit",
    displayName: "Audit",
    href: "/audit",
    order: 80,
  },
];

/**
 * The 8 top-level page routes -- one per nav link. The
 * `component` thunks are dynamic imports so Next.js's bundler can
 * tree-shake unused commercial pages out of the FOSS build.
 */
const pageRoutes: readonly PageRouteExtension[] = [
  {
    kind: "page-route",
    id: "builtin.page.dashboard",
    displayName: "Dashboard",
    path: "/dashboard",
    component: () =>
      import("@/app/dashboard/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.interfaces",
    displayName: "Interfaces",
    path: "/interfaces",
    component: () =>
      import("@/app/interfaces/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.messages",
    displayName: "Messages",
    path: "/messages",
    component: () =>
      import("@/app/messages/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.dlq",
    displayName: "DLQ",
    path: "/dlq",
    component: () =>
      import("@/app/dlq/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.subscriptions",
    displayName: "Subscriptions",
    path: "/subscriptions",
    component: () =>
      import("@/app/subscriptions/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.matchbox",
    displayName: "Matchbox",
    path: "/matchbox",
    component: () =>
      import("@/app/matchbox/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.settings",
    displayName: "Settings",
    path: "/settings",
    component: () =>
      import("@/app/settings/page").then((m) => ({ default: m.default })),
  },
  {
    kind: "page-route",
    id: "builtin.page.audit",
    displayName: "Audit",
    path: "/audit",
    component: () =>
      import("@/app/audit/page").then((m) => ({ default: m.default })),
  },
];

const extensions: readonly Extension[] = [...navLinks, ...pageRoutes];

/**
 * The single first-party manifest. Commercial bundles register their
 * own manifests with different ids; the registry merges them.
 */
export const builtinManifest: UiExtensionManifest = {
  schemaVersion: SPI_SCHEMA_VERSION,
  id: "subscription-service-core",
  version: "1.0.0",
  supplier: SUPPLIER,
  extensions,
};

/**
 * Convenience export for tests that want to assert "expected nav
 * links are present" without picking through `manifest.extensions`.
 */
export const builtinNavLinks: readonly NavLinkExtension[] = navLinks;

/**
 * Convenience export for tests that want to assert "expected page
 * routes are present" without picking through `manifest.extensions`.
 */
export const builtinPageRoutes: readonly PageRouteExtension[] = pageRoutes;
