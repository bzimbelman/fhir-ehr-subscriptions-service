import type { MessageStatus, MessageSummary } from "@/lib/dashboardTypes";

/**
 * Types specific to the DLQ viewer (Epic #398, ticket #403).
 *
 * The DLQ list re-uses the shared `MessageSummary` shape from the dashboard
 * (the backend serves the same JSON contract for `/admin/messages` regardless
 * of the `status` filter). We add a thin layer of UI-specific types for
 * filters, fingerprints, and bulk-action outcomes.
 */

/**
 * Source protocols the backend can report. Mirrors the Kotlin enum;
 * `OTHER` is a catch-all if the backend grows a new protocol before the UI.
 */
export const SOURCE_PROTOCOLS = [
  "HL7V2_MLLP",
  "FHIR_REST",
  "EHR_NATIVE_API",
  "OTHER",
] as const;
export type SourceProtocol = (typeof SOURCE_PROTOCOLS)[number];

/** UI-side time-range labels. Maps to an `Age` cutoff applied client-side. */
export const TIME_RANGES = ["1h", "24h", "7d", "all"] as const;
export type TimeRange = (typeof TIME_RANGES)[number];

export interface DlqFilters {
  sourceSystem: string;
  sourceProtocol: SourceProtocol | "all";
  messageType: string;
  timeRange: TimeRange;
  lastErrorPattern: string;
}

export const DEFAULT_FILTERS: DlqFilters = {
  sourceSystem: "",
  sourceProtocol: "all",
  messageType: "",
  timeRange: "all",
  lastErrorPattern: "",
};

export interface ErrorFingerprintGroup {
  fingerprint: string;
  /** Human-friendly sample line (truncated, lowercased -- same as fingerprint for v1). */
  sample: string;
  count: number;
}

/** Outcome of a single bulk-replay or bulk-delete attempt. */
export interface BulkActionOutcome {
  id: number;
  ok: boolean;
  /** Status code from the upstream when known (proxy may translate). */
  status?: number;
  /** Short error message when ok=false. */
  error?: string;
}

/**
 * Full detail row from `GET /admin/messages/{id}` -- includes raw_message
 * and a few timestamps the list endpoint doesn't carry.
 */
export interface MessageDetail extends MessageSummary {
  raw_message: string;
  raw_content_type: string;
  last_attempt_at?: string | null;
  next_attempt_at?: string | null;
  delivered_at?: string | null;
  status: MessageStatus;
}
