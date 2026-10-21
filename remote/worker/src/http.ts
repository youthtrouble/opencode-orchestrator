// The Worker's single error boundary: handlers throw HttpError, toResponse turns it into JSON.
import type { RouteClass } from "./routes.ts";

export class HttpError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "HttpError";
    this.status = status;
    this.code = code;
  }
}

/**
 * Every authentication or authorisation failure. `reason` is the code of the check that failed (such as
 * `browser_write_foreign_origin`), a fixed code, never an email, a token or anything else from the
 * request. The Worker's log always carries it. A browser-class 403 also sends it, so the page can say
 * what to do; a machine-class 403 keeps the same opaque body for every failure, because the Mac has no
 * page to explain it on and a caller past a missing or misconfigured Access must not learn which layer
 * (JWT claims, client id, bearer token) refused it.
 */
export class ForbiddenError extends HttpError {
  readonly reason: string;

  constructor(reason: string) {
    super(403, "forbidden", "forbidden");
    this.name = "ForbiddenError";
    this.reason = reason;
  }
}

/**
 * An Access JWT from another Access team than the Worker's: Access and ACCESS_TEAM_DOMAIN disagree, so
 * every sign-in would fail. Unlike ForbiddenError it says so, and names the team domain the Worker
 * expects, so the misconfiguration can be found from the phone or `orch remote status`. A team domain
 * is public (Access's login redirect shows it); the response names nothing else.
 */
export class WrongIssuerError extends HttpError {
  readonly expectedIssuer: string;

  constructor(expectedIssuer: string) {
    super(403, "wrong_issuer", `Access token from another team domain; this Worker expects ${expectedIssuer}`);
    this.name = "WrongIssuerError";
    this.expectedIssuer = expectedIssuer;
  }
}

/**
 * A deploy whose secrets are missing or malformed. The gate checks config before the JWT, so the
 * caller may be anyone: the response names nothing, and the bad secret names go to the log only.
 */
export class NotConfiguredError extends HttpError {
  readonly secretNames: readonly string[];

  constructor(secretNames: readonly string[]) {
    super(503, "not_configured", "not configured");
    this.name = "NotConfiguredError";
    this.secretNames = secretNames;
  }
}

/** A body whose `v` this Worker does not speak; the response lists the versions it does. */
export class UnsupportedVersionError extends HttpError {
  readonly supported: readonly number[];

  constructor(supported: readonly number[]) {
    super(400, "unsupported_version", `protocol version not supported; supported: ${supported.join(", ")}`);
    this.name = "UnsupportedVersionError";
    this.supported = supported;
  }
}

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}

/** A success with nothing to say, such as 204; the same no-store rule as every JSON response. */
export function emptyResponse(status: number): Response {
  return new Response(null, { status, headers: { "Cache-Control": "no-store" } });
}

export function toResponse(error: unknown, path: string, routeClass: RouteClass): Response {
  if (error instanceof ForbiddenError) {
    console.log(JSON.stringify({ auth: error.reason, path }));
  }
  if (error instanceof NotConfiguredError) {
    console.log(JSON.stringify({ not_configured: error.secretNames, path }));
  }
  if (error instanceof WrongIssuerError) {
    console.log(JSON.stringify({ auth: "jwt_wrong_issuer", path }));
    const body = { error: error.code, message: error.message, expected_issuer: error.expectedIssuer };
    return jsonResponse(error.status, body);
  }
  if (error instanceof ForbiddenError && routeClass === "browser") {
    return jsonResponse(error.status, { error: error.code, message: error.message, reason: error.reason });
  }
  if (error instanceof UnsupportedVersionError) {
    return jsonResponse(error.status, { error: error.code, message: error.message, supported: error.supported });
  }
  if (error instanceof HttpError) {
    return jsonResponse(error.status, { error: error.code, message: error.message });
  }
  console.log(JSON.stringify({ error: "internal", path, detail: describe(error) }));
  return jsonResponse(500, { error: "internal", message: "internal error" });
}

function describe(error: unknown): string {
  return error instanceof Error ? `${error.name}: ${error.message}` : String(error);
}
