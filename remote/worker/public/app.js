// The Work Trains page: polls the summary, renders it, and sends answers and actions. Rules live in
// model.js; this file owns the DOM, the clock and the network. Server text only ever reaches the
// page through text nodes (`el` children), never as markup.
import * as model from "./model.js";

const POLL_MS = 15_000;
const NOTE_MS = 6_000;

const page = {
  summary: null,
  offsetMs: 0,
  banner: null,
  /** Bumped whenever a write lands, so a summary requested before it cannot overwrite the result. */
  writeGeneration: 0,
  pollTimer: 0,
  showHidden: false,
  drafts: new Map(),
  openReplies: new Set(),
  expandedBodies: new Set(),
  openMenus: new Set(),
  /** Projects whose "Recent events" are open, so the open list survives the next poll's re-render. */
  openEvents: new Set(),
  /** One keyed write per question or project, sending or unconfirmed (model.writeToSend). */
  writes: new Map(),
  /** Undo requests in flight, by answer or action id. */
  undoing: new Set(),
  /** Undo windows already followed by a reload, so a window that reads closed reloads only once. */
  closedWindows: new Set(),
  /** Short messages on a card (such as "Already sent"), each with an expiry time. */
  notes: new Map(),
};

const $ = (id) => document.getElementById(id);

/** An element with attributes and children; string children become text nodes. */
function el(tag, attributes = {}, ...children) {
  const node = document.createElement(tag);
  for (const [name, value] of Object.entries(attributes)) {
    if (value === false || value === null || value === undefined) continue;
    if (name.startsWith("on")) node.addEventListener(name.slice(2), value);
    else if (name in node && typeof value !== "string") node[name] = value;
    else node.setAttribute(name, value === true ? "" : value);
  }
  node.append(...present(children));
  return node;
}

/**
 * Replaces a node's children. The DOM's own replaceChildren would render a null child as the text
 * "null"; this drops absent children the way `el` does.
 */
function setChildren(node, ...children) {
  node.replaceChildren(...present(children));
}

/** Children to render: null, undefined and false stand for "nothing here". */
function present(children) {
  return children.flat().filter((child) => child !== null && child !== undefined && child !== false);
}

// ---- network ----------------------------------------------------------------------------------

/**
 * One API call. Resolves to `{ ok, status, data }` or `{ ok: false, status, opaqueRedirect, error,
 * retryable }`; it never throws, so every caller handles failure explicitly.
 */
async function api(method, path, body) {
  const headers = { Accept: "application/json" };
  // The Worker requires JSON on every write, DELETE included (its CSRF check).
  if (method !== "GET") headers["Content-Type"] = "application/json";
  const init = { method, headers, credentials: "same-origin", redirect: "manual" };
  if (body !== undefined) init.body = JSON.stringify(body);
  let response;
  try {
    response = await fetch(path, init);
  } catch {
    return { ok: false, status: 0, opaqueRedirect: false, error: null, retryable: true };
  }
  if (response.type === "opaqueredirect") {
    return { ok: false, status: 0, opaqueRedirect: true, error: null, retryable: false };
  }
  const data = await jsonOrNull(response);
  if (response.ok) return { ok: true, status: response.status, data };
  return { ok: false, status: response.status, opaqueRedirect: false, error: data, retryable: response.status >= 500 };
}

/** A 204, or a body that is not JSON (an Access error page, say), yields null. */
async function jsonOrNull(response) {
  if (response.status === 204) return null;
  try {
    return await response.json();
  } catch {
    return null;
  }
}

async function loadSummary() {
  const generation = page.writeGeneration;
  const result = await api("GET", "/api/v1/summary");
  if (generation !== page.writeGeneration) return;
  Object.assign(page, model.applySummaryResult(page, result, Date.now()));
  render();
}

/** One poll, and always the next one: a failed or unreadable summary must not end polling. */
async function poll() {
  try {
    await loadSummary();
  } finally {
    schedulePoll();
  }
}

function schedulePoll() {
  clearTimeout(page.pollTimer);
  if (document.visibilityState !== "visible") return;
  page.pollTimer = setTimeout(poll, POLL_MS);
}

/**
 * Sends a write that carries a client_key (see model.writeToSend for the key and retry rules).
 * The stored row goes straight into the page through `merge`, so Undo shows at once even if the
 * next summary is slow or fails. Resolves to the stored row, or null.
 */
async function sendKeyedWrite(writeKey, path, body, merge) {
  const attempt = model.writeToSend(page.writes.get(writeKey), body, () => crypto.randomUUID());
  if (attempt.refused) {
    noteOn(writeKey, attempt.refused);
    render();
    return null;
  }
  page.writes.set(writeKey, attempt.write);
  render();
  const result = await api("POST", path, { ...body, client_key: attempt.write.clientKey });
  const settled = model.settleWrite(attempt.write, result);
  if (settled.write === null) page.writes.delete(writeKey);
  else page.writes.set(writeKey, settled.write);
  if (settled.row !== null) page.summary = merge(page.summary, settled.row);
  if (!result.ok) noteOn(writeKey, model.failureMessage(result));
  await afterWrite();
  return settled.row;
}

async function sendUndo(writeId, noteKey, path) {
  if (page.undoing.has(writeId)) return;
  page.undoing.add(writeId);
  render();
  const result = await api("DELETE", path);
  page.undoing.delete(writeId);
  if (!result.ok) noteOn(noteKey, model.undoFailureMessage(result.status, result.error));
  await afterWrite();
}

async function setHidden(projectKey, hidden) {
  page.openMenus.delete(projectKey);
  const result = await api("POST", `/api/v1/projects/${encodeURIComponent(projectKey)}/visibility`, { hidden });
  if (!result.ok) noteOn(`action:${projectKey}`, model.failureMessage(result));
  await afterWrite();
}

/** Shows the write's result now, then reloads; a summary requested before the write is dropped. */
async function afterWrite() {
  page.writeGeneration += 1;
  render();
  await loadSummary();
}

function noteOn(key, text) {
  page.notes.set(key, { text, until: Date.now() + NOTE_MS });
}

function noteFor(key) {
  const note = page.notes.get(key);
  if (!note) return null;
  if (Date.now() < note.until) return note.text;
  page.notes.delete(key);
  return null;
}

// ---- actions ----------------------------------------------------------------------------------

async function answer(questionId, body) {
  const row = await sendKeyedWrite(
    `answer:${questionId}`,
    `/api/v1/questions/${encodeURIComponent(questionId)}/answer`,
    body,
    (summary, stored) => model.withAnswer(summary, questionId, stored),
  );
  if (row !== null) {
    page.drafts.delete(questionId);
    page.openReplies.delete(questionId);
    render();
  }
}

function sendReply(questionId) {
  const text = (page.drafts.get(questionId) ?? "").trim();
  if (text === "") {
    noteOn(`answer:${questionId}`, "Write a reply first");
    render();
    return;
  }
  answer(questionId, { kind: "typed", text });
}

function projectAction(projectKey, kind) {
  sendKeyedWrite(
    `action:${projectKey}`,
    `/api/v1/projects/${encodeURIComponent(projectKey)}/actions`,
    { kind },
    (summary, stored) => model.withPendingAction(summary, projectKey, stored),
  );
}

function toggle(set, key) {
  if (set.has(key)) set.delete(key);
  else set.add(key);
  render();
}

// ---- rendering --------------------------------------------------------------------------------

function nowOnServer() {
  return model.serverNowMs(Date.now(), page.offsetMs);
}

function render() {
  const focus = rememberFocus();
  renderBanner();
  if (page.summary) {
    renderHeader(page.summary);
    renderNeedsYou(page.summary.projects);
    renderProjects(page.summary.projects);
  }
  restoreFocus(focus);
  tickCountdowns();
}

/** A re-render replaces nodes; focus and the caret move to the node with the same focus key. */
function rememberFocus() {
  const active = document.activeElement;
  if (!active?.dataset?.focus) return null;
  return { key: active.dataset.focus, start: active.selectionStart, end: active.selectionEnd };
}

function restoreFocus(focus) {
  if (!focus) return;
  const target = document.querySelector(`[data-focus="${CSS.escape(focus.key)}"]`);
  if (!target) return;
  target.focus();
  if (typeof focus.start === "number" && "setSelectionRange" in target)
    target.setSelectionRange(focus.start, focus.end);
}

/** The last summary's failure, and a link to PUBLIC_ORIGIN when the page is open elsewhere. */
function renderBanner() {
  const banner = $("banner");
  const home = page.summary?.public_origin;
  const moved = home !== undefined && home !== location.origin;
  banner.hidden = page.banner === null && !moved;
  setChildren(
    banner,
    page.banner && el("p", {}, page.banner),
    moved && el("p", {}, `${model.CANNOT_SEND_HERE}. Open `, el("a", { href: home }, home), "."),
  );
}

function renderHeader(summary) {
  const seen = model.macSeen(summary.machine, nowOnServer());
  setChildren(
    $("mac-seen"),
    `Mac seen ${seen.label}`,
    seen.stale ? el("span", { class: "badge badge-stale" }, "stale") : null,
  );
  setChildren(
    $("budget"),
    ...model.budgetChips(summary.budget).map((chip) => el("li", { class: `chip level-${chip.level}` }, chip.text)),
  );
}

function renderNeedsYou(projects) {
  const stack = model.needsYouStack(projects);
  $("needs-you-count").textContent = stack.length === 0 ? "" : String(stack.length);
  setChildren(
    $("questions"),
    ...(stack.length === 0 ? [el("p", { class: "empty" }, "Nothing needs you.")] : stack.map(questionCard)),
  );
}

function questionCard({ projectName, question }) {
  const headingId = `q-${question.id}`;
  return el(
    "article",
    { class: "card question", "aria-labelledby": headingId },
    el("p", { class: "eyebrow" }, projectName, question.blocking ? el("span", { class: "badge" }, "blocking") : null),
    el("h3", { id: headingId }, question.subject),
    questionBody(question),
    question.recommended === null
      ? null
      : el(
          "div",
          { class: "recommended" },
          el("p", { class: "label" }, "Recommended"),
          el("p", {}, question.recommended),
        ),
    question.answer === null
      ? answerControls(question)
      : writeState(question.answer, "answers", `answer:${question.id}`),
    noteLine(`answer:${question.id}`),
  );
}

function questionBody(question) {
  if (question.body === "") return null;
  const { preview, clamped } = model.clampBody(question.body);
  const expanded = page.expandedBodies.has(question.id);
  return el(
    "div",
    { class: "body" },
    el("p", {}, expanded ? question.body : preview),
    clamped
      ? el(
          "button",
          {
            type: "button",
            class: "link",
            "aria-expanded": String(expanded),
            "data-focus": `more:${question.id}`,
            onclick: () => toggle(page.expandedBodies, question.id),
          },
          expanded ? "Show less" : "Show more",
        )
      : null,
  );
}

function answerControls(question) {
  const write = page.writes.get(`answer:${question.id}`);
  if (write?.phase === "unconfirmed") return unconfirmedWrite(write, () => answer(question.id, write.body));
  const busy = write?.phase === "sending";
  const replying = page.openReplies.has(question.id);
  const replyId = `reply-${question.id}`;
  return el(
    "div",
    { class: "controls" },
    el(
      "div",
      { class: "row" },
      question.recommended === null
        ? null
        : el(
            "button",
            {
              type: "button",
              class: "primary",
              disabled: busy,
              "data-focus": `accept:${question.id}`,
              onclick: () => answer(question.id, { kind: "accept" }),
            },
            busy ? "Sending…" : "Accept",
          ),
      el(
        "button",
        {
          type: "button",
          "aria-expanded": String(replying),
          "aria-controls": replyId,
          "data-focus": `reply:${question.id}`,
          onclick: () => toggle(page.openReplies, question.id),
        },
        question.recommended === null ? "Reply" : "Type something else",
      ),
    ),
    replying ? replyForm(question.id, replyId, busy) : null,
  );
}

function replyForm(questionId, replyId, busy) {
  const counterId = `${replyId}-count`;
  const draft = page.drafts.get(questionId) ?? "";
  const counter = el("p", { class: "counter", id: counterId }, model.replyCounter(draft));
  const textarea = el(
    "textarea",
    {
      id: `${replyId}-text`,
      rows: "4",
      maxlength: String(model.REPLY_MAX_CHARS),
      "aria-describedby": counterId,
      "data-focus": `text:${questionId}`,
      oninput: (event) => {
        page.drafts.set(questionId, event.target.value);
        counter.textContent = model.replyCounter(event.target.value);
      },
    },
    draft,
  );
  return el(
    "div",
    { class: "reply", id: replyId },
    el("label", { for: `${replyId}-text` }, "Your reply"),
    textarea,
    counter,
    el(
      "button",
      {
        type: "button",
        class: "primary",
        disabled: busy,
        "data-focus": `send:${questionId}`,
        onclick: () => sendReply(questionId),
      },
      busy ? "Sending…" : "Send",
    ),
  );
}

/**
 * An answer or action the server holds. Inside the undo window it counts down (on the server's
 * clock) with an Undo; after it, the status the Mac reported.
 */
function writeState(write, collection, noteKey) {
  if (!model.canUndo(write, Date.now(), page.offsetMs))
    return el("p", { class: "status" }, model.writeStatusLabel(write.status, write.detail));
  return el(
    "div",
    { class: "undo" },
    el("p", { class: "countdown", "aria-live": "polite", "data-apply-after": write.apply_after }, ""),
    el(
      "button",
      {
        type: "button",
        disabled: page.undoing.has(write.id),
        "data-focus": `undo:${write.id}`,
        onclick: () => sendUndo(write.id, noteKey, `/api/v1/${collection}/${encodeURIComponent(write.id)}`),
      },
      "Undo",
    ),
  );
}

/** A write the server may or may not hold: only the same request, with the same key, can be retried. */
function unconfirmedWrite(write, retry) {
  return el(
    "div",
    { class: "unconfirmed" },
    el("p", { class: "status" }, model.unconfirmedText(write.body)),
    el("button", { type: "button", class: "primary", onclick: retry }, "Retry"),
  );
}

function noteLine(key) {
  const text = noteFor(key);
  return text === null ? null : el("p", { class: "note", role: "status" }, text);
}

function renderProjects(projects) {
  const keys = new Set(projects.map((project) => project.key));
  for (const key of page.openEvents) if (!keys.has(key)) page.openEvents.delete(key);
  const { shown, hidden } = model.partitionByVisibility(projects);
  setChildren(
    $("project-list"),
    ...(shown.length === 0 ? [el("p", { class: "empty" }, "No projects to show.")] : shown.map(projectCard)),
  );
  const section = $("hidden");
  section.hidden = hidden.length === 0;
  const toggleButton = $("hidden-toggle");
  toggleButton.textContent = `Hidden (${hidden.length})`;
  toggleButton.setAttribute("aria-expanded", String(page.showHidden));
  setChildren($("hidden-list"), ...(page.showHidden ? hidden.map(projectCard) : []));
}

function projectCard(project) {
  const headingId = `p-${project.key}`;
  const now = nowOnServer();
  return el(
    "article",
    { class: "card project", "aria-labelledby": headingId },
    el(
      "div",
      { class: "title-row" },
      el("h3", { id: headingId }, project.name),
      el("span", { class: `pill ${model.stateClass(project.state)}`.trim() }, model.stateLabel(project.state)),
      project.hidden ? unhideButton(project) : overflowMenu(project),
    ),
    project.state_reason ? el("p", { class: "reason" }, project.state_reason) : null,
    el("p", { class: "meta" }, `${project.door ?? "no door"} · ${model.runKindLabel(project.run)}`),
    sliceLine(project),
    progressLine(project.progress, project.state),
    project.ci.length === 0
      ? null
      : el(
          "ul",
          { class: "ci" },
          ...project.ci.map((run) => el("li", { class: `ci-${model.ciTone(run)}` }, model.ciLine(run))),
        ),
    el("p", { class: "meta" }, `updated ${model.relativeTime(project.received_at, now)}`),
    eventList(project, now),
    actionArea(project),
    noteLine(`action:${project.key}`),
  );
}

function sliceLine(project) {
  const parts = [project.slice, project.now].filter((part) => part !== null && part !== "");
  return parts.length === 0 ? null : el("p", { class: "now" }, parts.join(": "));
}

/** The bar takes the project's state colour from its `state-*` class. */
function progressLine(progress, state) {
  const text = model.progressText(progress);
  if (text === null) return null;
  return el(
    "div",
    { class: `progress ${model.stateClass(state)}`.trim() },
    el("progress", {
      max: String(progress.total),
      value: String(Math.min(progress.done, progress.total)),
      "aria-label": `Progress ${text}`,
    }),
    el("span", {}, text),
  );
}

function eventList(project, now) {
  if (project.events.length === 0) return null;
  return el(
    "details",
    {
      class: "events",
      open: page.openEvents.has(project.key),
      ontoggle: (event) => {
        if (event.target.open) page.openEvents.add(project.key);
        else page.openEvents.delete(project.key);
      },
    },
    el("summary", {}, `Recent events (${project.events.length})`),
    el(
      "ol",
      {},
      ...project.events.map((event) =>
        el(
          "li",
          {},
          el("span", { class: "meta" }, `${model.relativeTime(event.at, now)} · ${event.kind} · ${event.from}`),
          el("br"),
          event.subject,
        ),
      ),
    ),
  );
}

function actionArea(project) {
  const pending = project.pending_action;
  if (pending !== null) {
    const open = model.undoSecondsLeft(pending.apply_after, Date.now(), page.offsetMs) > 0;
    return open
      ? writeState({ ...pending, status: "undo_window" }, "actions", `action:${project.key}`)
      : el("p", { class: "status" }, model.pendingActionLabel(pending.kind));
  }
  const write = page.writes.get(`action:${project.key}`);
  if (write?.phase === "unconfirmed") return unconfirmedWrite(write, () => projectAction(project.key, write.body.kind));
  const kind = model.controlActionFor(project.state);
  const busy = write?.phase === "sending";
  return el(
    "div",
    { class: "row" },
    el(
      "button",
      {
        type: "button",
        disabled: busy,
        "data-focus": `act:${project.key}`,
        onclick: () => projectAction(project.key, kind),
      },
      busy ? "Sending…" : kind === "pause" ? "Pause" : "Resume",
    ),
  );
}

function overflowMenu(project) {
  const open = page.openMenus.has(project.key);
  const menuId = `menu-${project.key}`;
  return el(
    "div",
    { class: "menu" },
    el(
      "button",
      {
        type: "button",
        class: "icon",
        "aria-label": `More for ${project.name}`,
        "aria-expanded": String(open),
        "aria-controls": menuId,
        "data-focus": `menu:${project.key}`,
        onclick: () => toggle(page.openMenus, project.key),
      },
      "⋯",
    ),
    el(
      "div",
      { class: "menu-items", id: menuId, hidden: !open },
      el("button", { type: "button", onclick: () => setHidden(project.key, true) }, "Hide"),
    ),
  );
}

function unhideButton(project) {
  return el(
    "button",
    { type: "button", "data-focus": `unhide:${project.key}`, onclick: () => setHidden(project.key, false) },
    "Unhide",
  );
}

/**
 * Updates each countdown once a second without a re-render (which would disturb a screen reader).
 * When a window closes, the summary is reloaded to show what the server did.
 */
function tickCountdowns() {
  let windowClosed = false;
  for (const node of document.querySelectorAll("[data-apply-after]")) {
    const left = model.undoSecondsLeft(node.dataset.applyAfter, Date.now(), page.offsetMs);
    node.textContent = model.countdownText(left);
    if (left === 0 && !page.closedWindows.has(node.dataset.applyAfter)) {
      page.closedWindows.add(node.dataset.applyAfter);
      windowClosed = true;
    }
  }
  if (windowClosed) loadSummary();
}

// ---- start ------------------------------------------------------------------------------------

$("hidden-toggle").addEventListener("click", () => {
  page.showHidden = !page.showHidden;
  render();
});
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible") {
    clearTimeout(page.pollTimer);
    return;
  }
  poll();
});
setInterval(tickCountdowns, 1000);
poll();
