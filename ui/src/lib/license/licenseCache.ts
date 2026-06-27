/**
 * On-disk license cache with a 7-day TTL.
 *
 * Why disk? Per §3.2.1 of the master plan: a license-server outage MUST NOT
 * bring the customer's UI down. The Next.js server reads the cache on boot
 * (and on each `refresh()` cycle) so a transient outage degrades to
 * "stale-active" rather than "FOSS". After 7 days without a successful fetch
 * we give up and fall back to FOSS so a permanently-misconfigured server
 * doesn't pretend forever.
 *
 * Path: `~/.subscription-service/license-cache.json` by default. Tests pass an
 * explicit `cachePath` so they can write to a tmp dir.
 */

import { promises as fs } from "node:fs";
import path from "node:path";
import os from "node:os";
import type { EntitlementResponse, LicenseCacheEntry } from "./types";

/** Cache TTL in milliseconds. 7 days, per the master plan. */
export const CACHE_TTL_MS = 7 * 24 * 60 * 60 * 1000;

/** Default on-disk cache path. */
export function defaultCachePath(): string {
  return path.join(os.homedir(), ".subscription-service", "license-cache.json");
}

/** Read the cache. Returns `null` when the file does not exist or cannot be parsed. */
export async function readCache(cachePath: string = defaultCachePath()): Promise<LicenseCacheEntry | null> {
  let raw: string;
  try {
    raw = await fs.readFile(cachePath, "utf-8");
  } catch (err) {
    if (isNodeNotFound(err)) return null;
    // Other read errors (permissions, etc.) are treated like a missing cache;
    // a corrupt cache shouldn't crash UI boot.
    return null;
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }

  if (!isCacheEntry(parsed)) return null;
  return parsed;
}

/** Write the cache atomically (write tmp, rename) so a partial write can never corrupt it. */
export async function writeCache(
  entry: LicenseCacheEntry,
  cachePath: string = defaultCachePath(),
): Promise<void> {
  const dir = path.dirname(cachePath);
  await fs.mkdir(dir, { recursive: true });
  const tmp = `${cachePath}.tmp-${process.pid}-${Date.now()}`;
  await fs.writeFile(tmp, JSON.stringify(entry, null, 2), { encoding: "utf-8", mode: 0o600 });
  await fs.rename(tmp, cachePath);
}

/**
 * Returns the cached entry IF it exists AND is fresh (fetchedAt within
 * CACHE_TTL_MS of `now`). Returns `null` otherwise. Callers that still want
 * the stale entry (e.g. for diagnostics) should call `readCache` directly.
 */
export async function readFreshCache(
  cachePath: string = defaultCachePath(),
  now: Date = new Date(),
): Promise<LicenseCacheEntry | null> {
  const entry = await readCache(cachePath);
  if (!entry) return null;
  if (!isFresh(entry, now)) return null;
  return entry;
}

/** True iff `fetchedAt` is within CACHE_TTL_MS of `now`. */
export function isFresh(entry: LicenseCacheEntry, now: Date = new Date()): boolean {
  const fetchedAt = Date.parse(entry.fetchedAt);
  if (Number.isNaN(fetchedAt)) return false;
  return now.getTime() - fetchedAt < CACHE_TTL_MS;
}

/** When the entry's cache window expires. */
export function cacheValidUntil(entry: LicenseCacheEntry): Date {
  return new Date(Date.parse(entry.fetchedAt) + CACHE_TTL_MS);
}

/** Build a cache entry from a fresh server response and the current time. */
export function buildCacheEntry(
  response: EntitlementResponse,
  licenseKeyFingerprint: string,
  fetchedAt: Date = new Date(),
): LicenseCacheEntry {
  return {
    fetchedAt: fetchedAt.toISOString(),
    licenseKeyFingerprint,
    response,
  };
}

// ---------------------------------------------------------------------------

function isNodeNotFound(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { code?: string }).code === "ENOENT";
}

function isCacheEntry(value: unknown): value is LicenseCacheEntry {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  if (typeof v.fetchedAt !== "string") return false;
  if (typeof v.licenseKeyFingerprint !== "string") return false;
  if (typeof v.response !== "object" || v.response === null) return false;
  const r = v.response as Record<string, unknown>;
  if (!Array.isArray(r.entitlements)) return false;
  if (!r.entitlements.every((e) => typeof e === "string")) return false;
  if (typeof r.expiresAt !== "string") return false;
  if (typeof r.tierName !== "string") return false;
  if (typeof r.signature !== "string") return false;
  return true;
}
