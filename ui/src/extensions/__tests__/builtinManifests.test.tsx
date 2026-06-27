import { describe, expect, it } from "vitest";
import { SPI_SCHEMA_VERSION } from "@bzonfhir/ui-extensions";
import {
  builtinManifest,
  builtinNavLinks,
  builtinPageRoutes,
} from "../builtinManifests";

const EXPECTED_NAV_HREFS = [
  "/dashboard",
  "/interfaces",
  "/messages",
  "/dlq",
  "/subscriptions",
  "/matchbox",
  "/settings",
  "/audit",
];

const EXPECTED_PAGE_PATHS = [
  "/dashboard",
  "/interfaces",
  "/messages",
  "/dlq",
  "/subscriptions",
  "/matchbox",
  "/settings",
  "/audit",
];

describe("builtinManifests", () => {
  it("declares schemaVersion equal to SPI_SCHEMA_VERSION", () => {
    expect(builtinManifest.schemaVersion).toBe(SPI_SCHEMA_VERSION);
  });

  it("registers all 8 expected nav links", () => {
    const hrefs = builtinNavLinks.map((l) => l.href);
    expect(hrefs).toEqual(EXPECTED_NAV_HREFS);
    // No FOSS builtin has a requiredEntitlement.
    for (const link of builtinNavLinks) {
      expect(link.requiredEntitlement).toBeUndefined();
    }
  });

  it("registers all 8 expected page routes", () => {
    const paths = builtinPageRoutes.map((p) => p.path);
    expect(paths).toEqual(EXPECTED_PAGE_PATHS);
    for (const page of builtinPageRoutes) {
      expect(page.requiredEntitlement).toBeUndefined();
      // component is a lazy thunk -- don't actually invoke it here, but
      // it MUST be a function so the registry consumer can resolve it.
      expect(typeof page.component).toBe("function");
    }
  });

  it("has unique extension ids across the manifest", () => {
    const ids = builtinManifest.extensions.map((e) => e.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
});
