"use client";

/**
 * React hook that exposes the entitlement-filtered view of the
 * registry. Components call this to render nav links, panel widgets,
 * row actions, and detail tabs without knowing anything about the
 * license state -- the hook reads it from `LicenseProvider` and
 * applies the filter.
 *
 * Why a hook? Two reasons:
 *
 *   1. It re-renders consumers when the license state changes (e.g.
 *      after a refresh tick lands a new entitlement).
 *   2. It hides the registry instance and the EntitlementSet
 *      construction from callers. They get plain lists.
 */

import { useMemo } from "react";
import {
  EMPTY_ENTITLEMENT_SET,
  makeEntitlementSet,
  type DetailTabExtension,
  type DetailTabTarget,
  type EntitlementSet,
  type NavLinkExtension,
  type PageRouteExtension,
  type PanelSlot,
  type PanelWidgetExtension,
  type RowActionExtension,
  type RowActionTarget,
  type UiExtensionRegistry,
} from "@bzonfhir/ui-extensions";
import type { LicenseState } from "@/lib/license/types";
import { useLicenseState } from "./LicenseProvider";
import { getDefaultRegistry } from "./UiExtensionRegistry";

export interface UseExtensionsValue {
  readonly navLinks: readonly NavLinkExtension[];
  readonly pageRoutes: readonly PageRouteExtension[];
  readonly panelWidgets: (slot: PanelSlot) => readonly PanelWidgetExtension[];
  readonly rowActions: (
    target: RowActionTarget,
  ) => readonly RowActionExtension[];
  readonly detailTabs: (
    target: DetailTabTarget,
  ) => readonly DetailTabExtension[];
}

/**
 * Translate the license module's `LicenseState` into the SPI's
 * `EntitlementSet`. FOSS state -> empty set; active / stale-active
 * -> the entitlements on the cached response.
 */
export function entitlementSetFromLicenseState(
  state: LicenseState,
): EntitlementSet {
  if (state.kind === "foss") return EMPTY_ENTITLEMENT_SET;
  return makeEntitlementSet(state.entitlements.toArray());
}

export interface UseExtensionsOptions {
  /**
   * Override the registry the hook reads from. Production callers
   * leave this unset to get the process-wide default; tests new
   * their own with `createUiExtensionRegistry()`.
   */
  readonly registry?: UiExtensionRegistry;
}

export function useExtensions(
  options: UseExtensionsOptions = {},
): UseExtensionsValue {
  const licenseState = useLicenseState();
  const registry = options.registry ?? getDefaultRegistry();

  return useMemo<UseExtensionsValue>(() => {
    const entitlements = entitlementSetFromLicenseState(licenseState);
    return {
      navLinks: registry.getNavLinks(entitlements),
      pageRoutes: registry.getPageRoutes(entitlements),
      panelWidgets: (slot) => registry.getPanelWidgets(slot, entitlements),
      rowActions: (target) => registry.getRowActions(target, entitlements),
      detailTabs: (target) => registry.getDetailTabs(target, entitlements),
    };
  }, [licenseState, registry]);
}
