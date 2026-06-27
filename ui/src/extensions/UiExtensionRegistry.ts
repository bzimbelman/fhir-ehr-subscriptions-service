/**
 * In-memory `UiExtensionRegistry` implementation -- the host's single
 * source of truth for which extension fragments the operator UI knows
 * about.
 *
 * This is the concrete implementation referenced by the SPI in
 * `@bzonfhir/ui-extensions` (ticket #436). The interface lives there
 * so commercial bundles and FOSS builtins code against the same
 * shape; the impl lives here because it's host-internal state.
 *
 * Design choices:
 *
 *   - Manifests are stored keyed by `manifest.id`. Re-registering the
 *     same id REPLACES the previous registration (intentional: dev
 *     hot-reload paths and tests rely on this).
 *   - The registry validates `manifest.schemaVersion ===
 *     SPI_SCHEMA_VERSION` on every `register()` call and throws a
 *     clear error otherwise. Mismatched manifests never enter the
 *     store.
 *   - The five `get*` lookup methods filter by entitlement: an
 *     extension with no `requiredEntitlement` (FOSS) is always
 *     included; an extension with one is included only if the caller's
 *     `EntitlementSet` has it.
 *   - `NavLink` and `PanelWidget` results are sorted by `order` (low
 *     first), with ties broken alphabetically by `displayName`. This
 *     makes the rendered order deterministic across manifests.
 *
 * See master plan §3.2.1 for the rationale.
 */

import {
  SPI_SCHEMA_VERSION,
  type DetailTabExtension,
  type DetailTabTarget,
  type EntitlementSet,
  type Extension,
  type NavLinkExtension,
  type PageRouteExtension,
  type PanelSlot,
  type PanelWidgetExtension,
  type RowActionExtension,
  type RowActionTarget,
  type UiExtensionManifest,
  type UiExtensionRegistry,
} from "@bzonfhir/ui-extensions";

/**
 * Build a fresh registry. Each host gets one of these; tests new one
 * up per-case for isolation.
 */
export function createUiExtensionRegistry(): UiExtensionRegistry {
  const manifests = new Map<string, UiExtensionManifest>();

  function register(manifest: UiExtensionManifest): void {
    if (manifest.schemaVersion !== SPI_SCHEMA_VERSION) {
      throw new Error(
        `UiExtensionRegistry: schemaVersion mismatch for manifest "${manifest.id}": ` +
          `expected ${SPI_SCHEMA_VERSION}, got ${manifest.schemaVersion}`,
      );
    }
    manifests.set(manifest.id, manifest);
  }

  function unregister(manifestId: string): void {
    manifests.delete(manifestId);
  }

  function allExtensions(): Extension[] {
    const out: Extension[] = [];
    for (const manifest of manifests.values()) {
      for (const ext of manifest.extensions) {
        out.push(ext);
      }
    }
    return out;
  }

  function isEntitled(
    ext: Extension,
    entitlements: EntitlementSet,
  ): boolean {
    if (!ext.requiredEntitlement) return true;
    return entitlements.has(ext.requiredEntitlement);
  }

  function byOrderThenName<
    T extends { readonly order?: number; readonly displayName: string },
  >(a: T, b: T): number {
    const ao = a.order ?? Number.POSITIVE_INFINITY;
    const bo = b.order ?? Number.POSITIVE_INFINITY;
    if (ao !== bo) return ao - bo;
    return a.displayName.localeCompare(b.displayName);
  }

  function getNavLinks(
    entitlements: EntitlementSet,
  ): readonly NavLinkExtension[] {
    return allExtensions()
      .filter((e): e is NavLinkExtension => e.kind === "nav-link")
      .filter((e) => isEntitled(e, entitlements))
      .sort(byOrderThenName);
  }

  function getPageRoutes(
    entitlements: EntitlementSet,
  ): readonly PageRouteExtension[] {
    return allExtensions()
      .filter((e): e is PageRouteExtension => e.kind === "page-route")
      .filter((e) => isEntitled(e, entitlements));
  }

  function getPanelWidgets(
    slot: PanelSlot,
    entitlements: EntitlementSet,
  ): readonly PanelWidgetExtension[] {
    return allExtensions()
      .filter((e): e is PanelWidgetExtension => e.kind === "panel-widget")
      .filter((e) => e.slot === slot)
      .filter((e) => isEntitled(e, entitlements))
      .sort(byOrderThenName);
  }

  function getRowActions(
    target: RowActionTarget,
    entitlements: EntitlementSet,
  ): readonly RowActionExtension[] {
    return allExtensions()
      .filter((e): e is RowActionExtension => e.kind === "row-action")
      .filter((e) => e.target === target)
      .filter((e) => isEntitled(e, entitlements));
  }

  function getDetailTabs(
    target: DetailTabTarget,
    entitlements: EntitlementSet,
  ): readonly DetailTabExtension[] {
    return allExtensions()
      .filter((e): e is DetailTabExtension => e.kind === "detail-tab")
      .filter((e) => e.target === target)
      .filter((e) => isEntitled(e, entitlements))
      .sort(byOrderThenName);
  }

  return {
    register,
    unregister,
    getNavLinks,
    getPageRoutes,
    getPanelWidgets,
    getRowActions,
    getDetailTabs,
  };
}

/**
 * Lazily-instantiated process-wide registry. Most callers should use
 * this via {@link getDefaultRegistry}; tests that need isolation new
 * their own with {@link createUiExtensionRegistry}.
 *
 * Note: the default registry starts EMPTY. The host calls
 * `seedDefaultRegistryWithBuiltins()` once during boot (see
 * `defaultRegistrySetup.ts`) to register the FOSS manifest. We split
 * that out because importing `builtinManifests` here creates an
 * import cycle through the lazy `component:` thunks pointing at App
 * Router pages.
 */
let _defaultRegistry: UiExtensionRegistry | null = null;

export function getDefaultRegistry(): UiExtensionRegistry {
  if (_defaultRegistry === null) {
    _defaultRegistry = createUiExtensionRegistry();
  }
  return _defaultRegistry;
}

/**
 * Reset the default registry. ONLY for tests -- production code must
 * not touch this.
 */
export function __resetDefaultRegistryForTests(): void {
  _defaultRegistry = null;
}
