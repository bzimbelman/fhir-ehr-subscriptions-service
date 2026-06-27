import { describe, it, expect } from "vitest";
import {
  aggregateInterfaces,
  interfaceSlug,
  interfaceStatus,
  parseInterfaceSlug,
  throughputToSparkline,
} from "@/lib/interfaces";
import type {
  MessageStatus,
  MessageSummary,
  ObserveThroughput,
} from "@/lib/dashboardTypes";

const NOW = new Date("2026-06-26T12:00:00Z");

function makeMsg(
  overrides: Partial<MessageSummary> & {
    id: number;
    source_system: string;
    source_protocol: string;
  },
): MessageSummary {
  return {
    received_at: NOW.toISOString(),
    source_id: `src-${overrides.id}`,
    message_type: "ADT_A01",
    status: "DELIVERED",
    attempt_count: 1,
    ...overrides,
  };
}

describe("aggregateInterfaces", () => {
  it("groups 30 messages from 3 interfaces into 3 rows with correct totals", () => {
    const messages: MessageSummary[] = [];
    let id = 1;
    // 10 EPIC HL7V2_MLLP, all today, all DELIVERED
    for (let i = 0; i < 10; i++) {
      messages.push(
        makeMsg({
          id: id++,
          source_system: "EPIC",
          source_protocol: "HL7V2_MLLP",
          received_at: "2026-06-26T08:00:00Z",
        }),
      );
    }
    // 12 CERNER FHIR_REST -- 11 today DELIVERED, 1 today FAILED
    for (let i = 0; i < 11; i++) {
      messages.push(
        makeMsg({
          id: id++,
          source_system: "CERNER",
          source_protocol: "FHIR_REST",
          received_at: "2026-06-26T07:00:00Z",
        }),
      );
    }
    messages.push(
      makeMsg({
        id: id++,
        source_system: "CERNER",
        source_protocol: "FHIR_REST",
        received_at: "2026-06-26T07:30:00Z",
        status: "FAILED",
      }),
    );
    // 8 ALLSCRIPTS HL7V2_MLLP -- 5 yesterday, 3 today; all DELIVERED
    for (let i = 0; i < 5; i++) {
      messages.push(
        makeMsg({
          id: id++,
          source_system: "ALLSCRIPTS",
          source_protocol: "HL7V2_MLLP",
          received_at: "2026-06-25T10:00:00Z",
        }),
      );
    }
    for (let i = 0; i < 3; i++) {
      messages.push(
        makeMsg({
          id: id++,
          source_system: "ALLSCRIPTS",
          source_protocol: "HL7V2_MLLP",
          received_at: "2026-06-26T01:00:00Z",
        }),
      );
    }

    const rows = aggregateInterfaces(messages, NOW);
    expect(rows).toHaveLength(3);
    const epic = rows.find((r) => r.source_system === "EPIC")!;
    expect(epic.totalCount).toBe(10);
    expect(epic.todayCount).toBe(10);
    expect(epic.todayDelivered).toBe(10);
    expect(epic.successRateToday).toBe(1);

    const cerner = rows.find((r) => r.source_system === "CERNER")!;
    expect(cerner.totalCount).toBe(12);
    expect(cerner.todayCount).toBe(12);
    expect(cerner.successRateToday).toBeCloseTo(11 / 12, 6);

    const allscripts = rows.find((r) => r.source_system === "ALLSCRIPTS")!;
    expect(allscripts.totalCount).toBe(8);
    // Only 3 today, 5 yesterday
    expect(allscripts.todayCount).toBe(3);
  });

  it("builds a stable slug that round-trips", () => {
    const slug = interfaceSlug("EPIC", "HL7V2_MLLP");
    expect(slug).toBe("EPIC__HL7V2_MLLP");
    expect(parseInterfaceSlug(slug)).toEqual({
      sourceSystem: "EPIC",
      sourceProtocol: "HL7V2_MLLP",
    });
  });

  it("encodes oddball characters in slug and decodes them back", () => {
    const slug = interfaceSlug("Acme Co/X", "HL7V2_MLLP");
    expect(parseInterfaceSlug(slug)).toEqual({
      sourceSystem: "Acme Co/X",
      sourceProtocol: "HL7V2_MLLP",
    });
  });

  it("returns null for an invalid slug", () => {
    expect(parseInterfaceSlug("not-a-slug")).toBeNull();
  });
});

describe("interfaceStatus heuristic", () => {
  it("returns active when a message was received in the last 24h", () => {
    const msgs: MessageSummary[] = [
      makeMsg({
        id: 1,
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        received_at: "2026-06-25T13:00:00Z", // 23h ago
      }),
    ];
    expect(interfaceStatus(msgs, NOW)).toBe("active");
  });

  it("returns idle when last message is between 24h and 7d ago", () => {
    const msgs: MessageSummary[] = [
      makeMsg({
        id: 1,
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        received_at: "2026-06-23T12:00:00Z", // 3 days ago
      }),
    ];
    expect(interfaceStatus(msgs, NOW)).toBe("idle");
  });

  it("returns quiet when no messages in 7+ days", () => {
    const msgs: MessageSummary[] = [
      makeMsg({
        id: 1,
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        received_at: "2026-06-15T12:00:00Z", // 11 days ago
      }),
    ];
    expect(interfaceStatus(msgs, NOW)).toBe("quiet");
  });

  it("returns quiet on an empty list", () => {
    expect(interfaceStatus([], NOW)).toBe("quiet");
  });

  it("returns error when last 10 have >= 3 FAILED/DEAD_LETTER (and beats time-based 'active')", () => {
    const failed: MessageStatus[] = [
      "FAILED",
      "DEAD_LETTER",
      "FAILED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
    ];
    const msgs: MessageSummary[] = failed.map((s, i) =>
      makeMsg({
        id: i,
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        // All within the last hour -- time-based would say "active"
        received_at: new Date(NOW.getTime() - i * 60_000).toISOString(),
        status: s,
      }),
    );
    expect(interfaceStatus(msgs, NOW)).toBe("error");
  });

  it("does not flip to error with only 2 failures out of 10", () => {
    const statuses: MessageStatus[] = [
      "FAILED",
      "DEAD_LETTER",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
      "DELIVERED",
    ];
    const msgs: MessageSummary[] = statuses.map((s, i) =>
      makeMsg({
        id: i,
        source_system: "EPIC",
        source_protocol: "HL7V2_MLLP",
        received_at: new Date(NOW.getTime() - i * 60_000).toISOString(),
        status: s,
      }),
    );
    expect(interfaceStatus(msgs, NOW)).toBe("active");
  });
});

describe("throughputToSparkline", () => {
  it("sums every status in each bucket to a single total", () => {
    const t: ObserveThroughput = {
      schema_version: "1.0",
      window: "24h",
      bucket_width: "hour",
      buckets: [
        { bucket_start: "h0", counts: { DELIVERED: 5, FAILED: 1 } },
        { bucket_start: "h1", counts: { DELIVERED: 3 } },
      ],
    };
    expect(throughputToSparkline(t)).toEqual([
      { bucketStart: "h0", total: 6 },
      { bucketStart: "h1", total: 3 },
    ]);
  });
  it("returns [] for null", () => {
    expect(throughputToSparkline(null)).toEqual([]);
  });
});
