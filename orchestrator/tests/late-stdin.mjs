import { spawn } from "node:child_process";

// Preloaded into the child: wraps fs.readFileSync so that a read of fd 0 first signals on fd 3
// that the child is about to read its stdin.
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
 * output.
 */
export function runWithLateStdin(args, input, { env = process.env } = {}) {
  const preload = `--import=data:text/javascript,${encodeURIComponent(signalStdinRead)}`;
  const child = spawn(process.execPath, [preload, ...args], { env, stdio: ["pipe", "pipe", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  child.stdio[3].once("data", () => child.stdin.end(input));
  return new Promise((resolve, reject) => {
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
}
