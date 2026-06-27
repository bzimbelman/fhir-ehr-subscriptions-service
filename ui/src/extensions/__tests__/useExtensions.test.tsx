import { describe, expect, it } from "vitest";
import { act, render } from "@testing-library/react";
import {
  SPI_SCHEMA_VERSION,
  entitlement,
  type NavLinkExtension,
  type UiExtensionManifest,
} from "@bzonfhir/ui-extensions";
import { useState } from "react";
import {
  LicenseProvider,
  useSetLicenseState,
} from "../LicenseProvider";
import { createUiExtensionRegistry } from "../UiExtensionRegistry";
import {
  entitlementSetFromLicenseState,
  useExtensions,
} from "../useExtensions";
import { entitlementSetFromArray, type LicenseState } from "@/lib/license/types";

const fossNav: NavLinkExtension = {
  kind: "nav-link",
  id: "foss-link",
  displayName: "FOSS",
  href: "/foss",
  order: 10,
};
const paidNav: NavLinkExtension = {
  kind: "nav-link",
  id: "paid-link",
  displayName: "Paid",
  href: "/paid",
  order: 20,
  requiredEntitlement: entitlement("compliance.iti20"),
};

function buildRegistry() {
  const registry = createUiExtensionRegistry();
  const m: UiExtensionManifest = {
    schemaVersion: SPI_SCHEMA_VERSION,
    id: "test-pkg",
    version: "1.0.0",
    supplier: "first-party",
    extensions: [fossNav, paidNav],
  };
  registry.register(m);
  return registry;
}

function Harness({
  onValue,
  registry,
}: {
  onValue: (links: readonly NavLinkExtension[]) => void;
  registry: ReturnType<typeof createUiExtensionRegistry>;
}) {
  const { navLinks } = useExtensions({ registry });
  onValue(navLinks);
  return null;
}

const ACTIVE_STATE: LicenseState = {
  kind: "active",
  info: {
    tierName: "Pro",
    expiresAt: new Date("2027-03-01T00:00:00Z"),
    licenseKeyFingerprint: "deadbeef",
  },
  entitlements: entitlementSetFromArray(["compliance.iti20"]),
  fetchedAt: new Date("2026-06-27T00:00:00Z"),
  cacheValidUntil: new Date("2026-07-04T00:00:00Z"),
};

const FOSS_STATE: LicenseState = { kind: "foss", reason: "no-license-key" };

describe("useExtensions", () => {
  it("returns FOSS-only nav links when license state is foss", () => {
    const registry = buildRegistry();
    let captured: readonly NavLinkExtension[] = [];
    render(
      <LicenseProvider initialState={FOSS_STATE}>
        <Harness onValue={(v) => (captured = v)} registry={registry} />
      </LicenseProvider>,
    );
    expect(captured.map((l) => l.id)).toEqual(["foss-link"]);
  });

  it("returns FOSS + entitled nav links when license state is active", () => {
    const registry = buildRegistry();
    let captured: readonly NavLinkExtension[] = [];
    render(
      <LicenseProvider initialState={ACTIVE_STATE}>
        <Harness onValue={(v) => (captured = v)} registry={registry} />
      </LicenseProvider>,
    );
    expect(captured.map((l) => l.id)).toEqual(["foss-link", "paid-link"]);
  });

  it("re-renders consumers when the license state changes", () => {
    const registry = buildRegistry();
    let captured: readonly NavLinkExtension[] = [];

    function Switcher() {
      const [state, setLocal] = useState<LicenseState>(FOSS_STATE);
      return (
        <LicenseProvider initialState={state} key={state.kind}>
          <Harness onValue={(v) => (captured = v)} registry={registry} />
          <ProviderBridge onSetter={(s) => (switcherSet.current = s)} />
          <button
            type="button"
            data-testid="bump-to-active"
            onClick={() => setLocal(ACTIVE_STATE)}
          />
        </LicenseProvider>
      );
    }

    // Indirection: we need a setter from inside the provider to drive
    // the transition via the public hook surface.
    const switcherSet: { current: ((s: LicenseState) => void) | null } = {
      current: null,
    };
    function ProviderBridge({
      onSetter,
    }: {
      onSetter: (s: (st: LicenseState) => void) => void;
    }) {
      const setLicense = useSetLicenseState();
      onSetter(setLicense);
      return null;
    }

    render(<Switcher />);
    expect(captured.map((l) => l.id)).toEqual(["foss-link"]);

    act(() => {
      switcherSet.current?.(ACTIVE_STATE);
    });
    expect(captured.map((l) => l.id)).toEqual(["foss-link", "paid-link"]);
  });
});

describe("entitlementSetFromLicenseState", () => {
  it("returns empty for foss", () => {
    const set = entitlementSetFromLicenseState(FOSS_STATE);
    expect(set.toArray()).toEqual([]);
  });
  it("returns the entitlements for active", () => {
    const set = entitlementSetFromLicenseState(ACTIVE_STATE);
    expect(Array.from(set.toArray())).toEqual(["compliance.iti20"]);
  });
});
