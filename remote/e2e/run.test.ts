// End to end on one machine: the real Worker on a local D1 under `wrangler dev`, behind the Access emulator,
// driven by the real `orch remote once` and a simulated phone. It is the end-to-end test of acceptance
// criterion A5 and covers A1-A4 of the Work Trains plan, all but the owner's live phone. Run it with
// `node --test 'remote/e2e/*.test.ts'`; it takes about 90 s, most of it the Mac's 60 s spacing between two
// pushes of one project.
import assert from "node:assert/strict";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, it } from "node:test";
import {
  AccessEmulator,
  AUD,
  CO_OWNER_COOKIE,
  CO_OWNER_EMAIL,
  OUTSIDER_COOKIE,
  OWNER_COOKIE,
  OWNER_EMAIL,
  TEAM_DOMAIN,
} from "./access-emulator.ts";
import { RunCleanup } from "./cleanup.ts";
import { buildOrch, OrchSandbox, type Credentials } from "./orch.ts";
import { getOnHost, OLD_PAGE_ORIGIN, PAGE_ORIGIN, Phone, type Json } from "./phone.ts";
import { delay, freePort, startWorkerDev, type WorkerDev } from "./worker-dev.ts";

/** A little over the e2e Worker's 1 s undo window (wrangler.e2e.jsonc). */
const PAST_UNDO_WINDOW_MS = 1_200;
/** A little over the Mac's 60 s spacing between two accepted pushes of one project. */
const PAST_PUSH_SPACING_MS = 61_000;
/** Criterion A2: an answer on the phone reaches the mailbox within two minutes. */
const ANSWER_DEADLINE_MS = 120_000;

const DATABASE_QUESTION = "Which database?";
const NAMING_QUESTION = "What do we call the service?";
const OUTAGE_QUESTION = "Ship the beta on Friday?";
const LOST_ACK_QUESTION = "Keep the old endpoint?";
/** An Access team other than the run's, as when the hostname's Access app lives in another account. */
const ANOTHER_TEAM_DOMAIN = "https://other-example.cloudflareaccess.com";
const forbidden = (reason: string) => ({ error: "forbidden", message: "forbidden", reason });
/**
 * The database question's recommendation names an API route, which the page shows, and a filesystem path,
 * which the snapshot redacts before it leaves the Mac. Accepting it must still deliver this text.
 */
const DATABASE_RECOMMENDATION = "Use Postgres behind GET /v1/installations/me; keep the dumps in /srv/owner-backups/db";
const DATABASE_RECOMMENDATION_ON_PAGE = "Use Postgres behind GET /v1/installations/me; keep the dumps in <path>";

interface OnceResult {
  code: number;
  report: Json;
}

describe("Work Trains end to end", { timeout: 300_000 }, () => {
  const cleanup = new RunCleanup();
  const tmp = mkdtempSync(join(tmpdir(), "work-trains-e2e-"));
  cleanup.defer(() => rmSync(tmp, { recursive: true, force: true }));
  const credentials: Credentials = {
    machineToken: randomBytes(32).toString("base64url"),
    accessClientId: `${randomBytes(8).toString("hex")}.access`,
    accessClientSecret: randomBytes(32).toString("hex"),
  };
  let emulator: AccessEmulator;
  let dev: WorkerDev | undefined;
  let orch: OrchSandbox;
  let phone: Phone;
  let coOwnerPhone: Phone;
  let alpha: string;
  let beta: string;
  /** When the cycle that first pushed alpha returned: its next push is due 60 s after. */
  let alphaPushedAt: number;

  const once = async (): Promise<OnceResult> => {
    const { code, stdout } = await orch.run(["remote", "once", "--json"]);
    return { code, report: JSON.parse(stdout) };
  };
  const messages = async (root: string): Promise<Json[]> => {
    const { code, stdout, stderr } = await orch.run(["mailbox", "list", "--json"], root);
    assert.equal(code, 0, stderr);
    return JSON.parse(stdout);
  };
  const repliesTo = async (root: string, questionId: string) =>
    (await messages(root)).filter((message) => message.thread === questionId);
  const ask = async (root: string, subject: string, options: { recommended: string; blocking: boolean }) => {
    const { code, stderr } = await orch.run(["mailbox", "post", "--from", "conductor", "--to", "human", "--kind",
      "question", "--subject", subject, "--body", `${subject} The answer decides the next slice.`,
      "--recommended", options.recommended, "--blocking", String(options.blocking)], root);
    assert.equal(code, 0, stderr);
  };
  const flipOf = (root: string): Json => JSON.parse(readFileSync(join(root, ".orchestrator", "flip.json"), "utf8"));

  before(async () => {
    cleanup.releaseOnSignals();
    const orchBinary = buildOrch(tmp);
    const workerPort = await freePort();
    emulator = await AccessEmulator.create({
      upstream: `http://127.0.0.1:${workerPort}`,
      serviceToken: { clientId: credentials.accessClientId, clientSecret: credentials.accessClientSecret },
    });
    cleanup.defer(() => emulator.stop());
    dev = await startWorkerDev(tmp, workerPort, {
      ACCESS_TEAM_DOMAIN: TEAM_DOMAIN,
      ACCESS_AUD: AUD,
      OWNER_EMAIL: `${OWNER_EMAIL}, ${CO_OWNER_EMAIL}`,
      ACCESS_SERVICE_CLIENT_ID: credentials.accessClientId,
      MACHINE_TOKEN_SHA256: createHash("sha256").update(credentials.machineToken).digest("hex"),
      PUBLIC_ORIGIN: PAGE_ORIGIN,
      E2E_JWKS: JSON.stringify(emulator.jwks),
    }, cleanup);
    await emulator.start();
    phone = new Phone(emulator.url);
    coOwnerPhone = new Phone(emulator.url, { cookie: CO_OWNER_COOKIE });
    orch = new OrchSandbox(tmp, orchBinary, credentials);
    await orch.init(emulator.url);
    alpha = orch.createProject("alpha");
  });

  after(async () => {
    await cleanup.run();
    assert.equal(existsSync(tmp), false, "the run's temp directory is gone");
  });

  it("A1: the Worker refuses every route without Access; each identity reaches only its own routes", async () => {
    const routes = [
      ["GET", "/"],
      ["GET", "/app.js"],
      ["GET", "/api/v1/summary"],
      ["POST", "/api/v1/machine/push"],
      ["GET", "/api/v1/machine/pull"],
      ["POST", "/api/v1/machine/ack"],
      ["GET", "/__e2e/notifications"],
      ["GET", "/nope"],
    ];
    for (const [method, path] of routes) {
      const response = await fetch(dev!.url + path, { method, redirect: "manual" });
      assert.equal(response.status, 403, `${method} ${path} straight to the Worker`);
    }

    const viaAccess = (path: string, headers: Record<string, string>) =>
      fetch(emulator.url + path, { headers, redirect: "manual" });
    const serviceToken = {
      "CF-Access-Client-Id": credentials.accessClientId,
      "CF-Access-Client-Secret": credentials.accessClientSecret,
    };
    const nobody = await viaAccess("/api/v1/summary", {});
    assert.equal(nobody.status, 302, "Access sends a stranger to its login");
    const wrongBearer = await viaAccess("/api/v1/machine/pull", {
      ...serviceToken,
      Authorization: "Bearer not-the-token",
    });
    assert.equal(wrongBearer.status, 403, "the service token with a wrong machine token");
    const ownerOnMachineRoute = await viaAccess("/api/v1/machine/pull", {
      Cookie: OWNER_COOKIE,
      Authorization: `Bearer ${credentials.machineToken}`,
    });
    assert.equal(ownerOnMachineRoute.status, 403, "the owner's browser JWT on a machine route");
    const machineOnPage = await viaAccess("/api/v1/summary", serviceToken);
    assert.equal(machineOnPage.status, 403, "the service JWT on a browser route");
    const machineOnNotifications = await viaAccess("/__e2e/notifications", serviceToken);
    assert.equal(machineOnNotifications.status, 403, "the service JWT on the owner's notifications");
  });

  it("A1: Access replaces any assertion a caller sends with its own", async () => {
    const ownerAssertion = await emulator.ownerAssertion();
    const viaAccess = (headers: Record<string, string>) =>
      fetch(emulator.url + "/api/v1/summary", { headers, redirect: "manual" });

    const assertionAlone = await viaAccess({ "Cf-Access-Jwt-Assertion": ownerAssertion });
    assert.equal(assertionAlone.status, 302, "an owner assertion without a session is sent to the login");
    const machineAsOwner = await viaAccess({
      "CF-Access-Client-Id": credentials.accessClientId,
      "CF-Access-Client-Secret": credentials.accessClientSecret,
      "Cf-Access-Jwt-Assertion": ownerAssertion,
    });
    assert.equal(machineAsOwner.status, 403, "the service token's own JWT replaces the owner assertion it carried");
  });

  it("A1: each email OWNER_EMAIL lists signs in; someone else Access lets in is refused", async () => {
    assert.equal((await phone.loadSummary()).status, 200, "the owner");
    assert.equal((await coOwnerPhone.loadSummary()).status, 200, "the owner's second email");
    const outsider = await new Phone(emulator.url, { cookie: OUTSIDER_COOKIE }).loadSummary();
    assert.equal(outsider.status, 403, "an email on the owner's domain that the Worker does not list");
    assert.deepEqual(outsider.body, forbidden("browser_email_mismatch"));
  });

  it("the page on an old hostname redirects to PUBLIC_ORIGIN; the API still answers there", async () => {
    const oldHost = new URL(OLD_PAGE_ORIGIN).host;
    for (const path of ["/", "/app.js?v=1"]) {
      const moved = await phone.openPage(path, oldHost);
      assert.equal(moved.status, 302, path);
      assert.equal(moved.location, `${PAGE_ORIGIN}${path}`);
    }
    const home = await phone.openPage("/", new URL(PAGE_ORIGIN).host);
    assert.equal(home.status, 200, "the page is served on PUBLIC_ORIGIN");
    assert.equal(home.location, undefined);

    const pull = await getOnHost(emulator.url, oldHost, "/api/v1/machine/pull", {
      "CF-Access-Client-Id": credentials.accessClientId,
      "CF-Access-Client-Secret": credentials.accessClientSecret,
      Authorization: `Bearer ${credentials.machineToken}`,
    });
    assert.equal(pull.status, 200, "the Mac's pull on the old hostname");
  });

  it("pushes the machine's first project on its first cycle", async () => {
    const { code, report } = await once();
    alphaPushedAt = Date.now();
    assert.equal(code, 0, report.error);
    assert.equal(report.pushed, 1);
    assert.equal((await phone.project("alpha")).state, "idle");
  });

  it("A2, A4: a pause applies after its undo window; the paused state waits for the push spacing", async () => {
    const { key, state } = await phone.project("alpha");
    const tapKey = randomUUID();
    const tap = await phone.act(key, "pause", tapKey);
    assert.equal(tap.status, 201);
    assert.equal((await once()).report.applied, 0, "nothing applies inside the undo window");

    await delay(PAST_UNDO_WINDOW_MS);
    const { code, report } = await once();
    assert.equal(code, 0, report.error);
    assert.equal(report.applied, 1);
    assert.equal(report.pushed, 0, "alpha's next push is not due for 60 s");
    assert.equal(flipOf(alpha).awaiting_human.reason, "paused from Work Trains");
    const retried = await phone.act(key, "pause", tapKey);
    assert.equal(retried.body.action.status, "applied", "the tap, retried, shows its outcome");
    assert.equal((await phone.project("alpha")).state, state, "the Worker still shows the state pushed before");
  });

  it("A2: accepting the recommendation answers the question once, after the undo window, with the original text", async () => {
    beta = orch.createProject("beta");
    await ask(beta, DATABASE_QUESTION, { recommended: DATABASE_RECOMMENDATION, blocking: true });
    // Only the first blocks: the notification check expects exactly one.
    await ask(beta, NAMING_QUESTION, { recommended: "Call it Hopper", blocking: false });
    await ask(beta, OUTAGE_QUESTION, { recommended: "Yes", blocking: false });
    await ask(beta, LOST_ACK_QUESTION, { recommended: "No", blocking: false });
    const pushed = await once();
    assert.equal(pushed.report.pushed, 1, "beta, new since the last push");

    const question = await phone.question("beta", DATABASE_QUESTION);
    assert.equal(question.recommended, DATABASE_RECOMMENDATION_ON_PAGE, "the page shows the route, not the path");
    assert.equal(question.blocking, true);
    const fromOldHostname = await new Phone(emulator.url, { origin: OLD_PAGE_ORIGIN }).accept(question.id);
    assert.equal(fromOldHostname.status, 403, "after the move, a page on the old hostname cannot write");
    assert.deepEqual(fromOldHostname.body, forbidden("browser_write_foreign_origin"));
    const tappedAt = Date.now();
    const tapKey = randomUUID();
    const tap = await phone.accept(question.id, tapKey);
    assert.equal(tap.status, 201);
    assert.equal(tap.body.answer.status, "undo_window");
    const sameTap = await phone.accept(question.id, tapKey);
    assert.equal(sameTap.status, 200, "a retried tap is the same answer");
    assert.equal(sameTap.body.answer.id, tap.body.answer.id);
    const doubleTap = await phone.accept(question.id, randomUUID());
    assert.equal(doubleTap.status, 409, "a second live answer breaks the partial unique index");

    assert.equal((await once()).report.applied, 0);
    assert.deepEqual(await repliesTo(beta, question.id), [], "not delivered inside the undo window");

    await delay(PAST_UNDO_WINDOW_MS);
    const { code, report } = await once();
    assert.equal(code, 0, report.error);
    assert.equal(report.applied, 1);
    assert.ok(Date.now() - tappedAt < ANSWER_DEADLINE_MS, "the reply landed within two minutes of the tap");
    const replies = await repliesTo(beta, question.id);
    assert.equal(replies.length, 1);
    assert.equal(replies[0].via, "remote");
    assert.equal(replies[0].body, DATABASE_RECOMMENDATION, "the mailbox gets the recommendation as written");
    const asked = (await messages(beta)).find((message) => message.id === question.id);
    assert.ok(asked.acked_by.includes("human"), "the question is acked by the human");
    assert.equal((await phone.question("beta", DATABASE_QUESTION)).answer.status, "applied");
  });

  it("A2: an undo inside the window applies nothing, and a typed answer after it lands once", async () => {
    const question = await phone.question("beta", NAMING_QUESTION);
    const undone = await phone.reply(question.id, "Call it Hopper");
    assert.equal(undone.status, 201);
    const undo = await phone.undoAnswer(undone.body.answer.id);
    assert.equal(undo.status, 200);
    await delay(PAST_UNDO_WINDOW_MS);
    assert.equal((await once()).report.pulled, 0);
    assert.deepEqual(await repliesTo(beta, question.id), []);
    assert.equal((await phone.question("beta", NAMING_QUESTION)).answer, null);

    const tappedAt = Date.now();
    const typed = await coOwnerPhone.reply(question.id, "Call it Lovelace");
    assert.equal(typed.status, 201, "an undone answer leaves the question open to another, from either owner");
    await delay(PAST_UNDO_WINDOW_MS);
    const { code, report } = await once();
    assert.equal(code, 0, report.error);
    assert.equal(report.applied, 1);
    assert.ok(Date.now() - tappedAt < ANSWER_DEADLINE_MS);
    const replies = await repliesTo(beta, question.id);
    assert.equal(replies.length, 1);
    assert.equal(replies[0].via, "remote");
    assert.equal(replies[0].body, "Call it Lovelace");
  });

  it("notifies the owner once about the blocking question, without its text, linking to PUBLIC_ORIGIN", async () => {
    const notifications = await phone.notifications();
    assert.deepEqual(notifications, [
      { subject: "Work Trains: beta needs you", text: `Work Trains: beta needs you\n${PAGE_ORIGIN}\n` },
    ]);
  });

  it("A3: an Access outage fails the cycle and is recorded; the answer lands once when Access is back", async () => {
    await emulator.stop();
    const failed = await once();
    assert.equal(failed.code, 1);
    const status = await orch.run(["remote", "status", "--json"]);
    assert.equal(JSON.parse(status.stdout).last_error.kind, "network");

    await emulator.start();
    const question = await phone.question("beta", OUTAGE_QUESTION);
    assert.equal((await phone.reply(question.id, "Yes, Friday")).status, 201);
    await delay(PAST_UNDO_WINDOW_MS);
    const { code, report } = await once();
    assert.equal(code, 0, report.error);
    assert.equal((await repliesTo(beta, question.id)).length, 1);
    const recovered = await orch.run(["remote", "status", "--json"]);
    assert.equal(JSON.parse(recovered.stdout).last_error, null, "a good cycle clears the recorded error");
  });

  it("A3: an ack whose response is lost fails the cycle, and the answer stays applied once", async () => {
    const question = await phone.question("beta", LOST_ACK_QUESTION);
    const tapKey = randomUUID();
    const tap = await phone.reply(question.id, "Keep it until June", tapKey);
    assert.equal(tap.status, 201);
    await delay(PAST_UNDO_WINDOW_MS);
    emulator.dropNextAckResponse();
    const lost = await once();
    assert.equal(lost.code, 1);
    assert.match(lost.report.error, /POST \/api\/v1\/machine\/ack/);
    assert.equal((await repliesTo(beta, question.id)).length, 1);

    const next = await once();
    assert.equal(next.code, 0, next.report.error);
    assert.equal(next.report.pulled, 0, "the ack reached the Worker, which settled the answer");
    assert.equal((await repliesTo(beta, question.id)).length, 1);
    const stored = await phone.reply(question.id, "Keep it until June", tapKey);
    assert.equal(stored.body.answer.status, "applied");
  });

  it("Access of another team is refused, naming the team domain the Worker expects, to phone and Mac", async () => {
    emulator.issueAs(ANOTHER_TEAM_DOMAIN);
    try {
      const refused = await phone.loadSummary();
      assert.equal(refused.status, 403);
      assert.equal(refused.body.error, "wrong_issuer");
      assert.equal(refused.body.expected_issuer, TEAM_DOMAIN);
      const failed = await once();
      assert.equal(failed.code, 1);
      const status = await orch.run(["remote", "status", "--json"]);
      const lastError = JSON.parse(status.stdout).last_error;
      assert.equal(lastError.kind, "http_status");
      assert.ok(
        lastError.message.endsWith(`HTTP 403 wrong_issuer (the Worker expects Access team ${TEAM_DOMAIN})`),
        lastError.message,
      );
    } finally {
      emulator.issueAs(TEAM_DOMAIN);
    }
  });

  it("A2, A4: the paused state is pushed after the spacing; resume clears the gate, starts no supervisor", async () => {
    await delay(alphaPushedAt + PAST_PUSH_SPACING_MS - Date.now());
    const pushed = await once();
    assert.equal(pushed.code, 0, pushed.report.error);
    const paused = await phone.project("alpha");
    assert.equal(paused.state, "paused");

    // A live supervisor already holds the lock (this process), so resume has none to start.
    const lockFile = join(alpha, ".orchestrator", "supervisor.lock");
    writeFileSync(lockFile, String(process.pid));
    const tapKey = randomUUID();
    const tap = await phone.act(paused.key, "resume", tapKey);
    assert.equal(tap.status, 201);
    await delay(PAST_UNDO_WINDOW_MS);
    const { code, report } = await once();
    assert.equal(code, 0, report.error);
    assert.equal(report.applied, 1);
    const flip = flipOf(alpha);
    assert.equal(flip.awaiting_human, undefined, "the gate is cleared");
    assert.equal(flip.pending_launch.to, "opencode");
    assert.equal(flip.pending_launch.reason, "human gate cleared");
    assert.equal(readFileSync(lockFile, "utf8"), String(process.pid), "no supervisor was started");
    assert.equal((await phone.act(paused.key, "resume", tapKey)).body.action.status, "applied");
  });

  it("A1: neither the machine token nor the service secret appears in any output or file", () => {
    const remoteLog = join(orch.home, ".local", "state", "opencode", "orchestrator", "remote", "remote.log");
    const texts: [string, string][] = [
      ["wrangler dev output", dev!.output],
      ["orch output", orch.transcript],
      ["state.json", readFileSync(orch.stateFile, "utf8")],
      ...(existsSync(remoteLog) ? [["remote.log", readFileSync(remoteLog, "utf8")] as [string, string]] : []),
      ...filesUnder(orch.projectsRoot).map((file): [string, string] => [file, readFileSync(file, "latin1")]),
    ];
    for (const [where, text] of texts) {
      assert.ok(!text.includes(credentials.machineToken), `the machine token is in ${where}`);
      assert.ok(!text.includes(credentials.accessClientSecret), `the service secret is in ${where}`);
    }
  });
});

function filesUnder(dir: string): string[] {
  return readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => join(entry.parentPath, entry.name));
}
