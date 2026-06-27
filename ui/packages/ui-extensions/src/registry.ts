import type { EntitlementSet } from "./entitlements";
import type {
  DetailTabExtension,
  DetailTabTarget,
  NavLinkExtension,
  PageRouteExtension,
  PanelSlot,
  PanelWidgetExtension,
  RowActionExtension,
  RowActionTarget,
} from "./extensionPoints";
import type { UiExtensionManifest } from "./manifest";

/**
 * Public surface of the host's UI extension registry. The concrete
 * implementation lives in the host (ticket #437); this interface is
 * what every consumer (FOSS UI + commercial bundles) codes against.
 *
 * Lookup methods all take an {@link EntitlementSet} and return only
 * extensions whose `requiredEntitlement` is in the set (or is
 * omitted). This is the gate that hides paid features behind the
 * license check — there's no separate "is this entitled?" path the
 * caller has to remember to invoke.
 */
export interface UiExtensionRegistry {
  /**
   * Register every {@link Extension} declared in a manifest. The
   * host enforces:
   *   - `manifest.schemaVersion` equals the host's
   *     {@link SPI_SCHEMA_VERSION}
   *   - per-extension `id` is globally unique
   *   - `minHostVersion` (if set) is satisfied
   * Failures throw — registration is all-or-nothing per manifest.
   */
  register(manifest: UiExtensionManifest): void;

  /**
   * Remove every extension that came from the manifest with this
   * id. No-op if the manifest was never registered. Used by tests
   * and by hot-reload paths in dev.
   */
  unregister(manifestId: string): void;

  getNavLinks(entitlements: EntitlementSet): readonly NavLinkExtension[];

  getPageRoutes(entitlements: EntitlementSet): readonly PageRouteExtension[];

  getPanelWidgets(
    slot: PanelSlot,
    entitlements: EntitlementSet,
  ): readonly PanelWidgetExtension[];

  getRowActions(
    target: RowActionTarget,
    entitlements: EntitlementSet,
  ): readonly RowActionExtension[];

  getDetailTabs(
    target: DetailTabTarget,
    entitlements: EntitlementSet,
  ): readonly DetailTabExtension[];
}
