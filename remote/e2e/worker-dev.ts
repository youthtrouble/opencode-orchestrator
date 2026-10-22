// Runs the e2e Worker (remote/worker/wrangler.e2e.jsonc) under `wrangler dev --local` on a temp D1. Wrangler
// writes a .wrangler/ directory beside its config and in its working directory, so it runs on a temp copy of
// the Worker: nothing it writes lands in the repository.
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { cpSync, mkdirSync, symlinkSync } from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { RunCleanup } from "./cleanup.ts";

const REMOTE_DIR = fileURLToPath(new URL("../", import.meta.url));
const CONFIG = "wrangler.e2e.jsonc";
// What the e2e Worker needs to bundle and serve, under remote/: the Worker imports the protocol's schema
// and validator. Tests and deploy configs stay behind.
const REMOTE_FILES = [
  "protocol/v1",
  "protocol/validate.ts",
  ...["src", "public", "migrations", "test/e2e-entry.ts", CONFIG, "package.json"].map((path) => `worker/${path}`),
];
const READY_TIMEOUT_MS = 60_000;
// A failed build leaves wrangler accepting connections it never answers, so each probe gives up quickly.
const PROBE_TIMEOUT_MS = 1_000;
const STOP_TIMEOUT_MS = 10_000;

export class WorkerDev {
  readonly url: string;
  readonly #process: ChildProcess;
  readonly #output: string[];
  readonly #exited: Promise<void>;
  #stopped: Promise<void> | undefined;

  constructor(url: string, child: ChildProcess, output: string[]) {
    this.url = url;
    this.#process = child;
    this.#output = output;
    this.#exited = new Promise((resolve) => child.once("exit", () => resolve()));
  }

  /** Everything wrangler printed so far, stdout and stderr interleaved. */
  get output(): string {
    return this.#output.join("");
  }

  /**
   * Stops wrangler and the workerd it spawned: they share the process group wrangler leads. Wrangler may
   * exit before its workerd does, so the group is always killed once wrangler is gone. Only the first call
   * signals the group: once it is gone its id may belong to another.
   */
  stop(): Promise<void> {
    this.#stopped ??= this.#stopGroup();
    return this.#stopped;
  }

  async #stopGroup(): Promise<void> {
    const group = -this.#process.pid!;
    if (this.#process.exitCode === null && this.#process.signalCode === null) {
      signalGroup(group, "SIGTERM");
      const timedOut = await Promise.race([this.#exited.then(() => false), delay(STOP_TIMEOUT_MS).then(() => true)]);
      if (timedOut) signalGroup(group, "SIGKILL");
      await this.#exited;
    }
    signalGroup(group, "SIGKILL");
  }
}

/** Signals a process group; one already gone (ESRCH) is what stopping it wanted. */
function signalGroup(group: number, signal: NodeJS.Signals): void {
  try {
    process.kill(group, signal);
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
  }
}

/**
 * Copies the Worker into `dir`, applies its migrations to a D1 under `dir`, then starts `wrangler dev` on
 * `port` with `vars` (none of them a secret: the Worker holds only the machine token's hash and the public
 * key set) and resolves once it answers HTTP. `cleanup` owns the dev server from the moment it is spawned,
 * so an interruption while it is still starting stops it too.
 */
export async function startWorkerDev(
  dir: string,
  port: number,
  vars: Record<string, string>,
  cleanup: RunCleanup,
): Promise<WorkerDev> {
  const worker = copyWorker(join(dir, "remote"));
  const persistTo = join(dir, "d1");
  const env = wranglerEnv(dir);
  const migrate = spawnSync(
    "wrangler",
    ["d1", "migrations", "apply", "work-trains", "--local", "-c", CONFIG, "--persist-to", persistTo],
    { cwd: worker, env, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] },
  );
  if (migrate.status !== 0) throw new Error(`wrangler d1 migrations apply failed:\n${migrate.stdout}${migrate.stderr}`);

  const varArgs = Object.entries(vars).flatMap(([name, value]) => ["--var", `${name}:${value}`]);
  const child = spawn(
    "wrangler",
    ["dev", "-c", CONFIG, "--local", "--ip", "127.0.0.1", "--port", String(port), "--persist-to", persistTo,
      "--show-interactive-dev-session=false", ...varArgs],
    { cwd: worker, env, detached: true, stdio: ["ignore", "pipe", "pipe"] },
  );
  const output: string[] = [];
  child.stdout!.setEncoding("utf8").on("data", (chunk: string) => output.push(chunk));
  child.stderr!.setEncoding("utf8").on("data", (chunk: string) => output.push(chunk));
  const dev = new WorkerDev(`http://127.0.0.1:${port}`, child, output);
  cleanup.defer(() => dev.stop());
  try {
    await waitUntilAnswering(dev, child);
  } catch (error) {
    await dev.stop();
    throw error;
  }
  return dev;
}

/** Copies what the Worker needs into `remote`, keeping its layout, and returns the copy's Worker directory. */
function copyWorker(remote: string): string {
  for (const path of REMOTE_FILES) cpSync(join(REMOTE_DIR, path), join(remote, path), { recursive: true });
  const worker = join(remote, "worker");
  symlinkSync(join(REMOTE_DIR, "worker", "node_modules"), join(worker, "node_modules"));
  return worker;
}

/** Wrangler's own logs go under the run's directory too, and it reports nothing to Cloudflare. */
function wranglerEnv(dir: string): NodeJS.ProcessEnv {
  const logs = join(dir, "wrangler-logs");
  mkdirSync(logs, { recursive: true });
  return { ...process.env, WRANGLER_SEND_METRICS: "false", WRANGLER_LOG_PATH: logs };
}

async function waitUntilAnswering(dev: WorkerDev, child: ChildProcess): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`wrangler dev exited with ${child.exitCode}:\n${dev.output}`);
    try {
      await fetch(dev.url, { redirect: "manual", signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) });
      return;
    } catch (error) {
      // Nothing listening (a TypeError) or no answer in time: wrangler is still bundling or starting workerd.
      if (!(error instanceof TypeError) && !isTimeout(error)) throw error;
      await delay(250);
    }
  }
  throw new Error(`wrangler dev did not answer within ${READY_TIMEOUT_MS / 1000} s:\n${dev.output}`);
}

function isTimeout(error: unknown): boolean {
  return error instanceof DOMException && error.name === "TimeoutError";
}

/** A port free right now on loopback, for a server that binds it moments later. */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address() as { port: number };
      server.close(() => resolve(port));
    });
  });
}

export function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
