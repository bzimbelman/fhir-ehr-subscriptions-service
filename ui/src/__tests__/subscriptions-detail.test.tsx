import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

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

import { SubscriptionDetail } from "@/components/SubscriptionDetail";
import type {
  FhirSubscriptionResource,
  SubscriptionHistoryEnvelope,
} from "@/lib/subscriptionTypes";
import type { ApiResult } from "@/lib/subscriptionsClient";

function makeResource(
  over: Partial<FhirSubscriptionResource> = {},
): FhirSubscriptionResource {
  return {
    resourceType: "Subscription",
    id: "777",
    status: "active",
    criteria: "Patient?",
    channel: {
      type: "rest-hook",
      endpoint: "https://example.com/notify",
    },
    ...over,
  };
}

function ok<T>(data: T): ApiResult<T> {
  return { data, error: null };
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

describe("SubscriptionDetail — detail page (ticket #404)", () => {
  it("fetches the history endpoint with the right id on mount", async () => {
    const fetchHistory = vi.fn(
      async (): Promise<ApiResult<SubscriptionHistoryEnvelope>> =>
        ok({
          subscription_id: "Subscription/777",
          total: 0,
          limit: 50,
          offset: 0,
          items: [],
        }),
    );
    const fetchResource = vi.fn(async () => ok(makeResource()));
    const patchStatus = vi.fn(async () => ok(null));

    await act(async () => {
      render(
        <SubscriptionDetail
          id="777"
          fetchHistory={fetchHistory}
          fetchResource={fetchResource}
          patchStatus={patchStatus}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });

    await waitFor(() => {
      expect(fetchHistory).toHaveBeenCalledWith("777");
    });
    expect(fetchResource).toHaveBeenCalledWith("777");
  });

  it("renders the documented empty state when history items are empty", async () => {
    const fetchHistory = vi.fn(async () =>
      ok({
        subscription_id: "Subscription/777",
        total: 0,
        limit: 50,
        offset: 0,
        items: [],
      }),
    );
    const fetchResource = vi.fn(async () => ok(makeResource()));
    const patchStatus = vi.fn(async () => ok(null));

    await act(async () => {
      render(
        <SubscriptionDetail
          id="777"
          fetchHistory={fetchHistory}
          fetchResource={fetchResource}
          patchStatus={patchStatus}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("history-empty-state")).toBeInTheDocument();
    });
    expect(screen.getByTestId("history-empty-state").textContent).toContain(
      "$status",
    );
  });

  it("clicking the toggle button PATCHes the status endpoint", async () => {
    const fetchHistory = vi.fn(async () =>
      ok({
        subscription_id: "Subscription/777",
        total: 0,
        limit: 50,
        offset: 0,
        items: [],
      }),
    );
    // First read returns active; after PATCH the next read returns off.
    const fetchResource = vi
      .fn<typeof import("@/lib/subscriptionsClient").fetchSubscriptionResource>()
      .mockResolvedValueOnce(ok(makeResource({ status: "active" })))
      .mockResolvedValueOnce(ok(makeResource({ status: "off" })));
    const patchStatus = vi.fn(async () => ok(null));

    await act(async () => {
      render(
        <SubscriptionDetail
          id="777"
          fetchHistory={fetchHistory}
          fetchResource={fetchResource}
          patchStatus={patchStatus}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("toggle-status-button")).toBeInTheDocument();
    });

    await act(async () => {
      fireEvent.click(screen.getByTestId("toggle-status-button"));
    });

    await waitFor(() => {
      expect(patchStatus).toHaveBeenCalledTimes(1);
    });
    // active -> off
    expect(patchStatus).toHaveBeenCalledWith("777", "off");
    // After success the component reloads the resource so the
    // header pill reflects the new state.
    await waitFor(() => {
      expect(fetchResource).toHaveBeenCalledTimes(2);
    });
  });

  it("the manual-trigger button opens the curl-examples panel (no real fire)", async () => {
    const fetchHistory = vi.fn(async () =>
      ok({
        subscription_id: "Subscription/777",
        total: 0,
        limit: 50,
        offset: 0,
        items: [],
      }),
    );
    const fetchResource = vi.fn(async () => ok(makeResource()));
    const patchStatus = vi.fn(async () => ok(null));

    await act(async () => {
      render(
        <SubscriptionDetail
          id="777"
          fetchHistory={fetchHistory}
          fetchResource={fetchResource}
          patchStatus={patchStatus}
          nowProvider={() => new Date("2026-06-26T12:00:00Z")}
        />,
      );
    });

    // The panel is hidden by default — the test seam is "click + assert".
    expect(screen.queryByTestId("manual-trigger-panel")).toBeNull();

    await act(async () => {
      fireEvent.click(screen.getByTestId("manual-trigger-button"));
    });

    const panel = await screen.findByTestId("manual-trigger-panel");
    // v1 is a stub: it shows curl, it never POSTs anything for us.
    expect(patchStatus).not.toHaveBeenCalled();
    expect(panel.textContent).toContain("curl");
  });
});
