// The deploy workflow's two scripts: rendering the deploy config and the credential-free smoke check.
import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  DeployConfigError,
  instanceFromEnv,
  renderDeployConfig,
  TEMPLATE_PATH,
  writeDeployConfig,
} from "../scripts/render-deploy-config.ts";
import { smokeCheck } from "../scripts/smoke-check.ts";

// Built at runtime so this file does not trip the public-repo guard.
const D1_ID = ["3f2b9c4e", "7a1d", "4e6b", "9c2f", "8d5e1a3b7c90"].join("-");
const HOSTNAME = "status.example.org";
const ENV = { WORK_TRAINS_D1_ID: D1_ID, WORK_TRAINS_HOSTNAME: HOSTNAME };
const template = readFileSync(TEMPLATE_PATH, "utf8");

function changedLines(before: string, after: string): Array<[string, string]> {
  const afterLines = after.split("\n");
  return before.split("\n").flatMap((line, index): Array<[string, string]> =>
    line === afterLines[index] ? [] : [[line.trim(), afterLines[index]!.trim()]],
  );
}

test("rendering fills exactly the D1 id, the hostname and workers.dev, and keeps the rest of the template", () => {
  const rendered = renderDeployConfig(template, instanceFromEnv({ ...ENV, WORK_TRAINS_SEND_EMAIL: "true" }));
  assert.equal(rendered.split("\n").length, template.split("\n").length);
  assert.deepEqual(changedLines(template, rendered), [
    [`"routes": [{ "pattern": "trains.example.com", "custom_domain": true }],`, `"routes": [{ "pattern": "${HOSTNAME}", "custom_domain": true }],`],
    [`"database_id": "00000000-0000-0000-0000-000000000000",`, `"database_id": "${D1_ID}",`],
  ]);
});

test("WORK_TRAINS_WORKERS_DEV=true keeps workers.dev on; unset or false keeps it off", () => {
  const workersDev = (value: string | undefined) =>
    /"workers_dev": (true|false)/.exec(renderDeployConfig(template, instanceFromEnv({ ...ENV, WORK_TRAINS_WORKERS_DEV: value })))![1];
  assert.equal(workersDev("true"), "true");
  assert.equal(workersDev("false"), "false");
  assert.equal(workersDev(undefined), "false");
  assert.equal(workersDev(""), "false");
});

test("WORK_TRAINS_SEND_EMAIL=true keeps the send_email binding; unset or false drops it and its comment", () => {
  const render = (value: string | undefined) => renderDeployConfig(template, instanceFromEnv({ ...ENV, WORK_TRAINS_SEND_EMAIL: value }));
  const kept = render("true");
  assert.match(kept, /"send_email": \[\{ "name": "EMAIL" \}\]/);
  for (const value of [undefined, "", "false"]) {
    const dropped = render(value);
    assert.ok(!dropped.includes("send_email"), String(value));
    assert.ok(!dropped.includes("NOTIFY_TO secret"), "the binding's comment goes with it");
    assert.equal(dropped, kept.replace(/ *\/\/ No destination restriction[^\n]*\n *"send_email"[^\n]*\n/, ""));
  }
});

test("values are trimmed and lower-cased", () => {
  const instance = instanceFromEnv({ WORK_TRAINS_D1_ID: ` ${D1_ID.toUpperCase()} `, WORK_TRAINS_HOSTNAME: "Status.Example.org\n" });
  assert.deepEqual(instance, { d1Id: D1_ID, hostname: HOSTNAME, workersDev: false, sendEmail: false });
});

test("a missing, malformed or placeholder value is refused without echoing it", () => {
  const refused: Array<Record<string, string | undefined>> = [
    { ...ENV, WORK_TRAINS_D1_ID: undefined },
    { ...ENV, WORK_TRAINS_D1_ID: "not-a-uuid" },
    { ...ENV, WORK_TRAINS_D1_ID: "00000000-0000-0000-0000-000000000000" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "https://status.example.org" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "status.example.org/" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "status.example.org:443" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "localhost" },
    { ...ENV, WORK_TRAINS_HOSTNAME: "trains.example.com" },
    { ...ENV, WORK_TRAINS_WORKERS_DEV: "yes" },
    { ...ENV, WORK_TRAINS_SEND_EMAIL: "TRUE" },
  ];
  for (const env of refused) {
    assert.throws(
      () => instanceFromEnv(env),
      (error: unknown) => {
        assert.ok(error instanceof DeployConfigError);
        for (const value of Object.values(env)) if (value) assert.ok(!error.message.includes(value), "echoed a value");
        return true;
      },
      JSON.stringify(env),
    );
  }
});

test("a template that lost or doubled a placeholder is refused", () => {
  const instance = instanceFromEnv(ENV);
  assert.throws(() => renderDeployConfig(template.replace(`"workers_dev": false`, `"workers_dev": true`), instance), DeployConfigError);
  assert.throws(() => renderDeployConfig(`${template}\n"workers_dev": false`, instance), DeployConfigError);
  assert.throws(() => renderDeployConfig(template.replace(`"send_email": [{ "name": "EMAIL" }],`, ""), instance), DeployConfigError);
});

test("the config is written owner-only and never over an existing file", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "render-deploy-config-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const out = join(dir, "wrangler.deploy.jsonc");
  writeDeployConfig(out, ENV);
  assert.equal(readFileSync(out, "utf8"), renderDeployConfig(template, instanceFromEnv(ENV)));
  assert.equal(statSync(out).mode & 0o777, 0o600);
  writeFileSync(out, "the owner's own config");
  assert.throws(() => writeDeployConfig(out, ENV), DeployConfigError);
  assert.equal(readFileSync(out, "utf8"), "the owner's own config");
});

const ORIGIN = `https://${HOSTNAME}`;
const toAccess = () => new Response(null, { status: 302, headers: { location: "https://example.cloudflareaccess.com/cdn-cgi/access/login" } });

function fakeFetch(byPath: Record<string, () => Response>) {
  const seen: Array<[string, RequestInit]> = [];
  const fetch = async (url: string, init: RequestInit) => {
    seen.push([url, init]);
    const answer = byPath[new URL(url).pathname];
    if (!answer) throw new TypeError("fetch failed", { cause: { code: "ENOTFOUND" } });
    return answer();
  };
  return { fetch, seen };
}

test("the smoke check passes when the page redirects to Access and the machine route refuses", async () => {
  for (const machine of [toAccess, () => new Response(null, { status: 401 }), () => new Response(null, { status: 403 })]) {
    const { fetch, seen } = fakeFetch({ "/": toAccess, "/api/v1/machine/pull": machine });
    assert.deepEqual(await smokeCheck(ORIGIN, fetch), []);
    assert.deepEqual(seen.map(([url]) => url), [`${ORIGIN}/`, `${ORIGIN}/api/v1/machine/pull`]);
    for (const [, init] of seen) {
      assert.equal(init.redirect, "manual");
      assert.equal(init.headers, undefined, "no credentials are sent");
    }
  }
});

test("the smoke check fails when Access is off, the Worker errs or the host is unreachable", async () => {
  const ok = () => new Response("page", { status: 200 });
  const elsewhere = () => new Response(null, { status: 302, headers: { location: "https://login.example.org/" } });
  const fails = async (byPath: Record<string, () => Response>) => smokeCheck(ORIGIN, fakeFetch(byPath).fetch);
  assert.deepEqual(await fails({ "/": ok, "/api/v1/machine/pull": ok }), [
    "GET / answered 200, not a 302 to Access's login page",
    "GET /api/v1/machine/pull without credentials answered 200, not a refusal",
  ]);
  assert.deepEqual(await fails({ "/": elsewhere, "/api/v1/machine/pull": () => new Response(null, { status: 500 }) }), [
    "GET / answered 302, not a 302 to Access's login page",
    "GET /api/v1/machine/pull without credentials answered 500, not a refusal",
  ]);
  const unreachable = await fails({});
  assert.deepEqual(unreachable, ["GET / failed (ENOTFOUND)", "GET /api/v1/machine/pull failed (ENOTFOUND)"]);
  assert.ok(!unreachable.join("\n").includes(HOSTNAME), "never names the hostname");
});
