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
