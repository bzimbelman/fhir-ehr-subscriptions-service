/**
 * ES256 JWS signature verifier for license-server entitlement responses
 * (ticket #459 / Epic #428).
 *
 * The license server signs each entitlement response as a compact JWS using
 * ES256 and publishes its public keys at
 * `${licenseServerUrl}/.well-known/jwks.json`. This module:
 *
 *   1. Lazily fetches the JWKS the first time `verify()` is called.
 *   2. Caches the JWKS in memory for 1 hour. After the TTL we re-fetch on
 *      the next `verify()` call.
 *   3. Verifies a caller-supplied compact JWS using `jose.compactVerify`
 *      against the cached JWKS (matched by `kid` header). A signature with
 *      an unknown `kid`, missing header, or invalid signature throws.
 *
 * Verification failure is a SIGNAL to the caller (`licenseClient`) -- it
 * does NOT crash the boot. The client treats a verification failure the
 * same as a failed fetch (cached fallback -> FOSS).
 *
 * NOTE: This module is server-side only -- it must not be bundled into the
 * browser.
 */

import { compactVerify, createLocalJWKSet, type JSONWebKeySet } from "jose";

/** JWKS in-memory cache TTL: 1 hour, per ticket #459 spec. */
export const JWKS_CACHE_TTL_MS = 60 * 60 * 1000;

/** Result of a successful verification. */
export interface VerifiedJws {
  /** The verified payload bytes (raw -- caller parses JSON). */
  payload: Uint8Array;
  /** The `kid` of the JWK used to verify. Stored in the cache for future sanity checks. */
  kid: string;
}

/** Public API. */
export interface SignatureVerifier {
  /** Verify a compact JWS produced by the license server. Throws on any failure. */
  verify(compactJws: string): Promise<VerifiedJws>;
}

export interface CreateSignatureVerifierOptions {
  /** License-server base URL. Trailing slashes are normalised. */
  licenseServerUrl: string;
  /** Override `fetch`. Tests inject a vi.fn(); production uses globalThis.fetch. */
  fetchImpl?: typeof fetch;
  /** Inject "now" for deterministic cache-window tests. */
  now?: () => Date;
}

interface CachedJwks {
  resolver: ReturnType<typeof createLocalJWKSet>;
  jwks: JSONWebKeySet;
  fetchedAt: number;
}

export function createSignatureVerifier(
  options: CreateSignatureVerifierOptions,
): SignatureVerifier {
  const baseUrl = options.licenseServerUrl.replace(/\/$/, "");
  const jwksUrl = `${baseUrl}/.well-known/jwks.json`;
  const fetcher = options.fetchImpl ?? fetch;
  const nowFn = options.now ?? (() => new Date());

  let cached: CachedJwks | null = null;

  async function loadJwks(): Promise<CachedJwks> {
    const now = nowFn().getTime();
    if (cached && now - cached.fetchedAt < JWKS_CACHE_TTL_MS) {
      return cached;
    }
    const res = await fetcher(jwksUrl, {
      method: "GET",
      headers: { Accept: "application/json" },
    });
    if (!res.ok) {
      throw new Error(`failed to fetch JWKS: HTTP ${res.status}`);
    }
    const body: unknown = await res.json();
    if (!isJsonWebKeySet(body)) {
      throw new Error("license server returned a malformed JWKS document");
    }
    cached = {
      resolver: createLocalJWKSet(body),
      jwks: body,
      fetchedAt: now,
    };
    return cached;
  }

  return {
    async verify(compactJws: string): Promise<VerifiedJws> {
      if (typeof compactJws !== "string" || compactJws.length === 0) {
        throw new Error("compactJws must be a non-empty string");
      }
      const { resolver, jwks } = await loadJwks();

      const header = decodeProtectedHeader(compactJws);
      if (!header || typeof header.kid !== "string" || header.kid.length === 0) {
        throw new Error("JWS is missing a `kid` protected header");
      }
      const knownKid = jwks.keys.some((k) => k.kid === header.kid);
      if (!knownKid) {
        throw new Error(`JWS kid "${header.kid}" is not in the JWKS`);
      }
      if (header.alg !== "ES256") {
        throw new Error(`unsupported alg "${String(header.alg)}"; expected ES256`);
      }

      const { payload, protectedHeader } = await compactVerify(compactJws, resolver, {
        algorithms: ["ES256"],
      });
      return { payload, kid: String(protectedHeader.kid) };
    },
  };
}

// ---------------------------------------------------------------------------

interface ProtectedHeader {
  alg?: string;
  kid?: string;
  [key: string]: unknown;
}

function decodeProtectedHeader(compactJws: string): ProtectedHeader | null {
  const first = compactJws.split(".")[0];
  if (!first) return null;
  try {
    const padded = first + "===".slice((first.length + 3) % 4);
    const b64 = padded.replace(/-/g, "+").replace(/_/g, "/");
    const json = Buffer.from(b64, "base64").toString("utf-8");
    const parsed: unknown = JSON.parse(json);
    if (typeof parsed === "object" && parsed !== null) {
      return parsed as ProtectedHeader;
    }
    return null;
  } catch {
    return null;
  }
}

function isJsonWebKeySet(value: unknown): value is JSONWebKeySet {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  if (!Array.isArray(v.keys)) return false;
  return v.keys.every((k) => typeof k === "object" && k !== null);
}
