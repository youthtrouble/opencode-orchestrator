// The 4xx answers the browser routes share; the messages name the rule, never the stored text.
import { HttpError } from "../http.ts";

export function notFound(what: string): HttpError {
  return new HttpError(404, "not_found", `${what} not found`);
}

export function invalidRequest(message: string): HttpError {
  return new HttpError(400, "invalid_request", message);
}

export function conflict(message: string): HttpError {
  return new HttpError(409, "conflict", message);
}
