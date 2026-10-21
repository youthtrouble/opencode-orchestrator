// Screenshots of the Work Trains page from fixtures, for design review. Not a test (no `.test.ts`), so CI and
// the e2e glob never need Chrome. Run from the repository root:
//
//   node remote/e2e/screens.ts --out remote/docs/design/before [--chrome <path>]
//
// A loopback server serves remote/worker/public and answers GET /api/v1/summary with the current fixture,
// carrying the Worker's own security headers so a CSP violation shows as it would live. No Worker, D1, Access
// or orch runs, so no real data can reach a shot. Chrome is driven headless over the DevTools protocol with
// Node's global WebSocket: no dependencies. Chrome is not iOS Safari (no Dynamic Type, no safe-area insets,
// different font rasterising); the real-phone check is the owner's.
import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type Server } from "node:http";
import { homedir, tmpdir } from "node:os";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { compileSchema } from "../protocol/validate.ts";
import { SECURITY_HEADERS } from "../worker/src/http/headers.ts";
import { SCREEN_STATES, type Fixture } from "./screens/fixtures.ts";

const PUBLIC_DIR = fileURLToPath(new URL("../worker/public/", import.meta.url));
const SCHEMA_URL = new URL("../protocol/v1/schema.json", import.meta.url);
const CONTENT_TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".webmanifest": "application/manifest+json",
};

interface Device {
  name: string;
  width: number;
  height: number;
  mobile: boolean;
}

const DEVICES: readonly Device[] = [
  { name: "phone", width: 390, height: 844, mobile: true },
  { name: "desktop", width: 1280, height: 900, mobile: false },
];
const DEVICE_SCALE_FACTOR = 2;

const CHROME_START_TIMEOUT_MS = 15_000;
/** How long Chrome gets to exit after SIGTERM before it is SIGKILLed. */
const CHROME_STOP_GRACE_MS = 3_000;
const CDP_COMMAND_TIMEOUT_MS = 15_000;
const PAGE_READY_TIMEOUT_MS = 10_000;
const PAGE_READY_POLL_MS = 100;
/** The page's header text until its first summary renders (remote/worker/public/index.html). */
const LOADING_TEXT = "Loading…";
/** Exit codes for a run stopped by a signal, as a shell reports them (128 + signal number). */
const SIGNAL_EXIT_CODES = { SIGINT: 130, SIGTERM: 143 } as const;

class ScreensError extends Error {
  override name = "ScreensError";
}

/**
 * What a run has opened, released in reverse order exactly once: by main's finally, or by a signal handler
 * when the run is interrupted (SIGTERM to Node alone would otherwise orphan headless Chrome).
 */
class Cleanup {
  readonly #releases: (() => void | Promise<void>)[] = [];
  #running: Promise<void> | undefined;

  add(release: () => void | Promise<void>): void {
    this.#releases.push(release);
  }

  run(): Promise<void> {
    this.#running ??= this.#releaseAll();
    return this.#running;
  }

  async #releaseAll(): Promise<void> {
    for (const release of this.#releases.reverse()) {
      try {
        await release();
      } catch (error) {
        console.error("cleanup failed:", error);
      }
    }
  }
}

async function main(): Promise<void> {
  const { out, chrome } = parseCli();
  validateFixtures();
  mkdirSync(out, { recursive: true });

  const cleanup = new Cleanup();
  const stopOnSignal = (signal: keyof typeof SIGNAL_EXIT_CODES) => {
    void cleanup.run().finally(() => process.exit(SIGNAL_EXIT_CODES[signal]));
  };
  process.once("SIGINT", stopOnSignal).once("SIGTERM", stopOnSignal);
  try {
    const fixtureServer = await startFixtureServer();
    cleanup.add(() => fixtureServer.close());
    const profileDir = mkdtempSync(join(tmpdir(), "work-trains-screens-"));
    cleanup.add(() => rmSync(profileDir, { recursive: true, force: true }));
    const chromeProcess = startChrome(chrome, profileDir);
    cleanup.add(() => chromeProcess.stop());
    const browser = await connectToChrome(chromeProcess.child);
    cleanup.add(() => browser.close());

    const tab = await browser.openTab();
    const foreignRequests = await watchForeignRequests(tab, fixtureServer.origin);
    const shots: { file: string; png: Buffer }[] = [];
    for (const [state, fixture] of Object.entries(SCREEN_STATES)) {
      fixtureServer.serve(fixture);
      for (const device of DEVICES) {
        shots.push({ file: join(out, `${state}-${device.name}.png`), png: await shoot(tab, `${fixtureServer.origin}/`, device) });
      }
    }
    // Checked before anything is written, so a run that left loopback leaves no shots behind.
    if (foreignRequests.length > 0) {
      throw new ScreensError(`the page requested other origins than the fixture server: ${foreignRequests.join(", ")}`);
    }
    for (const { file, png } of shots) {
      writeFileSync(file, png);
      console.log(file);
    }
  } finally {
    await cleanup.run();
    process.off("SIGINT", stopOnSignal).off("SIGTERM", stopOnSignal);
  }
}

function parseCli(): { out: string; chrome: string } {
  const { values } = parseArgs({ options: { out: { type: "string" }, chrome: { type: "string" } } });
  if (values.out === undefined) throw new ScreensError("usage: node remote/e2e/screens.ts --out <dir> [--chrome <path>]");
  return { out: values.out, chrome: values.chrome ?? findChrome() };
}

/** `--chrome`, else $CHROME, else Google Chrome, else Playwright's chrome-headless-shell. */
function findChrome(): string {
  if (process.env.CHROME) return process.env.CHROME;
  const googleChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
  if (existsSync(googleChrome)) return googleChrome;
  const playwrightCache = join(homedir(), "Library", "Caches", "ms-playwright");
  const shells = existsSync(playwrightCache)
    ? readdirSync(playwrightCache).filter((name) => name.startsWith("chromium_headless_shell-")).sort().reverse()
    : [];
  for (const shell of shells) {
    const binary = join(playwrightCache, shell, "chrome-headless-shell-mac-arm64", "chrome-headless-shell");
    if (existsSync(binary)) return binary;
  }
  throw new ScreensError("no Chrome found: pass --chrome <path> or set CHROME");
}

/** Every fixture must be a Summary the Worker could send; one that drifted from the protocol fails the run. */
function validateFixtures(): void {
  const validate = compileSchema(JSON.parse(readFileSync(SCHEMA_URL, "utf8")));
  const nowMs = Date.now();
  const failures = Object.entries(SCREEN_STATES).flatMap(([state, fixture]) => {
    const result = validate("Summary", fixture(nowMs));
    return result.ok ? [] : result.errors.map(({ path, message }) => `${state}: ${path || "/"} ${message}`);
  });
  if (failures.length > 0) throw new ScreensError(`fixtures are not valid Summaries:\n${failures.join("\n")}`);
}

// ---- fixture server ---------------------------------------------------------------------------

interface FixtureServer {
  origin: string;
  /** The fixture every later GET /api/v1/summary answers with, built fresh per request. */
  serve(fixture: Fixture): void;
  close(): void;
}

function startFixtureServer(): Promise<FixtureServer> {
  let current: Fixture | null = null;
  const server: Server = createServer((request, response) => {
    const path = new URL(request.url ?? "/", "http://localhost").pathname;
    const headers = { ...SECURITY_HEADERS, "Cache-Control": "no-store" };
    if (request.method === "GET" && path === "/api/v1/summary" && current !== null) {
      response.writeHead(200, { ...headers, "Content-Type": "application/json" });
      response.end(JSON.stringify(current(Date.now())));
      return;
    }
    const asset = publicAsset(path);
    if (request.method === "GET" && asset !== null) {
      response.writeHead(200, { ...headers, "Content-Type": CONTENT_TYPES[extname(asset)] });
      response.end(readFileSync(join(PUBLIC_DIR, asset)));
      return;
    }
    response.writeHead(404, headers);
    response.end();
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address() as { port: number };
      resolve({
        origin: `http://127.0.0.1:${port}`,
        serve: (fixture) => {
          current = fixture;
        },
        close: () => {
          server.close();
          server.closeAllConnections();
        },
      });
    });
  });
}

/** The file under public/ a path names, or null; only the page's own top-level files are served. */
function publicAsset(path: string): string | null {
  const name = path === "/" ? "index.html" : path.slice(1);
  const servable = readdirSync(PUBLIC_DIR).filter((file) => extname(file) in CONTENT_TYPES);
  return servable.includes(name) ? name : null;
}

// ---- Chrome over the DevTools protocol --------------------------------------------------------

type CdpParams = Record<string, unknown>;
type CdpEventListener = (params: unknown) => void;

/** One DevTools WebSocket. Commands for a tab carry its flattened session id. */
class CdpConnection {
  readonly #socket: WebSocket;
  readonly #pending = new Map<number, { resolve: (result: unknown) => void; reject: (error: Error) => void }>();
  readonly #listeners = new Map<string, CdpEventListener[]>();
  #nextId = 1;

  private constructor(socket: WebSocket) {
    this.#socket = socket;
    socket.addEventListener("message", (message) => this.#receive(JSON.parse(String(message.data))));
    socket.addEventListener("close", () => {
      for (const { reject } of this.#pending.values()) reject(new ScreensError("Chrome closed the DevTools connection"));
      this.#pending.clear();
    });
  }

  static open(url: string): Promise<CdpConnection> {
    const socket = new WebSocket(url);
    return new Promise((resolve, reject) => {
      socket.addEventListener("open", () => resolve(new CdpConnection(socket)), { once: true });
      socket.addEventListener("error", () => reject(new ScreensError(`cannot connect to Chrome at ${url}`)), { once: true });
    });
  }

  /** Resolves with the command's result; rejects on a DevTools error, a closed connection or no answer in time. */
  send<Result>(method: string, params: CdpParams = {}, sessionId?: string): Promise<Result> {
    if (this.#socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new ScreensError(`DevTools ${method}: the connection to Chrome is closed`));
    }
    const id = this.#nextId++;
    return new Promise<Result>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.#pending.delete(id);
        reject(new ScreensError(`DevTools ${method}: no answer within ${CDP_COMMAND_TIMEOUT_MS / 1000} s`));
      }, CDP_COMMAND_TIMEOUT_MS);
      this.#pending.set(id, {
        resolve: (result) => {
          clearTimeout(timer);
          resolve(result as Result);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      this.#socket.send(JSON.stringify({ id, method, params, sessionId }));
    });
  }

  on(method: string, sessionId: string, listener: CdpEventListener): void {
    const key = `${sessionId}:${method}`;
    this.#listeners.set(key, [...(this.#listeners.get(key) ?? []), listener]);
  }

  close(): void {
    this.#socket.close();
  }

  #receive(message: { id?: number; result?: unknown; error?: { message: string }; method?: string; params?: unknown; sessionId?: string }): void {
    if (message.id !== undefined) {
      const pending = this.#pending.get(message.id);
      this.#pending.delete(message.id);
      if (message.error) pending?.reject(new ScreensError(`DevTools: ${message.error.message}`));
      else pending?.resolve(message.result);
      return;
    }
    for (const listener of this.#listeners.get(`${message.sessionId}:${message.method}`) ?? []) listener(message.params);
  }
}

interface Tab {
  send<Result>(method: string, params?: CdpParams): Promise<Result>;
  on<Params>(method: string, listener: (params: Params) => void): void;
}

interface Browser {
  openTab(): Promise<Tab>;
  /** Closes the DevTools connection; the Chrome process is stopped separately. */
  close(): void;
}

interface ChromeProcess {
  child: ChildProcess;
  stop(): Promise<void>;
}

function startChrome(binary: string, profileDir: string): ChromeProcess {
  // Chrome's own background traffic (updates, sync, safe browsing) is switched off, and everything but loopback
  // goes to a proxy that refuses connections, so a request off loopback fails instead of leaving the machine.
  const child = spawn(binary, [
    "--headless",
    "--remote-debugging-port=0",
    `--user-data-dir=${profileDir}`,
    "--proxy-server=127.0.0.1:1",
    "--proxy-bypass-list=127.0.0.1",
    "--hide-scrollbars",
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-background-networking",
    "--disable-component-update",
    "--disable-sync",
    "--disable-extensions",
    "--disable-default-apps",
    "--disable-domain-reliability",
    "--disable-client-side-phishing-detection",
    "about:blank",
  ], { stdio: ["ignore", "ignore", "pipe"] });
  // Registered at once, so an exit at any later moment is seen. A spawn failure emits "error" and no "exit".
  const exited = new Promise<void>((resolve) => {
    child.once("exit", () => resolve());
    child.once("error", () => resolve());
  });
  return { child, stop: () => stopChrome(child, exited) };
}

async function connectToChrome(child: ChildProcess): Promise<Browser> {
  const connection = await CdpConnection.open(await devToolsUrl(child));
  return {
    async openTab() {
      const { targetId } = await connection.send<{ targetId: string }>("Target.createTarget", { url: "about:blank" });
      const { sessionId } = await connection.send<{ sessionId: string }>("Target.attachToTarget", { targetId, flatten: true });
      return {
        send: (method, params) => connection.send(method, params, sessionId),
        on: (method, listener) => connection.on(method, sessionId, listener as CdpEventListener),
      };
    },
    close: () => connection.close(),
  };
}

/** SIGTERM, then SIGKILL if Chrome is still running after a grace period; resolves once it has exited. */
async function stopChrome(child: ChildProcess, exited: Promise<void>): Promise<void> {
  const running = child.pid !== undefined && child.exitCode === null && child.signalCode === null;
  if (!running) return;
  child.kill("SIGTERM");
  let graceTimer: ReturnType<typeof setTimeout> | undefined;
  const graceOver = new Promise<"grace over">((resolve) => {
    graceTimer = setTimeout(() => resolve("grace over"), CHROME_STOP_GRACE_MS);
  });
  const outcome = await Promise.race([exited, graceOver]);
  clearTimeout(graceTimer);
  if (outcome === "grace over") {
    child.kill("SIGKILL");
    await exited;
  }
}

/**
 * Chrome prints "DevTools listening on ws://..." on stderr once its debugging port is open. Its stderr is
 * drained for the rest of the run, so a chatty Chrome cannot block on a full pipe.
 */
function devToolsUrl(child: ChildProcess): Promise<string> {
  return new Promise((resolve, reject) => {
    let stderr = "";
    const settle = () => {
      clearTimeout(timer);
      child.off("error", onError);
      child.off("exit", onExit);
      child.stderr!.off("data", onData).resume();
    };
    const fail = (reason: string) => {
      settle();
      reject(new ScreensError(stderr.trim() === "" ? reason : `${reason}:\n${stderr.trim()}`));
    };
    const onError = (error: Error) => fail(`cannot start Chrome (${error.message})`);
    const onExit = (code: number | null) => fail(`Chrome exited with ${code}`);
    const onData = (chunk: string) => {
      stderr += chunk;
      const match = /DevTools listening on (ws:\/\/\S+)/.exec(stderr);
      if (match === null) return;
      settle();
      resolve(match[1]);
    };
    const timer = setTimeout(() => fail(`Chrome did not open DevTools within ${CHROME_START_TIMEOUT_MS / 1000} s`), CHROME_START_TIMEOUT_MS);
    child.on("error", onError);
    child.on("exit", onExit);
    child.stderr!.setEncoding("utf8").on("data", onData);
  });
}

/** Collects every URL the tab requests outside `origin` (data: URLs are inline, not requests). */
async function watchForeignRequests(tab: Tab, origin: string): Promise<string[]> {
  const foreign: string[] = [];
  tab.on<{ request: { url: string } }>("Network.requestWillBeSent", ({ request }) => {
    if (!request.url.startsWith(`${origin}/`) && !request.url.startsWith("data:")) foreign.push(request.url);
  });
  await tab.send("Network.enable");
  return foreign;
}

/** Loads the page at the device's size, in light mode, and returns a PNG of its viewport once the first summary renders. */
async function shoot(tab: Tab, url: string, device: Device): Promise<Buffer> {
  await tab.send("Emulation.setDeviceMetricsOverride", {
    width: device.width,
    height: device.height,
    deviceScaleFactor: DEVICE_SCALE_FACTOR,
    mobile: device.mobile,
  });
  // Pinned so a shot does not depend on the host's appearance setting; the redesign is light only.
  await tab.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
  const { errorText } = await tab.send<{ errorText?: string }>("Page.navigate", { url });
  if (errorText) throw new ScreensError(`cannot load ${url}: ${errorText}`);
  await waitForFirstRender(tab);
  const { data } = await tab.send<{ data: string }>("Page.captureScreenshot", { format: "png" });
  return Buffer.from(data, "base64");
}

async function waitForFirstRender(tab: Tab): Promise<void> {
  const rendered = `document.readyState === "complete" && document.getElementById("mac-seen")?.textContent !== ${JSON.stringify(LOADING_TEXT)}`;
  const deadline = Date.now() + PAGE_READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if ((await evaluate(tab, rendered)) === true) {
      // Two animation frames: the render has been laid out and painted, not just put in the DOM.
      await evaluate(tab, "new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))");
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, PAGE_READY_POLL_MS));
  }
  throw new ScreensError(`the page did not render a summary within ${PAGE_READY_TIMEOUT_MS / 1000} s`);
}

/** The value of `expression` in the page (awaited if it is a promise); a thrown exception fails the run. */
async function evaluate(tab: Tab, expression: string): Promise<unknown> {
  const { result, exceptionDetails } = await tab.send<{
    result: { value?: unknown };
    exceptionDetails?: { text: string; exception?: { description?: string } };
  }>("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  if (exceptionDetails) {
    throw new ScreensError(`the page threw evaluating ${expression}: ${exceptionDetails.exception?.description ?? exceptionDetails.text}`);
  }
  return result.value;
}

try {
  await main();
} catch (error) {
  console.error(error instanceof ScreensError ? error.message : error);
  process.exitCode = 1;
}
