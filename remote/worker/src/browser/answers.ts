// POST /api/v1/questions/:id/answer and DELETE /api/v1/answers/:id (plan D6.1-2): answer an open question
// once, by accepting its stored recommendation or by typing a reply, and undo that inside the window.
import { readJsonBody } from "../body.ts";
import { jsonResponse } from "../http.ts";
import { parseBody } from "../protocol.ts";
import type { CountedDb } from "../queries.ts";
import type { HandlerContext } from "../routes.ts";
import { conflict, invalidRequest, notFound } from "./errors.ts";
import { applyAfter, cancelWithinWindow, insertOnce, liveStatus, tooLate, type Lifecycle } from "./lifecycle.ts";
import type { AnswerKind, AnswerRequest } from "./wire.ts";

/** Counted in UTF-16 units, as the page counts them; the schema's cap counts code points. */
export const MAX_TYPED_LENGTH = 4000;
// C0 and C1 control characters and DEL, except tab (U+0009) and newline (U+000A).
const CONTROL_EXCEPT_TAB_AND_NEWLINE = /[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g;

interface StoredQuestion {
  id: string;
  recommended: string | null;
  closed_at: string | null;
}

export interface StoredAnswer extends Lifecycle {
  id: string;
  question_id: string;
  kind: AnswerKind;
  text: string;
}

const ANSWER_COLUMNS =
  "id, question_id, kind, text, apply_after, cancelled_at, delivered_at, outcome, outcome_detail";

export async function createAnswerHandler(ctx: HandlerContext): Promise<Response> {
  const { db, env, now, params } = ctx;
  const request = parseBody<AnswerRequest>("AnswerRequest", await readJsonBody(ctx.request));
  // A retried tap is answered from its stored row before any check of the question's current state:
  // the question may have closed since the first tap landed, and the retry must still see its answer.
  const earlier = await answerByClientKey(db, request.client_key);
  if (earlier !== null) return jsonResponse(200, answerResponse(sameTap(earlier, params.id, request), now));

  const question = await db
    .prepare("SELECT id, recommended, closed_at FROM questions WHERE id = ?")
    .bind(params.id)
    .first<StoredQuestion>();
  if (question === null) throw notFound("question");
  if (question.closed_at !== null) throw conflict("this question is closed");
  const text = answerText(request, question);

  const insert = db
    .prepare(
      `INSERT INTO answers (id, question_id, kind, text, client_key, created_at, apply_after)
       VALUES (?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(client_key) DO NOTHING`,
    )
    .bind(crypto.randomUUID(), question.id, request.kind, text, request.client_key, now.toISOString(),
      applyAfter(now, env));
  const created = await insertOnce(insert, () => liveAnswerTo(db, question.id), (live) => alreadyAnswered(live, now));
  // Either this insert created the row or a concurrent request with the same client_key did.
  const answer = await answerByClientKey(db, request.client_key);
  if (answer === null) throw new Error("answer missing after insert");
  if (created) return jsonResponse(201, answerResponse(answer, now));
  return jsonResponse(200, answerResponse(sameTap(answer, question.id, request), now));
}

export async function undoAnswerHandler({ db, now, params }: HandlerContext): Promise<Response> {
  const cancelled = await cancelWithinWindow(db, "answers", params.id, now);
  const answer = await db
    .prepare(`SELECT ${ANSWER_COLUMNS} FROM answers WHERE id = ?`)
    .bind(params.id)
    .first<StoredAnswer>();
  if (answer === null) throw notFound("answer");
  if (!cancelled) throw tooLate(answer);
  return jsonResponse(200, answerResponse(answer, now));
}

/** Accept sends the recommendation the Worker stored, never text from the client (plan D6.1). */
function answerText(request: AnswerRequest, question: StoredQuestion): string {
  if (request.kind === "accept") {
    if (question.recommended === null) throw invalidRequest("this question has no recommendation; reply instead");
    return question.recommended;
  }
  if (request.text === undefined) throw invalidRequest("a typed answer needs text");
  const text = cleanTyped(request.text);
  if (text.length === 0 || text.length > MAX_TYPED_LENGTH) {
    throw invalidRequest(`a typed answer is 1 to ${MAX_TYPED_LENGTH} characters`);
  }
  return text;
}

async function answerByClientKey(db: CountedDb, clientKey: string): Promise<StoredAnswer | null> {
  return db
    .prepare(`SELECT ${ANSWER_COLUMNS} FROM answers WHERE client_key = ?`)
    .bind(clientKey)
    .first<StoredAnswer>();
}

/**
 * The stored answer, when the request repeats the tap that made it: same question, same kind and, when
 * typed, the same text once cleaned. Anything else reuses a client_key for a different answer.
 */
function sameTap(stored: StoredAnswer, questionId: string, request: AnswerRequest): StoredAnswer {
  if (stored.question_id !== questionId) throw conflict("this client_key was used for another question");
  const sameText = request.kind === "accept" || stored.text === cleanTyped(request.text ?? "");
  if (stored.kind !== request.kind || !sameText) throw conflict("this client_key was used for a different answer");
  return stored;
}

function cleanTyped(text: string): string {
  return text.replace(CONTROL_EXCEPT_TAB_AND_NEWLINE, "").trim();
}

async function liveAnswerTo(db: CountedDb, questionId: string): Promise<StoredAnswer | null> {
  return db
    .prepare(`SELECT ${ANSWER_COLUMNS} FROM answers WHERE question_id = ? AND cancelled_at IS NULL`)
    .bind(questionId)
    .first<StoredAnswer>();
}

/** Names the existing answer by id and status only: its text is the owner's, not an error message's. */
function alreadyAnswered(live: StoredAnswer, now: Date) {
  return conflict(`this question already has an answer (${live.id}, ${liveStatus(live, now).status})`);
}

export function answerView(answer: StoredAnswer, now: Date) {
  const { status, detail } = liveStatus(answer, now);
  return { id: answer.id, kind: answer.kind, text: answer.text, status, apply_after: answer.apply_after, detail };
}

function answerResponse(answer: StoredAnswer, now: Date) {
  return { v: 1, answer: answerView(answer, now) };
}
