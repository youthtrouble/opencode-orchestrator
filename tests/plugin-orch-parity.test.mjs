// Differential test of the plugin against the REAL `orch` (S10c). Every case runs twice, in two
// sandboxes seeded byte-identically: JS mode with the real copied libs and scripts, and go mode
// with the real binary as `options.binary` over a fully poisoned lib/ and bin/. The tool results
// must be equal after normalizing what legitimately differs (sandbox paths, mailbox id/at, manifest
// created_at, the incident version per PE-4). This guards the scripted fake answers of the S10a
// and S10b tests against drift from the real output shapes. No case starts a worker, supervisor,
// bridge or flip run; the fakes on PATH keep every model and endpoint out of reach.
import test, { before, describe } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createSandbox, loadPlugin, openaiHeadroom, poisonHits, typescriptSupported, usageFixture, waitFor } from "./plugin-harness.mjs";

const REPO = fileURLToPath(new URL("../", import.meta.url));
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const ISO_MS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
// `orch version`: the VCS revision is appended only when the build saw a git checkout, so a
// build from a source tarball or `git archive` prints the bare release number.
const ORCH_VERSION = /^0\.5\.0( \([0-9a-f]+(\+dirty)?\))?$/;

function goOnPath() {
  try { execFileSync("go", ["version"], { stdio: "ignore" }); return true; } catch { return false; }
}

const skip = !typescriptSupported ? "Node cannot strip TypeScript types here (needs >= 22.18)"
  : !process.env.ORCH_TEST_BIN && !goOnPath() ? "no orch binary: set ORCH_TEST_BIN or put go on PATH" : false;

let orchBin = process.env.ORCH_TEST_BIN ? path.resolve(process.env.ORCH_TEST_BIN) : null;
before(() => {
  if (skip || orchBin) return;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "plugin-orch-bin-"));
  process.on("exit", () => fs.rmSync(dir, { recursive: true, force: true }));
  orchBin = path.join(dir, "orch");
  // The caller's environment passes through unchanged, GOCACHE included: under a temp HOME, set
  // GOCACHE to a shared cache, or every run fills a fresh one.
  execFileSync("go", ["build", "-o", orchBin, "./cmd/orch"], { cwd: path.join(REPO, "go"), env: { ...process.env, CGO_ENABLED: "0" }, stdio: "inherit" });
});

/**
 * A JS and a go sandbox seeded with the same bytes, and the plugin loaded into each in turn.
 * `seed(sandbox)` writes the case's project files; `run(plugin, sandbox)` drives the plugin.
 * The plugin reads HOME lazily, so each side runs to completion before the other is loaded.
 */
async function differential(t, { usage = usageFixture("green"), seed = () => {}, run }) {
  const sides = {};
  for (const runtime of ["js", "go"]) {
    const sandbox = createSandbox(t, { runtime, usage, cache: null, fakeOrchOnPath: false, realScripts: true });
    seed(sandbox);
    const plugin = runtime === "js"
      ? await loadPlugin(t, sandbox)
      : await loadPlugin(t, sandbox, { options: { runtime: "go", binary: orchBin }, env: { ORCH_HOME: sandbox.orchestrator, ORCH_REGISTRY: path.join(sandbox.orchestrator, "models.json") } });
    sides[runtime] = { sandbox, result: await run(plugin, sandbox) };
    assert.deepEqual(poisonHits(sandbox), [], `${runtime}: no poisoned lib or script may run`);
  }
  return sides;
}

/** The sandbox root (in both its tmpdir and realpath spellings) becomes $SANDBOX, recursively. */
function normalizePaths(value, sandbox) {
  const roots = [...new Set([fs.realpathSync(sandbox.root), sandbox.root])];
  let text = JSON.stringify(value ?? null);
  for (const root of roots) text = text.replaceAll(root, "$SANDBOX");
  return JSON.parse(text);
}

/** A tool result as {content, metadata}. No case expects a tool error, so a throw fails the case. */
async function outcome(promise) {
  const r = await promise;
  return { content: r.content, metadata: r.metadata };
}

function assertSameAfterNormalizing(sides) {
  assert.deepEqual(normalizePaths(sides.go.result, sides.go.sandbox), normalizePaths(sides.js.result, sides.js.sandbox));
}

const stateFile = (sandbox, name) => path.join(sandbox.home, ".local", "state", "opencode", "orchestrator", name);
const incidentLog = (sandbox) => stateFile(sandbox, "incidents.jsonl");
const manifestFile = (sandbox, task) => path.join(sandbox.project, ".orchestrator", "manifests", `${task}.json`);

describe("plugin against the real orch", { skip }, () => {
  test("usage_status (refresh false): same prompt and routing; runtime metadata per side", async (t) => {
    const sides = await differential(t, { run: (plugin) => outcome(plugin.callTool("usage_status", { refresh: false })) });
    assert.equal(sides.js.result.metadata.runtime, "js");
    assert.equal(sides.go.result.metadata.runtime, "go");
    assert.equal(sides.go.result.metadata.orch, orchBin);
    for (const side of Object.values(sides)) { delete side.result.metadata.runtime; delete side.result.metadata.orch; }
    assertSameAfterNormalizing(sides);
  });

  test("delegate on the native path names the same subagent and spawns no bridge", async (t) => {
    const sides = await differential(t, {
      usage: usageFixture(openaiHeadroom()),
      run: (plugin) => outcome(plugin.callTool("delegate", { role: "coder", prompt: "x", size: "medium" })),
    });
    assert.match(sides.go.result.content, /best served natively/);
    assertSameAfterNormalizing(sides);
  });

  test("codex_review with a red OpenAI cache is refused the same way", async (t) => {
    const sides = await differential(t, { usage: usageFixture("red"), run: (plugin) => outcome(plugin.callTool("codex_review", {})) });
    assert.match(sides.go.result.content, /^Refused: /);
    assertSameAfterNormalizing(sides);
  });

  test("mailbox_post then mailbox_list with to, unacked_by, kind and limit", async (t) => {
    const seeded = [
      { id: "seed-1", at: "2026-10-19T10:00:00.000Z", from: "planner", to: "coder", kind: "brief", subject: "first", body: "do it", refs: ["a.md"], thread: null },
      { id: "seed-2", at: "2026-10-19T10:01:00.000Z", from: "coder", to: "human", kind: "question", subject: "second", body: "", refs: [], thread: "t-1" },
    ];
    const sides = await differential(t, {
      seed: (sandbox) => {
        const dir = path.join(sandbox.project, ".orchestrator", "mailbox");
        fs.mkdirSync(dir, { recursive: true });
        fs.writeFileSync(path.join(dir, "messages.jsonl"), seeded.map((m) => JSON.stringify(m) + "\n").join(""));
      },
      run: async (plugin) => {
        const posted = await outcome(plugin.callTool("mailbox_post", { from: "conductor", to: "coder", kind: "brief", subject: "third", body: "line 1\n--not-a-flag", refs: ["b.md"], thread: "t-2" }));
        const lists = [];
        for (const input of [{}, { to: "coder" }, { unacked_by: "coder" }, { kind: "question" }, { limit: 1 }]) lists.push(await outcome(plugin.callTool("mailbox_list", input)));
        return { posted, lists };
      },
    });
    for (const side of Object.values(sides)) {
      const { id, at } = side.result.posted.metadata;
      assert.match(id, UUID_V4);
      assert.match(at, ISO_MS);
      side.result = JSON.parse(JSON.stringify(side.result).replaceAll(id, "$ID").replaceAll(at, "$AT"));
    }
    assertSameAfterNormalizing(sides);
  });

  test("manifest_create then manifest_verify: OK, then MISMATCH after an edit; byte-equal manifests", async (t) => {
    const sides = await differential(t, {
      seed: (sandbox) => {
        fs.mkdirSync(path.join(sandbox.project, "src"), { recursive: true });
        fs.writeFileSync(path.join(sandbox.project, "src", "a.txt"), "alpha\n");
        fs.writeFileSync(path.join(sandbox.project, "b.txt"), "beta\n");
      },
      run: async (plugin, sandbox) => {
        const created = await outcome(plugin.callTool("manifest_create", { task: "s10c", author: "coder", paths: ["src/a.txt", "b.txt"], baseline: "abc123" }));
        const ok = await outcome(plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/s10c.json" }));
        fs.writeFileSync(path.join(sandbox.project, "b.txt"), "edited\n");
        const mismatch = await outcome(plugin.callTool("manifest_verify", { manifest: ".orchestrator/manifests/s10c.json" }));
        return { created, ok, mismatch };
      },
    });
    const bytes = {};
    for (const [runtime, side] of Object.entries(sides)) {
      const text = fs.readFileSync(manifestFile(side.sandbox, "s10c"), "utf8");
      const onDisk = JSON.parse(text);
      assert.match(onDisk.created_at, ISO_MS);
      bytes[runtime] = text.replaceAll(onDisk.created_at, "$CREATED_AT");
      // manifest_sha256 hashes the manifest's `files` map (not created_at), so it is checked
      // against the file on disk here and compared across runtimes unmasked below.
      const filesSha = crypto.createHash("sha256").update(JSON.stringify(onDisk.files)).digest("hex");
      assert.equal(side.result.created.metadata.manifest_sha256, onDisk.manifest_sha256, `${runtime}: the reported sha is the manifest's`);
      assert.equal(onDisk.manifest_sha256, filesSha, `${runtime}: the manifest's sha is sha256 of its files map`);
    }
    assert.equal(bytes.go, bytes.js);
    assert.match(sides.go.result.ok.content, /^OK: all 2 files match$/);
    assert.match(sides.go.result.mismatch.content, /^MISMATCH: changed b\.txt; missing -$/);
    assertSameAfterNormalizing(sides);
  });

  test("incident: same result; the log line differs only in at and version (PE-4)", async (t) => {
    const sides = await differential(t, {
      run: (plugin) => outcome(plugin.callTool("incident", { kind: "note", component: "plugin", summary: "S10c differential", detail: "line 1\n--detail" })),
    });
    const lines = {};
    for (const [runtime, side] of Object.entries(sides)) {
      const logged = fs.readFileSync(incidentLog(side.sandbox), "utf8").trim().split("\n");
      assert.equal(logged.length, 1);
      lines[runtime] = JSON.parse(logged[0]);
      assert.match(lines[runtime].at, ISO_MS);
    }
    assert.match(lines.go.version, ORCH_VERSION);
    assert.equal(lines.js.version, "unknown");
    const comparable = (line) => ({ ...line, at: "$AT", version: "$VERSION" });
    assert.deepEqual(normalizePaths(comparable(lines.go), sides.go.sandbox), normalizePaths(comparable(lines.js), sides.js.sandbox));
    assertSameAfterNormalizing(sides);
  });

  test("worker_status over a seeded lane file", async (t) => {
    const sides = await differential(t, {
      seed: (sandbox) => {
        const dir = path.join(sandbox.project, ".orchestrator", "workers");
        fs.mkdirSync(dir, { recursive: true });
        // pid 0 is never alive, so process_alive is false on both sides.
        fs.writeFileSync(path.join(dir, "s10c.json"), JSON.stringify({ lane: "s10c", status: "finished", pid: 0, harness: "claude", brief: "briefs/s10c.md", run_id: "r-1", session_id: "s-1", started_at: "2026-10-19T10:00:00.000Z", updated_at: "2026-10-19T10:05:00.000Z" }, null, 2) + "\n");
      },
      run: async (plugin) => ({ all: await outcome(plugin.callTool("worker_status", {})), lane: await outcome(plugin.callTool("worker_status", { lane: "s10c" })) }),
    });
    assert.match(sides.go.result.all.content, /"lane": "s10c"/);
    assertSameAfterNormalizing(sides);
  });

  test("supervisor status over a seeded state, and with none", async (t) => {
    const sides = await differential(t, {
      seed: (sandbox) => {
        fs.mkdirSync(path.join(sandbox.project, ".orchestrator"), { recursive: true });
        fs.writeFileSync(path.join(sandbox.project, ".orchestrator", "supervisor-state.json"), JSON.stringify({ lanes: { s10c: { phase: "done", checked_at: "2026-10-19T10:00:00.000Z" } }, events: {} }, null, 2) + "\n");
      },
      run: async (plugin, sandbox) => {
        const seeded = await outcome(plugin.callTool("supervisor", { action: "status" }));
        fs.rmSync(path.join(sandbox.project, ".orchestrator", "supervisor-state.json"));
        return { seeded, none: await outcome(plugin.callTool("supervisor", { action: "status" })) };
      },
    });
    assert.match(sides.go.result.none.content, /not_started/);
    assertSameAfterNormalizing(sides);
  });

  test("context hook: a fresh cache pushes the same budget block", async (t) => {
    const sides = await differential(t, { run: (plugin) => plugin.context("conductor") });
    assert.equal(sides.go.result.length, 1);
    assertSameAfterNormalizing(sides);
  });

  test("context hook: no cache pushes nothing on either side", async (t) => {
    const sides = await differential(t, { usage: null, run: (plugin) => plugin.context("conductor") });
    assert.deepEqual(sides.js.result, []);
    assert.deepEqual(sides.go.result, []);
    // The missing cache started a collection against the failing fakes on both sides (the codex
    // app-server read waits out its 20 s timeout). Let both write their cache before the
    // sandboxes go, so a late write cannot recreate a removed temp root. The ceiling is three
    // times that timeout, so a loaded machine does not flake; it only costs time on failure.
    const cacheWritten = (side) => fs.existsSync(stateFile(side.sandbox, "usage.json"));
    assert.ok(await waitFor(() => cacheWritten(sides.js) && cacheWritten(sides.go), 60000), "both runtimes collect when there is no cache");
    await new Promise((r) => setTimeout(r, 500));
  });

  test("execute.before with a red OpenAI cache throws the same message", async (t) => {
    const sides = await differential(t, {
      usage: usageFixture("red"),
      run: async (plugin) => {
        try { await plugin.executeBefore({ tool: "subagent", input: { agent: "coder-oai" } }); return { thrown: null }; } catch (error) { return { thrown: String(error.message) }; }
      },
    });
    assert.match(sides.go.result.thrown, /^\[orchestrator\] OpenAI budget is red/);
    assertSameAfterNormalizing(sides);
  });
});
