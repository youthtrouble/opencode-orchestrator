// The machine token is the second factor on machine routes, beside the Access service token. It is
// 32 random bytes, so a plain SHA-256 (no slow KDF) is enough; only its digest is stored as a secret.
import { ForbiddenError } from "../http.ts";

const BEARER = /^Bearer ([^\s]+)$/;

export async function verifyMachineToken(authorization: string | null, expectedSha256: Uint8Array): Promise<void> {
  const token = authorization === null ? null : BEARER.exec(authorization)?.[1];
  if (!token) throw new ForbiddenError("machine_token_missing");
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token)));
  if (!constantTimeEqual(digest, expectedSha256)) throw new ForbiddenError("machine_token_mismatch");
}

/** Compares two digests without an early exit, so timing reveals nothing about the match. */
function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a[i] ^ b[i];
  return difference === 0;
}
