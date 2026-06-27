# @bzonfhir/ui-extensions

TypeScript SPI for [subscription-service](https://github.com/bzonfhir/subscription-service) UI extensions.

This package declares the contract for adding fragments to the operator UI — nav links, full pages, panel widgets, row actions, and detail tabs — without forking the FOSS app. The host UI ships one Docker image containing both FOSS and (in commercial builds) paid extension code. A license check at boot returns the set of entitlements; the registry only surfaces extensions whose `requiredEntitlement` is in that set. See subscription-service master plan §3.2.1 for the full model.

The FOSS UI registers its own built-in routes through these same types, so the SPI is exercised on every page mount. We can't quietly break it.

## Install

```bash
pnpm add @bzonfhir/ui-extensions
```

React 19 (or newer) is a peer dependency.

## The five extension points

| Kind | Use case |
|---|---|
| `nav-link` | Add an entry to the left nav. |
| `page-route` | Mount a full page at a Next.js route. |
| `panel-widget` | Add a card to a panel slot (Dashboard, Settings). |
| `row-action` | Add an action button to a table row (Messages, DLQ, Subscriptions). |
| `detail-tab` | Add a tab to a detail page (`/messages/[id]`, `/subscriptions/[id]`). |

### 1. NavLink — entry in the left nav

```ts
import { entitlement, type NavLinkExtension } from "@bzonfhir/ui-extensions";

export const complianceNav: NavLinkExtension = {
  kind: "nav-link",
  id: "compliance-nav",
  displayName: "Compliance",
  href: "/compliance/iti-20",
  icon: "shield",
  order: 50,
  requiredEntitlement: entitlement("audit.export.iti20"),
};
```

### 2. PageRoute — a full page at a route

```ts
import type { PageRouteExtension } from "@bzonfhir/ui-extensions";

export const iti20Page: PageRouteExtension = {
  kind: "page-route",
  id: "iti20-page",
  displayName: "ITI-20 Export",
  path: "/compliance/iti-20",
  requiredEntitlement: entitlement("audit.export.iti20"),
  // Lazy-imported so we don't pay the bundle cost when the entitlement isn't granted.
  component: () => import("./Iti20ExportPage"),
};
```

### 3. PanelWidget — a card in a sealed panel slot

```ts
import type { PanelWidgetExtension } from "@bzonfhir/ui-extensions";

export const datadogStatusWidget: PanelWidgetExtension = {
  kind: "panel-widget",
  id: "datadog-status",
  displayName: "Datadog status",
  slot: "dashboard.stats",
  order: 10,
  requiredEntitlement: entitlement("integrations.datadog"),
  component: () => import("./DatadogStatusCard"),
};
```

### 4. RowAction — a button in a table row's action menu

```ts
import type { RowActionExtension } from "@bzonfhir/ui-extensions";

export const syntheticResendAction: RowActionExtension = {
  kind: "row-action",
  id: "synthetic-resend",
  displayName: "Synthetic resend",
  target: "dlq-row",
  label: "Replay (synthetic)",
  requiredEntitlement: entitlement("simulation.synthetic-resend"),
  action: async (rowId, ctx) => {
    const res = await fetch(`/api/dlq/${rowId}/resend?synthetic=1`, {
      method: "POST",
    });
    ctx.refresh();
    return { ok: res.ok, refresh: true };
  },
};
```

### 5. DetailTab — a tab on a detail page

```ts
import type { DetailTabExtension } from "@bzonfhir/ui-extensions";

export const traceTimelineTab: DetailTabExtension = {
  kind: "detail-tab",
  id: "trace-timeline",
  displayName: "Trace timeline",
  target: "message-detail",
  label: "Trace",
  requiredEntitlement: entitlement("tracing.timeline"),
  component: () => import("./TraceTimelineTab"),
};
```

## Manifest

Bundle the extensions into a `UiExtensionManifest`. The host's registry registers manifests, not individual extensions.

```ts
import {
  SPI_SCHEMA_VERSION,
  type UiExtensionManifest,
} from "@bzonfhir/ui-extensions";

export const complianceManifest: UiExtensionManifest = {
  schemaVersion: SPI_SCHEMA_VERSION,
  id: "compliance-iti20",
  version: "1.0.0",
  supplier: "commercial",
  extensions: [complianceNav, iti20Page],
};
```

## Entitlements

`requiredEntitlement` is omitted for FOSS extensions (always shown) and set to a branded string for paid ones. The host's license-check client returns an `EntitlementSet` once at boot; the registry consults it for every lookup.

```ts
import { entitlement, makeEntitlementSet } from "@bzonfhir/ui-extensions";

// In the host:
const entitlements = makeEntitlementSet(licenseResponse.entitlements);
entitlements.has(entitlement("audit.export.iti20")); // true / false
```

## Schema version

`SPI_SCHEMA_VERSION` is the source of truth for "did the SPI change shape?". The host refuses manifests whose `schemaVersion` doesn't match. Bumping it is a breaking change; consumers re-emit and re-publish.

## License

Apache-2.0 — see [LICENSE](./LICENSE).
