import { describe, expect, test } from "vitest";
import {
  entitlement,
  makeEntitlementSet,
  SPI_SCHEMA_VERSION,
  type DetailTabExtension,
  type EntitlementSet,
  type Extension,
  type NavLinkExtension,
  type PageRouteExtension,
  type PanelSlot,
  type PanelWidgetExtension,
  type RowActionExtension,
  type RowActionTarget,
  type DetailTabTarget,
  type UiExtensionManifest,
  type UiExtensionRegistry,
} from "../src";

/**
 * A tiny in-memory registry stub used to prove the public interface
 * is implementable. The real registry lives in the host (ticket
 * #437); this stub only exists to exercise the {@link UiExtensionRegistry}
 * interface against the published types.
 */
function makeStubRegistry(): UiExtensionRegistry {
  const byManifestId = new Map<string, readonly Extension[]>();

  function visible<E extends Extension>(
    list: readonly E[],
    entitlements: EntitlementSet,
  ): readonly E[] {
    return list.filter(
      (e) =>
        e.requiredEntitlement === undefined ||
        entitlements.has(e.requiredEntitlement),
    );
  }

  function allOfKind<K extends Extension["kind"]>(
    kind: K,
  ): readonly Extract<Extension, { kind: K }>[] {
    const out: Extract<Extension, { kind: K }>[] = [];
    for (const exts of byManifestId.values()) {
      for (const e of exts) {
        if (e.kind === kind) {
          out.push(e as Extract<Extension, { kind: K }>);
        }
      }
    }
    return out;
  }

  return {
    register(manifest: UiExtensionManifest) {
      if (manifest.schemaVersion !== SPI_SCHEMA_VERSION) {
        throw new Error(`unsupported schemaVersion ${manifest.schemaVersion}`);
      }
      byManifestId.set(manifest.id, manifest.extensions);
    },
    unregister(manifestId: string) {
      byManifestId.delete(manifestId);
    },
    getNavLinks(entitlements) {
      return visible<NavLinkExtension>(allOfKind("nav-link"), entitlements);
    },
    getPageRoutes(entitlements) {
      return visible<PageRouteExtension>(
        allOfKind("page-route"),
        entitlements,
      );
    },
    getPanelWidgets(slot: PanelSlot, entitlements) {
      return visible<PanelWidgetExtension>(
        allOfKind("panel-widget").filter((w) => w.slot === slot),
        entitlements,
      );
    },
    getRowActions(target: RowActionTarget, entitlements) {
      return visible<RowActionExtension>(
        allOfKind("row-action").filter((a) => a.target === target),
        entitlements,
      );
    },
    getDetailTabs(target: DetailTabTarget, entitlements) {
      return visible<DetailTabExtension>(
        allOfKind("detail-tab").filter((t) => t.target === target),
        entitlements,
      );
    },
  };
}

const manifestWithEverything: UiExtensionManifest = {
  schemaVersion: SPI_SCHEMA_VERSION,
  id: "test-everything",
  version: "0.0.1",
  supplier: "commercial",
  extensions: [
    {
      kind: "nav-link",
      id: "n-foss",
      displayName: "FOSS",
      href: "/foss",
    },
    {
      kind: "nav-link",
      id: "n-paid",
      displayName: "Paid",
      href: "/paid",
      requiredEntitlement: entitlement("paid"),
    },
    {
      kind: "page-route",
      id: "p-paid",
      displayName: "Paid page",
      path: "/paid",
      requiredEntitlement: entitlement("paid"),
      component: async () => ({ default: () => null as unknown as never }),
    },
    {
      kind: "panel-widget",
      id: "w-paid",
      displayName: "Paid widget",
      slot: "dashboard.stats",
      requiredEntitlement: entitlement("paid"),
      component: async () => ({ default: () => null as unknown as never }),
    },
    {
      kind: "row-action",
      id: "a-paid",
      displayName: "Paid action",
      target: "dlq-row",
      label: "Replay",
      requiredEntitlement: entitlement("paid"),
      action: async () => ({ ok: true }),
    },
    {
      kind: "detail-tab",
      id: "t-paid",
      displayName: "Paid tab",
      target: "message-detail",
      label: "Trace",
      requiredEntitlement: entitlement("paid"),
      component: async () => ({ default: () => null as unknown as never }),
    },
  ],
};

describe("UiExtensionRegistry interface (stub impl)", () => {
  test("register + unregister round-trip", () => {
    const r = makeStubRegistry();
    r.register(manifestWithEverything);
    // FOSS entitlements only: just the unconditional nav-link.
    const foss = makeEntitlementSet([]);
    expect(r.getNavLinks(foss).map((n) => n.id)).toEqual(["n-foss"]);

    r.unregister("test-everything");
    expect(r.getNavLinks(foss)).toHaveLength(0);
  });

  test("register rejects unknown schemaVersion", () => {
    const r = makeStubRegistry();
    expect(() =>
      r.register({
        ...manifestWithEverything,
        schemaVersion: 999,
      }),
    ).toThrow(/schemaVersion/);
  });

  test("getters filter by entitlement", () => {
    const r = makeStubRegistry();
    r.register(manifestWithEverything);
    const paid = makeEntitlementSet(["paid"]);

    expect(r.getNavLinks(paid).map((n) => n.id).sort()).toEqual([
      "n-foss",
      "n-paid",
    ]);
    expect(r.getPageRoutes(paid).map((p) => p.id)).toEqual(["p-paid"]);
    expect(
      r.getPanelWidgets("dashboard.stats", paid).map((w) => w.id),
    ).toEqual(["w-paid"]);
    expect(r.getRowActions("dlq-row", paid).map((a) => a.id)).toEqual([
      "a-paid",
    ]);
    expect(
      r.getDetailTabs("message-detail", paid).map((t) => t.id),
    ).toEqual(["t-paid"]);
  });

  test("getters return empty for the wrong slot/target", () => {
    const r = makeStubRegistry();
    r.register(manifestWithEverything);
    const paid = makeEntitlementSet(["paid"]);

    expect(r.getPanelWidgets("settings.observability", paid)).toEqual([]);
    expect(r.getRowActions("subscription-row", paid)).toEqual([]);
    expect(r.getDetailTabs("subscription-detail", paid)).toEqual([]);
  });

  test("getters hide paid extensions for unentitled callers", () => {
    const r = makeStubRegistry();
    r.register(manifestWithEverything);
    const empty = makeEntitlementSet([]);

    expect(r.getNavLinks(empty).map((n) => n.id)).toEqual(["n-foss"]);
    expect(r.getPageRoutes(empty)).toEqual([]);
    expect(r.getPanelWidgets("dashboard.stats", empty)).toEqual([]);
    expect(r.getRowActions("dlq-row", empty)).toEqual([]);
    expect(r.getDetailTabs("message-detail", empty)).toEqual([]);
  });
});
