import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Footer } from "../Footer";
import { entitlementSetFromArray, type LicenseState } from "@/lib/license/types";

const FOSS_STATE: LicenseState = { kind: "foss", reason: "no-license-key" };

const ACTIVE_STATE: LicenseState = {
  kind: "active",
  info: {
    tierName: "Pro",
    expiresAt: new Date("2027-03-01T00:00:00Z"),
    licenseKeyFingerprint: "deadbeef",
  },
  entitlements: entitlementSetFromArray([
    "compliance.iti20",
    "datadog-adapter",
  ]),
  fetchedAt: new Date("2026-06-27T00:00:00Z"),
  cacheValidUntil: new Date("2026-07-04T00:00:00Z"),
};

describe("Footer", () => {
  it("renders the FOSS variant when license state is foss", () => {
    render(<Footer licenseState={FOSS_STATE} version="9.9.9" />);
    const footer = screen.getByTestId("ui-footer");
    expect(footer.textContent).toContain("subscription-service v9.9.9");
    expect(footer.textContent).toContain("FOSS (Apache 2.0)");
    expect(
      screen.getByRole("link", { name: /github\.com\/bzonfhir/ }),
    ).toBeInTheDocument();
  });

  it("renders the active variant with tier, features, and expiry", () => {
    render(<Footer licenseState={ACTIVE_STATE} version="1.2.3" />);
    const footer = screen.getByTestId("ui-footer");
    expect(footer.textContent).toContain("subscription-service v1.2.3");
    expect(footer.textContent).toContain("Pro tier");
    expect(footer.textContent).toContain(
      "entitled features: compliance.iti20, datadog-adapter",
    );
    expect(footer.textContent).toContain("license expires 2027-03-01");
  });

  it("renders the active variant for stale-active state too", () => {
    const stale: LicenseState = {
      kind: "stale-active",
      info: ACTIVE_STATE.info,
      entitlements: ACTIVE_STATE.entitlements,
      fetchedAt: ACTIVE_STATE.fetchedAt,
      bannerMessage: "stale",
    };
    render(<Footer licenseState={stale} version="1.2.3" />);
    expect(screen.getByTestId("ui-footer").textContent).toContain("Pro tier");
  });
});
