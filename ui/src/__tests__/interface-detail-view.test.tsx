import { describe, it, expect, vi } from "vitest";
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

import { InterfaceDetailView } from "@/components/InterfaceDetailView";
import type {
  MessagesListResponse,
  ObserveThroughput,
} from "@/lib/dashboardTypes";
import type {
  fetchInterfaceMessages,
  fetchThroughput24h,
} from "@/lib/interfacesClient";

const NOW = new Date("2026-06-26T12:00:00Z");

function makeThroughput(buckets = 24): ObserveThroughput {
  const out: ObserveThroughput["buckets"] = [];
  for (let i = 0; i < buckets; i++) {
    out.push({
      bucket_start: new Date(NOW.getTime() - i * 3600_000).toISOString(),
      counts: { DELIVERED: 5, FAILED: i % 7 === 0 ? 1 : 0 },
    });
  }
  return {
    schema_version: "1.0",
    window: "24h",
    bucket_width: "hour",
    buckets: out,
  };
}

function makeMessagesPage(): MessagesListResponse {
  return {
    total: 3,
    limit: 50,
    offset: 0,
    items: [
      {
        id: 101,
        received_at: "2026-06-26T11:00:00Z",
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        source_id: "MSG1",
        message_type: "ADT_A01",
        status: "DELIVERED",
        attempt_count: 1,
      },
      {
        id: 102,
        received_at: "2026-06-26T10:55:00Z",
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        source_id: "MSG2",
        message_type: "ADT_A03",
        status: "FAILED",
        attempt_count: 2,
      },
      {
        id: 103,
        received_at: "2026-06-26T10:50:00Z",
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        source_id: "MSG3",
        message_type: "ORM_O01",
        status: "DELIVERED",
        attempt_count: 1,
      },
    ],
  };
}

describe("InterfaceDetailView", () => {
  it("renders 24 sparkline points from the throughput response", async () => {
    const fetchMessages = vi.fn<typeof fetchInterfaceMessages>(async () => ({
      data: makeMessagesPage(),
      error: null,
    }));
    const fetchThroughput = vi.fn<typeof fetchThroughput24h>(async () => ({
      data: makeThroughput(24),
      error: null,
    }));

    await act(async () => {
      render(
        <InterfaceDetailView
          sourceSystem="EPIC"
          sourceProtocol="HL7V2_MLLP"
          nowProvider={() => NOW}
          fetchMessages={fetchMessages}
          fetchThroughput={fetchThroughput}
        />,
      );
    });

    await waitFor(() => {
      const points = screen.getAllByTestId("sparkline-point");
      expect(points).toHaveLength(24);
    });
  });

  it("status filter dropdown changes URL fetch params and reissues fetch", async () => {
    const fetchMessages = vi.fn<typeof fetchInterfaceMessages>(async () => ({
      data: makeMessagesPage(),
      error: null,
    }));
    const fetchThroughput = vi.fn<typeof fetchThroughput24h>(async () => ({
      data: makeThroughput(),
      error: null,
    }));

    await act(async () => {
      render(
        <InterfaceDetailView
          sourceSystem="EPIC"
          sourceProtocol="HL7V2_MLLP"
          nowProvider={() => NOW}
          fetchMessages={fetchMessages}
          fetchThroughput={fetchThroughput}
        />,
      );
    });
    await waitFor(() => {
      expect(fetchMessages).toHaveBeenCalledTimes(1);
    });
    // First call: no status filter
    expect(fetchMessages.mock.calls[0]![0]).toMatchObject({
      sourceSystem: "EPIC",
      status: undefined,
    });

    // Change the filter -> should reissue with status=FAILED
    await act(async () => {
      fireEvent.change(screen.getByTestId("status-filter"), {
        target: { value: "FAILED" },
      });
    });

    await waitFor(() => {
      expect(fetchMessages).toHaveBeenCalledTimes(2);
    });
    expect(fetchMessages.mock.calls[1]![0]).toMatchObject({
      sourceSystem: "EPIC",
      status: "FAILED",
    });
  });

  it("renders message rows linking to /messages/[id]", async () => {
    const fetchMessages = vi.fn<typeof fetchInterfaceMessages>(async () => ({
      data: makeMessagesPage(),
      error: null,
    }));
    const fetchThroughput = vi.fn<typeof fetchThroughput24h>(async () => ({
      data: makeThroughput(),
      error: null,
    }));

    await act(async () => {
      render(
        <InterfaceDetailView
          sourceSystem="EPIC"
          sourceProtocol="HL7V2_MLLP"
          nowProvider={() => NOW}
          fetchMessages={fetchMessages}
          fetchThroughput={fetchThroughput}
        />,
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("message-row-101")).toBeInTheDocument();
    });
    const link = screen.getByTestId("message-link-101");
    expect(link.getAttribute("href")).toBe("/messages/101");
  });

  it("message-type filter narrows displayed rows client-side without re-fetching", async () => {
    const fetchMessages = vi.fn<typeof fetchInterfaceMessages>(async () => ({
      data: makeMessagesPage(),
      error: null,
    }));
    const fetchThroughput = vi.fn<typeof fetchThroughput24h>(async () => ({
      data: makeThroughput(),
      error: null,
    }));

    await act(async () => {
      render(
        <InterfaceDetailView
          sourceSystem="EPIC"
          sourceProtocol="HL7V2_MLLP"
          nowProvider={() => NOW}
          fetchMessages={fetchMessages}
          fetchThroughput={fetchThroughput}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-row-101")).toBeInTheDocument();
    });

    await act(async () => {
      fireEvent.change(screen.getByTestId("message-type-filter"), {
        target: { value: "ORM" },
      });
    });

    // Only ORM_O01 (id=103) should remain.
    expect(screen.queryByTestId("message-row-101")).toBeNull();
    expect(screen.queryByTestId("message-row-102")).toBeNull();
    expect(screen.getByTestId("message-row-103")).toBeInTheDocument();

    // No additional fetch should have fired -- client-side filter only.
    expect(fetchMessages).toHaveBeenCalledTimes(1);
  });
});
