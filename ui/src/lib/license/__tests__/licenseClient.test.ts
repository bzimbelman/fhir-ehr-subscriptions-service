import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { promises as fs } from "node:fs";
import path from "node:path";
import os from "node:os";
import {
  CompactSign,
  exportJWK,
  generateKeyPair,
  type JWK,
} from "jose";
import {
  fetchEntitlements,
  loadLicenseState,
  readLicenseFromEnv,
  type LicenseLogger,
} from "@/lib/license/licenseClient";
import { buildCacheEntry, writeCache, CACHE_TTL_MS } from "@/lib/license/licenseCache";
import type { EntitlementResponse } from "@/lib/license/types";

// --- JWS signing helpers for the verifier-integration tests (ticket #459) --

interface TestKeypair {
  publicJwk: JWK;
  privateKey: CryptoKey;
  kid: string;
}

async function makeKeypair(kid: string): Promise<TestKeypair> {
  const { publicKey, privateKey } = await generateKeyPair("ES256", { extractable: true });
  const publicJwk = await exportJWK(publicKey);
  publicJwk.alg = "ES256";
  publicJwk.kid = kid;
  publicJwk.use = "sig";
  return { publicJwk, privateKey, kid };
}

async function signResponse(response: EntitlementResponse, kp: TestKeypair): Promise<string> {
  // Sign the response body WITHOUT the signature field (we'll splice it in
  // after). This mirrors what the real license server does: produce the body,
  // sign it, attach the signature.
  const body = {
    entitlements: response.entitlements,
    expiresAt: response.expiresAt,
    tierName: response.tierName,
  };
  const raw = new TextEncoder().encode(JSON.stringify(body));
  const encoded = new Uint8Array(raw);
  return new CompactSign(encoded)
    .setProtectedHeader({ alg: "ES256", kid: kp.kid })
    .sign(kp.privateKey);
}

function jwksResponse(keys: JWK[]): Response {
  return new Response(JSON.stringify({ keys }), {
    status: 200,
    headers: { "content-type": "application/json" },
  });
}

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "content-type": "application/json" },
    ...init,
  });
}

/**
 * Build a fetch mock that serves the JWKS at /.well-known/jwks.json and the
 * entitlement response at /entitlements. Mirrors the two-endpoint shape the
 * real license server exposes.
 */
function makeServerFetch(
  jwks: JWK[],
  entitlement: EntitlementResponse,
): typeof fetch {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : (input as URL).toString();
    if (url.endsWith("/.well-known/jwks.json")) {
      return jwksResponse(jwks);
    }
    if (url.endsWith("/entitlements")) {
      return jsonResponse(entitlement);
    }
    return new Response("not found", { status: 404 });
  }) as unknown as typeof fetch;
}

interface MockLogger extends LicenseLogger {
  warnings: Array<{ message: string; context?: Record<string, unknown> }>;
  infos: Array<{ message: string; context?: Record<string, unknown> }>;
}

function makeLogger(): MockLogger {
  const warnings: MockLogger["warnings"] = [];
  const infos: MockLogger["infos"] = [];
  return {
    warnings,
    infos,
    warn: (message, context) => warnings.push({ message, context }),
    info: (message, context) => infos.push({ message, context }),
  };
}

function buildResponse(overrides: Partial<EntitlementResponse> = {}): EntitlementResponse {
  return {
    entitlements: ["compliance.iti20", "simulation.pack"],
    expiresAt: "2027-03-01T00:00:00.000Z",
    tierName: "Pro",
    signature: "stub-signature",
    ...overrides,
  };
}

function mockFetchOnce(response: Response | (() => Response | Promise<Response>)): typeof fetch {
  const impl = typeof response === "function" ? response : () => response;
  return vi.fn(impl) as unknown as typeof fetch;
}

describe("readLicenseFromEnv", () => {
  it("returns null when LICENSE_KEY is unset", () => {
    expect(readLicenseFromEnv({})).toBeNull();
  });

  it("returns null when LICENSE_KEY is empty / whitespace", () => {
    expect(readLicenseFromEnv({ LICENSE_KEY: "" })).toBeNull();
    expect(readLicenseFromEnv({ LICENSE_KEY: "   " })).toBeNull();
  });

  it("returns the trimmed key when LICENSE_KEY is set", () => {
    expect(readLicenseFromEnv({ LICENSE_KEY: "abc-123" })).toBe("abc-123");
    expect(readLicenseFromEnv({ LICENSE_KEY: "  abc-123  " })).toBe("abc-123");
  });
});

describe("fetchEntitlements", () => {
  it("POSTs the right URL + body and parses the response", async () => {
    const captured: { url?: string; init?: RequestInit } = {};
    const fetchImpl = vi.fn(async (url: string, init: RequestInit) => {
      captured.url = url;
      captured.init = init;
      return new Response(JSON.stringify(buildResponse()), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }) as unknown as typeof fetch;

    const result = await fetchEntitlements("the-license-key", {
      licenseServerUrl: "https://license.example.com",
      fetchImpl,
    });

    expect(captured.url).toBe("https://license.example.com/entitlements");
    expect(captured.init?.method).toBe("POST");
    const headers = new Headers(captured.init?.headers as HeadersInit);
    expect(headers.get("content-type")).toBe("application/json");
    expect(captured.init?.body).toBe(
      JSON.stringify({ licenseKey: "the-license-key", productSlug: "subscription-service" }),
    );
    expect(result.entitlements).toEqual(["compliance.iti20", "simulation.pack"]);
    expect(result.tierName).toBe("Pro");
  });

  it("rejects malformed responses (missing entitlements field)", async () => {
    const fetchImpl = mockFetchOnce(
      new Response(JSON.stringify({ tierName: "Pro", expiresAt: "x", signature: "y" }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await expect(
      fetchEntitlements("the-license-key", {
        licenseServerUrl: "https://license.example.com",
        fetchImpl,
      }),
    ).rejects.toThrow(/malformed/i);
  });
});

describe("loadLicenseState", () => {
  let tmpDir: string;
  let cachePath: string;

  beforeEach(async () => {
    tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "license-client-"));
    cachePath = path.join(tmpDir, "license-cache.json");
  });

  afterEach(async () => {
    await fs.rm(tmpDir, { recursive: true, force: true });
  });

  it("returns FOSS when LICENSE_KEY is unset", async () => {
    const state = await loadLicenseState({ licenseKey: null, cachePath });
    expect(state.kind).toBe("foss");
    if (state.kind === "foss") {
      expect(state.reason).toBe("no-license-key");
    }
  });

  it("returns active when the server returns a valid response", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    const kp = await makeKeypair("key-1");
    const base = buildResponse();
    const signed = await signResponse(base, kp);
    const response: EntitlementResponse = { ...base, signature: signed };
    const fetchImpl = makeServerFetch([kp.publicJwk], response);

    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
    });

    expect(state.kind).toBe("active");
    if (state.kind === "active") {
      expect(state.info.tierName).toBe("Pro");
      expect(state.entitlements.has("compliance.iti20")).toBe(true);
      expect(state.entitlements.has("not-a-real-entitlement")).toBe(false);
      expect(state.entitlements.toArray()).toEqual([
        "compliance.iti20",
        "simulation.pack",
      ]);
      expect(state.info.licenseKeyFingerprint).toMatch(/^[0-9a-f]{8}$/);
      expect(state.fetchedAt.getTime()).toBe(now.getTime());
      expect(state.cacheValidUntil.getTime()).toBe(now.getTime() + CACHE_TTL_MS);
    }
  });

  it("returns stale-active when the server is unreachable but the cache is fresh", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    // Pre-populate a fresh cache.
    const fetchedAt = new Date(now.getTime() - 60 * 60 * 1000); // 1h ago
    await writeCache(buildCacheEntry(buildResponse(), "deadbeef", fetchedAt), cachePath);

    const fetchImpl = mockFetchOnce(() => {
      throw new Error("ECONNREFUSED");
    });
    const logger = makeLogger();

    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
      logger,
    });

    expect(state.kind).toBe("stale-active");
    if (state.kind === "stale-active") {
      expect(state.info.tierName).toBe("Pro");
      expect(state.entitlements.has("compliance.iti20")).toBe(true);
      expect(state.bannerMessage).toMatch(/license verification pending/i);
      expect(state.fetchedAt.getTime()).toBe(fetchedAt.getTime());
    }
    expect(logger.warnings.length).toBeGreaterThanOrEqual(1);
    expect(logger.warnings[0]?.message).toMatch(/unreachable/i);
  });

  it("returns FOSS when the server is unreachable AND the cache is stale", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    // Cache is 8 days old -> stale.
    const fetchedAt = new Date(now.getTime() - CACHE_TTL_MS - 24 * 60 * 60 * 1000);
    await writeCache(buildCacheEntry(buildResponse(), "deadbeef", fetchedAt), cachePath);

    const fetchImpl = mockFetchOnce(() => {
      throw new Error("ECONNREFUSED");
    });
    const logger = makeLogger();

    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
      logger,
    });

    expect(state.kind).toBe("foss");
    if (state.kind === "foss") {
      expect(state.reason).toBe("license-server-unreachable-and-stale-cache");
    }
    // Two warnings: "unreachable; falling back to cache" and "cache is stale; FOSS".
    expect(logger.warnings.length).toBeGreaterThanOrEqual(2);
  });

  it("updates the on-disk cache after a successful fetch", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    // After ticket #459: cache writes require a verifiable signature. Use the
    // full two-endpoint mock so the verifier can fetch the JWKS too.
    const kp = await makeKeypair("key-1");
    const baseResp = buildResponse({ tierName: "Cloud" });
    const signed = await signResponse(baseResp, kp);
    const response: EntitlementResponse = { ...baseResp, signature: signed };
    const fetchImpl = makeServerFetch([kp.publicJwk], response);

    await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
    });

    const onDisk = JSON.parse(await fs.readFile(cachePath, "utf-8")) as {
      fetchedAt: string;
      response: EntitlementResponse;
      signatureKid?: string;
    };
    expect(onDisk.fetchedAt).toBe(now.toISOString());
    expect(onDisk.response.tierName).toBe("Cloud");
    expect(onDisk.response.entitlements).toEqual([
      "compliance.iti20",
      "simulation.pack",
    ]);
    // Ticket #459: cache MUST preserve the signature so a future boot can
    // sanity-check it.
    expect(onDisk.response.signature).toBe(signed);
    expect(onDisk.signatureKid).toBe("key-1");
  });

  // --- Ticket #459: signature verification integration ---------------------

  it("fetchEntitlements result is run through the signature verifier (active)", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    const kp = await makeKeypair("key-A");
    const base = buildResponse();
    const signed = await signResponse(base, kp);
    const response: EntitlementResponse = { ...base, signature: signed };
    const fetchImpl = makeServerFetch([kp.publicJwk], response);
    const logger = makeLogger();

    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
      logger,
    });

    expect(state.kind).toBe("active");
    // The verifier was called -> JWKS endpoint was hit at least once.
    const fetchMock = fetchImpl as unknown as ReturnType<typeof vi.fn>;
    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.endsWith("/.well-known/jwks.json"))).toBe(true);
    expect(urls.some((u) => u.endsWith("/entitlements"))).toBe(true);
    // No signature-failure warning.
    expect(logger.warnings.some((w) => /signature/i.test(w.message))).toBe(false);
  });

  it("treats an invalid signature like a failed fetch (stale-active when cache is fresh)", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    // Server publishes JWKS for kp1, but signs the response with kp2 -> unknown kid.
    const kp1 = await makeKeypair("server-key");
    const kp2 = await makeKeypair("attacker-key");
    const base = buildResponse({ tierName: "Pro" });
    const badSig = await signResponse(base, kp2);
    const response: EntitlementResponse = { ...base, signature: badSig };
    const fetchImpl = makeServerFetch([kp1.publicJwk], response);

    // Pre-populate a fresh cache so we can prove we fell back to it.
    const fetchedAt = new Date(now.getTime() - 60 * 60 * 1000);
    await writeCache(
      buildCacheEntry(
        { ...buildResponse({ tierName: "PriorCacheTier" }), signature: "old-sig" },
        "deadbeef",
        fetchedAt,
      ),
      cachePath,
    );

    const logger = makeLogger();
    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
      logger,
    });

    expect(state.kind).toBe("stale-active");
    if (state.kind === "stale-active") {
      // We fell back to the prior cache entry, not the unsigned/invalid response.
      expect(state.info.tierName).toBe("PriorCacheTier");
    }
    // A WARN must be logged for the verification failure (separate from the
    // generic "unreachable" warning).
    expect(
      logger.warnings.some((w) => /signature|verif/i.test(w.message)),
    ).toBe(true);
  });

  it("falls back to FOSS when the signature is invalid AND no cache exists", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    const kp1 = await makeKeypair("server-key");
    const kp2 = await makeKeypair("attacker-key");
    const base = buildResponse();
    const badSig = await signResponse(base, kp2);
    const response: EntitlementResponse = { ...base, signature: badSig };
    const fetchImpl = makeServerFetch([kp1.publicJwk], response);
    const logger = makeLogger();

    const state = await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
      logger,
    });

    expect(state.kind).toBe("foss");
    if (state.kind === "foss") {
      expect(state.reason).toBe("license-server-unreachable-and-stale-cache");
    }
    expect(logger.warnings.some((w) => /signature|verif/i.test(w.message))).toBe(true);
  });

  it("preserves the signature in the cached entry on a valid response", async () => {
    const now = new Date("2026-06-27T12:00:00.000Z");
    const kp = await makeKeypair("key-Z");
    const base = buildResponse({ tierName: "Enterprise" });
    const signed = await signResponse(base, kp);
    const response: EntitlementResponse = { ...base, signature: signed };
    const fetchImpl = makeServerFetch([kp.publicJwk], response);

    await loadLicenseState({
      licenseKey: "the-license-key",
      licenseServerUrl: "https://license.example.com",
      cachePath,
      fetchImpl,
      now,
    });

    const onDisk = JSON.parse(await fs.readFile(cachePath, "utf-8")) as {
      response: EntitlementResponse;
      signatureKid?: string;
    };
    expect(onDisk.response.signature).toBe(signed);
    expect(onDisk.signatureKid).toBe("key-Z");
  });
});
