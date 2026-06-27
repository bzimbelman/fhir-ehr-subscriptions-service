import type {
  MatchboxHealth,
  StructureMapsEnvelope,
  TransformRequest,
  TransformResponse,
} from "@/lib/matchboxTypes";

/**
 * Browser-side data layer for `/admin/matchbox/*` endpoints
 * (Epic #398, ticket #405). Calls only the Next.js API route at
 * `/api/admin/[...path]` -- the bearer token never reaches the browser
 * (same model as #400/#404).
 *
 * Each function returns a `{data, error}` envelope rather than throwing,
 * so callers can render section-scoped error messages without blanking
 * the whole page on a transient upstream failure.
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

export async function fetchMatchboxHealth(
  opts: FetchOptions = {},
): Promise<ApiResult<MatchboxHealth>> {
  return safeFetch<MatchboxHealth>("/api/admin/matchbox/health", opts);
}

export async function fetchStructureMaps(
  opts: FetchOptions = {},
): Promise<ApiResult<StructureMapsEnvelope>> {
  return safeFetch<StructureMapsEnvelope>(
    "/api/admin/matchbox/structuremaps",
    opts,
  );
}

export async function runMatchboxTransform(
  body: TransformRequest,
  opts: FetchOptions = {},
): Promise<ApiResult<TransformResponse>> {
  return safeFetch<TransformResponse>("/api/admin/matchbox/transform", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal: opts.signal,
  });
}

/**
 * A known-good, obvious-synthetic ADT^A04 sample. Used by the "Try
 * sample" button in the transform panel so operators have a working
 * payload one click away. Names + numbers are obvious fakes (Test^Patient,
 * birthdate 19700101); the message is not from any real EHR or feed.
 *
 * Segment delimiter: HL7 v2's wire format uses `\r` (CR-only). The
 * textarea normalises newlines for human-friendliness so we use `\n` in
 * the source string -- the backend's Matchbox client tolerates either
 * (HAPI's v2 parser normalises both forms before parsing).
 */
export const SAMPLE_ADT_A04: string = [
  "MSH|^~\\&|EPICSIM|HOSP|RECV|CDS|20260626120000||ADT^A04|405-SAMPLE-1|P|2.5",
  "EVN|A04|20260626120000",
  "PID|1||MRN-405-SAMPLE^^^HOSP^MR||Test^Patient^^^^^L||19700101|M|||123 Fake St^^Anytown^CA^90000||(555)555-0100",
  "PV1|1|O|OUTPT^101^1^HOSP||||DR^Doc^Test^^^DR",
].join("\n");
