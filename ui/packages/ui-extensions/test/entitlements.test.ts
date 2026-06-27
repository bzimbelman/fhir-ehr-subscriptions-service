import { describe, expect, test } from "vitest";
import {
  EMPTY_ENTITLEMENT_SET,
  entitlement,
  makeEntitlementSet,
} from "../src";

describe("entitlement()", () => {
  test("brands a plain string", () => {
    const e = entitlement("audit.export.iti20");
    // The brand is a compile-time guard; at runtime it's still a string.
    expect(typeof e).toBe("string");
    expect(e).toBe("audit.export.iti20");
  });
});

describe("makeEntitlementSet()", () => {
  test("has() honors string identity", () => {
    const set = makeEntitlementSet([
      "audit.export.iti20",
      "alerting.pagerduty",
    ]);
    expect(set.has(entitlement("audit.export.iti20"))).toBe(true);
    expect(set.has(entitlement("alerting.pagerduty"))).toBe(true);
    expect(set.has(entitlement("nope"))).toBe(false);
  });

  test("collapses duplicates", () => {
    const set = makeEntitlementSet(["a", "a", "b"]);
    expect(set.toArray()).toHaveLength(2);
  });

  test("toArray returns the entitlements as the branded type", () => {
    const set = makeEntitlementSet(["one", "two"]);
    const arr = set.toArray();
    expect(arr).toEqual(["one", "two"]);
    // Re-membership check round-trips through the branded values.
    for (const e of arr) {
      expect(set.has(e)).toBe(true);
    }
  });
});

describe("EMPTY_ENTITLEMENT_SET", () => {
  test("is empty and matches nothing", () => {
    expect(EMPTY_ENTITLEMENT_SET.toArray()).toEqual([]);
    expect(EMPTY_ENTITLEMENT_SET.has(entitlement("anything"))).toBe(false);
  });
});
