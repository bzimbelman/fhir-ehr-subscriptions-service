import type { SystemSnapshot } from "@/lib/settingsTypes";
import type { MatchboxHealth } from "@/lib/matchboxTypes";

/**
 * Browser-side data layer for the Settings view (Epic #398, ticket #406).
 *
 * Calls only the Next.js API route at `/api/admin/[...path]` -- the bearer
 * token never reaches the browser. Each function returns a `{data, error}`
 * envelope rather than throwing so the view can render section-scoped
 * error messages without blanking the whole page.
 */

export interface ApiResult<T> {
  data: T | null;
  error: string | null;
}

interface FetchOptions {
  signal?: AbortSignal;
}

async function safeFetch<T>(
  url: string,
  init: RequestInit & { signal?: AbortSignal } = {},
): Promise<ApiResult<T>> {
  try {
    const res = await fetch(url, {
      method: "GET",
      ...init,
      headers: {
        Accept: "application/json",
        ...(init.headers ?? {}),
      },
      cache: "no-store",
    });
    if (!res.ok) {
      return { data: null, error: `${res.status} ${res.statusText}` };
    }
    if (res.status === 204) {
      return { data: null, error: null };
    }
    const body = (await res.json()) as T;
    return { data: body, error: null };
  } catch (e) {
    if ((e as Error).name === "AbortError") {
      return { data: null, error: "aborted" };
    }
    return { data: null, error: (e as Error).message };
  }
}

/**
 * GET /admin/observe/system -- the canonical system snapshot. Feature
 * toggles, downstream URLs, and the schema_version come from here.
 */
export async function fetchSystemSnapshot(
  opts: FetchOptions = {},
): Promise<ApiResult<SystemSnapshot>> {
  return safeFetch<SystemSnapshot>("/api/admin/observe/system", opts);
}

/**
 * GET /admin/matchbox/health -- separate probe for the "is Matchbox
 * reachable" pill on the downstream-components table. We do not have an
 * equivalent probe for HAPI or for the auth issuer in v1; those rows
 * show URL only (see `SettingsView` comments).
 */
export async function fetchMatchboxHealthForSettings(
  opts: FetchOptions = {},
): Promise<ApiResult<MatchboxHealth>> {
  return safeFetch<MatchboxHealth>("/api/admin/matchbox/health", opts);
}
