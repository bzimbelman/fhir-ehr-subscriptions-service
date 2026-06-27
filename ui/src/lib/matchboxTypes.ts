/**
 * TypeScript shapes for the `/admin/matchbox/*` API responses consumed
 * by the operator UI (Epic #398, ticket #405).
 *
 * Mirrors the Kotlin DTOs in
 * `interface-engine/.../admin/AdminMatchboxController.kt` and the
 * contract documented under docs/admin-api.md. Permissive on optional
 * fields so a stale UI doesn't crash on a newer backend.
 */

export interface MatchboxHealth {
  reachable: boolean;
  version: string | null;
  base_url: string;
  checked_at: string;
  response_time_ms: number;
  error: string | null;
}

export interface StructureMapItem {
  id: string;
  url: string | null;
  name: string | null;
  title: string | null;
  status: string | null;
  version: string | null;
}

export interface StructureMapsEnvelope {
  total: number;
  items: StructureMapItem[];
  error: string | null;
}

export interface TransformRequest {
  source_format?: string;
  raw_message: string;
  map_url?: string;
}

export interface TransformResponse {
  success: boolean;
  /**
   * On success this is a FHIR Bundle as a generic JSON tree. Rendered
   * pretty-printed in the output panel. `null` when success=false.
   */
  bundle: unknown | null;
  error: string | null;
}
