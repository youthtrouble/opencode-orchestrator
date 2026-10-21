// Pure view-model functions for the Work Trains page: no DOM, no clock, no network. Every time is
// passed in, so app.js stays thin and these rules are unit-tested (test/ui-model.test.ts).

export const STALE_AFTER_MS = 3 * 60 * 1000;
export const BODY_MAX_LINES = 6;
export const BODY_MAX_CHARS = 480;
export const REPLY_MAX_CHARS = 4000;

const STATE_LABELS = {
  needs_you: "Needs you",
  working: "Working",
  paused: "Paused",
  failed: "Failed",
  done: "Done",
  idle: "Idle",
};

/** The difference between the server's clock and this device's, from a Summary's `server_time`. */
export function serverOffsetMs(serverTimeIso, deviceNowMs) {
  return Date.parse(serverTimeIso) - deviceNowMs;
}

/** Now on the server's clock: the device clock alone may be minutes off. */
export function serverNowMs(deviceNowMs, offsetMs) {
  return deviceNowMs + offsetMs;
}

/** "just now", "20 s ago", "5 min ago", "3 h ago", "2 d ago". A time ahead of now is "just now". */
export function relativeTime(iso, nowMs) {
  const seconds = Math.floor((nowMs - Date.parse(iso)) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds} s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h ago`;
  return `${Math.floor(hours / 24)} d ago`;
}

/** How long ago the Mac last pulled, and whether that is too long for the page to be trusted. */
export function macSeen(machine, nowMs) {
  const lastPullAt = machine?.last_pull_at ?? null;
  if (lastPullAt === null) return { label: "never", stale: true };
  return { label: relativeTime(lastPullAt, nowMs), stale: nowMs - Date.parse(lastPullAt) > STALE_AFTER_MS };
}

/** One chip per vendor: "anthropic amber 72%", or "openai unknown" when the Mac sent nothing. */
export function budgetChips(budget) {
  return ["anthropic", "openai"].map((vendor) => {
    const posture = budget?.[vendor] ?? null;
    const level = posture?.level ?? "unknown";
    const pct = posture?.five_hour_pct ?? null;
    const text = pct === null ? `${vendor} ${level}` : `${vendor} ${level} ${Math.round(pct)}%`;
    return { vendor, level, text };
  });
}

/**
 * Every open question across all projects, blocking first, then oldest. Questions of hidden
 * projects stay in the stack: hiding a project tidies the page, it must not hide what blocks it.
 */
export function needsYouStack(projects) {
  const cards = projects.flatMap((project) =>
    project.questions.map((question) => ({ projectKey: project.key, projectName: project.name, question })),
  );
  return cards.sort(
    (a, b) =>
      Number(b.question.blocking) - Number(a.question.blocking) ||
      Date.parse(a.question.at) - Date.parse(b.question.at) ||
      a.question.id.localeCompare(b.question.id),
  );
}

/** The server keeps its order (needs_you first); the page only splits shown from hidden. */
export function partitionByVisibility(projects) {
  return {
    shown: projects.filter((project) => !project.hidden),
    hidden: projects.filter((project) => project.hidden),
  };
}

export function stateLabel(state) {
  return STATE_LABELS[state] ?? state;
}

export function runKindLabel(run) {
  return run.kind === "none" ? "no run" : run.kind;
}

/** "3 of 12", or null when the project reports no progress. */
export function progressText(progress) {
  return progress === null ? null : `${progress.done} of ${progress.total}`;
}

export function ciLine(run) {
  const outcome = run.status === "completed" ? (run.conclusion ?? "completed") : run.status.replace("_", " ");
  const name = run.workflow ? `${run.repo} ${run.workflow}` : run.repo;
  return `${name}: ${outcome}`;
}

/** A body longer than six lines (or one very long paragraph) shows a preview until expanded. */
export function clampBody(body, maxLines = BODY_MAX_LINES, maxChars = BODY_MAX_CHARS) {
  const lines = body.split("\n");
  let preview = lines.length > maxLines ? lines.slice(0, maxLines).join("\n") : body;
  if (preview.length > maxChars) preview = preview.slice(0, maxChars);
  const clamped = preview.length < body.length;
  return { preview: clamped ? `${preview.trimEnd()}…` : body, clamped };
}

/** Whole seconds until `applyAfterIso` on the server's clock; 0 once the window has closed. */
export function undoSecondsLeft(applyAfterIso, deviceNowMs, offsetMs) {
  const remainingMs = Date.parse(applyAfterIso) - serverNowMs(deviceNowMs, offsetMs);
  return Math.max(0, Math.ceil(remainingMs / 1000));
}

export function countdownText(secondsLeft) {
  return `Sending in ${secondsLeft} s`;
}

/**
 * What the page says about an answer once its undo window has closed. `undo_window` past its
 * `apply_after` means the server holds it until the Mac's next pull.
 */
export function writeStatusLabel(status, detail) {
  switch (status) {
    case "undo_window":
      return "Waiting for the Mac to collect it";
    case "sent":
      return "Delivered to the Mac";
    case "applied":
      return "The conductor has it";
    case "superseded":
      return detail ? `Already answered on the Mac: ${detail}` : "Already answered on the Mac";
    case "rejected":
      return detail ?? "Rejected";
    default:
      return status;
  }
}

/** A project's queued pause or resume after its undo window: delivered at the conductor's next step. */
export function pendingActionLabel(kind) {
  return kind === "pause" ? "Pausing at the next step" : "Resuming at the next step";
}

/** Resume is offered only where there is something to resume. */
export function controlActionFor(state) {
  return state === "paused" ? "resume" : "pause";
}

export function replyCounter(text) {
  return `${text.length} / ${REPLY_MAX_CHARS}`;
}

/**
 * The banner text for a failed request. `status` is 0 when the network failed; `error` is the
 * parsed Error body, or null when the body was not one. A 403 or an Access login redirect most
 * likely means the Access session expired.
 */
export function failureMessage({ status, opaqueRedirect, error }) {
  const signedOut = opaqueRedirect || status === 403;
  const message = error?.message || fallbackMessage(status, opaqueRedirect);
  return signedOut ? `${message}. Signed out? Reload to sign in` : message;
}

function fallbackMessage(status, opaqueRedirect) {
  if (opaqueRedirect) return "Redirected to sign-in";
  return status === 0 ? "Cannot reach the server" : `Request failed (${status})`;
}

/** The undo of a write that already left the window is a 409 `too_late`. */
export function undoFailureMessage(status, error) {
  return status === 409 && error?.error === "too_late"
    ? "Already sent"
    : failureMessage({ status, opaqueRedirect: false, error });
}

/** The banner when a summary arrives but cannot be used: a truncated body or a page that is not JSON. */
export const UNREADABLE_SUMMARY = "The server sent a summary the page cannot read";

/**
 * The page state after a summary request. A usable summary replaces the old one and resets the
 * clock offset. Anything else, including an unreadable body, keeps what the page already shows and
 * explains why in the banner. Keyed writes are left alone (see settleWrite).
 */
export function applySummaryResult(view, result, deviceNowMs) {
  const summary = result.ok ? usableSummary(result.data) : null;
  if (summary === null) {
    const banner = result.ok ? UNREADABLE_SUMMARY : failureMessage(result);
    return { summary: view.summary, offsetMs: view.offsetMs, banner };
  }
  return { summary, offsetMs: serverOffsetMs(summary.server_time, deviceNowMs), banner: null };
}

function usableSummary(data) {
  const usable =
    typeof data?.server_time === "string" &&
    Number.isFinite(Date.parse(data.server_time)) &&
    Array.isArray(data.projects) &&
    data.projects.every((project) => Array.isArray(project?.questions));
  return usable ? data : null;
}

/**
 * A keyed write (an answer, a pause or a resume) is `{ clientKey, body, phase }`, with phase
 * "sending" or "unconfirmed". Its body is fixed with its key: the server returns the row first
 * stored for a key, so a key must never carry a different body. A different body is refused until
 * an exact retry settles the unconfirmed write; the same body is a retry and reuses the key.
 */
export function writeToSend(current, body, makeClientKey) {
  if (current === undefined) return { write: { clientKey: makeClientKey(), body, phase: "sending" } };
  if (current.phase === "sending") return { refused: "Still sending" };
  if (!sameBody(current.body, body)) return { refused: "Retry the unconfirmed one first" };
  return { write: { ...current, phase: "sending" } };
}

function sameBody(a, b) {
  return a.kind === b.kind && a.text === b.text;
}

/**
 * What a write's response leaves behind. A stored row settles it. A failure the server would
 * repeat (a 4xx) settles it too. A network error, a 5xx or an unreadable success body leaves it
 * unconfirmed: the server may hold it, so only an exact retry with the same key is safe.
 *
 * Only a response settles a write, never a summary. A summary without the row may predate a POST
 * still in flight, and it cannot name the key behind an answer it shows. A retry always ends it:
 * the server returns the row stored for the key, even once the question closed, or a 4xx.
 */
export function settleWrite(write, result) {
  const row = result.ok ? storedRow(result.data) : null;
  if (row !== null) return { write: null, row };
  const unconfirmed = result.ok || result.retryable;
  return { write: unconfirmed ? { ...write, phase: "unconfirmed" } : null, row: null };
}

function storedRow(data) {
  const row = data?.answer ?? data?.action ?? null;
  const usable =
    typeof row?.id === "string" && typeof row.status === "string" && Number.isFinite(Date.parse(row.apply_after));
  return usable ? row : null;
}

/** The summary with a just-stored answer on its question, so Undo shows before the next poll. */
export function withAnswer(summary, questionId, answer) {
  return {
    ...summary,
    projects: summary.projects.map((project) => ({
      ...project,
      questions: project.questions.map((question) => (question.id === questionId ? { ...question, answer } : question)),
    })),
  };
}

/** The summary with a just-stored action pending on its project while its undo window is open. */
export function withPendingAction(summary, projectKey, action) {
  if (action.status !== "undo_window") return summary;
  const pending = { id: action.id, kind: action.kind, apply_after: action.apply_after };
  return {
    ...summary,
    projects: summary.projects.map((project) =>
      project.key === projectKey ? { ...project, pending_action: pending } : project,
    ),
  };
}

/** Undo is offered while the server still holds the write in its window, on the server's clock. */
export function canUndo(write, deviceNowMs, offsetMs) {
  return write.status === "undo_window" && undoSecondsLeft(write.apply_after, deviceNowMs, offsetMs) > 0;
}

const WRITE_NAMES = { accept: "Accept", typed: "Your reply", pause: "Pause", resume: "Resume" };

/** The line beside Retry on an unconfirmed write. */
export function unconfirmedText(body) {
  return `${WRITE_NAMES[body.kind] ?? body.kind} may not have reached the server`;
}
