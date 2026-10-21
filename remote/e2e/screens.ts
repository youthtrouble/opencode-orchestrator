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
const PAGE_READY_TIMEOUT_MS = 10_000;
const PAGE_READY_POLL_MS = 100;
/** The page's header text until its first summary renders (remote/worker/public/index.html). */
const LOADING_TEXT = "Loading…";

class ScreensError extends Error {
  override name = "ScreensError";
}

async function main(): Promise<void> {
  const { out, chrome } = parseCli();
  validateFixtures();
  mkdirSync(out, { recursive: true });

  const fixtureServer = await startFixtureServer();
  const profileDir = mkdtempSync(join(tmpdir(), "work-trains-screens-"));
  const browser = await launchChrome(chrome, profileDir);
  try {
    const tab = await browser.openTab();
    const foreignRequests = await watchForeignRequests(tab, fixtureServer.origin);
    for (const [state, fixture] of Object.entries(SCREEN_STATES)) {
      fixtureServer.serve(fixture);
      for (const device of DEVICES) {
        const file = join(out, `${state}-${device.name}.png`);
        writeFileSync(file, await shoot(tab, `${fixtureServer.origin}/`, device));
        console.log(file);
      }
    }
    if (foreignRequests.length > 0) {
      throw new ScreensError(`the page requested other origins than the fixture server: ${foreignRequests.join(", ")}`);
    }
  } finally {
    await browser.close();
    fixtureServer.close();
    rmSync(profileDir, { recursive: true, force: true });
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
        close: () => server.close(),
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
type CdpEventListener = (params: any) => void;

/** One DevTools WebSocket. Commands for a tab carry its flattened session id. */
class CdpConnection {
  readonly #socket: WebSocket;
  readonly #pending = new Map<number, { resolve: (result: any) => void; reject: (error: Error) => void }>();
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

  send(method: string, params: CdpParams = {}, sessionId?: string): Promise<any> {
    const id = this.#nextId++;
    this.#socket.send(JSON.stringify({ id, method, params, sessionId }));
    return new Promise((resolve, reject) => this.#pending.set(id, { resolve, reject }));
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
  send(method: string, params?: CdpParams): Promise<any>;
  on(method: string, listener: CdpEventListener): void;
}

interface Browser {
  openTab(): Promise<Tab>;
  close(): Promise<void>;
}

async function launchChrome(binary: string, profileDir: string): Promise<Browser> {
  // Chrome's own background traffic (updates, sync, safe browsing) is switched off: the run stays on loopback.
  const child = spawn(binary, [
    "--headless",
    "--remote-debugging-port=0",
    `--user-data-dir=${profileDir}`,
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
  const connection = await CdpConnection.open(await devToolsUrl(child)).catch((error) => {
    child.kill();
    throw error;
  });
  const exited = new Promise<void>((resolve) => child.once("exit", () => resolve()));
  return {
    async openTab() {
      const { targetId } = await connection.send("Target.createTarget", { url: "about:blank" });
      const { sessionId } = await connection.send("Target.attachToTarget", { targetId, flatten: true });
      return {
        send: (method, params) => connection.send(method, params, sessionId),
        on: (method, listener) => connection.on(method, sessionId, listener),
      };
    },
    async close() {
      connection.close();
      if (child.exitCode === null && child.signalCode === null) child.kill();
      await exited;
    },
  };
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
      child.kill();
      reject(new ScreensError(`${reason}:\n${stderr}`));
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
  tab.on("Network.requestWillBeSent", ({ request }: { request: { url: string } }) => {
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
  await tab.send("Page.navigate", { url });
  await waitForFirstRender(tab);
  const { data } = await tab.send("Page.captureScreenshot", { format: "png" });
  return Buffer.from(data, "base64");
}

async function waitForFirstRender(tab: Tab): Promise<void> {
  const rendered = `document.readyState === "complete" && document.getElementById("mac-seen")?.textContent !== ${JSON.stringify(LOADING_TEXT)}`;
  const deadline = Date.now() + PAGE_READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    const { result } = await tab.send("Runtime.evaluate", { expression: rendered, returnByValue: true });
    if (result.value === true) {
      // Two animation frames: the render has been laid out and painted, not just put in the DOM.
      await tab.send("Runtime.evaluate", {
        expression: "new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))",
        awaitPromise: true,
      });
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, PAGE_READY_POLL_MS));
  }
  throw new ScreensError(`the page did not render a summary within ${PAGE_READY_TIMEOUT_MS / 1000} s`);
}

try {
  await main();
} catch (error) {
  console.error(error instanceof ScreensError ? error.message : error);
  process.exitCode = 1;
}
