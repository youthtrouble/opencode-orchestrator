// The page as rendered: public/app.js against a fake DOM (see support/page.ts).
import test from "node:test";
import assert from "node:assert/strict";
import { FakeNode, pageShowing } from "./support/page.ts";

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
