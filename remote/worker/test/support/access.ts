// The identities a test call presents: a per-run Access signing key, the JWTs it signs, the machine
// token, and the secrets a configured Worker sees. Shared by every test that goes through the gate.
import { createLocalJWKSet, exportJWK, SignJWT, type JWTPayload } from "jose";
import { D1Double } from "./d1.ts";

export const TEAM = "https://example.cloudflareaccess.com";
export const AUD = "test-audience-tag";
export const ORIGIN = "https://trains.example.com";
export const CLIENT_ID = "test-client.access";

// Generated per run, so no key, token or digest is ever a fixture in the repository.
export const signingKeys = (await crypto.subtle.generateKey(
  { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
  true,
  ["sign", "verify"],
)) as CryptoKeyPair;
const publicJwk = { ...(await exportJWK(signingKeys.publicKey)), kid: "key-1", alg: "RS256" };
export const keys = createLocalJWKSet({ keys: [publicJwk] });
export const machineToken = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");
const machineTokenSha256 = Buffer.from(
  await crypto.subtle.digest("SHA-256", new TextEncoder().encode(machineToken)),
).toString("hex");

export function secrets(): Record<string, string> {
  return {
    ACCESS_TEAM_DOMAIN: TEAM,
    ACCESS_AUD: AUD,
    OWNER_EMAIL: "Owner@Example.com",
    ACCESS_SERVICE_CLIENT_ID: CLIENT_ID,
    MACHINE_TOKEN_SHA256: machineTokenSha256,
    PUBLIC_ORIGIN: ORIGIN,
  };
}

export function testEnv(overrides: Record<string, string | undefined> = {}, db = new D1Double()) {
  const assets = { fetch: async () => new Response("asset", { status: 200 }) };
  return { DB: db, ASSETS: assets, UNDO_SECONDS: "10", ...secrets(), ...overrides } as never;
}

export interface Claims {
  payload?: JWTPayload;
  alg?: string;
  kid?: string;
  key?: CryptoKey | Uint8Array;
}

/** Token makers for a caller whose clock reads `now`; each token is valid for five minutes from it. */
export function tokensAt(now: Date) {
  const issuedAt = Math.floor(now.getTime() / 1000);
  const accessJwt = async ({ payload = {}, alg = "RS256", kid = "key-1", key = signingKeys.privateKey }: Claims = {}) =>
    new SignJWT({ iss: TEAM, aud: [AUD], iat: issuedAt, exp: issuedAt + 300, ...payload })
      .setProtectedHeader({ alg, kid })
      .sign(key);
  return {
    accessJwt,
    browserJwt: (payload: JWTPayload = {}) =>
      accessJwt({ payload: { type: "app", email: "owner@example.com", sub: "user-1", ...payload } }),
    serviceJwt: (payload: JWTPayload = {}) =>
      accessJwt({ payload: { type: "app", common_name: CLIENT_ID, sub: "", ...payload } }),
  };
}
