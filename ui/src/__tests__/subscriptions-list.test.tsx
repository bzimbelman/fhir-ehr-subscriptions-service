import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";

// next/link emits a plain anchor in tests so we can assert hrefs.
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

import { SubscriptionsList } from "@/components/SubscriptionsList";
import type {
  SubscriptionHealthRow,
  SubscriptionsHealthEnvelope,
} from "@/lib/subscriptionTypes";
import type { ApiResult } from "@/lib/subscriptionsClient";

function makeRow(over: Partial<SubscriptionHealthRow> = {}): SubscriptionHealthRow {
  return {
    id: "Subscription/123",
    active: true,
    status: "active",
    criteria: "Patient?",
    channel_type: "rest-hook",
    endpoint: "https://example.com/notify",
    delivery_success_count: 0,
    delivery_failure_count: 0,
    last_attempt_at: null,
    last_attempt_outcome: null,
    last_error: null,
    ...over,
  };
}

function makeFetcher(envelope: SubscriptionsHealthEnvelope | null, error: string | null = null) {
  const result: ApiResult<SubscriptionsHealthEnvelope> = {
    data: envelope,
    error,
  };
  return vi.fn(async () => result);
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

describe("SubscriptionsList — list view (ticket #404)", () => {
  it("fetches /api/admin/subscriptions/health and renders the rows", async () => {
    const fetcher = makeFetcher({
      total: 2,
      items: [
        makeRow({ id: "Subscription/aa1" }),
        makeRow({ id: "Subscription/bb2", channel_type: "websocket" }),
      ],
    });
    await act(async () => {
      render(
        <SubscriptionsList
          fetcher={fetcher}
          enableAutoRefresh={false}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.getByTestId("subscription-row-aa1")).toBeInTheDocument();
    });
    expect(screen.getByTestId("subscription-row-bb2")).toBeInTheDocument();
    // The "Subscription/aa1" reference renders inside the link cell.
    expect(screen.getByTestId("subscription-row-aa1").textContent).toContain(
      "Subscription/aa1",
    );
  });

  it("renders the empty state with link to docs when total=0", async () => {
    const fetcher = makeFetcher({ total: 0, items: [] });
    await act(async () => {
      render(
        <SubscriptionsList
          fetcher={fetcher}
          enableAutoRefresh={false}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("subscriptions-empty-state"),
      ).toBeInTheDocument();
    });
    const link = screen.getByTestId("external-subscribers-link");
    expect(link.getAttribute("href")).toBe("/docs/external-subscribers");
  });

  describe("status pill reflects the FHIR Subscription.status code", () => {
    it.each([
      ["active", "subscription-pill-active"],
      ["off", "subscription-pill-off"],
      ["requested", "subscription-pill-requested"],
      ["error", "subscription-pill-error"],
    ])("status=%s renders %s", async (status, pillTestId) => {
      const fetcher = makeFetcher({
        total: 1,
        items: [
          makeRow({ id: "Subscription/s1", status, active: status === "active" }),
        ],
      });
      await act(async () => {
        render(
          <SubscriptionsList
            fetcher={fetcher}
            enableAutoRefresh={false}
            nowProvider={() => new Date("2026-06-26T12:00:00Z")}
          />,
        );
      });
      await waitFor(() => {
        expect(screen.getByTestId("subscription-row-s1")).toBeInTheDocument();
      });
      const row = screen.getByTestId("subscription-row-s1");
      expect(within(row).getByTestId(pillTestId)).toBeInTheDocument();
    });
  });

  it("renders success rate as em-dash when both counters are 0 (HAPI $status caveat)", async () => {
    const fetcher = makeFetcher({
      total: 1,
      items: [
        makeRow({
          id: "Subscription/z9",
          delivery_success_count: 0,
          delivery_failure_count: 0,
        }),
      ],
    });
    await act(async () => {
      render(
        <SubscriptionsList
          fetcher={fetcher}
          enableAutoRefresh={false}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("subscription-row-z9")).toBeInTheDocument();
    });
    const cell = screen.getByTestId("subscription-success-rate-z9");
    expect(cell.textContent).toBe("—");
    // Make sure we did NOT render "0.0%" or "0%" — the regression we
    // explicitly want to prevent (operators reading "0%" as "all
    // failing").
    expect(cell.textContent).not.toMatch(/0/);
  });

  it("each row links to /subscriptions/{bare-id}", async () => {
    const fetcher = makeFetcher({
      total: 1,
      items: [makeRow({ id: "Subscription/abc-123" })],
    });
    await act(async () => {
      render(
        <SubscriptionsList
          fetcher={fetcher}
          enableAutoRefresh={false}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });
    await waitFor(() => {
      expect(
        screen.getByTestId("subscription-row-link-abc-123"),
      ).toBeInTheDocument();
    });
    const link = screen.getByTestId("subscription-row-link-abc-123");
    expect(link.getAttribute("href")).toBe("/subscriptions/abc-123");
  });
});
