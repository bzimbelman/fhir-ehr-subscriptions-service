/**
 * TypeScript shapes for the admin API responses consumed by the dashboard.
 * These mirror the JSON contracts documented in docs/admin-api.md and the
 * Kotlin DTOs in interface-engine/.../admin/. Add fields here as the
 * backend grows; keep them OPTIONAL where the field is itself optional in
 * the contract so a stale UI doesn't crash on a newer backend.
 *
 * The shapes are deliberately permissive (Record<string, unknown> on
 * extension points) -- the dashboard reads a small slice and is robust to
 * extra fields.
 */

export interface ObserveSystem {
  schema_version: string;
  service: string;
  uptime_seconds: number;
  feature_toggles: Record<string, unknown>;
  downstream: Record<string, unknown>;
  queue: {
    received: number;
    transforming: number;
    failed: number;
    delivered: number;
    dead_letter: number;
  };
  /**
   * Component health surface. The backend reports this via the actuator
   * health endpoint; we re-expose it on /admin/observe/system as a future
   * extension. For #400 we fall back to actuator if this is absent.
   */
  components?: Array<{
    name: string;
    status: "UP" | "DEGRADED" | "DOWN" | "UNKNOWN";
    last_checked?: string;
    detail?: string;
  }>;
}

export interface ThroughputBucket {
  bucket_start: string;
  counts: Partial<Record<MessageStatus, number>>;
}

export interface ObserveThroughput {
  schema_version: string;
  window: string;
  bucket_width: "hour" | "day";
  buckets: ThroughputBucket[];
}

export type MessageStatus =
  | "RECEIVED"
  | "TRANSFORMING"
  | "FAILED"
  | "DELIVERED"
  | "DEAD_LETTER";

export interface MessageSummary {
  id: number;
  received_at: string | null;
  source_protocol: string;
  source_system: string;
  source_id: string;
  message_type: string;
  status: MessageStatus;
  attempt_count: number;
  last_error?: string | null;
  correlation_id?: string | null;
}

export interface MessagesListResponse {
  total: number;
  limit: number;
  offset: number;
  items: MessageSummary[];
}

export interface SubscriptionHealthItem {
  id: string;
  active: boolean;
  channel_type: string;
  endpoint: string;
  delivery_success_count: number;
  delivery_failure_count: number;
  last_attempt_outcome?: "success" | "failure" | null;
}

export interface SubscriptionsHealthResponse {
  total: number;
  items: SubscriptionHealthItem[];
}

export interface ActuatorHealthResponse {
  status: "UP" | "DOWN" | "OUT_OF_SERVICE" | "UNKNOWN" | string;
  components?: Record<
    string,
    {
      status: "UP" | "DOWN" | "OUT_OF_SERVICE" | "UNKNOWN" | string;
      details?: Record<string, unknown>;
    }
  >;
}

/**
 * Semantic system status used to colour the top-bar pill. Computed from the
 * `/admin/observe/system` + `/actuator/health/readiness` responses.
 */
export type SystemStatus = "UP" | "DEGRADED" | "DOWN" | "UNKNOWN";

export interface ComponentHealthRow {
  name: string;
  status: SystemStatus;
  detail?: string;
  lastChecked?: string;
}

export interface DashboardSnapshot {
  system: ObserveSystem | null;
  systemError: string | null;
  throughput: ObserveThroughput | null;
  throughputError: string | null;
  recentMessages: MessageSummary[];
  recentError: string | null;
  subscriptionsHealth: SubscriptionsHealthResponse | null;
  subscriptionsError: string | null;
  actuator: ActuatorHealthResponse | null;
  actuatorError: string | null;
  fetchedAt: string;
}
