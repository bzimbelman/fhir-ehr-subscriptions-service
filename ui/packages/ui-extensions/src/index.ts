/**
 * `@bzonfhir/ui-extensions` — public SPI for subscription-service UI
 * extensions. See README.md for the model and master plan §3.2.1
 * for the rationale.
 *
 * This package is types-and-helpers only. The host
 * {@link UiExtensionRegistry} implementation lives in
 * `subscription-service/ui` (ticket #437).
 */

export {
  EMPTY_ENTITLEMENT_SET,
  entitlement,
  makeEntitlementSet,
} from "./entitlements";
export type { Entitlement, EntitlementSet } from "./entitlements";

export type {
  BaseExtension,
  DetailTabExtension,
  DetailTabProps,
  DetailTabTarget,
  Extension,
  ExtensionKind,
  NavLinkExtension,
  PageRouteExtension,
  PanelSlot,
  PanelWidgetExtension,
  PanelWidgetProps,
  RowActionContext,
  RowActionExtension,
  RowActionResult,
  RowActionTarget,
} from "./extensionPoints";

export type { ExtensionSupplier, UiExtensionManifest } from "./manifest";

export type { UiExtensionRegistry } from "./registry";

export { SPI_SCHEMA_VERSION } from "./version";
export type { SpiSchemaVersion } from "./version";
