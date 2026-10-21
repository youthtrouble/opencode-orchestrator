// Every response carries the security headers: static assets, API answers and errors alike.
import test from "node:test";
import assert from "node:assert/strict";
import { jsonResponse } from "../src/http.ts";
import { SECURITY_HEADERS } from "../src/http/headers.ts";
import { createWorker } from "../src/index.ts";
import { keys, ORIGIN, testEnv, tokensAt } from "./support/access.ts";
import { clientKey, KEY, question, snapshot, trainsApp } from "./support/trains.ts";

const NOW = new Date("2026-10-20T12:00:00Z");
const worker = createWorker({
  keysFor: () => keys,
  routes: [{ method: "GET", pattern: "/api/v1/ping", class: "browser", handler: async () => jsonResponse(200, {}) }],
  now: () => NOW,
});
const { browserJwt } = tokensAt(NOW);

async function get(path: string, jwt?: string) {
  const headers: Record<string, string> = jwt === undefined ? {} : { "Cf-Access-Jwt-Assertion": jwt };
  return worker.fetch!(new Request(`${ORIGIN}${path}`, { headers }) as never, testEnv(), {} as never);
}

function assertSecured(response: { headers: Headers }) {
  for (const [name, value] of Object.entries(SECURITY_HEADERS)) assert.equal(response.headers.get(name), value, name);
}

test("the security headers carry the page's policy", () => {
  assert.equal(
    SECURITY_HEADERS["Content-Security-Policy"],
    "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
      "manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  );
  assert.equal(SECURITY_HEADERS["Referrer-Policy"], "no-referrer");
  assert.equal(SECURITY_HEADERS["X-Content-Type-Options"], "nosniff");
  assert.equal(SECURITY_HEADERS["Permissions-Policy"], "camera=(), microphone=(), geolocation=()");
});

test("a static asset response carries the security headers", async () => {
  const response = await get("/", await browserJwt());
  assert.equal(await response.text(), "asset");
  assertSecured(response);
});

test("an API response carries the security headers and stays uncached", async () => {
  const response = await get("/api/v1/ping", await browserJwt());
  assert.equal(response.status, 200);
  assertSecured(response);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
});

test("an error response carries the security headers", async () => {
  const response = await get("/api/v1/ping");
  assert.equal(response.status, 403);
  assertSecured(response);
});

test("the browser API's JSON answers, its 204 and its undo carry the security headers", async () => {
  const app = trainsApp();
  assert.equal((await app.push([snapshot({ questions: [question("q-1")] })])).status, 200);

  const summary = await app.summary();
  const answer = await app.answer("q-1", { kind: "accept", client_key: clientKey(1) });
  const undo = await app.undoAnswer(answer.body!.answer.id);
  const hide = await app.setHidden(KEY, true);

  assert.deepEqual([summary.status, answer.status, undo.status, hide.status], [200, 201, 200, 204]);
  for (const reply of [summary, answer, undo, hide]) assertSecured(reply);
});
