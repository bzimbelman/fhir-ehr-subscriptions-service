import type { ComponentType } from "react";
import type { Entitlement } from "./entitlements";

/**
 * Sealed enumeration of dashboard / settings panels that accept
 * {@link PanelWidgetExtension} cards. Adding a new slot is a
 * breaking change to the SPI schema version.
 */
export type PanelSlot =
  | "dashboard.stats"
  | "dashboard.activity"
  | "settings.downstream"
  | "settings.observability"
  | "subscription.summary";

/**
 * Sealed enumeration of tables that accept {@link RowActionExtension}
 * buttons in their per-row action menu.
 */
export type RowActionTarget = "message-row" | "dlq-row" | "subscription-row";

/**
 * Sealed enumeration of detail pages that accept additional
 * {@link DetailTabExtension} tabs.
 */
export type DetailTabTarget = "message-detail" | "subscription-detail";

/**
 * Context passed to a {@link RowActionExtension}'s action handler.
 * Minimal by design — the action gets the row id, a way to refresh
 * the table, and an opaque session token it can use against the
 * backend.
 */
export interface RowActionContext {
  readonly rowId: string;
  readonly refresh: () => void;
  readonly sessionToken?: string;
}

/**
 * Result returned by a {@link RowActionExtension}. The host surfaces
 * the message in a toast and decides whether to refresh the table.
 */
export interface RowActionResult {
  readonly ok: boolean;
  readonly message?: string;
  readonly refresh?: boolean;
}

/**
 * Props the host passes to a {@link DetailTabExtension}'s component
 * when the operator selects the tab.
 */
export interface DetailTabProps {
  readonly resourceId: string;
}

/**
 * Props the host passes to a {@link PanelWidgetExtension}'s
 * component when it's mounted in a panel slot.
 */
export interface PanelWidgetProps {
  readonly slot: PanelSlot;
}

/**
 * Common fields every {@link Extension} variant carries.
 *
 * - `id` is globally unique across all manifests; the registry
 *   rejects duplicates.
 * - `requiredEntitlement`, when omitted, means "always shown" — the
 *   FOSS case.
 * - `minHostVersion` is a semver string; the registry refuses to
 *   register an extension whose host requirement isn't satisfied.
 */
export interface BaseExtension {
  readonly id: string;
  readonly displayName: string;
  readonly requiredEntitlement?: Entitlement;
  readonly minHostVersion?: string;
}

/**
 * `NavLink` extension point — adds an entry to the operator UI's
 * left navigation. Example: a "Compliance" tab linking to a Pro-tier
 * audit-export page.
 */
export interface NavLinkExtension extends BaseExtension {
  readonly kind: "nav-link";
  readonly href: string;
  readonly icon?: string;
  readonly order?: number;
}

/**
 * `PageRoute` extension point — mounts a full page at a route. The
 * component is lazy-imported so we don't pay the bundle cost for
 * pages an entitlement doesn't unlock.
 */
export interface PageRouteExtension extends BaseExtension {
  readonly kind: "page-route";
  readonly path: string;
  readonly component: () => Promise<{ default: ComponentType }>;
}

/**
 * `PanelWidget` extension point — adds a card to a panel slot on the
 * Dashboard or Settings page. Example: a "Datadog status" card from
 * the Datadog adapter plugin.
 */
export interface PanelWidgetExtension extends BaseExtension {
  readonly kind: "panel-widget";
  readonly slot: PanelSlot;
  readonly component: () => Promise<{
    default: ComponentType<PanelWidgetProps>;
  }>;
  readonly order?: number;
}

/**
 * `RowAction` extension point — adds an action button to a table
 * row. Example: "Replay with synthetic resend" from a paid
 * simulation plugin, attached to the DLQ table.
 */
export interface RowActionExtension extends BaseExtension {
  readonly kind: "row-action";
  readonly target: RowActionTarget;
  readonly label: string;
  readonly action: (
    rowId: string,
    ctx: RowActionContext,
  ) => Promise<RowActionResult>;
}

/**
 * `DetailTab` extension point — adds a tab to a detail page. Example:
 * a "Trace timeline" tab from a Pro-tier tracing plugin on the
 * message-detail page.
 */
export interface DetailTabExtension extends BaseExtension {
  readonly kind: "detail-tab";
  readonly target: DetailTabTarget;
  readonly label: string;
  readonly component: () => Promise<{
    default: ComponentType<DetailTabProps>;
  }>;
  readonly order?: number;
}

/**
 * Discriminated union over every supported extension point. The
 * `kind` discriminator is exhaustive — adding a new variant is a
 * breaking change to the SPI schema version.
 */
export type Extension =
  | NavLinkExtension
  | PageRouteExtension
  | PanelWidgetExtension
  | RowActionExtension
  | DetailTabExtension;

/**
 * String literal union of every extension `kind`. Useful for switch
 * statements and exhaustiveness checks.
 */
export type ExtensionKind = Extension["kind"];
