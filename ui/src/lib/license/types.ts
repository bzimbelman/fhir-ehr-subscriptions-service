/**
 * License-stub types for ticket #438 (Epic #425).
 *
 * The real license-server backend lands in Epic #428. This module gives the UI
 * a typed surface to gate routes against (ticket #437 wires it in) without
 * waiting for the backend.
 *
 * SCOPE NOTE: `EntitlementSet` is owned by `@bzonfhir/ui-extensions` (ticket
 * #436) which does not yet exist on this branch. We declare a LOCAL interface
 * here matching the shape from §3.2.1 of the master plan. The integration
 * ticket (#437) will swap this local definition for the import once #436
 * lands. DO NOT add `import ... from "@bzonfhir/ui-extensions"` here.
 */

/**
 * The set of entitlements the customer's license grants. Mirrors the shape
 * that `@bzonfhir/ui-extensions` will export (ticket #436). Implemented here
 * as a tiny wrapper around a string array so the UI registry can ask
 * `entitlements.has("compliance.iti20")` and the footer can render
 * `entitlements.toArray().join(", ")`.
 */
export interface EntitlementSet {
  has(entitlement: string): boolean;
  toArray(): string[];
}

/**
 * Build an EntitlementSet from a flat array of entitlement strings. Order is
 * preserved in `toArray()` so the footer renders the same string the license
 * server sent.
 */
export function entitlementSetFromArray(entitlements: string[]): EntitlementSet {
  const set = new Set(entitlements);
  const ordered = [...entitlements];
  return {
    has: (entitlement: string) => set.has(entitlement),
    toArray: () => [...ordered],
  };
}

/**
 * Human-facing license metadata. The fingerprint is the first 8 chars of a
 * sha256 of the license key -- safe to log, useful for support tickets.
 */
export interface LicenseInfo {
  tierName: string;
  expiresAt: Date;
  licenseKeyFingerprint: string;
}

/**
 * The state of the UI's license check. Four discriminated variants:
 *
 *   - `foss`         -- no license configured OR fall-through after cache
 *                       expiry; the UI runs as the FOSS image does today.
 *   - `active`       -- license server returned a valid response; cache is
 *                       fresh.
 *   - `stale-active` -- license server is currently unreachable but the
 *                       on-disk cache is still inside the 7-day TTL. The UI
 *                       SHOULD show a soft banner (rendering is #437's job;
 *                       this stub just supplies the `bannerMessage` text).
 *
 * See §3.2.1 of the master plan for the full behavior model.
 */
export type LicenseState =
  | {
      kind: "foss";
      reason: "no-license-key" | "license-server-unreachable-and-stale-cache";
    }
  | {
      kind: "active";
      info: LicenseInfo;
      entitlements: EntitlementSet;
      fetchedAt: Date;
      cacheValidUntil: Date;
    }
  | {
      kind: "stale-active";
      info: LicenseInfo;
      entitlements: EntitlementSet;
      fetchedAt: Date;
      bannerMessage: string;
    };

/**
 * Shape returned by `POST /entitlements` on the license server.
 *
 * The license server lands in Epic #428. For this stub we accept any response
 * that has the right top-level shape; signature verification is a TODO for
 * #428.
 */
export interface EntitlementResponse {
  entitlements: string[];
  expiresAt: string;
  tierName: string;
  signature: string;
}

/**
 * On-disk cache shape. We persist the raw response plus the timestamp it was
 * fetched at so we can compute freshness without trusting the file's mtime.
 */
export interface LicenseCacheEntry {
  fetchedAt: string;
  licenseKeyFingerprint: string;
  response: EntitlementResponse;
}
