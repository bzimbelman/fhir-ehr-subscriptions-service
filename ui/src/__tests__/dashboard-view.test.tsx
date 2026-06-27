import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";

// next/link emits a plain <a> in the test environment.
vi.mock("next/link", () => ({
  __esModule: true,
  default: ({
    href,
    children,
    ...rest
  }: {
    href: string;
    children: React.ReactNode;
  }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

import { DashboardView } from "@/components/DashboardView";
import type { DashboardSnapshotWithTotals } from "@/lib/dashboardClient";

const SNAPSHOT_BASE: DashboardSnapshotWithTotals = {
  system: {
    schema_version: "1.0",
    service: "interface-engine",
    uptime_seconds: 100,
    feature_toggles: {},
    downstream: {},
    queue: {
      received: 1,
      transforming: 0,
      failed: 0,
      delivered: 5,
      dead_letter: 2,
    },
    components: [
      { name: "interface-engine", status: "UP" },
      { name: "matchbox", status: "UP" },
      { name: "hapi", status: "UP" },
      { name: "postgres", status: "UP" },
    ],
  },
  systemError: null,
  throughput: {
    schema_version: "1.0",
    window: "24h",
    bucket_width: "hour",
    buckets: [
      {
        bucket_start: "2026-06-26T00:00:00Z",
        counts: { DELIVERED: 100, FAILED: 1 },
      },
    ],
  },
  throughputError: null,
  throughputWeek: {
    schema_version: "1.0",
    window: "7d",
    bucket_width: "day",
    buckets: [
      { bucket_start: "2026-06-20T00:00:00Z", counts: { DELIVERED: 700 } },
    ],
  },
  throughputMonth: {
    schema_version: "1.0",
    window: "30d",
    bucket_width: "day",
    buckets: [
      { bucket_start: "2026-05-26T00:00:00Z", counts: { DELIVERED: 3000 } },
    ],
  },
  recentMessages: [
    {
      id: 42,
      received_at: "2026-06-26T11:55:00Z",
      source_protocol: "HL7V2_MLLP",
      source_system: "EPIC",
      source_id: "MSGCTRL00042",
      message_type: "ADT_A01",
      status: "DELIVERED",
      attempt_count: 1,
    },
    {
      id: 43,
      received_at: "2026-06-26T11:50:00Z",
      source_protocol: "FHIR_REST",
      source_system: "CERNER",
      source_id: "obs-43",
      message_type: "Observation",
      status: "DEAD_LETTER",
      attempt_count: 5,
      last_error: "schema violation",
    },
  ],
  recentError: null,
  subscriptionsHealth: {
    total: 2,
    items: [
      {
        id: "Subscription/1",
        active: true,
        channel_type: "rest-hook",
        endpoint: "https://a.example/notify",
        delivery_success_count: 10,
        delivery_failure_count: 0,
      },
      {
        id: "Subscription/2",
        active: true,
        channel_type: "rest-hook",
        endpoint: "https://b.example/notify",
        delivery_success_count: 5,
        delivery_failure_count: 0,
      },
    ],
  },
  subscriptionsError: null,
  actuator: null,
  actuatorError: null,
  fetchedAt: "2026-06-26T12:00:00Z",
  totalsToday: 101,
  totalsWeek: 700,
  totalsMonth: 3000,
  successRateToday: 100 / 101,
};

function makeFetcher(snap: DashboardSnapshotWithTotals) {
  return vi.fn(async () => snap);
}

const noopSignOut = <div data-testid="sign-out-slot" />;

beforeEach(() => {
  // Pin document.visibilityState to "visible" via Object.defineProperty so
  // the visibility-change branch is reachable.
  Object.defineProperty(document, "visibilityState", {
    value: "visible",
    writable: true,
    configurable: true,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("DashboardView", () => {
  it("renders all six stat cards after the initial fetch", async () => {
    const fetcher = makeFetcher(SNAPSHOT_BASE);
    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("stat-card-today")).toBeInTheDocument();
    });
    expect(screen.getByTestId("stat-card-this-week")).toBeInTheDocument();
    expect(screen.getByTestId("stat-card-this-month")).toBeInTheDocument();
    expect(screen.getByTestId("stat-card-success-today")).toBeInTheDocument();
    expect(screen.getByTestId("stat-card-dlq-size")).toBeInTheDocument();
    expect(screen.getByTestId("stat-card-active-subs")).toBeInTheDocument();

    // Values are present (textContent search is robust to formatting).
    const todayCard = screen.getByTestId("stat-card-today");
    expect(todayCard.textContent).toContain("101");
    const dlqCard = screen.getByTestId("stat-card-dlq-size");
    expect(dlqCard.textContent).toContain("2");
  });

  it("renders the system status pill as GREEN (UP) when all components UP", async () => {
    const fetcher = makeFetcher(SNAPSHOT_BASE);
    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
        />,
      );
    });
    await waitFor(() => {
      const topBar = screen.getByTestId("dashboard-top-bar");
      expect(within(topBar).getByTestId("status-pill-UP")).toBeInTheDocument();
    });
  });

  it("renders the system status pill as YELLOW (DEGRADED) when any component DEGRADED", async () => {
    const snap = {
      ...SNAPSHOT_BASE,
      system: {
        ...SNAPSHOT_BASE.system!,
        components: [
          { name: "interface-engine", status: "UP" as const },
          { name: "hapi", status: "DEGRADED" as const },
        ],
      },
    };
    const fetcher = makeFetcher(snap);
    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
        />,
      );
    });
    await waitFor(() => {
      const topBar = screen.getByTestId("dashboard-top-bar");
      expect(
        within(topBar).getByTestId("status-pill-DEGRADED"),
      ).toBeInTheDocument();
    });
  });

  it("renders the system status pill as RED (DOWN) when any component DOWN", async () => {
    const snap = {
      ...SNAPSHOT_BASE,
      system: {
        ...SNAPSHOT_BASE.system!,
        components: [
          { name: "interface-engine", status: "UP" as const },
          { name: "postgres", status: "DOWN" as const },
          { name: "hapi", status: "DEGRADED" as const },
        ],
      },
    };
    const fetcher = makeFetcher(snap);
    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
        />,
      );
    });
    await waitFor(() => {
      const topBar = screen.getByTestId("dashboard-top-bar");
      expect(
        within(topBar).getByTestId("status-pill-DOWN"),
      ).toBeInTheDocument();
    });
  });

  it("renders the recent-activity rows with status badge + source_system + message_type", async () => {
    const fetcher = makeFetcher(SNAPSHOT_BASE);
    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("recent-row-42")).toBeInTheDocument();
    });

    const row42 = screen.getByTestId("recent-row-42");
    expect(row42.textContent).toContain("DELIVERED");
    expect(row42.textContent).toContain("EPIC");
    expect(row42.textContent).toContain("ADT_A01");
    // Link points at the placeholder per-message route (lands in #402).
    const link = row42.querySelector("a");
    expect(link?.getAttribute("href")).toBe("/messages/42");

    const row43 = screen.getByTestId("recent-row-43");
    expect(row43.textContent).toContain("DEAD_LETTER");
    expect(row43.textContent).toContain("CERNER");
  });

  it("skips auto-refresh while document.hidden is true (only initial fetch fires)", async () => {
    vi.useFakeTimers();
    const fetcher = makeFetcher(SNAPSHOT_BASE);

    await act(async () => {
      render(
        <DashboardView
          username="alice"
          signOutSlot={noopSignOut}
          fetchSnapshot={fetcher}
          enableVisibilityRefresh={false}
        />,
      );
    });
    // Initial mount triggers exactly one call.
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Hide the tab BEFORE the next interval tick.
    Object.defineProperty(document, "visibilityState", {
      value: "hidden",
      writable: true,
      configurable: true,
    });

    // Advance time well past the 30s polling interval.
    await act(async () => {
      vi.advanceTimersByTime(120_000);
    });

    // No additional fetches while hidden.
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Become visible again -- next interval tick should fetch.
    Object.defineProperty(document, "visibilityState", {
      value: "visible",
      writable: true,
      configurable: true,
    });
    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });
    expect(fetcher).toHaveBeenCalledTimes(2);

    vi.useRealTimers();
  });
});
