// Atomic, owner-only file replacement shared by the scripts that write private state.
import { randomInt } from "node:crypto";
import fs from "node:fs";

// Random names to try before giving up on an exclusive temp: a collision means another writer's
// temp, which is never touched, so each one costs only a fresh name.
const TEMP_NAME_ATTEMPTS = 8;

// Replaces file with text through an exclusive owner-only sibling temp and a rename, so a symlink
// at file is replaced, never followed, and a reader never sees a partial file. Only the temp this
// call created is ever removed.
export function replaceFile(file, text) {
  const { tmp, fd } = openFreshTemp(file);
  try {
    try { fs.writeFileSync(fd, text); } finally { fs.closeSync(fd); }
    fs.renameSync(tmp, file);
  } catch (err) {
    fs.rmSync(tmp, { force: true });
    throw err;
  }
}

function openFreshTemp(file) {
  for (let attempt = 1; ; attempt++) {
    const tmp = `${file}.tmp-${randomInt(2 ** 32)}`;
    try {
      return { tmp, fd: fs.openSync(tmp, "wx", 0o600) };
    } catch (err) {
      if (err.code !== "EEXIST" || attempt === TEMP_NAME_ATTEMPTS) throw err;
    }
  }
}
