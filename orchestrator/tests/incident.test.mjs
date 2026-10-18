import test from "node:test";
import assert from "node:assert/strict";
import os from "node:os";
import { redact, fingerprint, summarize } from "../lib/incident.mjs";

test("redact strips home paths, emails, tokens, uuids and session ids", () => {
  const s = redact(`${os.homedir()}/x and /Users/someone/y me@example.com ses_abc123XYZ 123e4567-e89b-12d3-a456-426614174000`);
  assert.equal(s.includes(os.homedir()), false);
  assert.match(s, /~\/x and ~\/y <email> <session> <uuid>/);
});
test("fingerprint is stable across numbers and case", () => {
  assert.equal(fingerprint("bridge", "crash", "exit 2 after 31s"), fingerprint("bridge", "crash", "EXIT 7 after 90s"));
  assert.notEqual(fingerprint("bridge", "crash", "exit 2"), fingerprint("worker", "crash", "exit 2"));
});
test("summarize groups by fingerprint, most frequent first", () => {
  const e = (fp, at) => ({ fp, at, component: "x", kind: "bug", summary: "s", project: "p" });
  const s = summarize([e("a", "1"), e("b", "2"), e("a", "3")]);
  assert.deepEqual(s.map((r) => [r.fp, r.count]), [["a", 2], ["b", 1]]);
});
