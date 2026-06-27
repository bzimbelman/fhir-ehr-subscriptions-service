/**
 * TypeScript shapes for the `/admin/subscriptions/*` API responses
 * consumed by the operator UI (Epic #398, ticket #404).
 *
 * Mirrors the Kotlin DTOs in
 * `interface-engine/src/main/kotlin/.../admin/AdminSubscriptionsController.kt`
 * and the contract documented in `docs/admin-api.md`. Permissive on
 * optional fields so a stale UI doesn't crash on a newer backend.
 *
 * Why a separate file from `dashboardTypes.ts`: the dashboard's
 * existing `SubscriptionHealthItem` type is intentionally a NARROWER
 * subset of the backend's response (only the fields the stat-card
 * counter needs). The subscriptions screens need the full shape
 * including `status`, `criteria`, and the per-row timestamps. Rather
 * than widen the dashboard's type and risk a regression in #400's
 * code paths, this file owns the screen-specific shape.
 */

export type SubscriptionStatusCode =
  | "active"
  | "off"
  | "requested"
  | "error"
  | "unknown";

/**
 * One row in the `/admin/subscriptions/health` response. Mirrors the
 * Kotlin `SubscriptionHealthItem` after ticket #404's additions.
 */
export interface SubscriptionHealthRow {
  id: string;
  active: boolean;
  status: SubscriptionStatusCode | string;
  criteria: string;
  channel_type: string;
  endpoint: string | null;
  delivery_success_count: number;
  delivery_failure_count: number;
  last_attempt_at: string | null;
  last_attempt_outcome: "success" | "failure" | null;
  last_error: string | null;
}

export interface SubscriptionsHealthEnvelope {
  total: number;
  items: SubscriptionHealthRow[];
}

export interface DeliveryAttempt {
  attempted_at: string | null;
  outcome: "success" | "failure" | string;
  http_status: number | null;
  error: string | null;
  duration_ms: number | null;
}

export interface SubscriptionHistoryEnvelope {
  subscription_id: string;
  total: number;
  limit: number;
  offset: number;
  items: DeliveryAttempt[];
}

/**
 * Minimal shape we read from a FHIR R4 Subscription resource. The
 * `/resource` endpoint returns the whole resource (resourceType etc.)
 * but the UI only inspects the fields it renders; the rest is
 * displayed as raw JSON.
 */
export interface FhirSubscriptionResource {
  resourceType?: "Subscription";
  id?: string;
  status?: SubscriptionStatusCode | string;
  criteria?: string;
  channel?: {
    type?: string;
    endpoint?: string;
    payload?: string;
  };
  error?: string;
  [key: string]: unknown;
}
