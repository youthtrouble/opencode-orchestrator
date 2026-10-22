// Summaries the screenshot script (remote/e2e/screens.ts) serves to the page, one per state worth seeing.
// Every name, URL and id is made up: no real project, owner or machine can reach a screenshot. Times are
// offsets from `nowMs`, so "24 s ago" on the page is exact whenever the shot is taken.
import type { Json } from "../phone.ts";

export type Fixture = (nowMs: number) => Json;

/** A state to shoot: its summary, and optionally a control to click once it renders (a CSS selector). */
export interface ScreenState {
  fixture: Fixture;
  click?: string;
}

const SECOND = 1_000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const UNDO_SECONDS = 10;

const iso = (ms: number): string => new Date(ms).toISOString();

/** A placeholder project key (one repeated hex digit, as the protocol examples use). */
const projectKey = (digit: string): string => digit.repeat(32);

/** A placeholder UUID, mostly zeros, as the protocol examples number theirs. */
const placeholderUuid = (n: number): string => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;

function summary(nowMs: number, { lastPullAgoMs, budget, projects }: { lastPullAgoMs: number; budget: Json; projects: Json[] }): Json {
  return {
    v: 1,
    server_time: iso(nowMs),
    machine: { orch_version: "0.9.0", last_pull_at: iso(nowMs - lastPullAgoMs), last_push_at: iso(nowMs - lastPullAgoMs) },
    budget,
    undo_seconds: UNDO_SECONDS,
    projects,
  };
}

/** A posture whose 5-hour window resets in 2 h 10 min and weekly one in 4 days; the 5-hour window drives it. */
function posture(level: string, fiveHourPct: number, weeklyPct: number, nowMs: number): Json {
  return {
    level,
    five_hour_pct: fiveHourPct,
    weekly_pct: weeklyPct,
    resets_at: iso(nowMs + 2 * HOUR + 10 * MINUTE),
    weekly_resets_at: iso(nowMs + 4 * DAY + 3 * HOUR),
    window: "five_hour",
  };
}

/** OpenAI at its weekly limit, so the weekly window drives it, and drawing on ChatGPT credits. */
function openaiOnCredits(nowMs: number): Json {
  return {
    ...posture("red", 64, 100, nowMs),
    window: "weekly",
    credits: { balance: "195.62", has_credits: true, unlimited: false, plan_limit_reached: true, banked_resets: 2 },
  };
}

function project(nowMs: number, fields: Json): Json {
  return {
    hidden: false,
    state_reason: "",
    door: "opencode",
    run: { kind: "headless", since: iso(nowMs - 3 * HOUR), last_activity_at: iso(nowMs - 24 * SECOND) },
    slice: null,
    now: null,
    progress: null,
    ci: [],
    events: [],
    received_at: iso(nowMs - 24 * SECOND),
    sent_at: iso(nowMs - 25 * SECOND),
    pending_action: null,
    questions: [],
    ...fields,
  };
}

function ciRun(nowMs: number, workflow: string, status: string, conclusion: string | null, n: number): Json {
  return {
    repo: `example/${workflow === "deploy" ? "ledger" : "atlas-api"}`,
    workflow,
    status,
    conclusion,
    url: `https://ci.example.com/runs/${n}`,
    at: iso(nowMs - 4 * MINUTE),
  };
}

function event(nowMs: number, n: number, agoMs: number, kind: string, subject: string): Json {
  return { id: `m-${String(n).padStart(4, "0")}`, at: iso(nowMs - agoMs), kind, from: "conductor", subject };
}

const needsYou: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 24 * SECOND,
    budget: { anthropic: posture("green", 28, 41, nowMs), openai: posture("amber", 74, 62, nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "needs_you",
        state_reason: "1 blocking question",
        door: "claude",
        slice: "AT4",
        now: "Rate limits for the public endpoints",
        progress: { done: 3, total: 7 },
        questions: [
          {
            id: "m-0101",
            at: iso(nowMs - 12 * MINUTE),
            from: "conductor",
            subject: "Ship the rate limiter behind a flag?",
            body: "The slice is green. Shipping behind a flag keeps the old path live until the load test passes.",
            recommended: "Yes, behind a flag, on for 10% of traffic first.",
            blocking: true,
            answer: null,
          },
          {
            id: "m-0102",
            at: iso(nowMs - 40 * MINUTE),
            from: "planner",
            subject: "Keep the v1 error codes?",
            body: "Clients pin them. Renaming would need a deprecation window.",
            recommended: "Keep them.",
            blocking: true,
            answer: {
              id: placeholderUuid(101),
              kind: "accept",
              text: "Keep them.",
              status: "undo_window",
              apply_after: iso(nowMs + (UNDO_SECONDS - 2) * SECOND),
              detail: null,
            },
          },
        ],
      }),
      project(nowMs, {
        key: projectKey("b"),
        name: "field-notes",
        state: "working",
        slice: "FN2",
        now: "Offline sync",
        progress: { done: 5, total: 9 },
        questions: [
          {
            id: "m-0103",
            at: iso(nowMs - 2 * HOUR),
            from: "conductor",
            subject: "Which export format comes first?",
            body: "CSV is quick; Markdown keeps the formatting. Either fits this week.",
            recommended: null,
            blocking: false,
            answer: null,
          },
        ],
      }),
      project(nowMs, { key: projectKey("c"), name: "ledger", state: "idle", run: { kind: "none", since: null, last_activity_at: null } }),
    ],
  });

const calm: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 24 * SECOND,
    budget: { anthropic: posture("green", 28, 41, nowMs), openai: posture("green", 12, 30, nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "working",
        door: "claude",
        slice: "AT5",
        now: "Pagination for the search endpoint",
        progress: { done: 6, total: 8 },
        ci: [ciRun(nowMs, "ci", "completed", "success", 201)],
        events: [
          event(nowMs, 201, 6 * MINUTE, "progress", "AT4 merged locally"),
          event(nowMs, 202, 50 * MINUTE, "review", "AT4 review passed"),
        ],
      }),
      project(nowMs, {
        key: projectKey("b"),
        name: "field-notes",
        state: "done",
        state_reason: "Plan finished",
        run: { kind: "none", since: null, last_activity_at: null },
        progress: { done: 9, total: 9 },
        ci: [ciRun(nowMs, "ci", "completed", "success", 202)],
      }),
      project(nowMs, { key: projectKey("c"), name: "ledger", state: "idle", run: { kind: "none", since: null, last_activity_at: null } }),
    ],
  });

const staleMac: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 10 * MINUTE,
    budget: { anthropic: posture("amber", 81, 55, nowMs), openai: posture("green", 12, 30, nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "working",
        door: "claude",
        slice: "AT5",
        now: "Pagination for the search endpoint",
        progress: { done: 6, total: 8 },
        received_at: iso(nowMs - 10 * MINUTE),
        sent_at: iso(nowMs - 10 * MINUTE),
      }),
      project(nowMs, {
        key: projectKey("b"),
        name: "field-notes",
        state: "working",
        slice: "FN3",
        progress: { done: 1, total: 4 },
        received_at: iso(nowMs - 10 * MINUTE),
        sent_at: iso(nowMs - 10 * MINUTE),
      }),
    ],
  });

const failedCiPaused: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 24 * SECOND,
    budget: { anthropic: posture("green", 28, 41, nowMs), openai: posture("amber", 74, 62, nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "failed",
        state_reason: "Verifier: 3 tests fail on main",
        door: "claude",
        slice: "AT5",
        progress: { done: 6, total: 8 },
        ci: [ciRun(nowMs, "ci", "completed", "failure", 301)],
        events: [event(nowMs, 301, 4 * MINUTE, "incident", "CI failed on main")],
      }),
      project(nowMs, {
        key: projectKey("b"),
        name: "field-notes",
        state: "paused",
        state_reason: "Paused from the page",
        run: { kind: "none", since: null, last_activity_at: null },
        slice: "FN3",
        progress: { done: 1, total: 4 },
      }),
      project(nowMs, {
        key: projectKey("c"),
        name: "ledger",
        state: "working",
        slice: "LG1",
        now: "Monthly statements",
        progress: { done: 2, total: 6 },
        ci: [ciRun(nowMs, "deploy", "in_progress", null, 302)],
      }),
    ],
  });

/** Anthropic has room; OpenAI is at its weekly limit and the Mac reports a credits balance. */
const budgetCredits: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 24 * SECOND,
    budget: { anthropic: posture("green", 28, 41, nowMs), openai: openaiOnCredits(nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "working",
        door: "claude",
        slice: "AT5",
        now: "Pagination for the search endpoint",
        progress: { done: 6, total: 8 },
      }),
    ],
  });

/** One blocking action question: the owner is asked to do something, so the card offers Copy and "I've done it", no Accept. */
const actionQuestion: Fixture = (nowMs) =>
  summary(nowMs, {
    lastPullAgoMs: 24 * SECOND,
    budget: { anthropic: posture("green", 28, 41, nowMs), openai: posture("green", 35, 50, nowMs) },
    projects: [
      project(nowMs, {
        key: projectKey("a"),
        name: "atlas-api",
        state: "needs_you",
        state_reason: "1 blocking question",
        door: "claude",
        slice: "AT6",
        now: "Production deploy workflow",
        progress: { done: 2, total: 5 },
        questions: [
          {
            id: "m-0201",
            at: iso(nowMs - 8 * MINUTE),
            from: "conductor",
            subject: "Create the production environment",
            body: [
              "1. Open the repository's Settings > Environments.",
              "2. Add an environment named work-trains-production.",
              "3. Add the secret CLOUDFLARE_API_TOKEN to it.",
              "4. Run: orch version",
            ].join("\n"),
            ask: "action",
            recommended: null,
            blocking: true,
            answer: null,
          },
        ],
      }),
    ],
  });

/** The states the script shoots, by the name its PNGs carry. */
export const SCREEN_STATES: Readonly<Record<string, ScreenState>> = {
  "needs-you": { fixture: needsYou },
  calm: { fixture: calm },
  "stale-mac": { fixture: staleMac },
  "failed-ci-paused": { fixture: failedCiPaused },
  "budget-credits": { fixture: budgetCredits },
  // The same budget with OpenAI's chip open: both windows' resets and the credits in full.
  "budget-expanded": { fixture: budgetCredits, click: '[data-focus="budget:openai"]' },
  "action-question": { fixture: actionQuestion },
};
