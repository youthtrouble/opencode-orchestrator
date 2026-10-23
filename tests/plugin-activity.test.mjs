// The plugin's per-instance activity ledger (S13s1, plan go-s13s 3.1): what any session of a project
// last did and how many tool calls are in flight, written under <P>/.orchestrator/activity/opencode/.
// Bus events come from the harness's fake subscription and the plugin clock is virtual, so no case
// sleeps; only the incident case waits for real process I/O.
import test, { describe } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { calls, createSandbox, loadPlugin, routingFixture, setAnswers, typescriptSupported, waitFor } from "./plugin-harness.mjs";

const skip = typescriptSupported ? false : "Node cannot strip TypeScript types here (needs >= 22.18)";
const T0 = Date.parse("2026-10-23T06:00:00.000Z");
const iso = (ms) => new Date(ms).toISOString();

/**
 * A plugin loaded on a virtual clock that starts at T0. The clock covers setInterval too, so it must
 * be installed before setup creates the alive timer; unloading runs before the clock is restored.
 */
async function loadLedger(t, { project = true } = {}) {
  const sandbox = createSandbox(t);
  const doc = routingFixture("green");
  setAnswers(sandbox, { "routing --json": { stdout: doc }, "routing --json --refresh": { stdout: doc }, "routing --cached --json": { stdout: doc }, "session beat": { stdout: "" }, "incident record": { stdout: { recorded: "fp" } } });
  if (project) fs.mkdirSync(path.join(sandbox.project, ".orchestrator"), { recursive: true });
  const clock = t.mock.timers;
  clock.enable({ apis: ["Date", "setTimeout", "setInterval"], now: T0 });
  const plugin = await loadPlugin(t, sandbox);
  t.after(async () => {
    try { await plugin.dispose(); } finally { clock.reset(); }
  });
  return { sandbox, plugin, clock };
}

const ledgerDir = (root) => path.join(root, ".orchestrator", "activity", "opencode");
const ledgerFiles = (root) => (fs.existsSync(ledgerDir(root)) ? fs.readdirSync(ledgerDir(root)).filter((f) => /^\d+-[0-9a-f]{8}\.json$/.test(f)) : []);
function readLedger(root) {
  const files = ledgerFiles(root);
  assert.equal(files.length, 1, `one ledger file of this instance under ${ledgerDir(root)}`);
  return JSON.parse(fs.readFileSync(path.join(ledgerDir(root), files[0]), "utf8"));
}

async function deliver(plugin, ...events) {
  for (const event of events) plugin.emit(event);
  await new Promise(setImmediate);
}
const bus = (type, data) => ({ type, data });
const tool = (type, sessionID, id, extra = {}) => bus(`session.tool.${type}`, { sessionID, assistantMessageID: "msg_1", id, ...extra });

describe("opencode activity ledger", { skip }, () => {
  test("a subagent session's events advance last_activity_at for the parent's project", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    clock.tick(1000);
    await deliver(plugin, { ...bus("session.step.started", { sessionID: "ses_child" }), location: { directory: sandbox.project } });
    const ledger = readLedger(sandbox.project);
    assert.deepEqual(Object.keys(ledger), ["schema", "instance", "pid", "alive_at", "last_activity_at", "tools_in_flight"]);
    assert.deepEqual(ledger, { schema: 1, instance: ledger.instance, pid: process.pid, alive_at: iso(T0 + 1000), last_activity_at: iso(T0 + 1000), tools_in_flight: 0 });
    assert.match(ledger.instance, new RegExp(`^${process.pid}-[0-9a-f]{8}$`));
    assert.equal(fs.statSync(path.join(ledgerDir(sandbox.project), `${ledger.instance}.json`)).mode & 0o777, 0o600);
    assert.equal(fs.statSync(ledgerDir(sandbox.project)).mode & 0o777, 0o700);
  });

  test("events without a string sessionID or outside the session allowlist are not activity", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    await deliver(plugin, bus("session.step.started", {}), bus("session.updated", { sessionID: "ses_1" }), bus("message.updated", { sessionID: "ses_1" }));
    assert.deepEqual(ledgerFiles(sandbox.project), []);
  });

  test("the context hook of any agent is activity, subagents' included", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    await plugin.context("coder-oai", "ses_child");
    assert.equal(readLedger(sandbox.project).last_activity_at, iso(T0));
  });

  test("an event from a worktree under <P>/.orchestrator/worktrees/x is recorded for P", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    const worktree = path.join(sandbox.project, ".orchestrator", "worktrees", "x");
    fs.mkdirSync(worktree, { recursive: true });
    await deliver(plugin, { ...bus("session.text.delta", { sessionID: "ses_wt" }), location: { directory: worktree } });
    assert.equal(readLedger(sandbox.project).last_activity_at, iso(T0));
    assert.equal(fs.existsSync(path.join(worktree, ".orchestrator")), false);
  });

  test("called/success pairs count, and a call named subagent is not counted", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    await deliver(plugin, tool("input.started", "ses_1", "c1", { name: "subagent" }), tool("called", "ses_1", "c1"));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
    await deliver(plugin, tool("input.started", "ses_1", "c2", { name: "bash" }), tool("called", "ses_1", "c2"), tool("called", "ses_1", "c3"));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1, "a change between two counts above 0 is coalesced");
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 2, "a called whose name was never seen counts");
    await deliver(plugin, tool("success", "ses_1", "c2"));
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await deliver(plugin, tool("failed", "ses_1", "c3"), tool("success", "ses_1", "c1"));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
  });

  test("shell started/ended pairs count", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    await deliver(plugin, bus("session.shell.started", { sessionID: "ses_1", id: "sh1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await deliver(plugin, bus("session.shell.ended", { sessionID: "ses_1", id: "sh1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
  });

  test("a session's execution end or idle clears its entries only", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    await deliver(plugin, tool("called", "ses_a", "a1"), tool("called", "ses_a", "a2"), tool("called", "ses_b", "b1"));
    await deliver(plugin, bus("session.execution.succeeded", { sessionID: "ses_a" }));
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await deliver(plugin, bus("session.idle", { sessionID: "ses_b" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
  });

  test("an execute.before no bus event tracked still counts, and a bus-tracked call is not counted twice", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    await plugin.executeBefore({ tool: "bash", callID: "call_1", sessionID: "ses_1", input: {} });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await plugin.executeAfter({ tool: "bash", callID: "call_1", sessionID: "ses_1", status: "completed" });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);

    await deliver(plugin, tool("called", "ses_1", "call_2"));
    await plugin.executeBefore({ tool: "read", callID: "call_2", sessionID: "ses_1", input: {} });
    await plugin.executeBefore({ tool: "subagent", callID: "call_3", sessionID: "ses_1", input: { agent: "general" } });
    await plugin.executeBefore({ tool: "grep", input: {} });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await plugin.executeAfter({ tool: "grep", status: "completed" });
    await deliver(plugin, tool("success", "ses_1", "call_2"));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0, "a call without an id pairs with its tool's oldest open call");
  });

  // Review R1: an id-less completion closed the oldest call of that tool in any session.
  test("an id-less execute.after closes a call of its own session only", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    await plugin.executeBefore({ tool: "read", sessionID: "ses_a", input: {} });
    await plugin.executeBefore({ tool: "read", sessionID: "ses_b", input: {} });
    await plugin.executeAfter({ tool: "read", sessionID: "ses_b", status: "completed" });
    await deliver(plugin, bus("session.idle", { sessionID: "ses_b" }));
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1, "ses_a's read is still running");
    await plugin.executeAfter({ tool: "read", sessionID: "ses_a", status: "completed" });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
  });

  // Review R2: an id-less end used a fresh counter key and never matched its start.
  test("id-less shell and tool ends close the oldest id-less start of their session", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    await deliver(plugin, bus("session.shell.started", { sessionID: "ses_1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await deliver(plugin, bus("session.shell.ended", { sessionID: "ses_2" }));
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1, "another session's end does not close it");
    await deliver(plugin, bus("session.shell.ended", { sessionID: "ses_1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
    await deliver(plugin, bus("session.tool.called", { sessionID: "ses_1", assistantMessageID: "msg_1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1);
    await deliver(plugin, bus("session.tool.success", { sessionID: "ses_1", assistantMessageID: "msg_1" }));
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);
  });

  // Review R3: bus ids were a plugin-wide set that outlived their entries.
  test("a hook call is deduplicated only against an active bus entry of its session and project", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    const other = path.join(sandbox.root, "other-project");
    fs.mkdirSync(path.join(other, ".orchestrator"), { recursive: true });

    await deliver(plugin, tool("called", "ses_a", "c1"), bus("session.idle", { sessionID: "ses_a" }));
    await plugin.executeBefore({ tool: "bash", callID: "c1", sessionID: "ses_a", input: {} });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 1, "an id reused after its session went idle counts");
    await plugin.executeAfter({ tool: "bash", callID: "c1", sessionID: "ses_a", status: "completed" });
    assert.equal(readLedger(sandbox.project).tools_in_flight, 0);

    await deliver(plugin, tool("called", "ses_a", "c2"));
    await plugin.executeBefore({ tool: "bash", callID: "c2", sessionID: "ses_b", input: {} });
    await deliver(plugin, { ...tool("called", "ses_c", "c3"), location: { directory: other } });
    await plugin.executeBefore({ tool: "bash", callID: "c3", sessionID: "ses_c", input: {} });
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 3, "the same id in another session or project is another call");
    assert.equal(readLedger(other).tools_in_flight, 1);

    await plugin.executeBefore({ tool: "bash", callID: "c4", sessionID: "ses_a", input: {} });
    await deliver(plugin, tool("called", "ses_a", "c4"));
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 4, "the bus entry replaces the hook entry of the same call");
    await deliver(plugin, tool("success", "ses_a", "c4"));
    await plugin.executeAfter({ tool: "bash", callID: "c4", sessionID: "ses_a", status: "completed" });
    clock.tick(15000);
    assert.equal(readLedger(sandbox.project).tools_in_flight, 3);
  });

  test("activity writes coalesce to one per 15 s; a 0 to >0 change of tools_in_flight is written at once", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    const step = bus("session.step.started", { sessionID: "ses_1" });
    await deliver(plugin, step);
    clock.tick(1000);
    await deliver(plugin, step);
    clock.tick(4000);
    await deliver(plugin, step);
    assert.deepEqual([readLedger(sandbox.project).alive_at, readLedger(sandbox.project).last_activity_at], [iso(T0), iso(T0)], "inside the interval nothing is written");
    clock.tick(9999);
    assert.equal(readLedger(sandbox.project).alive_at, iso(T0));
    clock.tick(1);
    assert.deepEqual([readLedger(sandbox.project).alive_at, readLedger(sandbox.project).last_activity_at], [iso(T0 + 15000), iso(T0 + 5000)], "one trailing write of the latest state");

    clock.tick(1000);
    await deliver(plugin, tool("called", "ses_1", "c1"));
    assert.deepEqual([readLedger(sandbox.project).alive_at, readLedger(sandbox.project).tools_in_flight], [iso(T0 + 16000), 1]);
    clock.tick(1000);
    await deliver(plugin, tool("success", "ses_1", "c1"));
    assert.deepEqual([readLedger(sandbox.project).alive_at, readLedger(sandbox.project).tools_in_flight], [iso(T0 + 17000), 0]);
  });

  test("the alive timer refreshes alive_at every 60 s", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    await deliver(plugin, bus("session.step.started", { sessionID: "ses_1" }));
    clock.tick(60000);
    assert.deepEqual([readLedger(sandbox.project).alive_at, readLedger(sandbox.project).last_activity_at], [iso(T0 + 60000), iso(T0)]);
    clock.tick(60000);
    assert.equal(readLedger(sandbox.project).alive_at, iso(T0 + 120000));
  });

  test("a directory without .orchestrator gets no file", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t, { project: false });
    await deliver(plugin, bus("session.step.started", { sessionID: "ses_1" }), tool("called", "ses_1", "c1"));
    await plugin.executeBefore({ tool: "bash", callID: "call_1", input: {} });
    await plugin.context("coder-oai", "ses_1");
    clock.tick(60000);
    assert.equal(fs.existsSync(path.join(sandbox.project, ".orchestrator")), false);
  });

  test("a failed write is reported once and every hook still resolves", async (t) => {
    const { sandbox, plugin, clock } = await loadLedger(t);
    fs.writeFileSync(path.join(sandbox.project, ".orchestrator", "activity"), "not a directory");
    const incidents = () => calls(sandbox).filter((c) => c.self === "path" && c.argv[0] === "incident");
    await deliver(plugin, bus("session.step.started", { sessionID: "ses_1" }), tool("called", "ses_1", "c1"));
    await plugin.executeBefore({ tool: "bash", callID: "call_1", input: {} });
    await plugin.executeAfter({ tool: "bash", callID: "call_1", status: "completed" });
    await plugin.context("coder-oai", "ses_1");
    // The report is a real process: let it finish before virtual time could fire its timeout.
    assert.ok(await waitFor(() => incidents().length === 1));
    assert.ok(await waitFor(() => { try { process.kill(incidents()[0].pid, 0); return false; } catch { return true; } }));
    clock.tick(60000);
    await deliver(plugin, tool("success", "ses_1", "c1"));
    assert.equal(await waitFor(() => incidents().length > 1, 1000), false, "reported once per 10 min");
    assert.match(JSON.parse(incidents()[0].stdin).summary, /^activity ledger write failed: /);
    assert.equal(plugin.subscriptions[0].ended, false, "the event pump keeps running");
  });

  test("unloading removes this instance's own file and no other", async (t) => {
    const { sandbox, plugin } = await loadLedger(t);
    await deliver(plugin, bus("session.step.started", { sessionID: "ses_1" }));
    const other = path.join(ledgerDir(sandbox.project), "1-0000beef.json");
    fs.writeFileSync(other, "{}\n");
    assert.equal(fs.readdirSync(ledgerDir(sandbox.project)).length, 2);
    await plugin.dispose();
    assert.deepEqual(fs.readdirSync(ledgerDir(sandbox.project)), ["1-0000beef.json"]);
  });
});
