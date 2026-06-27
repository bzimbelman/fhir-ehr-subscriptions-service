import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { promises as fs } from "node:fs";
import path from "node:path";
import os from "node:os";
import {
  CACHE_TTL_MS,
  buildCacheEntry,
  readCache,
  readFreshCache,
  writeCache,
} from "@/lib/license/licenseCache";
import type { EntitlementResponse } from "@/lib/license/types";

/**
 * Cache tests run against a per-test tmp dir so the user's
 * `~/.subscription-service/license-cache.json` is never touched.
 */

function buildResponse(overrides: Partial<EntitlementResponse> = {}): EntitlementResponse {
  return {
    entitlements: ["compliance.iti20", "simulation.pack"],
    expiresAt: "2027-03-01T00:00:00.000Z",
    tierName: "Pro",
    signature: "stub-signature",
    ...overrides,
  };
}

describe("licenseCache", () => {
  let tmpDir: string;
  let cachePath: string;

  beforeEach(async () => {
    tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "license-cache-"));
    cachePath = path.join(tmpDir, "license-cache.json");
  });

  afterEach(async () => {
    await fs.rm(tmpDir, { recursive: true, force: true });
  });

  it("round-trips a write+read", async () => {
    const fetchedAt = new Date("2026-06-27T12:00:00.000Z");
    const entry = buildCacheEntry(buildResponse(), "abcd1234", fetchedAt);

    await writeCache(entry, cachePath);
    const loaded = await readCache(cachePath);

    expect(loaded).not.toBeNull();
    expect(loaded?.fetchedAt).toBe(fetchedAt.toISOString());
    expect(loaded?.licenseKeyFingerprint).toBe("abcd1234");
    expect(loaded?.response.entitlements).toEqual(["compliance.iti20", "simulation.pack"]);
    expect(loaded?.response.tierName).toBe("Pro");
  });

  it("returns null when the cache file does not exist", async () => {
    const missing = path.join(tmpDir, "does-not-exist.json");
    expect(await readCache(missing)).toBeNull();
    expect(await readFreshCache(missing)).toBeNull();
  });

  it("returns null from readFreshCache when the entry is older than 7 days", async () => {
    const fetchedAt = new Date("2026-06-27T12:00:00.000Z");
    const entry = buildCacheEntry(buildResponse(), "abcd1234", fetchedAt);
    await writeCache(entry, cachePath);

    // Exactly TTL + 1ms past fetchedAt -> stale.
    const now = new Date(fetchedAt.getTime() + CACHE_TTL_MS + 1);
    expect(await readFreshCache(cachePath, now)).toBeNull();

    // Sanity: the raw entry is still readable -- only the freshness gate dropped it.
    const raw = await readCache(cachePath);
    expect(raw).not.toBeNull();
  });
});
