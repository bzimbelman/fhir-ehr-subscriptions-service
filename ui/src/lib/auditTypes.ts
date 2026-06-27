/**
 * TypeScript shapes for the `/admin/audit` API (Epic #398, ticket #407).
 *
 * Mirrors the Kotlin DTOs in
 * `interface-engine/.../admin/AdminAuditController.kt`. Permissive on
 * optional fields so a stale UI doesn't crash on a newer backend.
 *
 * Why the flat row shape rather than raw FHIR AuditEvent: see the
 * controller docs. The UI renders straight from these rows; the
 * full FHIR JSON is fetched on-demand via /admin/audit/{id} for row
 * expansion.
 */

export interface AuditEventRow {
  id: string;
  recorded: string | null;
  type_code: string | null;
  type_display: string | null;
  subtype_code: string | null;
  /** FHIR R4 AuditEvent.outcome code: "0" | "4" | "8" | "12". */
  outcome: string | null;
  /** Human label for `outcome`: "Success" | "Minor failure" | ... */
  outcome_display: string | null;
  /** AuditEvent.action code: "C" | "R" | "U" | "D" | "E". */
  action: string | null;
  agent_who: string | null;
  agent_name: string | null;
  entity_what: string | null;
  entity_type: string | null;
}

export interface AuditSearchResponse {
  total: number;
  limit: number;
  offset: number;
  items: AuditEventRow[];
  error?: string | null;
}

/**
 * Filter inputs the audit view supports. All optional; an empty filter
 * means "all events" subject to limit/offset.
 */
export interface AuditFilters {
  type?: string;
  subtype?: string;
  outcome?: string;
  agent?: string;
  dateFrom?: string;
  dateTo?: string;
}
