import { NextRequest, NextResponse } from "next/server";
import { auth } from "@/lib/auth";

/**
 * Server-side proxy to the interface-engine admin API (Epic #398, ticket #400).
 *
 * Why this route exists: the admin API is gated by a single shared bearer
 * token (IPF_ADMIN_AUTH_TOKEN -- see docs/admin-api.md), which must NEVER
 * reach the browser. The pattern is:
 *
 *   browser  -- GET /api/admin/observe/system -->  this route
 *   this route reads the user's NextAuth session (must be authenticated)
 *   this route fetches ${ADMIN_API_BASE_URL}/admin/observe/system with the
 *     server-side ADMIN_API_BEARER_TOKEN attached
 *   upstream response streams back, preserving status + content-type
 *
 * The catch-all path segment after `/api/admin/` becomes the upstream
 * path. So `/api/admin/observe/system` -> `${ADMIN_API_BASE_URL}/admin/observe/system`.
 *
 * Auth model in this layer: require ANY authenticated NextAuth session.
 * #400 deliberately does NOT introduce a scope/role check beyond
 * "logged in" -- finer-grained authorization is operational scope for the
 * IdP-side role mapping and lands in a later story.
 */

const ADMIN_PREFIX = "/admin";

interface RouteContext {
  params: Promise<{ path: string[] }>;
}

async function proxy(req: NextRequest, ctx: RouteContext): Promise<Response> {
  const session = await auth();
  if (!session) {
    // 401 -- the browser-side data hook will treat this as "session expired"
    // and trigger a sign-in redirect.
    return NextResponse.json(
      { error: "unauthorized", message: "no session" },
      { status: 401 },
    );
  }

  const baseUrl = process.env.ADMIN_API_BASE_URL;
  const bearer = process.env.ADMIN_API_BEARER_TOKEN;
  if (!baseUrl) {
    return NextResponse.json(
      {
        error: "misconfigured",
        message: "ADMIN_API_BASE_URL is not set on the UI server",
      },
      { status: 500 },
    );
  }

  // Join the catch-all path segments back together. NextRequest URL preserves
  // the query string, which we forward verbatim.
  const { path } = await ctx.params;
  const subPath = (path ?? []).join("/");
  const upstreamUrl = new URL(`${ADMIN_PREFIX}/${subPath}`, baseUrl);
  const incoming = new URL(req.url);
  upstreamUrl.search = incoming.search;

  const headers: Record<string, string> = {
    Accept: req.headers.get("accept") ?? "application/json",
  };
  if (bearer) {
    headers.Authorization = `Bearer ${bearer}`;
  }
  // Preserve Content-Type for body-bearing requests so the backend can parse.
  const contentType = req.headers.get("content-type");
  if (contentType) {
    headers["Content-Type"] = contentType;
  }

  const upstream = await fetch(upstreamUrl.toString(), {
    method: req.method,
    headers,
    body:
      req.method === "GET" || req.method === "HEAD"
        ? undefined
        : await req.text(),
    cache: "no-store",
  });

  // Audit breadcrumb for state-changing actions (ticket #403). Proper
  // AuditEvent emission against HAPI lands in #407; here we just make sure
  // the breadcrumb exists in the UI server logs so operators can correlate
  // "who did what" against the inbound HTTP record. We log AFTER the
  // upstream call so the upstream status is part of the record.
  if (
    req.method === "POST" ||
    req.method === "DELETE" ||
    req.method === "PUT"
  ) {
    const subject = subPath || "/";
    const auditLine = {
      event: "ui_admin_action",
      method: req.method,
      path: subject,
      upstream_status: upstream.status,
      user:
        session.user?.username ??
        session.user?.email ??
        session.user?.name ??
        "unknown",
      ts: new Date().toISOString(),
    };
    console.log(JSON.stringify(auditLine));
  }

  // Stream the upstream body back, preserving status + the most relevant
  // headers. We deliberately do NOT forward Set-Cookie / WWW-Authenticate
  // (the admin API doesn't issue cookies; if it ever returns
  // WWW-Authenticate: Bearer for a bad token, that's an internal misconfig,
  // not something the browser should see).
  const responseHeaders = new Headers();
  const upstreamContentType = upstream.headers.get("content-type");
  if (upstreamContentType) {
    responseHeaders.set("content-type", upstreamContentType);
  }
  // No client-side caching of admin data -- it's always real-time.
  responseHeaders.set("cache-control", "no-store");

  const body = await upstream.arrayBuffer();
  return new Response(body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: responseHeaders,
  });
}

export const GET = proxy;
export const POST = proxy;
export const PUT = proxy;
export const DELETE = proxy;
