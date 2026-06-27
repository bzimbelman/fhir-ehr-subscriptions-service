import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";

// next/link emits a plain <a> -- the DLQ view doesn't currently use it, but
// future expansion (link to /messages/{id}) would.
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

import { DlqView } from "@/components/DlqView";
import type {
  MessageSummary,
  MessagesListResponse,
} from "@/lib/dashboardTypes";
import type { BulkActionOutcome, MessageDetail } from "@/lib/dlqTypes";

const NOW = new Date("2026-06-26T12:00:00Z");

function mkRow(over: Partial<MessageSummary> = {}): MessageSummary {
  return {
    id: 1,
    received_at: "2026-06-26T11:55:00Z",
    source_protocol: "HL7V2_MLLP",
    source_system: "EPIC",
    source_id: "MSG-1",
    message_type: "ADT_A01",
    status: "DEAD_LETTER",
    attempt_count: 5,
    last_error: "Connection refused on port 8080",
    correlation_id: "corr-1",
    ...over,
  };
}

function mkPage(items: MessageSummary[]): MessagesListResponse {
  return { total: items.length, limit: 50, offset: 0, items };
}

function mkDetail(row: MessageSummary): MessageDetail {
  return {
    ...row,
    raw_message: `MSH|^~\\&|EPIC|EPIC_FACILITY||||||ADT^A01|${row.id}|P|2.5\nPID|||MRN-${row.id}||DOE^JOHN`,
    raw_content_type: "application/hl7-v2",
    last_attempt_at: "2026-06-26T11:54:30Z",
    next_attempt_at: null,
    delivered_at: null,
  };
}

beforeEach(() => {
  // Stable now() so age-band assertions are deterministic.
  vi.spyOn(global.Date, "now").mockReturnValue(NOW.getTime());
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("DlqView", () => {
  it("test #1: fetches /api/admin/messages?status=DEAD_LETTER via the proxy", async () => {
    // Spy on the real default fetch path by checking the call to our wrapper.
    const fetchPage = vi.fn(async () => mkPage([mkRow({ id: 10 })]));
    await act(async () => {
      render(<DlqView fetchPage={fetchPage} nowProvider={() => NOW} />);
    });
    await waitFor(() => {
      expect(fetchPage).toHaveBeenCalledTimes(1);
    });
    expect(fetchPage).toHaveBeenCalledWith({ limit: 50, offset: 0 });
  });

  it("test #1b: client-side fetch wrapper hits /api/admin/messages?status=DEAD_LETTER", async () => {
    // Verifies the underlying fetchDlqPage URL contract. The view test above
    // uses the wrapper as a seam; this one exercises the real wrapper.
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
      const mod = await import("@/lib/dlqClient");
      await mod.fetchDlqPage();
      expect(captured.url).toBe(
        "/api/admin/messages?status=DEAD_LETTER&limit=50&offset=0",
      );
    } finally {
      global.fetch = origFetch;
    }
  });

  it("test #2 (indirect): age badges reflect the band thresholds for rendered rows", async () => {
    const rows = [
      // 30 min ago -> green
      mkRow({ id: 1, received_at: "2026-06-26T11:30:00Z" }),
      // 5 hours ago -> yellow
      mkRow({ id: 2, received_at: "2026-06-26T07:00:00Z" }),
      // 3 days ago -> red
      mkRow({ id: 3, received_at: "2026-06-23T12:00:00Z" }),
    ];
    const fetchPage = vi.fn(async () => mkPage(rows));
    await act(async () => {
      render(<DlqView fetchPage={fetchPage} nowProvider={() => NOW} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-1")).toBeInTheDocument();
    });

    const row1 = screen.getByTestId("dlq-row-1");
    expect(within(row1).getByTestId("age-badge-green")).toBeInTheDocument();
    const row2 = screen.getByTestId("dlq-row-2");
    expect(within(row2).getByTestId("age-badge-yellow")).toBeInTheDocument();
    const row3 = screen.getByTestId("dlq-row-3");
    expect(within(row3).getByTestId("age-badge-red")).toBeInTheDocument();
  });

  it("test #3: bulk replay sends one POST per selected id", async () => {
    const rows = [mkRow({ id: 11 }), mkRow({ id: 12 }), mkRow({ id: 13 })];
    const fetchPage = vi.fn(async () => mkPage(rows));
    const bulkRetryFn = vi.fn(async (ids: readonly number[]) =>
      ids.map((id) => ({ id, ok: true, status: 200 }) as BulkActionOutcome),
    );

    await act(async () => {
      render(
        <DlqView
          fetchPage={fetchPage}
          bulkRetryFn={bulkRetryFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-11")).toBeInTheDocument();
    });

    // Select rows 11 and 13 (not 12).
    fireEvent.click(screen.getByTestId("dlq-row-select-11"));
    fireEvent.click(screen.getByTestId("dlq-row-select-13"));

    expect(screen.getByTestId("dlq-selection-count").textContent).toBe("2");

    await act(async () => {
      fireEvent.click(screen.getByTestId("dlq-bulk-replay"));
    });

    await waitFor(() => {
      expect(bulkRetryFn).toHaveBeenCalledTimes(1);
    });
    // The wrapper was called with exactly the selected ids.
    const calledWith = bulkRetryFn.mock.calls[0]![0]!;
    expect([...calledWith].sort()).toEqual([11, 13]);
  });

  it("test #4: bulk delete opens confirm modal; confirm fires N DELETEs; cancel does nothing", async () => {
    const rows = [mkRow({ id: 21 }), mkRow({ id: 22 })];
    const fetchPage = vi.fn(async () => mkPage(rows));
    const bulkDeleteFn = vi.fn(async (ids: readonly number[]) =>
      ids.map((id) => ({ id, ok: true, status: 204 }) as BulkActionOutcome),
    );

    await act(async () => {
      render(
        <DlqView
          fetchPage={fetchPage}
          bulkDeleteFn={bulkDeleteFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-21")).toBeInTheDocument();
    });

    // Select both rows.
    fireEvent.click(screen.getByTestId("dlq-row-select-21"));
    fireEvent.click(screen.getByTestId("dlq-row-select-22"));

    // Clicking Discard opens the modal but does NOT fire deletes yet.
    fireEvent.click(screen.getByTestId("dlq-bulk-delete"));
    expect(screen.getByTestId("confirm-delete-modal")).toBeInTheDocument();
    expect(bulkDeleteFn).not.toHaveBeenCalled();

    // Cancel closes the modal without firing.
    fireEvent.click(screen.getByTestId("confirm-delete-cancel"));
    expect(screen.queryByTestId("confirm-delete-modal")).not.toBeInTheDocument();
    expect(bulkDeleteFn).not.toHaveBeenCalled();

    // Re-open and confirm -> 1 call with both ids.
    fireEvent.click(screen.getByTestId("dlq-bulk-delete"));
    expect(screen.getByTestId("confirm-delete-modal")).toBeInTheDocument();
    await act(async () => {
      fireEvent.click(screen.getByTestId("confirm-delete-confirm"));
    });

    await waitFor(() => {
      expect(bulkDeleteFn).toHaveBeenCalledTimes(1);
    });
    const calledWith = bulkDeleteFn.mock.calls[0]![0]!;
    expect([...calledWith].sort()).toEqual([21, 22]);
  });

  it("test #5: filter by source_protocol narrows the rendered rows", async () => {
    const rows = [
      mkRow({ id: 31, source_protocol: "HL7V2_MLLP" }),
      mkRow({ id: 32, source_protocol: "FHIR_REST" }),
      mkRow({ id: 33, source_protocol: "HL7V2_MLLP" }),
      mkRow({ id: 34, source_protocol: "EHR_NATIVE_API" }),
    ];
    const fetchPage = vi.fn(async () => mkPage(rows));

    await act(async () => {
      render(<DlqView fetchPage={fetchPage} nowProvider={() => NOW} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-31")).toBeInTheDocument();
    });
    // All four visible initially.
    expect(screen.getByTestId("dlq-row-32")).toBeInTheDocument();
    expect(screen.getByTestId("dlq-row-33")).toBeInTheDocument();
    expect(screen.getByTestId("dlq-row-34")).toBeInTheDocument();

    // Change the dropdown to FHIR_REST.
    fireEvent.change(screen.getByTestId("filter-source-protocol"), {
      target: { value: "FHIR_REST" },
    });

    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-32")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("dlq-row-31")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dlq-row-33")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dlq-row-34")).not.toBeInTheDocument();
  });

  it("test #6: common-errors panel groups by fingerprint and shows correct counts", async () => {
    const rows: MessageSummary[] = [
      // pattern A x 4
      ...[1, 2, 3, 4].map((id) =>
        mkRow({ id, last_error: `Connection refused on port ${8000 + id}` }),
      ),
      // pattern B x 3
      ...[5, 6, 7].map((id) =>
        mkRow({ id, last_error: "Schema violation in Patient resource" }),
      ),
      // pattern C x 3
      ...[8, 9, 10].map((id) =>
        mkRow({ id, last_error: "HTTP 503 Service Unavailable" }),
      ),
    ];
    const fetchPage = vi.fn(async () => mkPage(rows));

    await act(async () => {
      render(<DlqView fetchPage={fetchPage} nowProvider={() => NOW} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-1")).toBeInTheDocument();
    });

    const panel = screen.getByTestId("common-errors-panel");
    // 3 distinct fingerprints visible.
    const itemButtons = within(panel).getAllByRole("button");
    // The fingerprint rows are buttons + the count badges (spans). The
    // fingerprint buttons all have aria-label starting with "Filter by".
    const fpButtons = itemButtons.filter((b) =>
      (b.getAttribute("aria-label") ?? "").startsWith("Filter by"),
    );
    expect(fpButtons).toHaveLength(3);

    // Counts: top 4, then 3, 3.
    const counts = within(panel)
      .getAllByText(/^[0-9]+$/)
      .map((el) => el.textContent);
    expect(counts).toContain("4");
    expect(counts.filter((c) => c === "3")).toHaveLength(2);
  });

  it("test #7: clicking a fingerprint applies it as a last-error-pattern filter", async () => {
    const rows: MessageSummary[] = [
      ...[1, 2].map((id) =>
        mkRow({ id, last_error: "Connection refused on port 8080" }),
      ),
      mkRow({ id: 3, last_error: "Different sort of error message" }),
    ];
    const fetchPage = vi.fn(async () => mkPage(rows));

    await act(async () => {
      render(<DlqView fetchPage={fetchPage} nowProvider={() => NOW} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-1")).toBeInTheDocument();
    });

    // Click the top fingerprint (the connection-refused one). We don't know
    // the exact fingerprint string here, but it's the only one with count 2,
    // which we identify by its button aria-label.
    const panel = screen.getByTestId("common-errors-panel");
    const buttons = within(panel)
      .getAllByRole("button")
      .filter((b) =>
        (b.getAttribute("aria-label") ?? "").startsWith("Filter by"),
      );
    const firstButton = buttons[0]!;
    const firstFingerprint = firstButton.textContent ?? "";
    fireEvent.click(firstButton);

    // The last-error filter input is now populated with the fingerprint text.
    const input = screen.getByTestId(
      "filter-last-error",
    ) as HTMLInputElement;
    expect(input.value).toBe(firstFingerprint);

    // Filter narrows the rendered rows.
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-1")).toBeInTheDocument();
      expect(screen.getByTestId("dlq-row-2")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("dlq-row-3")).not.toBeInTheDocument();
  });

  it("test #8: clicking a row expands it and shows the raw_message", async () => {
    const row = mkRow({ id: 99 });
    const fetchPage = vi.fn(async () => mkPage([row]));
    const fetchDetailFn = vi.fn(async (id: number) => mkDetail(mkRow({ id })));

    await act(async () => {
      render(
        <DlqView
          fetchPage={fetchPage}
          fetchDetailFn={fetchDetailFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-99")).toBeInTheDocument();
    });

    // Click the ID cell to expand.
    await act(async () => {
      fireEvent.click(screen.getByTestId("dlq-row-id-99"));
    });

    // Expansion row appears, then raw_message resolves.
    await waitFor(() => {
      expect(screen.getByTestId("dlq-row-expansion-99")).toBeInTheDocument();
    });
    await waitFor(() => {
      expect(screen.getByTestId("dlq-raw-99")).toBeInTheDocument();
    });

    const raw = screen.getByTestId("dlq-raw-99");
    expect(raw.textContent).toContain("MSH|^~");
    expect(fetchDetailFn).toHaveBeenCalledWith(99);
  });
});
