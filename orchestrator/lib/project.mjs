// The commands that read or stop a project's existing state refuse to run where there is no project:
// an empty answer (or a stray .orchestrator/ created by a stop) in the wrong directory looks like success.
// Commands that create state (mailbox post, flip run|pause|gate, worker start, supervisor start) do not
// use this: they are how a project is born.
import fs from "node:fs";
import path from "node:path";

// One stat, so the directory cannot vanish between a check and a read. Only "nothing there" means
// "not a project"; any other failure (EACCES, ELOOP, ...) is real and propagates.
export function isProject(root) {
  try {
    return fs.statSync(path.join(root, ".orchestrator")).isDirectory();
  } catch (err) {
    if (err.code === "ENOENT" || err.code === "ENOTDIR") return false;
    throw err;
  }
}

// Exits 2 with one line naming the directory searched and the flag (--root or --cwd) that names another.
export function requireProject(root, flag) {
  if (isProject(root)) return;
  console.error(`not an orchestrator project: ${root} has no .orchestrator/ directory; cd into the project or pass ${flag} <project dir>`);
  process.exit(2);
}
