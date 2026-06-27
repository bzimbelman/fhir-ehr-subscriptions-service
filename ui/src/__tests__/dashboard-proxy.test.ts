import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock the auth module BEFORE importing the route -- the route imports
// `auth` at module-load time. Each test rewires what auth() returns.
const authMock = vi.fn();
vi.mock("@/lib/auth", () => ({
  auth: () => authMock(),
}));

// Mock next/server's NextResponse.json for the jsdom-less route. The real
// implementation requires the Next.js runtime; we only need .json() to
// produce a Response.
vi.mock("next/server", async () => {
  return {
    NextRequest: class {
      constructor(
        public url: string,
        public init: { method?: string; headers?: Record<string, string> } = {},
      ) {}
      get method() {
        return this.init.method ?? "GET";
      }
      get headers() {
        return new Headers(this.init.headers ?? {});
      }
      async text() {
        return "";
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

import { GET } from "@/app/api/admin/[...path]/route";
import { NextRequest } from "next/server";

describe("/api/admin/[...path] proxy route", () => {
  const realFetch = global.fetch;
  const realEnv = { ...process.env };

  beforeEach(() => {
    authMock.mockReset();
    process.env.ADMIN_API_BASE_URL = "http://interface-engine:8090";
    process.env.ADMIN_API_BEARER_TOKEN = "test-bearer-token-123";
  });

  afterEach(() => {
    global.fetch = realFetch;
    process.env = { ...realEnv };
  });

  it("forwards the bearer token and returns the upstream response", async () => {
    authMock.mockResolvedValueOnce({
      user: { username: "alice" },
    });
    const upstreamBody = JSON.stringify({ status: "UP", queue: { dead_letter: 0 } });
    const captured: { url?: string; headers?: Headers } = {};
    global.fetch = vi.fn(async (url: string, init: RequestInit) => {
      captured.url = url;
      captured.headers = new Headers(init.headers as HeadersInit);
      return new Response(upstreamBody, {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/observe/system",
    ) as unknown as Parameters<typeof GET>[0];
    const ctx = { params: Promise.resolve({ path: ["observe", "system"] }) };
    const res = await GET(req, ctx);

    expect(res.status).toBe(200);
    const body = await res.json();
    expect(body).toEqual({ status: "UP", queue: { dead_letter: 0 } });

    expect(captured.url).toBe(
      "http://interface-engine:8090/admin/observe/system",
    );
    expect(captured.headers?.get("authorization")).toBe(
      "Bearer test-bearer-token-123",
    );
  });

  it("returns 401 when there is no session, without calling upstream", async () => {
    authMock.mockResolvedValueOnce(null);
    const fetchSpy = vi.fn();
    global.fetch = fetchSpy as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/observe/system",
    ) as unknown as Parameters<typeof GET>[0];
    const ctx = { params: Promise.resolve({ path: ["observe", "system"] }) };
    const res = await GET(req, ctx);

    expect(res.status).toBe(401);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("forwards the query string verbatim", async () => {
    authMock.mockResolvedValueOnce({ user: { username: "alice" } });
    const captured: { url?: string } = {};
    global.fetch = vi.fn(async (url: string) => {
      captured.url = url;
      return new Response("{}", {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/observe/throughput?window=24h&extra=1",
    ) as unknown as Parameters<typeof GET>[0];
    const ctx = {
      params: Promise.resolve({ path: ["observe", "throughput"] }),
    };
    await GET(req, ctx);

    expect(captured.url).toBe(
      "http://interface-engine:8090/admin/observe/throughput?window=24h&extra=1",
    );
  });

  it("forwards source_system + status filters verbatim (ticket #401)", async () => {
    authMock.mockResolvedValueOnce({ user: { username: "alice" } });
    const captured: { url?: string } = {};
    global.fetch = vi.fn(async (url: string) => {
      captured.url = url;
      return new Response(JSON.stringify({ total: 0, items: [] }), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/messages?source_system=EPIC&status=DEAD_LETTER&limit=50",
    ) as unknown as Parameters<typeof GET>[0];
    const ctx = { params: Promise.resolve({ path: ["messages"] }) };
    await GET(req, ctx);

    expect(captured.url).toBe(
      "http://interface-engine:8090/admin/messages?source_system=EPIC&status=DEAD_LETTER&limit=50",
    );
  });

  it("returns 500 when ADMIN_API_BASE_URL is missing", async () => {
    authMock.mockResolvedValueOnce({ user: { username: "alice" } });
    delete process.env.ADMIN_API_BASE_URL;
    global.fetch = vi.fn() as unknown as typeof fetch;

    const req = new NextRequest(
      "http://ui.local/api/admin/observe/system",
    ) as unknown as Parameters<typeof GET>[0];
    const ctx = { params: Promise.resolve({ path: ["observe", "system"] }) };
    const res = await GET(req, ctx);

    expect(res.status).toBe(500);
    const body = await res.json();
    expect(body.error).toBe("misconfigured");
  });
});
