import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";

import { SettingsView } from "@/components/SettingsView";
import type { ApiResult } from "@/lib/settingsClient";
import type { SystemSnapshot } from "@/lib/settingsTypes";
import type { MatchboxHealth } from "@/lib/matchboxTypes";

function baseSnapshot(over: Partial<SystemSnapshot> = {}): SystemSnapshot {
  return {
    schema_version: "1.0",
    service: "subscription-service-interface-engine",
    version: "0.0.1-SNAPSHOT",
    uptime_seconds: 123,
    feature_toggles: {
      auth_enabled: true,
      validation_mode: "warn",
      channel_security_mode: "strict",
      multitenancy_mode: "disabled",
    },
    downstream: {
      matchbox_base_url: "http://matchbox:8080/matchboxv3/fhir",
      hapi_base_url: "http://hapi:8080/fhir",
      auth_issuer: "https://keycloak.example/auth/realms/cds",
    },
    ...over,
  };
}

function healthyHealth(over: Partial<MatchboxHealth> = {}): MatchboxHealth {
  return {
    reachable: true,
    version: "v3.9.13",
    base_url: "http://matchbox:8080/matchboxv3/fhir",
    checked_at: "2026-06-26T11:59:55Z",
    response_time_ms: 42,
    error: null,
    ...over,
  };
}

function ok<T>(data: T): () => Promise<ApiResult<T>> {
  return vi.fn(async () => ({ data, error: null }));
}

beforeEach(() => {
  Object.defineProperty(document, "visibilityState", {
    value: "visible",
    writable: true,
    configurable: true,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("SettingsView (ticket #406)", () => {
  it("fetches /api/admin/observe/system on mount", async () => {
    const fetchSystem = ok(baseSnapshot());
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    expect(fetchSystem).toHaveBeenCalledTimes(1);
    expect(fetchMatchbox).toHaveBeenCalledTimes(1);
  });

  it("renders all 4 feature toggle cards with the values from the snapshot", async () => {
    const fetchSystem = ok(baseSnapshot());
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("settings-toggle-auth_enabled")).toBeInTheDocument();
    });
    expect(screen.getByTestId("settings-toggle-validation_mode")).toBeInTheDocument();
    expect(
      screen.getByTestId("settings-toggle-channel_security_mode"),
    ).toBeInTheDocument();
    expect(
      screen.getByTestId("settings-toggle-multitenancy_mode"),
    ).toBeInTheDocument();
  });

  it("renders mode toggles as the mode name pill", async () => {
    const fetchSystem = ok(
      baseSnapshot({
        feature_toggles: {
          auth_enabled: true,
          validation_mode: "enforce",
          channel_security_mode: "strict",
          multitenancy_mode: "disabled",
        },
      }),
    );
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-toggle-validation_mode-pill").textContent,
      ).toBe("enforce");
    });
    expect(
      screen.getByTestId("settings-toggle-channel_security_mode-pill").textContent,
    ).toBe("strict");
    expect(
      screen.getByTestId("settings-toggle-multitenancy_mode-pill").textContent,
    ).toBe("disabled");
  });

  it("renders boolean toggle as 'on' / 'off'", async () => {
    const fetchOn = ok(
      baseSnapshot({
        feature_toggles: { auth_enabled: true, validation_mode: "off" },
      }),
    );
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView fetchSystem={fetchOn} fetchMatchbox={fetchMatchbox} />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-toggle-auth_enabled-pill").textContent,
      ).toBe("on");
    });

    // Re-render with auth off.
    const fetchOff = ok(
      baseSnapshot({
        feature_toggles: { auth_enabled: false, validation_mode: "off" },
      }),
    );
    await act(async () => {
      render(
        <SettingsView fetchSystem={fetchOff} fetchMatchbox={fetchMatchbox} />,
      );
    });
    await waitFor(() => {
      const pills = screen.getAllByTestId("settings-toggle-auth_enabled-pill");
      // Two renders are now in the DOM; the second-most-recent contains "off".
      expect(pills.some((el) => el.textContent === "off")).toBe(true);
    });
  });

  it("renders all 3 downstream-component rows (matchbox + hapi + auth issuer)", async () => {
    const fetchSystem = ok(baseSnapshot());
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-downstream-matchbox"),
      ).toBeInTheDocument();
    });
    expect(screen.getByTestId("settings-downstream-hapi")).toBeInTheDocument();
    expect(screen.getByTestId("settings-downstream-auth")).toBeInTheDocument();
  });

  it("Matchbox row shows 'reachable' pill driven by /admin/matchbox/health", async () => {
    const fetchSystem = ok(baseSnapshot());
    const fetchMatchbox = ok(healthyHealth({ reachable: true }));
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-downstream-matchbox-status-reachable"),
      ).toBeInTheDocument();
    });

    // And unreachable when health.reachable=false:
    const fetchUnreachable = ok(
      healthyHealth({ reachable: false, error: "connection refused" }),
    );
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchUnreachable}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.queryAllByTestId(
          "settings-downstream-matchbox-status-unreachable",
        ).length,
      ).toBeGreaterThan(0);
    });
  });

  it("HAPI and auth issuer rows show URL only -- no probe pill", async () => {
    const fetchSystem = ok(baseSnapshot());
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-downstream-hapi-status").textContent,
      ).toBe("—");
    });
    expect(
      screen.getByTestId("settings-downstream-auth-status").textContent,
    ).toBe("—");
    // The HAPI row's URL cell should contain the configured HAPI URL.
    const hapiRow = screen.getByTestId("settings-downstream-hapi");
    expect(hapiRow.textContent).toContain("hapi:8080");
    const authRow = screen.getByTestId("settings-downstream-auth");
    expect(authRow.textContent).toContain("keycloak");
  });

  it("renders schema_version + log schema rows and build version", async () => {
    const fetchSystem = ok(baseSnapshot({ version: "1.2.3-test" }));
    const fetchMatchbox = ok(healthyHealth());
    await act(async () => {
      render(
        <SettingsView
          fetchSystem={fetchSystem}
          fetchMatchbox={fetchMatchbox}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("settings-schema-observe").textContent,
      ).toContain("1.0");
    });
    expect(screen.getByTestId("settings-schema-log").textContent).toContain("1.0");
    expect(screen.getByTestId("settings-build-version").textContent).toBe(
      "1.2.3-test",
    );
  });
});
