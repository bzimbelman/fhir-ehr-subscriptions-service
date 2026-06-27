import type { MessageStatus, MessageSummary } from "@/lib/dashboardTypes";

/**
 * Types for the message viewer (Epic #398, ticket #402).
 *
 * The list view reuses [MessageSummary] from the dashboard contract; this
 * module adds:
 *   - filter shape for the list page
 *   - full detail row shape (includes raw_message + raw_content_type, which
 *     the summary projection deliberately omits to keep page payload small)
 *   - the `/admin/messages/{id}/effects` response shape (FHIR resources +
 *     subscription fires)
 *
 * The detail row shape is a structural superset of [MessageDetail] in
 * `dlqTypes.ts`, but we keep this independent so the two pages can evolve
 * without coupling. Re-exporting would create a circular concern when the
 * effects model grows.
 */

/** Status options shown in the list filter dropdown. */
export const STATUS_OPTIONS = [
  "ALL",
  "RECEIVED",
  "TRANSFORMING",
  "DELIVERED",
  "FAILED",
  "DEAD_LETTER",
] as const;
export type StatusOption = (typeof STATUS_OPTIONS)[number];

/**
 * Time-range presets. Server-side filtering isn't (yet) supported on
 * /admin/messages — the controller exposes status / source_system / limit /
 * offset only. We apply the range CLIENT-SIDE over whatever page was
 * fetched, and document the scaling limitation prominently in the UI.
 *
 * Follow-up: extend [AdminMessagesController] with a `received_after`
 * query param so the time filter can scale beyond one page.
 */
export const TIME_RANGES = ["today", "24h", "7d", "all"] as const;
export type TimeRange = (typeof TIME_RANGES)[number];

export interface MessagesListFilters {
  status: StatusOption;
  /** Free-text "contains" filter applied client-side over the current page. */
  sourceSystem: string;
  /** Free-text "contains" filter applied client-side over the current page. */
  messageType: string;
  timeRange: TimeRange;
}

export const DEFAULT_LIST_FILTERS: MessagesListFilters = {
  status: "ALL",
  sourceSystem: "",
  messageType: "",
  timeRange: "all",
};

/**
 * Full detail projection from `GET /admin/messages/{id}`. Mirrors
 * [IngestedMessageDetail] on the backend.
 */
export interface MessageDetailRow extends MessageSummary {
  raw_message: string;
  raw_content_type: string;
  last_attempt_at?: string | null;
  next_attempt_at?: string | null;
  delivered_at?: string | null;
  status: MessageStatus;
}

/**
 * Per-resource entry on the effects response.
 *
 * `resource_type` + `id` are the two pieces the backend cleanly splits;
 * `id` itself carries the canonical `ResourceType/id` form per the Kotlin
 * controller's docstring (defensive split tolerating
 * `_history` suffixes).
 */
export interface MessageEffectsResource {
  resource_type: string;
  id: string;
}

/**
 * One Subscription match recorded for the message.
 *
 * `tbd_match_precise` is a backend caveat — when true the match was
 * inferred from criteria type prefix rather than running the criteria as a
 * FHIR search against HAPI. We surface it as a tooltip in the UI so an
 * operator knows when to second-guess the result.
 */
export interface MessageEffectsSubscription {
  id: string;
  channel_type: string;
  endpoint?: string | null;
  criteria: string;
  matched_resource: string;
  tbd_match_precise?: boolean;
}

/**
 * One notification attempt recorded for the message. The backend's time-
 * windowed join (`tbd_time_windowed`) is also tooltipped where set.
 */
export interface MessageEffectsNotification {
  subscription_id: string;
  channel_type: string;
  endpoint?: string | null;
  attempted_at?: string | null;
  outcome: string;
  http_status?: number | null;
  duration_ms?: number | null;
  error?: string | null;
  tbd_time_windowed?: boolean;
}

/**
 * Top-level `effects_status` is one of:
 *   - "delivered"  — message reached HAPI; resources/subs are populated.
 *   - "pending"    — RECEIVED or TRANSFORMING; nothing to show yet.
 *   - "failed"     — FAILED or DEAD_LETTER; surface `last_error`.
 *   - "unknown"    — DELIVERED but `created_resource_refs` is null
 *                    (pre-V005 row OR no-transform short-circuit path).
 */
export type EffectsStatus = "delivered" | "pending" | "failed" | "unknown";

export interface MessageEffectsResponse {
  effects_status: EffectsStatus;
  message: {
    id: number | null;
    correlation_id?: string | null;
    received_at?: string | null;
    status: string;
    source_system: string;
    source_id: string;
    message_type: string;
    last_error?: string | null;
  };
  transform: {
    delivered_at?: string | null;
    attempt_count: number;
    last_attempt_at?: string | null;
  };
  fhir_resources_created: MessageEffectsResource[];
  subscriptions_matched: MessageEffectsSubscription[];
  notifications_fired: MessageEffectsNotification[];
}
