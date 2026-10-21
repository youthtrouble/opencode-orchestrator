// TypeScript views of the browser payloads in remote/protocol/v1/schema.json, like machine/wire.ts: the
// schema stays the single definition, and `parseBody` checks a body against it before these are trusted.
import type { Outcome } from "../machine/wire.ts";

export type AnswerKind = "accept" | "typed";
export type ActionKind = "pause" | "resume";

export interface AnswerRequest {
  kind: AnswerKind;
  text?: string;
  client_key: string;
}

export interface ActionRequest {
  kind: ActionKind;
  client_key: string;
}

export interface VisibilityRequest {
  hidden: boolean;
}

/** Where an answer or action stands, as the page shows it. */
export type LiveStatus = "undo_window" | "sent" | Outcome;
