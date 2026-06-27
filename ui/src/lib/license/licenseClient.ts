/**
 * License-server client (stub for Epic #428).
 *
 * This module is the UI's view of "what license tier is the customer on, and
 * what entitlements does that unlock." Ticket #437 wires route gating against
 * `loadLicenseState()`; this stub exists so #437 doesn't block on Epic #428's
 * server-side work.
 *
 * Behavior summary (full model in §3.2.1 of the master plan):
 *
 *   1. No `LICENSE_KEY` env var       -> `LicenseState.foss` ("no-license-key").
 *   2. Key + reachable server         -> `LicenseState.active`. Cache is updated.
 *   3. Key + unreachable + fresh cache -> `LicenseState.stale-active`. WARN log.
 *   4. Key + unreachable + stale cache -> `LicenseState.foss`
 *                                        ("license-server-unreachable-and-stale-cache").
 *
 * `refresh()` re-runs the same logic on demand; auto-refresh every 12h is
 * stubbed via `startAutoRefresh()` and lives behind a feature flag because
 * production scheduling is also Epic #428's problem.
 *
 * NOTE: this file is server-side only. The license server URL and key MUST
 * NOT leak into the client bundle.
 */

import { createHash } from "node:crypto";
import {
  buildCacheEntry,
  cacheValidUntil,
  defaultCachePath,
  isFresh,
  readCache,
  writeCache,
} from "./licenseCache";
import {
  entitlementSetFromArray,
  type EntitlementResponse,
  type LicenseCacheEntry,
  type LicenseInfo,
  type LicenseState,
} from "./types";

const DEFAULT_LICENSE_SERVER_URL = "https://license.bzonfhir.com";
const PRODUCT_SLUG = "subscription-service";
const STALE_BANNER_MESSAGE =
  "License verification pending — using cached entitlements. We will retry automatically.";
const AUTO_REFRESH_MS = 12 * 60 * 60 * 1000;

export interface LoadLicenseStateOptions {
  /** Override the env var read for the license key. Mostly for tests. */
  licenseKey?: string | null;
  /** Override the license-server base URL. Mostly for tests. */
  licenseServerUrl?: string;
  /** Override the on-disk cache path. Required for tests so they don't touch the user's home dir. */
  cachePath?: string;
  /** Override `fetch` so tests can simulate network failures. */
  fetchImpl?: typeof fetch;
  /** Inject "now" for deterministic freshness checks. */
  now?: Date;
  /** Override the structured logger (for capturing warnings in tests). */
  logger?: LicenseLogger;
}

export interface LicenseLogger {
  warn(message: string, context?: Record<string, unknown>): void;
  info(message: string, context?: Record<string, unknown>): void;
}

const defaultLogger: LicenseLogger = {
  // eslint-disable-next-line no-console
  warn: (msg, ctx) => console.warn(`[license] ${msg}`, ctx ?? {}),
  // eslint-disable-next-line no-console
  info: (msg, ctx) => console.info(`[license] ${msg}`, ctx ?? {}),
};

/** Read the license key from `process.env.LICENSE_KEY`. Returns null when unset or empty. */
export function readLicenseFromEnv(env?: { LICENSE_KEY?: string | undefined }): string | null {
  const source = env ?? (process.env as { LICENSE_KEY?: string | undefined });
  const raw = source.LICENSE_KEY;
  if (typeof raw !== "string") return null;
  const trimmed = raw.trim();
  return trimmed.length === 0 ? null : trimmed;
}

/**
 * Call the license server and return a parsed `EntitlementResponse`. Throws
 * on network failure, non-2xx response, or a malformed JSON body.
 *
 * Signature verification is a TODO for Epic #428.
 */
export async function fetchEntitlements(
  licenseKey: string,
  options: {
    licenseServerUrl?: string;
    fetchImpl?: typeof fetch;
  } = {},
): Promise<EntitlementResponse> {
  const url = `${(options.licenseServerUrl ?? DEFAULT_LICENSE_SERVER_URL).replace(/\/$/, "")}/entitlements`;
  const fetcher = options.fetchImpl ?? fetch;
  const res = await fetcher(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
    },
    body: JSON.stringify({ licenseKey, productSlug: PRODUCT_SLUG }),
  });
  if (!res.ok) {
    throw new Error(`license server returned HTTP ${res.status}`);
  }
  const body: unknown = await res.json();
  if (!isEntitlementResponse(body)) {
    throw new Error("license server returned a malformed entitlement response");
  }
  return body;
}

/**
 * Resolve the full LicenseState. Used at UI boot and on every `refresh()` tick.
 *
 * Steps:
 *   1. Read `LICENSE_KEY`. If absent -> FOSS.
 *   2. Try the license server.
 *      - Success -> write cache, return `active`.
 *      - Failure -> read the cache:
 *        - Fresh -> log WARN, return `stale-active`.
 *        - Stale -> log WARN, return FOSS.
 */
export async function loadLicenseState(options: LoadLicenseStateOptions = {}): Promise<LicenseState> {
  const logger = options.logger ?? defaultLogger;
  const now = options.now ?? new Date();
  const cachePath = options.cachePath ?? defaultCachePath();

  const licenseKey =
    options.licenseKey === undefined ? readLicenseFromEnv() : options.licenseKey;
  if (!licenseKey) {
    return { kind: "foss", reason: "no-license-key" };
  }
  const fingerprint = fingerprintLicenseKey(licenseKey);

  try {
    const response = await fetchEntitlements(licenseKey, {
      licenseServerUrl: options.licenseServerUrl,
      fetchImpl: options.fetchImpl,
    });
    const entry = buildCacheEntry(response, fingerprint, now);
    try {
      await writeCache(entry, cachePath);
    } catch (err) {
      // A cache write failure shouldn't prevent the UI from booting with the
      // fresh entitlements we already have.
      logger.warn("failed to persist license cache", {
        error: err instanceof Error ? err.message : String(err),
        cachePath,
      });
    }
    return buildActiveState(entry);
  } catch (fetchErr) {
    logger.warn("license server unreachable; falling back to cache", {
      error: fetchErr instanceof Error ? fetchErr.message : String(fetchErr),
      fingerprint,
    });

    const cached = await readCache(cachePath);
    if (cached && isFresh(cached, now)) {
      return buildStaleActiveState(cached);
    }
    logger.warn("license server unreachable and cache is stale; running in FOSS mode", {
      fingerprint,
      hadCache: cached !== null,
    });
    return { kind: "foss", reason: "license-server-unreachable-and-stale-cache" };
  }
}

/**
 * Run `loadLicenseState()` again. Surfaces a more explicit name for UI code
 * that wants to drive the refresh from a "Re-check license" button.
 */
export function refresh(options: LoadLicenseStateOptions = {}): Promise<LicenseState> {
  return loadLicenseState(options);
}

/**
 * Stub auto-refresh. Production scheduling lives in Epic #428 -- this is just
 * enough plumbing for #437 to call. Returns the timer handle so callers can
 * `clearInterval()` it in dev hot-reload.
 *
 * NOTE: deliberately does NOT call `loadLicenseState()` immediately; callers
 * pair `loadLicenseState()` (boot) with `startAutoRefresh()` (cycle).
 */
export function startAutoRefresh(
  onState: (state: LicenseState) => void,
  options: LoadLicenseStateOptions = {},
): NodeJS.Timeout {
  const handle = setInterval(() => {
    void loadLicenseState(options).then(onState).catch((err) => {
      const logger = options.logger ?? defaultLogger;
      logger.warn("auto-refresh tick failed", {
        error: err instanceof Error ? err.message : String(err),
      });
    });
  }, AUTO_REFRESH_MS);
  // `setInterval` keeps the Node process alive; unref so a stuck timer
  // doesn't block shutdown.
  if (typeof handle.unref === "function") handle.unref();
  return handle;
}

// ---------------------------------------------------------------------------

function buildActiveState(entry: LicenseCacheEntry): LicenseState {
  const info: LicenseInfo = {
    tierName: entry.response.tierName,
    expiresAt: new Date(entry.response.expiresAt),
    licenseKeyFingerprint: entry.licenseKeyFingerprint,
  };
  return {
    kind: "active",
    info,
    entitlements: entitlementSetFromArray(entry.response.entitlements),
    fetchedAt: new Date(entry.fetchedAt),
    cacheValidUntil: cacheValidUntil(entry),
  };
}

function buildStaleActiveState(entry: LicenseCacheEntry): LicenseState {
  const info: LicenseInfo = {
    tierName: entry.response.tierName,
    expiresAt: new Date(entry.response.expiresAt),
    licenseKeyFingerprint: entry.licenseKeyFingerprint,
  };
  return {
    kind: "stale-active",
    info,
    entitlements: entitlementSetFromArray(entry.response.entitlements),
    fetchedAt: new Date(entry.fetchedAt),
    bannerMessage: STALE_BANNER_MESSAGE,
  };
}

function fingerprintLicenseKey(licenseKey: string): string {
  return createHash("sha256").update(licenseKey).digest("hex").slice(0, 8);
}

function isEntitlementResponse(value: unknown): value is EntitlementResponse {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  if (!Array.isArray(v.entitlements)) return false;
  if (!v.entitlements.every((e) => typeof e === "string")) return false;
  if (typeof v.expiresAt !== "string") return false;
  if (typeof v.tierName !== "string") return false;
  if (typeof v.signature !== "string") return false;
  return true;
}
