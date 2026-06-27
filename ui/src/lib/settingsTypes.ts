/**
 * TypeScript shapes for the Settings view (Epic #398, ticket #406).
 *
 * The Settings view is a read-only operator surface that answers "how
 * is this deployment configured?". Data is sourced from
 * `/admin/observe/system` (mirroring `SystemSnapshot` in
 * `interface-engine/src/main/resources/admin/observe/openapi.json`)
 * plus a separate matchbox health probe.
 *
 * Permissive on optional fields so a stale UI doesn't crash on a newer
 * backend that grew an extra feature toggle.
 */

export interface FeatureToggles {
  auth_enabled?: boolean;
  validation_mode?: string;
  channel_security_mode?: string;
  multitenancy_mode?: string;
  // Future toggles surface here as the backend adds them. We render any
  // unknown key the same way as the known ones (see SettingsView).
  [key: string]: unknown;
}

export interface Downstream {
  matchbox_base_url?: string;
  hapi_base_url?: string;
  auth_issuer?: string;
  [key: string]: unknown;
}

export interface SystemSnapshot {
  schema_version: string;
  service: string;
  version?: string;
  uptime_seconds: number;
  feature_toggles: FeatureToggles;
  downstream: Downstream;
}
