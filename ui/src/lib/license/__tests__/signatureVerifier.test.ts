/**
 * Tests for the ES256 JWS signature verifier (ticket #459 / Epic #428).
 *
 * The verifier fetches a JWKS from `${licenseServerUrl}/.well-known/jwks.json`,
 * caches it for 1h, and verifies the compact JWS that the license server
 * returns alongside an entitlement response.
 *
 * Dependency: `jose` (currently pinned to ^6.2.3 -- a recent stable that
 * supports `CompactSign`, `createLocalJWKSet`, `exportJWK`, and `generateKeyPair`).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CompactSign,
  exportJWK,
  generateKeyPair,
  type JWK,
} from "jose";
import {
  createSignatureVerifier,
  JWKS_CACHE_TTL_MS,
  type SignatureVerifier,
} from "@/lib/license/signatureVerifier";

const LICENSE_SERVER_URL = "https://license.example.com";
const JWKS_URL = `${LICENSE_SERVER_URL}/.well-known/jwks.json`;

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

async function signPayload(payload: object, kp: TestKeypair): Promise<string> {
  // jsdom's TextEncoder returns a Uint8Array subclass that fails jose's
  // `instanceof Uint8Array` check. Re-wrap in a fresh Uint8Array on the
  // global constructor so the type narrows correctly.
  const raw = new TextEncoder().encode(JSON.stringify(payload));
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

describe("signatureVerifier", () => {
  let kp1: TestKeypair;
  let kp2: TestKeypair;

  beforeEach(async () => {
    kp1 = await makeKeypair("key-1");
    kp2 = await makeKeypair("key-2");
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("accepts a JWS signed by a known ES256 keypair", async () => {
    const fetchImpl = vi.fn(async () => jwksResponse([kp1.publicJwk])) as unknown as typeof fetch;
    const verifier: SignatureVerifier = createSignatureVerifier({
      licenseServerUrl: LICENSE_SERVER_URL,
      fetchImpl,
    });

    const payload = { entitlements: ["compliance.iti20"], tierName: "Pro" };
    const jws = await signPayload(payload, kp1);

    const result = await verifier.verify(jws);
    expect(result.kid).toBe("key-1");
    expect(JSON.parse(new TextDecoder().decode(result.payload))).toEqual(payload);
  });

  it("rejects when the signature is tampered (flip a byte in the payload)", async () => {
    const fetchImpl = vi.fn(async () => jwksResponse([kp1.publicJwk])) as unknown as typeof fetch;
    const verifier = createSignatureVerifier({
      licenseServerUrl: LICENSE_SERVER_URL,
      fetchImpl,
    });

    const jws = await signPayload({ tierName: "Pro" }, kp1);
    const parts = jws.split(".");
    expect(parts).toHaveLength(3);
    const tamperedPayload = Buffer.from(JSON.stringify({ tierName: "Enterprise" }))
      .toString("base64")
      .replace(/=+$/, "")
      .replace(/\+/g, "-")
      .replace(/\//g, "_");
    const tamperedJws = `${parts[0]}.${tamperedPayload}.${parts[2]}`;

    await expect(verifier.verify(tamperedJws)).rejects.toThrow();
  });

  it("rejects when the kid header is unknown", async () => {
    const fetchImpl = vi.fn(async () => jwksResponse([kp1.publicJwk])) as unknown as typeof fetch;
    const verifier = createSignatureVerifier({
      licenseServerUrl: LICENSE_SERVER_URL,
      fetchImpl,
    });

    const jws = await signPayload({ tierName: "Pro" }, kp2);
    await expect(verifier.verify(jws)).rejects.toThrow();
  });

  it("caches the JWKS fetch for 1h (second verify in the window uses cache)", async () => {
    const fetchImpl = vi.fn(async () => jwksResponse([kp1.publicJwk])) as unknown as typeof fetch;
    const t0 = new Date("2026-06-27T12:00:00.000Z");
    const verifier = createSignatureVerifier({
      licenseServerUrl: LICENSE_SERVER_URL,
      fetchImpl,
      now: () => t0,
    });
    const jws = await signPayload({ tierName: "Pro" }, kp1);

    await verifier.verify(jws);
    await verifier.verify(jws);
    await verifier.verify(jws);

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const mock = fetchImpl as unknown as ReturnType<typeof vi.fn>;
    expect(mock.mock.calls[0]?.[0]).toBe(JWKS_URL);
  });

  it("re-fetches the JWKS once the 1h cache window expires", async () => {
    const fetchImpl = vi.fn(async () => jwksResponse([kp1.publicJwk])) as unknown as typeof fetch;
    let now = new Date("2026-06-27T12:00:00.000Z");
    const verifier = createSignatureVerifier({
      licenseServerUrl: LICENSE_SERVER_URL,
      fetchImpl,
      now: () => now,
    });
    const jws = await signPayload({ tierName: "Pro" }, kp1);

    await verifier.verify(jws);
    now = new Date(now.getTime() + JWKS_CACHE_TTL_MS + 1);
    await verifier.verify(jws);

    expect(fetchImpl).toHaveBeenCalledTimes(2);
  });
});
