import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Same mock pattern as dashboard-proxy.test.ts.
const authMock = vi.fn();
vi.mock("@/lib/auth", () => ({
  auth: () => authMock(),
}));

vi.mock("next/server", async () => {
  return {
    NextRequest: class {
      constructor(
        public url: string,
        public init: {
          method?: string;
          headers?: Record<string, string>;
          body?: string;
        } = {},
      ) {}
      get method() {
        return this.init.method ?? "GET";
      }
      get headers() {
        return new Headers(this.init.headers ?? {});
      }
      async text() {
        return this.init.body ?? "";
      }
    },
    NextResponse: {
      json(body: unknown, init?: { status?: number }) {
        return new Response(JSON.stringify(body), {
          status: init?.status ?? 200,
          headers: { "content-type": "application/json" },
        });
      },
    },
  };
});

import { POST, DELETE } from "@/app/api/admin/[...path]/route";
import { NextRequest } from "next/server";

describe("/api/admin/[...path] audit breadcrumbs for state-changing actions (ticket #403)", () => {
  const realFetch = global.fetch;
  const realEnv = { ...process.env };
  let logSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    authMock.mockReset();
    process.env.ADMIN_API_BASE_URL = "http://interface-engine:8090";
    process.env.ADMIN_API_BEARER_TOKEN = "test-bearer";
    logSpy = vi.spyOn(console, "log").mockImplementation(() => {});
  });

  afterEach(() => {
    global.fetch = realFetch;
    process.env = { ...realEnv };
    logSpy.mockRestore();
  });

  it("emits an audit breadcrumb on POST .../retry with the user + path + upstream status", async () => {
    authMock.mockResolvedValueOnce({ user: { username: "alice" } });
    global.fetch = vi.fn(async () => {
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/messages/42/retry",
      { method: "POST" },
    ) as unknown as Parameters<typeof POST>[0];
    const ctx = {
      params: Promise.resolve({ path: ["messages", "42", "retry"] }),
    };
    await POST(req, ctx);

    expect(logSpy).toHaveBeenCalled();
    const allLines = logSpy.mock.calls.map((c) => c[0] as string);
    const auditLine = allLines.find((l) => {
      try {
        const parsed = JSON.parse(l);
        return parsed.event === "ui_admin_action";
      } catch {
        return false;
      }
    });
    expect(auditLine).toBeDefined();
    const parsed = JSON.parse(auditLine!);
    expect(parsed.method).toBe("POST");
    expect(parsed.path).toBe("messages/42/retry");
    expect(parsed.user).toBe("alice");
    expect(parsed.upstream_status).toBe(200);
  });

  it("emits an audit breadcrumb on DELETE .../messages/{id} including 409 outcomes", async () => {
    authMock.mockResolvedValueOnce({ user: { username: "bob" } });
    global.fetch = vi.fn(async () => {
      return new Response("{}", {
        status: 409,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const req = new NextRequest("http://ui.local/api/admin/messages/77", {
      method: "DELETE",
    }) as unknown as Parameters<typeof DELETE>[0];
    const ctx = { params: Promise.resolve({ path: ["messages", "77"] }) };
    await DELETE(req, ctx);

    const allLines = logSpy.mock.calls.map((c) => c[0] as string);
    const auditLine = allLines.find((l) => {
      try {
        const parsed = JSON.parse(l);
        return parsed.event === "ui_admin_action";
      } catch {
        return false;
      }
    });
    expect(auditLine).toBeDefined();
    const parsed = JSON.parse(auditLine!);
    expect(parsed.method).toBe("DELETE");
    expect(parsed.path).toBe("messages/77");
    expect(parsed.user).toBe("bob");
    expect(parsed.upstream_status).toBe(409);
  });
});
