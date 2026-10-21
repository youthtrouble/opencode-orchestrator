// The Worker's single error boundary: handlers throw HttpError, toResponse turns it into JSON.

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
 * Every authentication or authorisation failure. The response never says which check failed;
 * `reason` is a code for the Worker's own log only.
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

export function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}

export function toResponse(error: unknown, path: string): Response {
  if (error instanceof ForbiddenError) {
    console.log(JSON.stringify({ auth: error.reason, path }));
  }
  if (error instanceof NotConfiguredError) {
    console.log(JSON.stringify({ not_configured: error.secretNames, path }));
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
