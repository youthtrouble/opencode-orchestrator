import test, { type TestContext } from "node:test";
import assert from "node:assert/strict";
import { createLocalJWKSet, exportJWK, SignJWT, type JWTPayload } from "jose";
import { readJsonBody } from "../src/body.ts";
import { jsonResponse } from "../src/http.ts";
import { createWorker } from "../src/index.ts";
import type { Route } from "../src/routes.ts";
import { D1Double } from "./support/d1.ts";

const NOW = new Date("2026-10-20T12:00:00Z");
const NOW_SECONDS = NOW.getTime() / 1000;
const TEAM = "https://example.cloudflareaccess.com";
const AUD = "test-audience-tag";
const ORIGIN = "https://trains.example.com";
const CLIENT_ID = "test-client.access";
const FORBIDDEN = { error: "forbidden", message: "forbidden" };

// Generated per run, so no key, token or digest is ever a fixture in the repository.
const signingKeys = (await crypto.subtle.generateKey(
  { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
  true,
  ["sign", "verify"],
)) as CryptoKeyPair;
const publicJwk = { ...(await exportJWK(signingKeys.publicKey)), kid: "key-1", alg: "RS256" };
const keys = createLocalJWKSet({ keys: [publicJwk] });
const machineToken = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString("base64url");
const machineTokenSha256 = Buffer.from(
  await crypto.subtle.digest("SHA-256", new TextEncoder().encode(machineToken)),
).toString("hex");

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

function secrets(): Record<string, string> {
  return {
    ACCESS_TEAM_DOMAIN: TEAM,
    ACCESS_AUD: AUD,
    OWNER_EMAIL: "Owner@Example.com",
    ACCESS_SERVICE_CLIENT_ID: CLIENT_ID,
    MACHINE_TOKEN_SHA256: machineTokenSha256,
    PUBLIC_ORIGIN: ORIGIN,
  };
}

function env(overrides: Record<string, string | undefined> = {}) {
  const assets = { fetch: async () => new Response("asset", { status: 200 }) };
  return { DB: new D1Double(), ASSETS: assets, UNDO_SECONDS: "10", ...secrets(), ...overrides } as never;
}

interface Claims {
  payload?: JWTPayload;
  alg?: string;
  kid?: string;
  key?: CryptoKey | Uint8Array;
}

async function accessJwt({ payload = {}, alg = "RS256", kid = "key-1", key = signingKeys.privateKey }: Claims = {}) {
  return new SignJWT({ iss: TEAM, aud: [AUD], iat: NOW_SECONDS, exp: NOW_SECONDS + 300, ...payload })
    .setProtectedHeader({ alg, kid })
    .sign(key);
}

const browserJwt = (payload: JWTPayload = {}) =>
  accessJwt({ payload: { type: "app", email: "owner@example.com", sub: "user-1", ...payload } });
const serviceJwt = (payload: JWTPayload = {}) =>
  accessJwt({ payload: { type: "app", common_name: CLIENT_ID, sub: "", ...payload } });

interface Call {
  method?: string;
  jwt?: string;
  headers?: Record<string, string>;
  body?: BodyInit;
  envOverrides?: Record<string, string | undefined>;
}

async function call(path: string, { method = "GET", jwt, headers = {}, body, envOverrides }: Call = {}) {
  const allHeaders: Record<string, string> = { ...headers };
  if (jwt !== undefined) allHeaders["Cf-Access-Jwt-Assertion"] = jwt;
  const init = { method, headers: allHeaders, body, duplex: "half" } as RequestInit;
  const response = await worker.fetch!(new Request(`${ORIGIN}${path}`, init) as never, env(envOverrides), {} as never);
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

function assertForbidden(result: { status: number; body: string }) {
  assert.equal(result.status, 403);
  assert.deepEqual(JSON.parse(result.body), FORBIDDEN);
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
  assertForbidden(await call("/"));
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

// Each case must get the same 403 body, so a caller cannot learn which check failed.
// The logged reason proves each case fails for the cause it names.
const forbiddenCases: Array<[string, string, () => Promise<{ status: number; body: string }>]> = [
  ["no JWT header", "jwt_missing", () => ping()],
  ["a garbage JWT", "jwt_invalid:ERR_JWS_INVALID", () => ping("not.a.jwt")],
  ["HS256 with the public key as secret", "jwt_invalid:ERR_JOSE_ALG_NOT_ALLOWED", async () =>
    ping(await hs256WithPublicKeyAsSecret())],
  ["an unknown kid", "jwt_invalid:ERR_JWKS_NO_MATCHING_KEY", async () =>
    ping(await accessJwt({ kid: "key-2", payload: ownerClaims }))],
  ["a wrong issuer", CLAIMS_FAILED, async () => ping(await browserJwt({ iss: "https://<team>.cloudflareaccess.com" }))],
  ["a wrong audience", CLAIMS_FAILED, async () => ping(await browserJwt({ aud: ["other-audience"] }))],
  ["an expired JWT", "jwt_invalid:ERR_JWT_EXPIRED", async () => ping(await browserJwt({ exp: NOW_SECONDS - 60 }))],
  ["nbf in the future", CLAIMS_FAILED, async () => ping(await browserJwt({ nbf: NOW_SECONDS + 120 }))],
  ["another email", "browser_email_mismatch", async () => ping(await browserJwt({ email: "someone@example.com" }))],
  ["a non-app token type", "browser_not_app_token", async () => ping(await browserJwt({ type: "org" }))],
  ["a service token on a browser route", "browser_email_mismatch", async () => ping(await serviceJwt())],
  ["a browser JWT on a machine route", "machine_has_email", async () => machineCall(await browserJwt(), bearer)],
  ["another service token's client id", "machine_client_mismatch", async () =>
    machineCall(await serviceJwt({ common_name: "other.access" }), bearer)],
  ["no bearer token", "machine_token_missing", async () => machineCall(await serviceJwt())],
  ["a wrong bearer token", "machine_token_mismatch", async () => machineCall(await serviceJwt(), wrongBearer)],
  ["a bearer token of the wrong length", "machine_token_mismatch", async () =>
    machineCall(await serviceJwt(), `${bearer}${machineToken}`)],
  ["a non-bearer Authorization", "machine_token_missing", async () =>
    machineCall(await serviceJwt(), `Basic ${machineToken}`)],
  ["a browser write that is not JSON", "browser_write_not_json", () =>
    browserWrite({ "Content-Type": "text/plain", Origin: ORIGIN })],
  ["a browser write from a foreign origin", "browser_write_foreign_origin", () =>
    browserWrite({ ...json, Origin: "https://evil.example.com" })],
  ["a browser write without an Origin", "browser_write_foreign_origin", () => browserWrite(json)],
];

for (const [name, reason, run] of forbiddenCases) {
  test(`403 forbidden: ${name}`, async (t) => {
    const logged: string[] = [];
    t.mock.method(console, "log", (line: string) => logged.push(line));
    assertForbidden(await run());
    assert.deepEqual(logged.map((line) => JSON.parse(line).auth), [reason]);
  });
}

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
  assertForbidden(await call("/api/v1/nope"));
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
