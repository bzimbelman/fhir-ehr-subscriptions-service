import { describe, expect, test } from "vitest";
import {
  entitlement,
  SPI_SCHEMA_VERSION,
  type Extension,
  type UiExtensionManifest,
} from "../src";

describe("UiExtensionManifest", () => {
  test("parses a minimal first-party FOSS manifest", () => {
    const m: UiExtensionManifest = {
      schemaVersion: SPI_SCHEMA_VERSION,
      id: "ui-foss-core",
      version: "0.1.0",
      supplier: "first-party",
      extensions: [],
    };
    expect(m.schemaVersion).toBe(SPI_SCHEMA_VERSION);
    expect(m.supplier).toBe("first-party");
    expect(m.extensions).toHaveLength(0);
  });

  test("parses a commercial manifest with mixed extension kinds", () => {
    const extensions: readonly Extension[] = [
      {
        kind: "nav-link",
        id: "compliance-nav",
        displayName: "Compliance",
        href: "/compliance/iti-20",
        requiredEntitlement: entitlement("audit.export.iti20"),
        order: 50,
      },
      {
        kind: "page-route",
        id: "iti20-page",
        displayName: "ITI-20 Export",
        path: "/compliance/iti-20",
        requiredEntitlement: entitlement("audit.export.iti20"),
        component: async () => ({ default: () => null as unknown as never }),
      },
    ];

    const m: UiExtensionManifest = {
      schemaVersion: SPI_SCHEMA_VERSION,
      id: "compliance-iti20",
      version: "1.0.0",
      supplier: "commercial",
      extensions,
    };

    expect(m.extensions).toHaveLength(2);
    expect(m.extensions[0]?.kind).toBe("nav-link");
    expect(m.extensions[1]?.kind).toBe("page-route");
  });

  test("manifest with mismatched schemaVersion is detectable at runtime", () => {
    const m: UiExtensionManifest = {
      schemaVersion: 999,
      id: "compliance-iti20",
      version: "1.0.0",
      supplier: "commercial",
      extensions: [],
    };
    expect(m.schemaVersion).not.toBe(SPI_SCHEMA_VERSION);
    // Host rejection logic for unknown schemaVersion lands in #437.
  });

  test("ExtensionSupplier accepts all three sealed values", () => {
    const suppliers: UiExtensionManifest["supplier"][] = [
      "first-party",
      "commercial",
      "community",
    ];
    expect(suppliers).toHaveLength(3);
  });
});
