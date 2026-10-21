// POST /api/v1/machine/ack (plan D6.4): records how the Mac applied each pulled answer or action. The
// first ack wins: a repeat, an unknown id, or an id never pulled changes nothing and so does not count
// as settled. Requiring delivery keeps an unpulled answer, maybe still inside its undo window, unsettled.
import { readJsonBody } from "../body.ts";
import { jsonResponse } from "../http.ts";
import { parseBody } from "../protocol.ts";
import type { HandlerContext } from "../routes.ts";
import type { AckRequest } from "./wire.ts";

const TABLE_FOR = { answer: "answers", action: "actions" } as const;

export async function ackHandler({ request, db, now }: HandlerContext): Promise<Response> {
  const ack = parseBody<AckRequest>("AckRequest", await readJsonBody(request));
  const settledAt = now.toISOString();
  const statements = ack.results.map(({ id, type, outcome, detail }) =>
    db
      .prepare(
        `UPDATE ${TABLE_FOR[type]} SET outcome = ?, outcome_detail = ?, settled_at = ?
         WHERE id = ? AND delivered_at IS NOT NULL AND outcome IS NULL`,
      )
      .bind(outcome, detail, settledAt, id),
  );
  const results = await db.batch(statements);
  const settled = results.reduce((sum, result) => sum + result.meta.changes, 0);
  return jsonResponse(200, { v: 1, settled });
}
