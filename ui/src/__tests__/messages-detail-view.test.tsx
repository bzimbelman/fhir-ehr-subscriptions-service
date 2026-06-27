import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";

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

import { MessageDetailView } from "@/components/MessageDetailView";
import type { MessageStatus } from "@/lib/dashboardTypes";
import type {
  MessageDetailRow,
  MessageEffectsResponse,
} from "@/lib/messagesTypes";

const NOW = new Date("2026-06-26T12:00:00Z");

const HL7_SAMPLE =
  "MSH|^~\\&|EPIC|EPIC_FAC||DEST|20260626|||ADT^A01|MSG-1|P|2.5\r" +
  "EVN|A01|20260626\r" +
  "PID|||MRN-1||DOE^JOHN\r";

const FHIR_SAMPLE_ONELINE =
  '{"resourceType":"Patient","id":"abc","name":[{"family":"Smith"}]}';

function mkDetail(over: Partial<MessageDetailRow> = {}): MessageDetailRow {
  return {
    id: 99,
    received_at: "2026-06-26T11:55:00Z",
    source_protocol: "HL7V2_MLLP",
    source_system: "EPIC",
    source_id: "MSG-99",
    message_type: "ADT_A01",
    status: "DELIVERED" as MessageStatus,
    attempt_count: 1,
    last_error: null,
    correlation_id: "corr-99",
    raw_message: HL7_SAMPLE,
    raw_content_type: "application/hl7-v2",
    last_attempt_at: null,
    next_attempt_at: null,
    delivered_at: "2026-06-26T11:55:01Z",
    ...over,
  };
}

function mkEffects(
  over: Partial<MessageEffectsResponse> = {},
): MessageEffectsResponse {
  return {
    effects_status: "delivered",
    message: {
      id: 99,
      correlation_id: "corr-99",
      received_at: "2026-06-26T11:55:00Z",
      status: "DELIVERED",
      source_system: "EPIC",
      source_id: "MSG-99",
      message_type: "ADT_A01",
      last_error: null,
    },
    transform: {
      delivered_at: "2026-06-26T11:55:01Z",
      attempt_count: 1,
      last_attempt_at: null,
    },
    fhir_resources_created: [],
    subscriptions_matched: [],
    notifications_fired: [],
    ...over,
  };
}

beforeEach(() => {
  vi.spyOn(global.Date, "now").mockReturnValue(NOW.getTime());
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("MessageDetailView (ticket #402)", () => {
  // Test #5 — fetches /admin/messages/{id} and renders raw_message.
  it("fetches the detail endpoint with the id and renders raw_message", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });

    await waitFor(() => {
      expect(fetchDetailFn).toHaveBeenCalledWith("99");
    });
    const body = await screen.findByTestId("message-raw-body");
    expect(body.textContent).toContain("MSH|^~");
  });

  // Test #6 — HL7v2 renders segment-per-line.
  it("HL7v2 raw renders with one segment per line", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    const body = await screen.findByTestId("message-raw-body");
    // The HL7 sample carries three CR-delimited segments. After rendering
    // those should appear as three newline-delimited lines in the DOM.
    const text = body.textContent ?? "";
    expect(text.split("\n")).toHaveLength(3);
    expect(text).toContain("MSH|");
    expect(text).toContain("EVN|");
    expect(text).toContain("PID|");
    expect(screen.getByTestId("message-raw-label").textContent).toBe(
      "HL7 v2.x",
    );
  });

  // Test #7 — FHIR JSON pretty-printed.
  it("FHIR_REST raw is pretty-printed JSON with newlines + includes resourceType", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({
        source_protocol: "FHIR_REST",
        raw_message: FHIR_SAMPLE_ONELINE,
      }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });

    const body = await screen.findByTestId("message-raw-body");
    const text = body.textContent ?? "";
    // The original is single-line; pretty-print should introduce newlines.
    expect(text).toContain("\n");
    expect(text).toContain("Patient");
    expect(screen.getByTestId("message-raw-label").textContent).toBe(
      "FHIR JSON",
    );
  });

  // Test #8 — effects endpoint + resources + notifications render.
  it("fetches /admin/messages/{id}/effects and renders resources + subscription fires", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () =>
      mkEffects({
        fhir_resources_created: [
          { resource_type: "Patient", id: "Patient/abc" },
          { resource_type: "Encounter", id: "Encounter/def" },
        ],
        notifications_fired: [
          {
            subscription_id: "Subscription/77",
            channel_type: "rest-hook",
            endpoint: "https://example.com/notify",
            attempted_at: "2026-06-26T11:55:02Z",
            outcome: "success",
            http_status: 200,
            duration_ms: 42,
            error: null,
          },
        ],
      }),
    );

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });

    await waitFor(() => {
      expect(fetchEffectsFn).toHaveBeenCalledWith("99");
    });
    const panel = await screen.findByTestId("message-effects-panel");
    expect(
      within(panel).getByTestId("message-effects-resource-0").textContent,
    ).toContain("Patient/abc");
    expect(
      within(panel).getByTestId("message-effects-resource-1").textContent,
    ).toContain("Encounter/def");
    expect(
      within(panel).getByTestId("message-effects-notification-0").textContent,
    ).toContain("Subscription/77");
  });

  // Test #9 — empty effects shows explicit empty-state.
  it("renders an explicit empty-state when effects are empty", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    const empty = await screen.findByTestId("message-effects-empty");
    expect(empty.textContent).toMatch(/No downstream effects/i);
  });

  // Test #10 — Retry button visible only for FAILED|DEAD_LETTER.
  it("Retry button is visible for FAILED status", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "FAILED" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-retry")).toBeInTheDocument();
    });
  });

  it("Retry button is hidden for DELIVERED status", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail()); // DELIVERED
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-detail-header")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("message-action-retry")).toBeNull();
  });

  // Test #11 — Delete button visible only for DEAD_LETTER.
  it("Delete button is visible for DEAD_LETTER status", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "DEAD_LETTER" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-delete")).toBeInTheDocument();
    });
  });

  it("Delete button is hidden for FAILED status", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "FAILED" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-retry")).toBeInTheDocument();
    });
    expect(screen.queryByTestId("message-action-delete")).toBeNull();
  });

  // Test #12 — Retry click POSTs and reloads.
  it("Retry click invokes retryFn and then reloads", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "FAILED" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());
    const retryFn = vi.fn(async () => undefined);
    const reloadFn = vi.fn(async () => undefined);

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          retryFn={retryFn}
          reloadFn={reloadFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-retry")).toBeInTheDocument();
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("message-action-retry"));
    });
    await waitFor(() => {
      expect(retryFn).toHaveBeenCalledWith("99");
    });
    await waitFor(() => {
      expect(reloadFn).toHaveBeenCalledTimes(1);
    });
  });

  // Test #13a — Delete confirm path.
  it("Delete click prompts confirm; on confirm calls deleteFn and reloads", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "DEAD_LETTER" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());
    const deleteFn = vi.fn(async () => undefined);
    const reloadFn = vi.fn(async () => undefined);
    const confirmFn = vi.fn(() => true);

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          deleteFn={deleteFn}
          reloadFn={reloadFn}
          confirmFn={confirmFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-delete")).toBeInTheDocument();
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("message-action-delete"));
    });
    await waitFor(() => {
      expect(confirmFn).toHaveBeenCalledTimes(1);
    });
    expect(deleteFn).toHaveBeenCalledWith("99");
    await waitFor(() => {
      expect(reloadFn).toHaveBeenCalledTimes(1);
    });
  });

  // Test #13b — Delete cancel path.
  it("Delete click + cancel prompt does not call deleteFn or reload", async () => {
    const fetchDetailFn = vi.fn(async () =>
      mkDetail({ status: "DEAD_LETTER" as MessageStatus }),
    );
    const fetchEffectsFn = vi.fn(async () => mkEffects());
    const deleteFn = vi.fn(async () => undefined);
    const reloadFn = vi.fn(async () => undefined);
    const confirmFn = vi.fn(() => false);

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          deleteFn={deleteFn}
          reloadFn={reloadFn}
          confirmFn={confirmFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-action-delete")).toBeInTheDocument();
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("message-action-delete"));
    });
    await waitFor(() => {
      expect(confirmFn).toHaveBeenCalledTimes(1);
    });
    expect(deleteFn).not.toHaveBeenCalled();
    expect(reloadFn).not.toHaveBeenCalled();
  });

  // Test #14 — Timeline renders Received and Delivered when both set.
  it("the timeline renders Received and Delivered steps when both timestamps are set", async () => {
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-timeline-list")).toBeInTheDocument();
    });
    expect(
      screen.getByTestId("message-timeline-step-received"),
    ).toBeInTheDocument();
    expect(
      screen.getByTestId("message-timeline-step-delivered"),
    ).toBeInTheDocument();
  });

  // Test #15 — Copy button writes raw_message to clipboard.
  it("Copy click writes raw_message to navigator.clipboard.writeText", async () => {
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      writable: true,
      configurable: true,
    });
    const fetchDetailFn = vi.fn(async () => mkDetail());
    const fetchEffectsFn = vi.fn(async () => mkEffects());

    await act(async () => {
      render(
        <MessageDetailView
          id="99"
          fetchDetailFn={fetchDetailFn}
          fetchEffectsFn={fetchEffectsFn}
          nowProvider={() => NOW}
        />,
      );
    });
    await waitFor(() => {
      expect(screen.getByTestId("message-raw-copy")).toBeInTheDocument();
    });
    await act(async () => {
      fireEvent.click(screen.getByTestId("message-raw-copy"));
    });
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledTimes(1);
    });
    expect(writeText).toHaveBeenCalledWith(HL7_SAMPLE);
  });
});
