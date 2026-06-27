import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { AuditView } from "@/components/AuditView";
import {
  buildAuditQueryString,
  type ApiResult,
} from "@/lib/auditClient";
import type {
  AuditEventRow,
  AuditFilters,
  AuditSearchResponse,
} from "@/lib/auditTypes";

function row(over: Partial<AuditEventRow> = {}): AuditEventRow {
  return {
    id: "AuditEvent/abc",
    recorded: "2026-06-26T12:00:00Z",
    type_code: "rest",
    type_display: "REST",
    subtype_code: "create",
    outcome: "0",
    outcome_display: "Success",
    action: "C",
    agent_who: "Practitioner/123",
    agent_name: "alice@example",
    entity_what: "Patient/456",
    entity_type: "Patient",
    ...over,
  };
}

function pageOf(
  items: AuditEventRow[],
  over: Partial<AuditSearchResponse> = {},
): AuditSearchResponse {
  return {
    total: items.length,
    limit: 50,
    offset: 0,
    items,
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

describe("AuditView (ticket #407)", () => {
  it("fetches /admin/audit with default limit=50 offset=0 on mount", async () => {
    const fetchList = vi.fn(
      async (
        _f: AuditFilters,
        limit: number,
        offset: number,
      ): Promise<ApiResult<AuditSearchResponse>> => {
        return { data: pageOf([row()], { limit, offset }), error: null };
      },
    );
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      expect(fetchList).toHaveBeenCalledTimes(1);
    });
    const firstCall = fetchList.mock.calls[0]!;
    expect(firstCall[1]).toBe(50);
    expect(firstCall[2]).toBe(0);
    expect(firstCall[0]).toEqual({});
  });

  it("color-codes outcome pill: 0 -> green", async () => {
    const fetchList = ok(pageOf([row({ id: "AuditEvent/a", outcome: "0" })]));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      const pill = screen.getByTestId("audit-outcome-0");
      expect(pill).toBeInTheDocument();
      expect(pill.className).toContain("bg-green-100");
    });
  });

  it("color-codes outcome pill: 4 -> yellow", async () => {
    const fetchList = ok(pageOf([row({ id: "AuditEvent/b", outcome: "4" })]));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      const pill = screen.getByTestId("audit-outcome-4");
      expect(pill.className).toContain("bg-yellow-100");
    });
  });

  it("color-codes outcome pill: 8 -> orange", async () => {
    const fetchList = ok(pageOf([row({ id: "AuditEvent/c", outcome: "8" })]));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      const pill = screen.getByTestId("audit-outcome-8");
      expect(pill.className).toContain("bg-orange-100");
    });
  });

  it("color-codes outcome pill: 12 -> red", async () => {
    const fetchList = ok(pageOf([row({ id: "AuditEvent/d", outcome: "12" })]));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      const pill = screen.getByTestId("audit-outcome-12");
      expect(pill.className).toContain("bg-red-100");
    });
  });

  it("filter change re-fetches with the right query params", async () => {
    const fetchList = vi.fn(
      async (
        f: AuditFilters,
        limit: number,
        offset: number,
      ): Promise<ApiResult<AuditSearchResponse>> => {
        return { data: pageOf([], { limit, offset, total: 0 }), error: null };
      },
    );
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      expect(fetchList).toHaveBeenCalledTimes(1);
    });

    await act(async () => {
      fireEvent.change(screen.getByTestId("audit-filter-outcome"), {
        target: { value: "8" },
      });
    });
    await waitFor(() => {
      expect(fetchList.mock.calls.length).toBeGreaterThanOrEqual(2);
    });
    const lastCall = fetchList.mock.calls[fetchList.mock.calls.length - 1]!;
    expect(lastCall[0].outcome).toBe("8");
    // Offset resets to 0 on filter change.
    expect(lastCall[2]).toBe(0);

    await act(async () => {
      fireEvent.change(screen.getByTestId("audit-filter-agent"), {
        target: { value: "alice" },
      });
    });
    await waitFor(() => {
      const last = fetchList.mock.calls[fetchList.mock.calls.length - 1]!;
      expect(last[0].agent).toBe("alice");
      expect(last[0].outcome).toBe("8");
    });
  });

  it("renders the empty state when total=0", async () => {
    const fetchList = ok(pageOf([], { total: 0 }));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("audit-empty")).toBeInTheDocument();
    });
    expect(screen.getByTestId("audit-empty").textContent).toContain(
      "No AuditEvents yet",
    );
  });

  it("row click expands to show the full JSON detail", async () => {
    const fetchList = ok(pageOf([row({ id: "AuditEvent/xyz" })]));
    const fetchDetail = vi.fn(
      async (id: string): Promise<ApiResult<unknown>> => ({
        data: { resourceType: "AuditEvent", id: id.split("/").pop() },
        error: null,
      }),
    );
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={fetchDetail} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("audit-row-xyz")).toBeInTheDocument();
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("audit-row-xyz"));
    });
    await waitFor(() => {
      expect(screen.getByTestId("audit-row-xyz-json")).toBeInTheDocument();
    });
    const pre = screen.getByTestId("audit-row-xyz-json");
    expect(pre.textContent).toContain('"resourceType": "AuditEvent"');
    expect(fetchDetail).toHaveBeenCalledWith("AuditEvent/xyz");
  });

  it("Previous is disabled on the first page; Next disables at the boundary", async () => {
    // Page with total > PAGE_SIZE so Next is enabled initially.
    const items = Array.from({ length: 50 }, (_, i) =>
      row({ id: `AuditEvent/${i}` }),
    );
    const fetchList = ok(pageOf(items, { total: 120, limit: 50, offset: 0 }));
    await act(async () => {
      render(<AuditView fetchList={fetchList} fetchDetail={vi.fn()} />);
    });
    await waitFor(() => {
      expect(screen.getByTestId("audit-page-prev")).toBeDisabled();
    });
    expect(screen.getByTestId("audit-page-next")).not.toBeDisabled();
  });

  it("buildAuditQueryString omits empty filters and emits limit/offset", () => {
    expect(buildAuditQueryString({}, 50, 0)).toBe("limit=50&offset=0");
    expect(
      buildAuditQueryString(
        {
          outcome: "0",
          agent: "alice",
          dateFrom: "2026-06-01",
          dateTo: "2026-06-30",
        },
        25,
        50,
      ),
    ).toContain("outcome=0");
    const qs = buildAuditQueryString(
      {
        type: "rest",
        outcome: "4",
        dateFrom: "2026-06-01",
      },
      50,
      0,
    );
    expect(qs).toContain("type=rest");
    expect(qs).toContain("outcome=4");
    expect(qs).toContain("date-from=2026-06-01");
    expect(qs).not.toContain("date-to");
  });
});
