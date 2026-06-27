import { auth } from "@/lib/auth";

/**
 * Server-side admin API proxy helper.
 *
 * The UI MUST NOT expose the admin bearer token to the browser. Every admin
 * API call from the UI goes through a Next.js API route that:
 *   1. Reads the user's session (must be authenticated).
 *   2. Optionally enforces a role/scope check from the session.
 *   3. Forwards the request to the backend with the server-side bearer token.
 *   4. Streams the response back.
 *
 * This module is the building block for step 3. Subsequent UI tickets
 * (#400-#408) wire concrete admin endpoints on top of it.
 *
 * IMPORTANT: This is server-side ONLY. Importing it from a "use client"
 * component will leak the bearer token via the client bundle.
 */

export interface AdminFetchOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  body?: unknown;
  headers?: Record<string, string>;
}

export class AdminApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly statusText: string,
    public readonly body: string,
  ) {
    super(`admin API ${status} ${statusText}: ${body}`);
    this.name = "AdminApiError";
  }
}

/**
 * Call an admin API endpoint with the server-side bearer token, returning the
 * parsed JSON body on success. Throws AdminApiError on any non-2xx response.
 *
 * Caller is responsible for ensuring the user is authenticated -- typically by
 * calling `auth()` at the top of the API route handler and 401ing if it
 * returns null.
 */
export async function serverSideAdminFetch<T = unknown>(
  path: string,
  options: AdminFetchOptions = {},
): Promise<T> {
  const session = await auth();
  if (!session) {
    throw new AdminApiError(401, "Unauthorized", "no session");
  }

  const baseUrl = process.env.ADMIN_API_BASE_URL;
  const bearer = process.env.ADMIN_API_BEARER_TOKEN;
  if (!baseUrl) {
    throw new AdminApiError(
      500,
      "Misconfigured",
      "ADMIN_API_BASE_URL is not set",
    );
  }

  const url = new URL(path, baseUrl).toString();
  const headers: Record<string, string> = {
    Accept: "application/json",
    ...(options.body ? { "Content-Type": "application/json" } : {}),
    ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}),
    ...options.headers,
  };

  const res = await fetch(url, {
    method: options.method ?? "GET",
    headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
    // Admin API responses are small JSON; no need to stream.
    cache: "no-store",
  });

  const text = await res.text();
  if (!res.ok) {
    throw new AdminApiError(res.status, res.statusText, text);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}
