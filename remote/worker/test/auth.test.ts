import test, { type TestContext } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { compileSchema } from "../../protocol/validate.ts";
import { readJsonBody } from "../src/body.ts";
import { jsonResponse } from "../src/http.ts";
import { createWorker } from "../src/index.ts";
import type { Route } from "../src/routes.ts";
import { keys, machineToken, ORIGIN, signingKeys, TEAM, testEnv, tokensAt } from "./support/access.ts";

const NOW = new Date("2026-10-20T12:00:00Z");
const NOW_SECONDS = NOW.getTime() / 1000;
const validate = compileSchema(
  JSON.parse(readFileSync(new URL("../../protocol/v1/schema.json", import.meta.url), "utf8")),
);

const ok = (name: string) => async () => jsonResponse(200, { route: name });
const testRoutes: Route[] = [
  { method: "GET", pattern: "/api/v1/ping", class: "browser", handler: ok("browser-ping") },
  {
    method: "POST",
    pattern: "/api/v1/echo",
    class: "browser",
    handler: async ({ request }) => jsonResponse(200, { echo: await readJsonBody(request) }),
  },
  { method: "POST", pattern: "/api/v1/machine/ping", class: "machine", handler: ok("machine-ping") },
];
const worker = createWorker({ keysFor: () => keys, routes: testRoutes, now: () => NOW });

const { accessJwt, browserJwt, serviceJwt } = tokensAt(NOW);

interface Call {
  method?: string;
  jwt?: string;
  headers?: Record<string, string>;
  body?: BodyInit;
  envOverrides?: Record<string, string | undefined>;
  /** The origin the request is addressed to; PUBLIC_ORIGIN unless a test says otherwise. */
  origin?: string;
}

async function call(path: string, { method = "GET", jwt, headers = {}, body, envOverrides, origin = ORIGIN }: Call = {}) {
  const allHeaders: Record<string, string> = { ...headers };
  if (jwt !== undefined) allHeaders["Cf-Access-Jwt-Assertion"] = jwt;
  const init = { method, headers: allHeaders, body, duplex: "half" } as RequestInit;
  const response = await worker.fetch!(new Request(`${origin}${path}`, init) as never, testEnv(envOverrides), {} as never);
  return { status: response.status, body: await response.text(), headers: response.headers };
}

const machineCall = async (jwt: string, authorization?: string) =>
  call("/api/v1/machine/ping", {
    method: "POST",
    jwt,
    headers: authorization === undefined ? {} : { Authorization: authorization },
  });

const browserWrite = async (headers: Record<string, string>, body: BodyInit = "{}") =>
  call("/api/v1/echo", { method: "POST", jwt: await browserJwt(), headers, body });

/** A browser-class 403, whose body names the check that failed by its code, and nothing else. */
function assertForbidden(result: { status: number; body: string }, reason: string) {
  assert.equal(result.status, 403);
  const body = JSON.parse(result.body);
  assert.deepEqual(body, { error: "forbidden", message: "forbidden", reason });
  assert.deepEqual(validate("Error", body), { ok: true });
}

/** A machine-class 403: the same body for every failure, so a caller cannot learn which check failed. */
function assertOpaqueForbidden(result: { status: number; body: string }) {
  assert.equal(result.status, 403);
  assert.deepEqual(JSON.parse(result.body), { error: "forbidden", message: "forbidden" });
}

test("a valid owner JWT reaches a browser route", async () => {
  const result = await call("/api/v1/ping", { jwt: await browserJwt() });
  assert.equal(result.status, 200);
  assert.deepEqual(JSON.parse(result.body), { route: "browser-ping" });
  assert.equal(result.headers.get("Cache-Control"), "no-store");
});

test("the owner email is compared case-insensitively", async () => {
  const result = await call("/api/v1/ping", { jwt: await browserJwt({ email: "OWNER@example.COM" }) });
  assert.equal(result.status, 200);
});

test("every email OWNER_EMAIL lists is an owner, normalised; another email on the same domain is not", async (t) => {
  const envOverrides = { OWNER_EMAIL: " Owner@Example.com ,CO-OWNER@example.com, owner@example.com" };
  for (const email of ["owner@example.com", "co-owner@example.com", "Co-Owner@Example.COM"]) {
    const result = await call("/api/v1/ping", { jwt: await browserJwt({ email }), envOverrides });
    assert.equal(result.status, 200, email);
  }
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  assertForbidden(
    await call("/api/v1/ping", { jwt: await browserJwt({ email: "other@example.com" }), envOverrides }),
    "browser_email_mismatch",
  );
  assert.deepEqual(logged.map((line) => JSON.parse(line).auth), ["browser_email_mismatch"]);
});

test("a service token JWT plus the machine token reaches a machine route", async () => {
  const result = await machineCall(await serviceJwt(), `Bearer ${machineToken}`);
  assert.equal(result.status, 200);
  assert.deepEqual(JSON.parse(result.body), { route: "machine-ping" });
});

test("a browser GET of a page goes to static assets after auth", async (t) => {
  const result = await call("/", { jwt: await browserJwt() });
  assert.equal(result.status, 200);
  assert.equal(result.body, "asset");
  t.mock.method(console, "log", () => {});
  assertForbidden(await call("/"), "jwt_missing");
});

const OLD_HOST = "https://work-trains.example.workers.dev";

test("a page or asset GET on another hostname redirects to the same path on PUBLIC_ORIGIN, after auth", async (t) => {
  for (const path of ["/", "/app.js", "/?project=a&x=1"]) {
    const result = await call(path, { jwt: await browserJwt(), origin: OLD_HOST });
    assert.equal(result.status, 302, path);
    assert.equal(result.headers.get("Location"), `${ORIGIN}${path}`);
    assert.equal(result.headers.get("Cache-Control"), "no-store");
  }
  const head = await call("/", { method: "HEAD", jwt: await browserJwt(), origin: OLD_HOST });
  assert.equal(head.status, 302);
  t.mock.method(console, "log", () => {});
  assertForbidden(await call("/", { origin: OLD_HOST }), "jwt_missing");
});

test("API routes answer on every hostname, so an old tab and the Mac keep working", async () => {
  const browserRoute = await call("/api/v1/ping", { jwt: await browserJwt(), origin: OLD_HOST });
  assert.equal(browserRoute.status, 200);
  const machineRoute = await call("/api/v1/machine/ping", {
    method: "POST",
    jwt: await serviceJwt(),
    headers: { Authorization: bearer },
    origin: OLD_HOST,
  });
  assert.equal(machineRoute.status, 200);
});

const CLAIMS_FAILED = "jwt_invalid:ERR_JWT_CLAIM_VALIDATION_FAILED";
const ownerClaims = { type: "app", email: "owner@example.com" };
const json = { "Content-Type": "application/json" };
const bearer = `Bearer ${machineToken}`;
const wrongBearer = `Bearer ${machineToken.slice(0, -1)}${machineToken.endsWith("A") ? "B" : "A"}`;
const ping = (jwt?: string) => call("/api/v1/ping", { jwt });

async function hs256WithPublicKeyAsSecret() {
  const spki = new Uint8Array(await crypto.subtle.exportKey("spki", signingKeys.publicKey));
  return accessJwt({ alg: "HS256", key: spki, payload: ownerClaims });
}

type ForbiddenCase = [name: string, reason: string, run: () => Promise<{ status: number; body: string }>];

// On a browser route the 403 body and the log line name the check that failed by the same code, which
// proves the case fails for the cause it names; the page turns the code into what to do about it.
const browserForbiddenCases: ForbiddenCase[] = [
  ["no JWT header", "jwt_missing", () => ping()],
  ["a garbage JWT", "jwt_invalid:ERR_JWS_INVALID", () => ping("not.a.jwt")],
  ["HS256 with the public key as secret", "jwt_invalid:ERR_JOSE_ALG_NOT_ALLOWED", async () =>
    ping(await hs256WithPublicKeyAsSecret())],
  ["an unknown kid", "jwt_invalid:ERR_JWKS_NO_MATCHING_KEY", async () =>
    ping(await accessJwt({ kid: "key-2", payload: ownerClaims }))],
  ["a wrong issuer", CLAIMS_FAILED, async () => ping(await browserJwt({ iss: "https://<team>.cloudflareaccess.com" }))],
  // Only an issuer shaped like a team domain is diagnosed as wrong_issuer; any other stays opaque.
  ["an issuer that is not a team domain", CLAIMS_FAILED, async () =>
    ping(await browserJwt({ iss: "https://example.com" }))],
  ["a wrong audience", CLAIMS_FAILED, async () => ping(await browserJwt({ aud: ["other-audience"] }))],
  ["an expired JWT", "jwt_invalid:ERR_JWT_EXPIRED", async () => ping(await browserJwt({ exp: NOW_SECONDS - 60 }))],
  ["nbf in the future", CLAIMS_FAILED, async () => ping(await browserJwt({ nbf: NOW_SECONDS + 120 }))],
  ["another email", "browser_email_mismatch", async () => ping(await browserJwt({ email: "someone@example.com" }))],
  ["a non-app token type", "browser_not_app_token", async () => ping(await browserJwt({ type: "org" }))],
  ["a service token on a browser route", "browser_email_mismatch", async () => ping(await serviceJwt())],
  ["a browser write that is not JSON", "browser_write_not_json", () =>
    browserWrite({ "Content-Type": "text/plain", Origin: ORIGIN })],
  ["a browser write from a foreign origin", "browser_write_foreign_origin", () =>
    browserWrite({ ...json, Origin: "https://evil.example.com" })],
  ["a browser write without an Origin", "browser_write_foreign_origin", () => browserWrite(json)],
];

// On a machine route only the log line names the check: the body is the same for every case.
const machineForbiddenCases: ForbiddenCase[] = [
  ["no JWT on a machine route", "jwt_missing", () => call("/api/v1/machine/ping", { method: "POST" })],
  ["an expired service JWT", "jwt_invalid:ERR_JWT_EXPIRED", async () =>
    machineCall(await serviceJwt({ exp: NOW_SECONDS - 60 }), bearer)],
  ["a service JWT for another audience", CLAIMS_FAILED, async () =>
    machineCall(await serviceJwt({ aud: ["other-audience"] }), bearer)],
  ["a browser JWT on a machine route", "machine_has_email", async () => machineCall(await browserJwt(), bearer)],
  ["another service token's client id", "machine_client_mismatch", async () =>
    machineCall(await serviceJwt({ common_name: "other.access" }), bearer)],
  ["no bearer token", "machine_token_missing", async () => machineCall(await serviceJwt())],
  ["a wrong bearer token", "machine_token_mismatch", async () => machineCall(await serviceJwt(), wrongBearer)],
  ["a bearer token of the wrong length", "machine_token_mismatch", async () =>
    machineCall(await serviceJwt(), `${bearer}${machineToken}`)],
  ["a non-bearer Authorization", "machine_token_missing", async () =>
    machineCall(await serviceJwt(), `Basic ${machineToken}`)],
];

for (const [routeClass, cases, assertBody] of [
  ["browser", browserForbiddenCases, assertForbidden],
  ["machine", machineForbiddenCases, assertOpaqueForbidden],
] as const) {
  for (const [name, reason, run] of cases) {
    test(`403 forbidden on a ${routeClass} route: ${name}`, async (t) => {
      const logged: string[] = [];
      t.mock.method(console, "log", (line: string) => logged.push(line));
      assertBody(await run(), reason);
      assert.deepEqual(logged.map((line) => JSON.parse(line).auth), [reason]);
    });
  }
}

const ANOTHER_TEAM = "https://other-example.cloudflareaccess.com";

// Access of another team signs with that team's keys, which this Worker does not know: the realistic
// misconfiguration, which must still say which team the Worker expects.
const wrongTeamCases: Array<[string, () => Promise<{ status: number; body: string }>]> = [
  ["a browser JWT", async () => ping(await browserJwt({ iss: ANOTHER_TEAM }))],
  ["a browser JWT signed with another team's key", async () =>
    ping(await accessJwt({ kid: "key-2", payload: { ...ownerClaims, iss: ANOTHER_TEAM } }))],
  ["a service JWT", async () => machineCall(await serviceJwt({ iss: ANOTHER_TEAM }), bearer)],
];

for (const [name, run] of wrongTeamCases) {
  test(`403 wrong_issuer names the expected team domain: ${name} from another team`, async (t) => {
    const logged: string[] = [];
    t.mock.method(console, "log", (line: string) => logged.push(line));
    const result = await run();
    assert.equal(result.status, 403);
    const body = JSON.parse(result.body);
    assert.deepEqual(body, {
      error: "wrong_issuer",
      message: `Access token from another team domain; this Worker expects ${TEAM}`,
      expected_issuer: TEAM,
    });
    assert.deepEqual(validate("Error", body), { ok: true });
    assert.deepEqual(logged.map((line) => JSON.parse(line).auth), ["jwt_wrong_issuer"]);
  });
}

test("a correctly signed JWT without exp is refused before any handler or asset", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const reached: string[] = [];
  const reach = (name: string) => async () => (reached.push(name), new Response(name));
  const spyWorker = createWorker({
    keysFor: () => keys,
    routes: [
      { method: "GET", pattern: "/api/v1/ping", class: "browser", handler: reach("browser") },
      { method: "POST", pattern: "/api/v1/machine/ping", class: "machine", handler: reach("machine") },
    ],
    now: () => NOW,
  });
  const env = { ...(testEnv() as object), ASSETS: { fetch: reach("assets") } } as never;
  const noExp = { exp: undefined };
  const requests = [
    new Request(`${ORIGIN}/api/v1/ping`, { headers: { "Cf-Access-Jwt-Assertion": await browserJwt(noExp) } }),
    new Request(`${ORIGIN}/api/v1/machine/ping`, {
      method: "POST",
      headers: { "Cf-Access-Jwt-Assertion": await serviceJwt(noExp), Authorization: bearer },
    }),
    new Request(`${ORIGIN}/`, { headers: { "Cf-Access-Jwt-Assertion": await browserJwt(noExp) } }),
  ];
  for (const request of requests) {
    const response = await spyWorker.fetch!(request as never, env, {} as never);
    const result = { status: response.status, body: await response.text() };
    if (new URL(request.url).pathname.startsWith("/api/v1/machine/")) assertOpaqueForbidden(result);
    else assertForbidden(result, CLAIMS_FAILED);
  }
  assert.deepEqual(reached, []);
  assert.deepEqual(logged.map((line) => JSON.parse(line).auth), [CLAIMS_FAILED, CLAIMS_FAILED, CLAIMS_FAILED]);
});

test("auth failure logs carry a reason code and the path, never the JWT or bearer token", async (t) => {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const jwt = await serviceJwt();
  await machineCall(jwt, wrongBearer);
  await call("/api/v1/ping", { jwt });
  await machineCall(await browserJwt({ exp: NOW_SECONDS - 60 }), `Bearer ${machineToken}`);
  assert.deepEqual(logged.map((line) => JSON.parse(line)), [
    { auth: "machine_token_mismatch", path: "/api/v1/machine/ping" },
    { auth: "browser_email_mismatch", path: "/api/v1/ping" },
    { auth: "jwt_invalid:ERR_JWT_EXPIRED", path: "/api/v1/machine/ping" },
  ]);
  for (const line of logged) {
    assert.ok(!line.includes(machineToken.slice(0, 16)), "a log line contains the machine token");
    assert.ok(!line.includes(jwt.split(".")[1]), "a log line contains the JWT");
  }
});

test("an unknown path is 403 without a JWT and 404 with one", async (t) => {
  t.mock.method(console, "log", () => {});
  assertForbidden(await call("/api/v1/nope"), "jwt_missing");
  const result = await call("/api/v1/nope", { jwt: await browserJwt() });
  assert.equal(result.status, 404);
  assert.deepEqual(JSON.parse(result.body), { error: "not_found", message: "not found" });
});

test("a route is only reachable with an identity of its class", async () => {
  // POST /api/v1/echo exists for browsers; the same method on an unknown machine path is 404.
  const result = await call("/api/v1/machine/echo", {
    method: "POST",
    jwt: await serviceJwt(),
    headers: { Authorization: `Bearer ${machineToken}` },
  });
  assert.equal(result.status, 404);
});

const NOT_CONFIGURED = { error: "not_configured", message: "not configured" };

/** Calls a route with the given secret overrides; returns the response and the secret names logged. */
async function callMisconfigured(
  t: TestContext,
  envOverrides: Record<string, string | undefined>,
  path = "/api/v1/ping",
) {
  const logged: string[] = [];
  t.mock.method(console, "log", (line: string) => logged.push(line));
  const result = await call(path, { jwt: await browserJwt(), envOverrides });
  t.mock.restoreAll();
  return { result, loggedNames: logged.map((line) => JSON.parse(line).not_configured) };
}

test("missing or malformed secrets fail closed with a 503 that names nothing; the log names the secrets", async (t) => {
  const overrides = { OWNER_EMAIL: undefined, ACCESS_TEAM_DOMAIN: "https://example.com" };
  for (const path of ["/", "/api/v1/ping", "/api/v1/machine/ping"]) {
    const { result, loggedNames } = await callMisconfigured(t, overrides, path);
    assert.equal(result.status, 503);
    assert.deepEqual(JSON.parse(result.body), NOT_CONFIGURED);
    assert.deepEqual(loggedNames, [["ACCESS_TEAM_DOMAIN", "OWNER_EMAIL"]]);
  }
  // Names only: a malformed value never reaches the log.
  const { loggedNames } = await callMisconfigured(t, { MACHINE_TOKEN_SHA256: "ABC" });
  assert.deepEqual(loggedNames, [["MACHINE_TOKEN_SHA256"]]);
});

test("an OWNER_EMAIL with any malformed or empty entry fails closed", async (t) => {
  const notAllowlists = [
    ",",
    "owner@example.com,",
    "owner@example.com,,co-owner@example.com",
    "owner@example.com,co-owner",
    "owner@example.com;co-owner@example.com",
    "owner @example.com",
  ];
  for (const value of notAllowlists) {
    const { result, loggedNames } = await callMisconfigured(t, { OWNER_EMAIL: value });
    assert.equal(result.status, 503, value);
    assert.deepEqual(loggedNames, [["OWNER_EMAIL"]], value);
  }
});

test("PUBLIC_ORIGIN must be exactly an https origin", async (t) => {
  const notOrigins = [
    `${ORIGIN}/`,
    `${ORIGIN}?x`,
    `${ORIGIN}#x`,
    `${ORIGIN}/app`,
    `https://user:pass${"@"}trains.example.com`, // built so the public-repo guard does not read an email
    "http://trains.example.com",
    "https://Trains.Example.com",
    "trains.example.com",
  ];
  for (const value of notOrigins) {
    const { result, loggedNames } = await callMisconfigured(t, { PUBLIC_ORIGIN: value });
    assert.equal(result.status, 503, value);
    assert.deepEqual(loggedNames, [["PUBLIC_ORIGIN"]], value);
  }
  const withPort = await callMisconfigured(t, { PUBLIC_ORIGIN: "https://trains.example.com:8443" });
  assert.equal(withPort.result.status, 200);
});

test("NOTIFY_FROM and NOTIFY_TO are both set as email addresses, or neither", async (t) => {
  const from = "trains@example.com";
  const to = "owner@example.com";
  const cases: Array<[Record<string, string>, string[]]> = [
    [{ NOTIFY_FROM: from }, ["NOTIFY_TO"]],
    [{ NOTIFY_TO: to }, ["NOTIFY_FROM"]],
    [{ NOTIFY_FROM: "trains", NOTIFY_TO: to }, ["NOTIFY_FROM"]],
    [{ NOTIFY_FROM: from, NOTIFY_TO: "owner@@example.com" }, ["NOTIFY_TO"]],
    [{ NOTIFY_FROM: "trains @example.com", NOTIFY_TO: "@example.com" }, ["NOTIFY_FROM", "NOTIFY_TO"]],
  ];
  for (const [overrides, names] of cases) {
    const { result, loggedNames } = await callMisconfigured(t, overrides);
    assert.equal(result.status, 503, JSON.stringify(overrides));
    assert.deepEqual(loggedNames, [names], JSON.stringify(overrides));
  }
  for (const overrides of [{}, { NOTIFY_FROM: from, NOTIFY_TO: to }, { NOTIFY_FROM: "", NOTIFY_TO: "" }]) {
    const { result } = await callMisconfigured(t, overrides);
    assert.equal(result.status, 200, JSON.stringify(overrides));
  }
});

test("a browser write with JSON from the public origin passes", async () => {
  const result = await browserWrite({ "Content-Type": "application/json; charset=utf-8", Origin: ORIGIN }, '{"a":1}');
  assert.equal(result.status, 200);
  assert.deepEqual(JSON.parse(result.body), { echo: { a: 1 } });
});

test("a body over 256 KiB is 413, by declared length and by actual length", async () => {
  const headers = { "Content-Type": "application/json", Origin: ORIGIN };
  const oversized = `"${"x".repeat(256 * 1024)}"`;
  // A small body declared as oversized: only the gate's Content-Length check can refuse it.
  const declared = await browserWrite({ ...headers, "Content-Length": String(256 * 1024 + 1) }, "{}");
  assert.equal(declared.status, 413);
  assert.equal(JSON.parse(declared.body).error, "payload_too_large");

  const undeclared = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(oversized));
      controller.close();
    },
  });
  const streamed = await browserWrite(headers, undeclared);
  assert.equal(streamed.status, 413);
  assert.equal(JSON.parse(streamed.body).error, "payload_too_large");
});

test("a Content-Length that is not a byte count is 400 bad_request", async () => {
  const headers = { "Content-Type": "application/json", Origin: ORIGIN };
  for (const declared of ["abc", "-1", "1e9", "12.5", "0x400000"]) {
    const result = await browserWrite({ ...headers, "Content-Length": declared }, "{}");
    assert.equal(result.status, 400, declared);
    assert.deepEqual(JSON.parse(result.body), { error: "bad_request", message: "Content-Length is not a byte count" });
  }
});

test("a body that is not JSON is 400 invalid_json", async () => {
  const result = await browserWrite({ "Content-Type": "application/json", Origin: ORIGIN }, "{");
  assert.equal(result.status, 400);
  assert.equal(JSON.parse(result.body).error, "invalid_json");
});
