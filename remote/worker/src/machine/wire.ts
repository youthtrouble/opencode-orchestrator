// TypeScript views of the machine payloads in remote/protocol/v1/schema.json. They name only the fields
// the Worker reads; the schema stays the single definition, and `parseBody` checks a body against it
// before any of these types is trusted.

export type ProjectState = "working" | "needs_you" | "paused" | "failed" | "done" | "idle";

export interface Question {
  id: string;
  at: string;
  from: string;
  subject: string;
  body: string;
  recommended: string | null;
  blocking: boolean;
  /** Absent for a decision; "action" for steps the owner must do. */
  ask?: QuestionAsk;
}

export type QuestionAsk = "decision" | "action";

export interface ProjectSnapshot {
  key: string;
  name: string;
  state: ProjectState;
  questions: Question[];
  snapshot_hash: string;
  [field: string]: unknown;
}

export interface PushRequest {
  v: 1;
  machine: { id: string; orch_version: string; sent_at: string };
  budget: Record<string, unknown> | null;
  projects: ProjectSnapshot[];
}

export type PushResult = "accepted" | "unchanged" | "deferred";

export interface ProjectPushResult {
  key: string;
  result: PushResult;
  retry_after_seconds: number | null;
}

export type Outcome = "applied" | "superseded" | "rejected";

export interface AckRequest {
  v: 1;
  results: Array<{ id: string; type: "answer" | "action"; outcome: Outcome; detail: string }>;
}
