// The page as rendered: public/app.js against a fake DOM (see support/page.ts).
import test from "node:test";
import assert from "node:assert/strict";
import { FakeNode, PAGE_ORIGIN, pageShowing } from "./support/page.ts";

function summaryWithMacSeen(secondsAgo: number) {
  const now = Date.now();
  return {
    v: 1,
    server_time: new Date(now).toISOString(),
    machine: { orch_version: "0.9.0", last_pull_at: new Date(now - secondsAgo * 1000).toISOString() },
    budget: null,
    undo_seconds: 10,
    projects: [],
  };
}

const staleBadges = (header: FakeNode) =>
  header.children.filter((child) => child instanceof FakeNode && child.attributes.get("class") === "badge badge-stale");

test("a fresh Mac reads 'Mac seen 24 s ago' with no stale badge and no stray text", async () => {
  const header = (await pageShowing(summaryWithMacSeen(24))).getElementById("mac-seen");
  assert.equal(header.textContent, "Mac seen 24 s ago");
  assert.doesNotMatch(header.textContent, /null|undefined/);
  assert.equal(staleBadges(header).length, 0);
});

test("a stale Mac reads 'Mac seen 10 min ago' followed by one stale badge", async () => {
  const header = (await pageShowing(summaryWithMacSeen(600))).getElementById("mac-seen");
  assert.equal(header.textContent, "Mac seen 10 min agostale");
  assert.doesNotMatch(header.textContent, /null|undefined/);
  assert.equal(staleBadges(header).length, 1);
});

const OLD_ORIGIN = "https://work-trains.example.workers.dev";

test("a page open on an address other than PUBLIC_ORIGIN links to PUBLIC_ORIGIN in the banner", async () => {
  const summary = { ...summaryWithMacSeen(24), public_origin: PAGE_ORIGIN };
  const banner = (await pageShowing(summary, OLD_ORIGIN)).getElementById("banner");
  assert.equal(banner.hidden, false);
  assert.equal(banner.textContent, `This address can't send answers. Open ${PAGE_ORIGIN}.`);
  const link = banner.children[0] instanceof FakeNode ? banner.children[0].children[1] : null;
  assert.ok(link instanceof FakeNode && link.tagName === "a");
  assert.equal(link.attributes.get("href"), PAGE_ORIGIN);
});

test("a page open on PUBLIC_ORIGIN, or given none, shows no banner", async () => {
  for (const summary of [{ ...summaryWithMacSeen(24), public_origin: PAGE_ORIGIN }, summaryWithMacSeen(24)]) {
    const banner = (await pageShowing(summary)).getElementById("banner");
    assert.equal(banner.hidden, true);
    assert.equal(banner.textContent, "");
  }
});

const RECOMMENDED = "Yes, behind a flag.";

function questionFor(id: string, recommended: string | null) {
  const at = new Date(Date.now() - 60_000).toISOString();
  return { id, at, blocking: false, from: "conductor", subject: id, body: "", recommended, answer: null };
}

function ciRun(status: string, conclusion: string | null) {
  return { repo: "example/app", workflow: "ci", status, conclusion, url: null, at: new Date().toISOString() };
}

/** One working project with two questions (one with a recommendation), two CI runs and one event. */
function summaryWithProject() {
  const now = new Date().toISOString();
  return {
    ...summaryWithMacSeen(24),
    projects: [
      {
        key: "p1",
        name: "atlas-api",
        hidden: false,
        state: "working",
        state_reason: "",
        door: "opencode",
        run: { kind: "headless", since: now, last_activity_at: now },
        slice: null,
        now: null,
        progress: { done: 6, total: 8 },
        ci: [ciRun("completed", "success"), ciRun("completed", "failure")],
        events: [{ id: "m-1", at: now, kind: "note", from: "conductor", subject: "Slice merged" }],
        received_at: now,
        sent_at: now,
        pending_action: null,
        questions: [questionFor("with-rec", RECOMMENDED), questionFor("without-rec", null)],
      },
    ],
  };
}

const byTag = (tag: string) => (node: FakeNode) => node.tagName === tag;
const classOf = (node: FakeNode) => node.attributes.get("class");

test("the Needs-you count is a plain number", async () => {
  const document = await pageShowing(summaryWithProject());
  assert.equal(document.getElementById("needs-you-count").textContent, "2");
});

test("the second answer button reads 'Type something else' beside a recommendation, else 'Reply'", async () => {
  const questions = (await pageShowing(summaryWithProject())).getElementById("questions");
  const labels = questions.findAll(byTag("button")).map((button) => button.textContent);
  assert.deepEqual(labels, ["Accept", "Type something else", "Reply"]);
});

test("each CI run carries its tone class and the progress bar its project's state", async () => {
  const projects = (await pageShowing(summaryWithProject())).getElementById("project-list");
  assert.deepEqual(
    projects
      .findAll(byTag("li"))
      .filter((li) => classOf(li)?.startsWith("ci-"))
      .map(classOf),
    ["ci-ok", "ci-fail"],
  );
  assert.deepEqual(projects.findAll((node) => classOf(node)?.startsWith("progress") ?? false).map(classOf), [
    "progress state-working",
  ]);
});

test("Recent events opened by the owner stay open across the next re-render", async () => {
  let reopened: FakeNode | undefined;
  await pageShowing(summaryWithProject(), PAGE_ORIGIN, (document) => {
    const [events] = document.getElementById("project-list").findAll(byTag("details"));
    assert.equal(events.open, false, "closed until opened");
    events.open = true;
    events.dispatch("toggle");
    document.getElementById("hidden-toggle").dispatch("click"); // any re-render will do
    [reopened] = document.getElementById("project-list").findAll(byTag("details"));
  });
  assert.ok(reopened);
  assert.equal(reopened.open, true);
});
