// An interrupted e2e run stops its detached Worker and removes its temp directory (Sol review R1). A fake
// wrangler stands in for the real one: its migrations succeed and `dev` answers HTTP on its port, never
// answers, or fails at once, so interruptions after readiness and while it is pending are covered, and so is a
// failed start.
//
// The test never signals a process by a recorded pid, which may belong to another process by then (Sol review
// R4). It owns each driver through its ChildProcess handle and stdin, and each fake through a lifeline: a Unix
// socket the fake connects to and exits when it closes. The lifeline closing from the fake's side is also the
// proof that the fake is gone. Both also end themselves after a bounded lifetime if their owner disappears.
import assert from "node:assert/strict";
import { spawn, type ChildProcessByStdio } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Socket } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Readable, Writable } from "node:stream";
import { fileURLToPath } from "node:url";
import { after, afterEach, before, describe, it } from "node:test";
import { delay } from "./worker-dev.ts";

const DRIVER = fileURLToPath(new URL("./fixtures/interrupted-run.ts", import.meta.url));
const STARTED_TIMEOUT_MS = 30_000;
/** How long a dead process's lifeline or port may take to close. */
const GONE_TIMEOUT_MS = 10_000;
const FAKE_LIFETIME_MS = 60_000;

// CommonJS so it runs whatever the scratch directory's package scope says. It lives only while the test holds
// its lifeline, and never past FAKE_WRANGLER_LIFETIME_MS.
const FAKE_WRANGLER = `#!/usr/bin/env node
const { createServer } = require("node:http");
const { connect } = require("node:net");
const args = process.argv.slice(2);
if (args[0] === "d1") process.exit(0);
const mode = process.env.FAKE_WRANGLER_MODE;
if (mode === "fail") {
  console.error("fake wrangler dev failed to start");
  process.exit(1);
}
setTimeout(() => process.exit(1), Number(process.env.FAKE_WRANGLER_LIFETIME_MS));
const lifeline = connect(process.env.FAKE_WRANGLER_LIFELINE, () =>
  lifeline.write("hello " + process.env.FAKE_WRANGLER_CASE + " " + process.pid + "\\n"));
lifeline.on("close", () => process.exit(1));
lifeline.on("error", () => process.exit(1));
if (mode === "ready") {
  const port = Number(args[args.indexOf("--port") + 1]);
  createServer((request, response) => response.end("fake wrangler")).listen(port, "127.0.0.1");
}
`;

type Mode = "ready" | "pending" | "fail";
type Exit = { code: number | null; signal: NodeJS.Signals | null };

/** A fake wrangler dev, known by the lifeline it opened rather than by its pid. */
interface Fake {
  pid: number;
  lifeline: Socket;
  /** Settles when the lifeline closes: from the fake's side, that means it exited. */
  closed: Promise<void>;
}

interface Run {
  driver: ChildProcessByStdio<Writable, Readable, Readable>;
  runDir: string;
  exited: Promise<Exit>;
  stdout(): string;
  stderr(): string;
}

describe("an interrupted e2e run", { timeout: 120_000 }, () => {
  const scratch = mkdtempSync(join(tmpdir(), "work-trains-cleanup-"));
  const bin = join(scratch, "bin");
  mkdirSync(bin);
  writeFileSync(join(bin, "wrangler"), FAKE_WRANGLER);
  chmodSync(join(bin, "wrangler"), 0o755);
  const lifelinePath = join(scratch, "lifeline.sock");
  const lifelines = createServer();
  const fakes = new Map<string, Fake>();
  const runs = new Set<Run>();

  lifelines.on("connection", (socket) => {
    const closed = new Promise<void>((resolve) => socket.once("close", () => resolve()));
    let received = "";
    socket.setEncoding("utf8").on("data", (chunk: string) => {
      received += chunk;
      if (!received.includes("\n")) return;
      const [name, pid] = parseHello(received.slice(0, received.indexOf("\n")));
      fakes.set(name, { pid, lifeline: socket, closed });
    });
  });

  before(() => new Promise<void>((resolve) => lifelines.listen(lifelinePath, resolve)));

  // A failed case may leave its driver running: stop it through its handle, which cannot name another process.
  afterEach(async () => {
    for (const run of runs) {
      if (run.driver.exitCode === null && run.driver.signalCode === null) run.driver.kill("SIGKILL");
      await run.exited;
    }
    runs.clear();
  });

  // Every fake still alive exits once its lifeline closes.
  after(async () => {
    for (const fake of fakes.values()) fake.lifeline.destroy();
    await new Promise((resolve) => lifelines.close(resolve));
    rmSync(scratch, { recursive: true, force: true });
  });

  const launch = (name: string, mode: Mode, fakeLifetimeMs = FAKE_LIFETIME_MS): Run => {
    const runDir = join(scratch, name);
    mkdirSync(runDir);
    const driver = spawn(process.execPath, [DRIVER, runDir], {
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        FAKE_WRANGLER_MODE: mode,
        FAKE_WRANGLER_CASE: name,
        FAKE_WRANGLER_LIFELINE: lifelinePath,
        FAKE_WRANGLER_LIFETIME_MS: String(fakeLifetimeMs),
      },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    driver.stdout.setEncoding("utf8").on("data", (chunk: string) => (stdout += chunk));
    driver.stderr.setEncoding("utf8").on("data", (chunk: string) => (stderr += chunk));
    const exited = new Promise<Exit>((resolve) => driver.once("exit", (code, signal) => resolve({ code, signal })));
    const run = { driver, runDir, exited, stdout: () => stdout, stderr: () => stderr };
    runs.add(run);
    return run;
  };

  const fakeOf = async (name: string, run: Run): Promise<Fake> => {
    await until(() => fakes.has(name), () => `the fake wrangler never called in:\n${run.stdout()}${run.stderr()}`);
    return fakes.get(name)!;
  };

  const readyUrl = async (run: Run): Promise<string> => {
    await until(() => /^ready \S+$/m.test(run.stdout()), () => `never ready:\n${run.stdout()}${run.stderr()}`);
    return run.stdout().match(/^ready (\S+)$/m)![1];
  };

  const cases: [Exclude<Mode, "fail">, NodeJS.Signals][] = [
    ["ready", "SIGTERM"],
    ["ready", "SIGINT"],
    ["pending", "SIGTERM"],
    ["pending", "SIGINT"],
  ];
  for (const [mode, signal] of cases) {
    it(`stops the Worker and removes the run directory on ${signal} with readiness ${mode}`, async () => {
      const run = launch(`${mode}-${signal}`, mode);
      const url = mode === "ready" ? await readyUrl(run) : undefined;
      const fake = await fakeOf(`${mode}-${signal}`, run);
      if (url) assert.equal((await fetch(url)).status, 200, "the fake Worker answers before the signal");

      run.driver.kill(signal);
      assert.deepEqual(await run.exited, { code: null, signal }, `the driver dies by ${signal}:\n${run.stderr()}`);
      await within(fake.closed, `fake wrangler ${fake.pid} still holds its lifeline`);
      if (url) await assert.rejects(fetch(url), TypeError, "nothing listens on the Worker's port");
      assert.equal(existsSync(run.runDir), false, "the run directory is gone");
    });
  }

  it("cleans up when wrangler dev fails to start", async () => {
    const run = launch("fail", "fail");
    const exit = await run.exited;
    assert.deepEqual(exit, { code: 1, signal: null }, run.stderr());
    assert.match(run.stderr(), /wrangler dev exited with 1/);
    assert.equal(existsSync(run.runDir), false, "the run directory is gone");
  });

  it("cleans up when its owner lets go of it", async () => {
    const run = launch("abandoned", "pending");
    const fake = await fakeOf("abandoned", run);

    run.driver.stdin.end();
    const exit = await within(run.exited, "the driver outlived its closed stdin");
    assert.deepEqual(exit, { code: 1, signal: null }, run.stderr());
    await within(fake.closed, `fake wrangler ${fake.pid} still holds its lifeline`);
    assert.equal(existsSync(run.runDir), false, "the run directory is gone");
  });

  it("a fake orphaned by a killed driver exits when its lifeline closes", async () => {
    const run = launch("orphan-lifeline", "ready");
    const url = await readyUrl(run);
    const fake = await fakeOf("orphan-lifeline", run);

    run.driver.kill("SIGKILL");
    await run.exited;
    assert.equal((await fetch(url)).status, 200, "SIGKILL leaves no cleanup: the fake still answers");
    fake.lifeline.destroy();
    await stopsAnswering(url);
  });

  it("a fake orphaned by a killed driver exits at the end of its lifetime", async () => {
    const run = launch("orphan-lifetime", "ready", 1_500);
    const url = await readyUrl(run);
    const fake = await fakeOf("orphan-lifetime", run);

    run.driver.kill("SIGKILL");
    await run.exited;
    await within(fake.closed, `fake wrangler ${fake.pid} outlived its lifetime`);
    await assert.rejects(fetch(url), TypeError, "nothing listens on the Worker's port");
  });
});

/** Reads the fake's `hello <case> <pid>` line; the pid is only reported, so it must at least be one. */
function parseHello(line: string): [string, number] {
  const match = /^hello (\S+) (\d+)$/.exec(line);
  assert.ok(match, `a fake's lifeline greeting: ${JSON.stringify(line)}`);
  const pid = Number(match[2]);
  assert.ok(Number.isSafeInteger(pid) && pid >= 2, `a fake's pid: ${match[2]}`);
  return [match[1], pid];
}

async function until(condition: () => boolean, failure: () => string): Promise<void> {
  const deadline = Date.now() + STARTED_TIMEOUT_MS;
  while (!condition()) {
    if (Date.now() > deadline) assert.fail(failure());
    await delay(50);
  }
}

/** Our side closed the lifeline, so the fake's own exit shows only as its port going quiet. */
async function stopsAnswering(url: string): Promise<void> {
  const deadline = Date.now() + GONE_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      await fetch(url, { signal: AbortSignal.timeout(1_000) });
    } catch (error) {
      if (error instanceof TypeError) return;
      throw error;
    }
    await delay(50);
  }
  assert.fail(`${url} still answers after its lifeline closed`);
}

/** What `promise` settles to, failing the test if that takes over GONE_TIMEOUT_MS. */
async function within<T>(promise: Promise<T>, failure: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new assert.AssertionError({ message: failure })), GONE_TIMEOUT_MS);
  });
  try {
    return await Promise.race([promise, timeout]);
  } finally {
    clearTimeout(timer);
  }
}
