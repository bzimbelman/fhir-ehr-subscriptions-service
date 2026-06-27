import { describe, expect, it } from "vitest";
import {
  EMPTY_ENTITLEMENT_SET,
  SPI_SCHEMA_VERSION,
  entitlement,
  makeEntitlementSet,
  type NavLinkExtension,
  type UiExtensionManifest,
} from "@bzonfhir/ui-extensions";
import { createUiExtensionRegistry } from "../UiExtensionRegistry";

function navLink(
  id: string,
  overrides: Partial<NavLinkExtension> = {},
): NavLinkExtension {
  return {
    kind: "nav-link",
    id,
    displayName: id,
    href: `/${id}`,
    order: 100,
    ...overrides,
  };
}

function manifest(
  id: string,
  exts: readonly NavLinkExtension[],
  overrides: Partial<UiExtensionManifest> = {},
): UiExtensionManifest {
  return {
    schemaVersion: SPI_SCHEMA_VERSION,
    id,
    version: "1.0.0",
    supplier: "first-party",
    extensions: exts,
    ...overrides,
  };
}

describe("UiExtensionRegistry", () => {
  it("register() with a valid manifest stores the extensions", () => {
    const registry = createUiExtensionRegistry();
    const m = manifest("test-pkg", [navLink("a"), navLink("b")]);

    registry.register(m);

    expect(registry.getNavLinks(EMPTY_ENTITLEMENT_SET)).toHaveLength(2);
  });

  it("register() with the wrong schemaVersion throws a clear error", () => {
    const registry = createUiExtensionRegistry();
    const bad: UiExtensionManifest = {
      schemaVersion: 999,
      id: "bad",
      version: "1.0.0",
      supplier: "first-party",
      extensions: [navLink("oops")],
    };

    expect(() => registry.register(bad)).toThrow(/schemaVersion mismatch/);
    // and nothing was stored
    expect(registry.getNavLinks(EMPTY_ENTITLEMENT_SET)).toHaveLength(0);
  });

  it("register() of a duplicate id REPLACES the previous registration", () => {
    const registry = createUiExtensionRegistry();
    registry.register(manifest("dup", [navLink("first")]));
    registry.register(manifest("dup", [navLink("second"), navLink("third")]));

    const links = registry.getNavLinks(EMPTY_ENTITLEMENT_SET);
    const ids = links.map((l) => l.id).sort();
    expect(ids).toEqual(["second", "third"]);
  });

  it("getNavLinks() with empty entitlements returns only FOSS links", () => {
    const registry = createUiExtensionRegistry();
    registry.register(
      manifest("mix", [
        navLink("foss"),
        navLink("paid", { requiredEntitlement: entitlement("pro") }),
      ]),
    );

    const links = registry.getNavLinks(EMPTY_ENTITLEMENT_SET);
    expect(links.map((l) => l.id)).toEqual(["foss"]);
  });

  it("getNavLinks() with a matching entitlement returns FOSS + the entitled link, sorted by order", () => {
    const registry = createUiExtensionRegistry();
    registry.register(
      manifest("mix", [
        navLink("foss", { order: 50 }),
        navLink("paid", {
          requiredEntitlement: entitlement("pro"),
          order: 10,
        }),
        navLink("other-paid", {
          requiredEntitlement: entitlement("compliance"),
          order: 1,
        }),
      ]),
    );

    const ents = makeEntitlementSet(["pro"]);
    const links = registry.getNavLinks(ents);
    // FOSS + pro included; "compliance" not entitled, excluded.
    expect(links.map((l) => l.id)).toEqual(["paid", "foss"]);
  });

  it("unregister() removes every extension that came from a manifest id", () => {
    const registry = createUiExtensionRegistry();
    registry.register(manifest("a", [navLink("a1"), navLink("a2")]));
    registry.register(manifest("b", [navLink("b1")]));

    registry.unregister("a");

    const links = registry.getNavLinks(EMPTY_ENTITLEMENT_SET);
    expect(links.map((l) => l.id)).toEqual(["b1"]);
  });
});
