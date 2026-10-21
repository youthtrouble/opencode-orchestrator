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

/**
 * One chip per vendor: "anthropic amber 72%", or "openai unknown" when the Mac sent nothing.
 * OpenAI credits, when reported, get a chip of their own: they are a separate fact from the
 * window's level, and banked resets are window resets, not money.
 */
export function budgetChips(budget) {
  const chips = ["anthropic", "openai"].map((vendor) => {
    const posture = budget?.[vendor] ?? null;
    const level = posture?.level ?? "unknown";
    const pct = posture?.five_hour_pct ?? null;
    const text = pct === null ? `${vendor} ${level}` : `${vendor} ${level} ${Math.round(pct)}%`;
    return { vendor, level, text };
  });
  const credits = budget?.openai?.credits;
  if (credits) chips.push({ vendor: "openai", level: "credits", text: `openai ${creditsText(credits)}` });
  return chips;
}

/** "credits 215.68 left; plan limit reached; 3 banked resets (window resets, not money)", as orch prints it. */
function creditsText(credits) {
  const parts = [];
  if (credits.has_credits !== null) {
    if (credits.unlimited) parts.push("credits unlimited");
    else if (!credits.has_credits) parts.push("no credits");
    else parts.push(credits.balance === null ? "credits available" : `credits ${credits.balance} left`);
  }
  if (credits.plan_limit_reached) parts.push("plan limit reached");
  const banked = credits.banked_resets;
  if (banked > 0) parts.push(`${banked} banked reset${banked === 1 ? "" : "s"} (window resets, not money)`);
  return parts.join("; ");
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

/** A known state's tone class; an unknown state gets none. */
export function stateClass(state) {
  return Object.hasOwn(STATE_LABELS, state) ? `state-${state}` : "";
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

const CI_FAILED = new Set(["failure", "timed_out", "startup_failure"]);
const CI_RUNNING = new Set(["in_progress", "queued", "waiting", "pending", "requested"]);

/** The colour of a CI run's mark: red only for a real failure; a cancelled or skipped run stays neutral. */
export function ciTone(run) {
  if (run.status === "completed") {
    if (run.conclusion === "success") return "ok";
    return CI_FAILED.has(run.conclusion) ? "fail" : "neutral";
  }
  return CI_RUNNING.has(run.status) ? "running" : "neutral";
}

/** A body longer than six lines (or one very long paragraph) shows a preview until expanded. */
export function clampBody(body, maxLines = BODY_MAX_LINES, maxChars = BODY_MAX_CHARS) {
  const lines = body.split("\n");
  let preview = lines.length > maxLines ? lines.slice(0, maxLines).join("\n") : body;
  if (preview.length > maxChars) preview = preview.slice(0, maxChars);
  const clamped = preview.length < body.length;
  return { preview: clamped ? `${preview.trimEnd()}…` : body, clamped };
}

/**
 * Whether a question asks the owner to do something rather than decide. Its body is the steps, and it
 * offers no Accept: one tap must never tell the conductor that work it asked for is done.
 */
export function isAction(question) {
  return question.ask === "action";
}

/** The typed answer "I've done it" sends: a claim the conductor checks against reality, never proof. */
export const OWNER_REPORTS_DONE = Object.freeze({ kind: "typed", text: "Owner reports done (unverified)." });

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
 * The text for a failed request. `status` is 0 when the network failed; `error` is the parsed Error
 * body, or null. Only an Access login redirect, a 401 or an expired token asks for a sign-in; any
 * other 403 says what is wrong by the Worker's reason code.
 */
export function failureMessage({ status, opaqueRedirect, error }) {
  if (opaqueRedirect || status === 401) return SIGNED_OUT;
  if (error?.error === "forbidden") return forbiddenMessage(error.reason);
  return error?.message || (status === 0 ? "Cannot reach the server" : `Request failed (${status})`);
}

const SIGNED_OUT = "Signed out. Reload to sign in";

/** The page is open on an address other than PUBLIC_ORIGIN, whose writes the Worker refuses. */
export const CANNOT_SEND_HERE = "This address can't send answers";

const FORBIDDEN_MESSAGES = {
  browser_write_foreign_origin: `${CANNOT_SEND_HERE}. Open the address in the banner`,
  browser_email_mismatch: "Signed in as an address that isn't an owner",
  "jwt_invalid:ERR_JWT_EXPIRED": SIGNED_OUT,
  jwt_missing: "This address is not behind Cloudflare Access",
};

function forbiddenMessage(reason) {
  if (!reason) return "Not allowed";
  if (Object.hasOwn(FORBIDDEN_MESSAGES, reason)) return FORBIDDEN_MESSAGES[reason];
  if (reason.startsWith("jwt_invalid:")) return "Access sign-in doesn't match ACCESS_AUD or ACCESS_TEAM_DOMAIN";
  return `Not allowed (${reason})`;
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
