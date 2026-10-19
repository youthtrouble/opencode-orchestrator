import { spawn } from "node:child_process";

// A child that never reads fd 0 and never exits would otherwise hang node --test.
const LATE_STDIN_TIMEOUT_MS = 30_000;

// Preloaded into the child: wraps fs.readFileSync so that a read of fd 0 first signals on fd 3
// that the child is about to read its stdin. It patches the default `fs` object, so code under
// test must call `fs.readFileSync(0, ...)`; a named `readFileSync` import bypasses the signal.
const signalStdinRead = `
import fs from "node:fs";
const readFileSync = fs.readFileSync;
fs.readFileSync = (file, ...rest) => {
  if (file === 0) fs.writeSync(3, "reading");
  return readFileSync(file, ...rest);
};
`;

/**
 * Run `node ...args` and write `input` to its stdin pipe late: only once the child signals that
 * it is reading fd 0, so the read finds the pipe open and empty, as a bridge does when its worker
 * runner has not written the prompt yet (incident e80dd9b226b2). Resolves to the exit code and
 * output; rejects if the child has not exited within LATE_STDIN_TIMEOUT_MS (the child is killed).
 */
export function runWithLateStdin(args, input, { env = process.env } = {}) {
  const preload = `--import=data:text/javascript,${encodeURIComponent(signalStdinRead)}`;
  const child = spawn(process.execPath, [preload, ...args], { env, stdio: ["pipe", "pipe", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  // EPIPE when the child exits between signalling and our write. Ignored on purpose: the exit
  // code and stderr resolved on 'close' carry the child's failure to the test.
  child.stdin.on("error", () => {});
  child.stdio[3].once("data", () => child.stdin.end(input));
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error(`runWithLateStdin: child did not exit within ${LATE_STDIN_TIMEOUT_MS} ms (node ${args.join(" ")}); killed it`));
    }, LATE_STDIN_TIMEOUT_MS);
    child.on("error", (error) => { clearTimeout(timer); reject(error); });
    child.on("close", (code) => { clearTimeout(timer); resolve({ code, stdout, stderr }); });
  });
}
