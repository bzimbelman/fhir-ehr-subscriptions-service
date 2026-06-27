import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";

// next/link renders as a plain anchor so links are inspectable without
// the Next.js runtime. Matches the pattern used in dlq-view.test.tsx.
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

import { MessagesListView } from "@/components/MessagesListView";
import type {
  MessageSummary,
  MessagesListResponse,
} from "@/lib/dashboardTypes";

const NOW = new Date("2026-06-26T12:00:00Z");

function mkRow(over: Partial<MessageSummary> = {}): MessageSummary {
  return {
    id: 1,
    received_at: "2026-06-26T11:55:00Z",
    source_protocol: "HL7V2_MLLP",
    source_system: "EPIC",
    source_id: "MSG-1",
    message_type: "ADT_A01",
    status: "DELIVERED",
    attempt_count: 1,
    last_error: null,
    correlation_id: "corr-1",
    ...over,
  };
}

function mkPage(items: MessageSummary[]): MessagesListResponse {
  return { total: items.length, limit: 50, offset: 0, items };
}

beforeEach(() => {
  vi.spyOn(global.Date, "now").mockReturnValue(NOW.getTime());
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("MessagesListView (ticket #402)", () => {
  // Test #1 — initial fetch URL contract.
  it("the underlying fetchMessages wrapper hits /api/admin/messages with limit/offset", async () => {
    const captured: { url?: string } = {};
    const origFetch = global.fetch;
    global.fetch = vi.fn(async (url: string) => {
      captured.url = url;
      return new Response(JSON.stringify(mkPage([])), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;
    try {
      const mod = await import("@/lib/messagesClient");
      await mod.fetchMessages({ status: "FAILED", limit: 50, offset: 0 });
      expect(captured.url).toBe(
        "/api/admin/messages?status=FAILED&limit=50&offset=0",
      );
    } finally {
      global.fetch = origFetch;
    }
  });

  // Test #2 — status dropdown changes URL + reissues fetch.
  it("changing the status filter triggers a new fetch with status=...", async () => {
    const fetchMessagesFn = vi.fn(
      async (
        q?: import("@/lib/messagesClient").MessagesListQuery,
      ): Promise<MessagesListResponse> => {
        void q;
        return mkPage([mkRow({ id: 1 })]);
      },
    );

    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });

    await waitFor(() => {
      expect(fetchMessagesFn).toHaveBeenCalled();
    });
    // Initial call: status = ALL (mapped to no `status` param).
    expect(fetchMessagesFn.mock.calls[0]![0]!.status).toBe("ALL");

    fireEvent.change(screen.getByTestId("messages-filter-status"), {
      target: { value: "FAILED" },
    });

    await waitFor(() => {
      expect(fetchMessagesFn).toHaveBeenCalledTimes(2);
    });
    expect(fetchMessagesFn.mock.calls[1]![0]!.status).toBe("FAILED");
  });

  // Test #3 — time-range narrows rendered rows client-side.
  it("the time-range filter narrows rendered rows (client-side)", async () => {
    const rows = [
      // 30 min ago -> inside 24h, today
      mkRow({ id: 11, received_at: "2026-06-26T11:30:00Z" }),
      // 3 days ago -> outside 24h, today
      mkRow({ id: 12, received_at: "2026-06-23T12:00:00Z" }),
      // 10 days ago -> outside 7d
      mkRow({ id: 13, received_at: "2026-06-16T12:00:00Z" }),
    ];
    const fetchMessagesFn = vi.fn(async () => mkPage(rows));

    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-row-11")).toBeInTheDocument();
    });
    expect(screen.getByTestId("messages-row-12")).toBeInTheDocument();
    expect(screen.getByTestId("messages-row-13")).toBeInTheDocument();

    // Narrow to last 24h.
    fireEvent.change(screen.getByTestId("messages-filter-time"), {
      target: { value: "24h" },
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-row-11")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("messages-row-12")).not.toBeInTheDocument();
    expect(screen.queryByTestId("messages-row-13")).not.toBeInTheDocument();
  });

  // Test #4 — pagination boundary states.
  it("pagination buttons disable correctly at the boundaries", async () => {
    const rows = Array.from({ length: 50 }, (_, i) => mkRow({ id: i + 1 }));
    const fetchMessagesFn = vi.fn(
      async (q: { offset?: number } | undefined) => {
        const offset = q?.offset ?? 0;
        // Total = 100 so there are two pages.
        return {
          total: 100,
          limit: 50,
          offset,
          items: rows.map((r) => ({ ...r, id: r.id + offset })),
        };
      },
    );

    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-page-prev")).toBeInTheDocument();
    });

    // At offset 0: Previous disabled, Next enabled.
    const prev = screen.getByTestId(
      "messages-page-prev",
    ) as HTMLButtonElement;
    const next = screen.getByTestId(
      "messages-page-next",
    ) as HTMLButtonElement;
    expect(prev.disabled).toBe(true);
    expect(next.disabled).toBe(false);

    // Click Next → offset becomes 50, this is the last page; Next disables.
    await act(async () => {
      fireEvent.click(next);
    });
    await waitFor(() => {
      expect(
        (screen.getByTestId("messages-page-next") as HTMLButtonElement)
          .disabled,
      ).toBe(true);
    });
    expect(
      (screen.getByTestId("messages-page-prev") as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  // Test #5 — source-system filter narrows client-side.
  it("the source-system filter narrows rendered rows (contains, case-insensitive)", async () => {
    const rows = [
      mkRow({ id: 21, source_system: "EPIC" }),
      mkRow({ id: 22, source_system: "CERNER" }),
      mkRow({ id: 23, source_system: "EPIC_PROD" }),
    ];
    const fetchMessagesFn = vi.fn(async () => mkPage(rows));

    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-row-21")).toBeInTheDocument();
    });
    fireEvent.change(screen.getByTestId("messages-filter-source"), {
      target: { value: "epic" },
    });
    await waitFor(() => {
      expect(screen.queryByTestId("messages-row-22")).not.toBeInTheDocument();
    });
    expect(screen.getByTestId("messages-row-21")).toBeInTheDocument();
    expect(screen.getByTestId("messages-row-23")).toBeInTheDocument();
  });

  // Test #6 — rows link to /messages/{id}.
  it("rows render a link to /messages/{id}", async () => {
    const fetchMessagesFn = vi.fn(async () => mkPage([mkRow({ id: 42 })]));
    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-link-42")).toBeInTheDocument();
    });
    expect(
      screen.getByTestId("messages-link-42").getAttribute("href"),
    ).toBe("/messages/42");
  });

  // Test #7 — attempts highlighted in amber when > 1.
  it("the attempts cell uses an amber tone when attempt_count > 1", async () => {
    const fetchMessagesFn = vi.fn(async () =>
      mkPage([
        mkRow({ id: 31, attempt_count: 1 }),
        mkRow({ id: 32, attempt_count: 3 }),
      ]),
    );
    await act(async () => {
      render(
        <MessagesListView
          fetchMessagesFn={fetchMessagesFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("messages-attempts-31")).toBeInTheDocument();
    });
    expect(screen.getByTestId("messages-attempts-31").className).not.toMatch(
      /text-amber-700/,
    );
    expect(screen.getByTestId("messages-attempts-32").className).toMatch(
      /text-amber-700/,
    );
  });
});
