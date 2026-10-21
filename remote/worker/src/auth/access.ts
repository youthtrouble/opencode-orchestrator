// Cloudflare Access JWT verification and identity pinning (plan D4). The Access policy decides who
// may reach the hostname; these checks make sure a loosened policy still cannot expose data.
import { errors, jwtVerify, type JWTPayload, type JWTVerifyGetKey } from "jose";
import type { Config } from "../env.ts";
import { ForbiddenError } from "../http.ts";

export interface AccessVerification {
  issuer: string;
  audience: string;
  /** A jose key-set function: createRemoteJWKSet in production, createLocalJWKSet in tests. */
  keys: JWTVerifyGetKey;
  now: Date;
}

export type Identity = { kind: "browser"; email: string } | { kind: "machine"; clientId: string };

/** Verifies signature (RS256 only), iss, aud, exp and nbf with 30 s of clock skew. */
export async function verifyAccessJwt(token: string, check: AccessVerification): Promise<JWTPayload> {
  try {
    const { payload } = await jwtVerify(token, check.keys, {
      algorithms: ["RS256"],
      issuer: check.issuer,
      audience: check.audience,
      clockTolerance: 30,
      currentDate: check.now,
    });
    return payload;
  } catch (error) {
    // Only token problems are a 403; anything else (a bug, an unreachable JWKS that is not a jose
    // timeout) propagates and surfaces as a 500 with its cause logged.
    if (error instanceof errors.JOSEError) throw new ForbiddenError(`jwt_invalid:${error.code}`);
    throw error;
  }
}

/** A person signed in through Access: an app token whose email is the owner's. */
export function browserIdentity(payload: JWTPayload, cfg: Config): Identity {
  const email = typeof payload.email === "string" ? payload.email.toLowerCase() : null;
  if (payload.type !== "app") throw new ForbiddenError("browser_not_app_token");
  if (email !== cfg.ownerEmail) throw new ForbiddenError("browser_email_mismatch");
  return { kind: "browser", email };
}

/** The Mac's Access service token: `common_name` is the client id and there is no email. */
export function machineIdentity(payload: JWTPayload, cfg: Config): Identity {
  if (payload.email !== undefined) throw new ForbiddenError("machine_has_email");
  if (payload.common_name !== cfg.accessServiceClientId) throw new ForbiddenError("machine_client_mismatch");
  return { kind: "machine", clientId: cfg.accessServiceClientId };
}
