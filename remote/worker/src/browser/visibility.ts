// POST /api/v1/projects/:key/visibility (plan D13): hide or show a project on the page. A Worker-only
// flag; the summary still lists a hidden project, marked, and the page filters it.
import { readJsonBody } from "../body.ts";
import { emptyResponse } from "../http.ts";
import { parseBody } from "../protocol.ts";
import type { HandlerContext } from "../routes.ts";
import { notFound } from "./errors.ts";
import type { VisibilityRequest } from "./wire.ts";

export async function visibilityHandler({ request, db, params }: HandlerContext): Promise<Response> {
  const { hidden } = parseBody<VisibilityRequest>("VisibilityRequest", await readJsonBody(request));
  const result = await db
    .prepare("UPDATE projects SET hidden = ? WHERE key = ?")
    .bind(Number(hidden), params.key)
    .run();
  if (result.meta.changes === 0) throw notFound("project");
  return emptyResponse(204);
}
