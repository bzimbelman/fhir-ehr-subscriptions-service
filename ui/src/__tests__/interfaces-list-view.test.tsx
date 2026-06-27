import { describe, it, expect, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";

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

import { InterfacesListView } from "@/components/InterfacesListView";
import type { MessageSummary } from "@/lib/dashboardTypes";

const NOW = new Date("2026-06-26T12:00:00Z");

function msg(
  id: number,
  source_system: string,
  source_protocol: string,
  received_at: string,
  overrides: Partial<MessageSummary> = {},
): MessageSummary {
  return {
    id,
    received_at,
    source_system,
    source_protocol,
    source_id: `src-${id}`,
    message_type: "ADT_A01",
    status: "DELIVERED",
    attempt_count: 1,
    ...overrides,
  };
}

describe("InterfacesListView", () => {
  it("aggregates messages by (source_system, source_protocol) into rows with click-through links", async () => {
    const items: MessageSummary[] = [];
    let id = 1;
    for (let i = 0; i < 10; i++) {
      items.push(msg(id++, "EPIC", "HL7V2_MLLP", "2026-06-26T11:00:00Z"));
    }
    for (let i = 0; i < 12; i++) {
      items.push(msg(id++, "CERNER", "FHIR_REST", "2026-06-26T10:00:00Z"));
    }
    for (let i = 0; i < 8; i++) {
      items.push(msg(id++, "ALLSCRIPTS", "HL7V2_MLLP", "2026-06-25T10:00:00Z"));
    }

    const fetchMessages = vi.fn(async () => ({
      data: { total: items.length, limit: 500, offset: 0, items },
      error: null,
    }));

    await act(async () => {
      render(
        <InterfacesListView
          fetchMessages={fetchMessages}
          nowProvider={() => NOW}
        />,
      );
    });

    await waitFor(() => {
      expect(screen.getByTestId("interfaces-table")).toBeInTheDocument();
    });

    // 3 rows
    expect(screen.getByTestId("interface-row-EPIC__HL7V2_MLLP")).toBeInTheDocument();
    expect(
      screen.getByTestId("interface-row-CERNER__FHIR_REST"),
    ).toBeInTheDocument();
    expect(
      screen.getByTestId("interface-row-ALLSCRIPTS__HL7V2_MLLP"),
    ).toBeInTheDocument();

    // EPIC row total count is visible
    const epicRow = screen.getByTestId("interface-row-EPIC__HL7V2_MLLP");
    expect(epicRow.textContent).toContain("10");

    // Click-through link present
    const link = epicRow.querySelector("a");
    expect(link?.getAttribute("href")).toBe("/interfaces/EPIC__HL7V2_MLLP");
  });

  it("surfaces the fetch error", async () => {
    const fetchMessages = vi.fn(async () => ({
      data: null,
      error: "500 Internal Server Error",
    }));
    await act(async () => {
      render(
        <InterfacesListView
          fetchMessages={fetchMessages}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Failed to load interfaces: 500 Internal Server Error",
      );
    });
  });
});
