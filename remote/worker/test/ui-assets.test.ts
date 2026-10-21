// The page must run under the Worker's CSP: same-origin files only, no inline script or style, and
// server text never parsed as markup.
import test from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";

const publicDir = new URL("../public/", import.meta.url);
const read = (name: string) => readFileSync(new URL(name, publicDir), "utf8");

test("index.html loads only same-origin scripts and styles, with nothing inline", () => {
  const html = read("index.html");
  const references = [...html.matchAll(/<(?:script|link)\b[^>]*\b(?:src|href)="([^"]*)"/g)].map((match) => match[1]);
  assert.deepEqual(references.sort(), ["app.css", "app.js", "icon.svg", "manifest.webmanifest"]);
  for (const [, body] of html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)) assert.equal(body.trim(), "");
  assert.doesNotMatch(html, /\sstyle\s*=/i);
  assert.doesNotMatch(html, /<style\b/i);
  assert.doesNotMatch(html, /\son[a-z]+\s*=/i, "no inline event handlers");
});

test("no script sets innerHTML or other markup sinks", () => {
  for (const name of ["app.js", "model.js"]) {
    assert.doesNotMatch(read(name), /innerHTML|outerHTML|insertAdjacentHTML|document\.write/, name);
  }
});

test("the page's files stay under 40 KB together", () => {
  const total = readdirSync(publicDir).reduce((sum, name) => sum + statSync(new URL(name, publicDir)).size, 0);
  assert.ok(total < 40 * 1024, `public/ is ${total} bytes`);
});
