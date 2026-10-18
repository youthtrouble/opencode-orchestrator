// Minimal JSON-RPC client for `codex app-server` (stdio). Zero dependencies.
// Used to read ChatGPT/Codex plan + rate limits without spending a model turn.
import { spawn } from "node:child_process";
import readline from "node:readline";

export async function withCodexAppServer(fn, { timeoutMs = 20000, codexBin = "codex" } = {}) {
  const proc = spawn(codexBin, ["app-server"], { stdio: ["pipe", "pipe", "pipe"] });
  const rl = readline.createInterface({ input: proc.stdout });
  let nextId = 0;
  const pending = new Map();
  let stderr = "";
  proc.stderr.on("data", (c) => (stderr += c));

  const request = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++nextId;
      pending.set(id, { resolve, reject, method });
      proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
    });
  const notify = (method, params = {}) =>
    proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", method, params }) + "\n");

  rl.on("line", (line) => {
    let msg;
    try { msg = JSON.parse(line); } catch { return; }
    if (msg.id !== undefined && pending.has(msg.id)) {
      const p = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? p.reject(Object.assign(new Error(msg.error.message || p.method), { rpc: msg.error })) : p.resolve(msg.result);
    }
  });

  const timer = setTimeout(() => {
    for (const p of pending.values()) p.reject(new Error("codex app-server timeout"));
    pending.clear();
    proc.kill();
  }, timeoutMs);

  try {
    await request("initialize", {
      clientInfo: { name: "opencode-orchestrator", title: "opencode orchestrator", version: "0.1.0" },
      capabilities: { experimentalApi: false },
    });
    notify("initialized", {});
    return await fn({ request, notify });
  } finally {
    clearTimeout(timer);
    proc.stdin.end();
    proc.kill();
  }
}

export async function readCodexAccountAndLimits(opts) {
  return withCodexAppServer(async ({ request }) => {
    const account = await request("account/read", {});
    const limits = await request("account/rateLimits/read", { excludeResetCreditDetails: true });
    return { account, limits };
  }, opts);
}
