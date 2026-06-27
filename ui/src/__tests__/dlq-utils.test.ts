import { describe, it, expect } from "vitest";
import {
  ageBand,
  fingerprintError,
  groupByFingerprint,
  matchesFilters,
  truncateError,
} from "@/lib/dlqUtils";
import type { MessageSummary } from "@/lib/dashboardTypes";

const now = new Date("2026-06-26T12:00:00Z");

function mkRow(over: Partial<MessageSummary> = {}): MessageSummary {
  return {
    id: 1,
    received_at: "2026-06-26T11:59:00Z",
    source_protocol: "HL7V2_MLLP",
    source_system: "EPIC",
    source_id: "MSG-1",
    message_type: "ADT_A01",
    status: "DEAD_LETTER",
    attempt_count: 5,
    last_error: null,
    ...over,
  };
}

describe("dlqUtils", () => {
  describe("ageBand thresholds (test #2)", () => {
    it("green when received_at is less than 1 hour ago", () => {
      // 30 minutes ago
      expect(ageBand("2026-06-26T11:30:00Z", now)).toBe("green");
    });

    it("yellow when received_at is 1 to 24 hours ago", () => {
      // 5 hours ago
      expect(ageBand("2026-06-26T07:00:00Z", now)).toBe("yellow");
    });

    it("red when received_at is more than 24 hours ago", () => {
      // 2 days ago
      expect(ageBand("2026-06-24T12:00:00Z", now)).toBe("red");
    });

    it("red on the threshold (exactly 24h) and on null/invalid", () => {
      // exactly 24h boundary is treated as "not less than 24h" -> red
      expect(ageBand("2026-06-25T12:00:00Z", now)).toBe("red");
      expect(ageBand(null, now)).toBe("red");
      expect(ageBand("not a date", now)).toBe("red");
    });
  });

  describe("fingerprintError", () => {
    it("normalises volatile tokens out of the error", () => {
      const a = fingerprintError(
        "Connection refused to https://hapi.example.com/fhir/Patient/123",
      );
      const b = fingerprintError(
        "Connection refused to https://hapi.example.com/fhir/Patient/456",
      );
      expect(a).toBe(b);
      expect(a).toContain("<url>");
    });

    it("strips UUIDs and trims to 60 chars", () => {
      const fp = fingerprintError(
        "Failed to deliver to subscription a1b2c3d4-e5f6-7890-abcd-1234567890ab after timeout",
      );
      expect(fp).toContain("<uuid>");
      expect(fp.length).toBeLessThanOrEqual(60);
    });

    it("handles null and empty input", () => {
      expect(fingerprintError(null)).toBe("(unknown error)");
      expect(fingerprintError("")).toBe("(unknown error)");
    });
  });

  describe("groupByFingerprint (test #6)", () => {
    it("groups 10 rows with 3 distinct error patterns into top-5 = 3 entries with correct counts", () => {
      const rows: MessageSummary[] = [
        mkRow({ id: 1, last_error: "Connection refused: 127.0.0.1:8080" }),
        mkRow({ id: 2, last_error: "Connection refused: 127.0.0.1:8081" }),
        mkRow({ id: 3, last_error: "Connection refused: 127.0.0.1:8082" }),
        mkRow({ id: 4, last_error: "Connection refused: 127.0.0.1:8083" }),
        mkRow({ id: 5, last_error: "Schema violation in Patient resource" }),
        mkRow({ id: 6, last_error: "Schema violation in Patient resource" }),
        mkRow({ id: 7, last_error: "Schema violation in Patient resource" }),
        mkRow({ id: 8, last_error: "HTTP 503 Service Unavailable" }),
        mkRow({ id: 9, last_error: "HTTP 503 Service Unavailable" }),
        mkRow({ id: 10, last_error: "HTTP 503 Service Unavailable" }),
      ];

      const groups = groupByFingerprint(rows, 5);
      expect(groups).toHaveLength(3);

      // Counts: connection refused = 4, schema violation = 3, http 503 = 3
      // Sort: descending by count, lexical on tie
      const [g0, g1, g2] = groups;
      expect(g0!.count).toBe(4);
      expect(g1!.count).toBe(3);
      expect(g2!.count).toBe(3);

      const fps = groups.map((g) => g.fingerprint);
      expect(fps[0]!).toContain("connection refused");
      // Remaining two are deterministic ordering by lex
      expect(fps.slice(1).sort()).toEqual(
        ["http <n> service unavailable", "schema violation in patient resource"].sort(),
      );
    });

    it("returns at most topN groups", () => {
      const distinctErrors = [
        "alpha bravo charlie",
        "delta echo foxtrot",
        "golf hotel india",
        "juliet kilo lima",
        "mike november oscar",
        "papa quebec romeo",
        "sierra tango uniform",
        "victor whiskey xray",
      ];
      const rows: MessageSummary[] = distinctErrors.map((err, i) =>
        mkRow({ id: i, last_error: err }),
      );
      const groups = groupByFingerprint(rows, 5);
      expect(groups).toHaveLength(5);
    });
  });

  describe("matchesFilters (test #5 source_protocol)", () => {
    const rows: MessageSummary[] = [
      mkRow({ id: 1, source_protocol: "HL7V2_MLLP" }),
      mkRow({ id: 2, source_protocol: "FHIR_REST" }),
      mkRow({ id: 3, source_protocol: "HL7V2_MLLP" }),
      mkRow({ id: 4, source_protocol: "EHR_NATIVE_API" }),
    ];

    it("source_protocol=all keeps everything", () => {
      const kept = rows.filter((r) =>
        matchesFilters(
          r,
          {
            sourceSystem: "",
            sourceProtocol: "all",
            messageType: "",
            timeRange: "all",
            lastErrorPattern: "",
          },
          now,
        ),
      );
      expect(kept).toHaveLength(4);
    });

    it("source_protocol=FHIR_REST narrows to one row", () => {
      const kept = rows.filter((r) =>
        matchesFilters(
          r,
          {
            sourceSystem: "",
            sourceProtocol: "FHIR_REST",
            messageType: "",
            timeRange: "all",
            lastErrorPattern: "",
          },
          now,
        ),
      );
      expect(kept.map((r) => r.id)).toEqual([2]);
    });

    it("source_protocol=HL7V2_MLLP narrows to two rows", () => {
      const kept = rows.filter((r) =>
        matchesFilters(
          r,
          {
            sourceSystem: "",
            sourceProtocol: "HL7V2_MLLP",
            messageType: "",
            timeRange: "all",
            lastErrorPattern: "",
          },
          now,
        ),
      );
      expect(kept.map((r) => r.id)).toEqual([1, 3]);
    });
  });

  describe("truncateError", () => {
    it("collapses whitespace and clips to the limit", () => {
      const out = truncateError("a\n  b\nc".repeat(100), 20);
      expect(out.length).toBeLessThanOrEqual(20);
    });
    it("returns empty for null/undefined", () => {
      expect(truncateError(null)).toBe("");
      expect(truncateError(undefined)).toBe("");
    });
  });
});
