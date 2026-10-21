// The Worker's side of the wire contract (plan D3): the v1 schema, compiled once per isolate, and the
// version rule every request body shares. Handlers parse bodies through `parseBody`, never by casting.
import schema from "../../protocol/v1/schema.json" with { type: "json" };
import { compileSchema, type ValidationError } from "../../protocol/validate.ts";
import { HttpError, UnsupportedVersionError } from "./http.ts";

export const SUPPORTED_VERSIONS: readonly number[] = [1];

// The schema's Error.message is capped at 300 characters, and a path can carry a client-chosen key.
const MAX_PATH_IN_MESSAGE = 120;

const validate = compileSchema(schema);

/**
 * Returns the body typed as T once it validates against `defName`. A body that names another protocol
 * version is refused before validation, so the caller learns what to speak rather than which field is off.
 */
export function parseBody<T>(defName: string, body: unknown): T {
  if (isObject(body) && Object.hasOwn(body, "v") && !SUPPORTED_VERSIONS.includes(body.v as number)) {
    throw new UnsupportedVersionError(SUPPORTED_VERSIONS);
  }
  const result = validate(defName, body);
  if (!result.ok) throw invalidRequest(result.errors[0]);
  return body as T;
}

/** Names where the body went wrong, never what it held: the validator's messages quote only the schema. */
function invalidRequest({ path, message }: ValidationError): HttpError {
  const where = (path || "/").slice(0, MAX_PATH_IN_MESSAGE);
  return new HttpError(400, "invalid_request", `request body is invalid at ${where}: ${message}`);
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
