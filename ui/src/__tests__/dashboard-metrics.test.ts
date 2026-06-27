import { describe, it, expect } from "vitest";
import {
  activeSubscriptionsCount,
  componentHealthRows,
  dlqSize,
  formatRate,
  relativeTime,
  rollupSystemStatus,
  successRate,
  totalMessagesIn,
} from "@/lib/dashboardMetrics";
import type {
  ActuatorHealthResponse,
  ObserveSystem,
  ObserveThroughput,
  SubscriptionsHealthResponse,
} from "@/lib/dashboardTypes";

const sys = (
  components: ObserveSystem["components"] = undefined,
  deadLetter = 0,
): ObserveSystem => ({
  schema_version: "1.0",
  service: "test",
  uptime_seconds: 100,
  feature_toggles: {},
  downstream: {},
  queue: {
    received: 0,
    transforming: 0,
    failed: 0,
    delivered: 0,
    dead_letter: deadLetter,
  },
  components,
});

const through = (
  buckets: ObserveThroughput["buckets"],
): ObserveThroughput => ({
  schema_version: "1.0",
  window: "24h",
  bucket_width: "hour",
  buckets,
});

describe("dashboardMetrics", () => {
  it("totalMessagesIn sums every count across every bucket", () => {
    const t = through([
      { bucket_start: "t0", counts: { DELIVERED: 5, FAILED: 1 } },
      { bucket_start: "t1", counts: { DELIVERED: 3, RECEIVED: 2 } },
    ]);
    expect(totalMessagesIn(t)).toBe(11);
    expect(totalMessagesIn(null)).toBe(0);
  });

  it("successRate is delivered / total, null on empty", () => {
    const t = through([
      { bucket_start: "t0", counts: { DELIVERED: 9, FAILED: 1 } },
    ]);
    expect(successRate(t)).toBe(0.9);
    expect(successRate(through([]))).toBeNull();
    expect(formatRate(0.9)).toBe("90.0%");
    expect(formatRate(null)).toBe("--");
  });

  it("dlqSize comes from observe/system.queue.dead_letter", () => {
    expect(dlqSize(sys(undefined, 7))).toBe(7);
    expect(dlqSize(null)).toBe(0);
  });

  it("activeSubscriptionsCount counts items where active=true", () => {
    const subs: SubscriptionsHealthResponse = {
      total: 3,
      items: [
        {
          id: "Subscription/1",
          active: true,
          channel_type: "rest-hook",
          endpoint: "x",
          delivery_success_count: 0,
          delivery_failure_count: 0,
        },
        {
          id: "Subscription/2",
          active: false,
          channel_type: "rest-hook",
          endpoint: "y",
          delivery_success_count: 0,
          delivery_failure_count: 0,
        },
        {
          id: "Subscription/3",
          active: true,
          channel_type: "rest-hook",
          endpoint: "z",
          delivery_success_count: 0,
          delivery_failure_count: 0,
        },
      ],
    };
    expect(activeSubscriptionsCount(subs)).toBe(2);
    expect(activeSubscriptionsCount(null)).toBe(0);
  });

  describe("rollupSystemStatus", () => {
    it("returns GREEN when all components UP", () => {
      const system = sys([
        { name: "interface-engine", status: "UP" },
        { name: "matchbox", status: "UP" },
      ]);
      expect(rollupSystemStatus(system, null)).toBe("UP");
    });

    it("returns YELLOW when any component DEGRADED", () => {
      const system = sys([
        { name: "interface-engine", status: "UP" },
        { name: "hapi", status: "DEGRADED" },
      ]);
      expect(rollupSystemStatus(system, null)).toBe("DEGRADED");
    });

    it("returns RED when any component DOWN", () => {
      const system = sys([
        { name: "interface-engine", status: "UP" },
        { name: "postgres", status: "DOWN" },
        { name: "matchbox", status: "DEGRADED" },
      ]);
      expect(rollupSystemStatus(system, null)).toBe("DOWN");
    });

    it("returns UNKNOWN with no components and no actuator", () => {
      expect(rollupSystemStatus(sys(), null)).toBe("UNKNOWN");
      expect(rollupSystemStatus(null, null)).toBe("UNKNOWN");
    });

    it("falls back to actuator/health/readiness when observe.components empty", () => {
      const actuator: ActuatorHealthResponse = {
        status: "UP",
        components: {
          matchbox: { status: "UP" },
          hapi: { status: "DOWN" },
        },
      };
      expect(rollupSystemStatus(null, actuator)).toBe("DOWN");
    });
  });

  describe("componentHealthRows", () => {
    it("prefers observe.components when present", () => {
      const system = sys([
        {
          name: "interface-engine",
          status: "UP",
          last_checked: "2026-06-26T00:00:00Z",
        },
      ]);
      const rows = componentHealthRows(system, {
        status: "UP",
        components: { matchbox: { status: "UP" } },
      });
      expect(rows).toEqual([
        {
          name: "interface-engine",
          status: "UP",
          detail: undefined,
          lastChecked: "2026-06-26T00:00:00Z",
        },
      ]);
    });

    it("translates actuator OUT_OF_SERVICE -> DEGRADED", () => {
      const rows = componentHealthRows(null, {
        status: "DOWN",
        components: {
          hapi: { status: "OUT_OF_SERVICE" },
        },
      });
      expect(rows).toEqual([{ name: "hapi", status: "DEGRADED" }]);
    });
  });

  describe("relativeTime", () => {
    const now = new Date("2026-06-26T12:00:00Z");
    it("just now under 5 seconds", () => {
      expect(relativeTime("2026-06-26T11:59:58Z", now)).toBe("just now");
    });
    it("seconds under a minute", () => {
      expect(relativeTime("2026-06-26T11:59:30Z", now)).toBe("30s ago");
    });
    it("minutes under an hour", () => {
      expect(relativeTime("2026-06-26T11:57:00Z", now)).toBe("3 min ago");
    });
    it("hours under a day", () => {
      expect(relativeTime("2026-06-26T09:00:00Z", now)).toBe("3 hr ago");
    });
    it("days", () => {
      expect(relativeTime("2026-06-23T12:00:00Z", now)).toBe("3 day ago");
    });
    it("null / invalid -> --", () => {
      expect(relativeTime(null, now)).toBe("--");
      expect(relativeTime("not a date", now)).toBe("--");
    });
  });
});
