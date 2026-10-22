// The holdlock fixture's lifecycle as run.test.ts relies on it (Sol review go-s13c R2): a holder
// reports its kernel lock, a failed release rejects instead of passing for success, and a holder
// that never reports the lock is ended and waited for before holdLock rejects. Every process here
// is the test's own child, owned through its ChildProcess handle.
import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, it } from "node:test";
import { buildLockHolder, holdLock } from "./orch.ts";

/** Whether pid names a process: signal 0 only checks, it delivers nothing. */
function running(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

describe("holdlock fixture", () => {
  let tmp: string;
  let binary: string;
  before(() => {
    tmp = mkdtempSync(join(tmpdir(), "holdlock-"));
    binary = buildLockHolder(tmp);
  });
  after(() => rmSync(tmp, { recursive: true, force: true }));

  it("holds the lock with its pid and removes it on release", async () => {
    const lock = join(tmp, "held.lock");
    const holder = await holdLock(binary, lock);
    assert.equal(readFileSync(lock, "utf8"), String(holder.pid));
    await holder.release();
    assert.equal(existsSync(lock), false);
    assert.equal(running(holder.pid), false);
  });

  it("rejects the release when the holder cannot release (its path was replaced)", async () => {
    const lock = join(tmp, "replaced.lock");
    const holder = await holdLock(binary, lock);
    const other = join(tmp, "other");
    writeFileSync(other, "");
    renameSync(other, lock);
    await assert.rejects(holder.release(), /holdlock .* exited with 1: .*was replaced/);
    assert.equal(running(holder.pid), false);
  });

  it("rejects a start that never reports the lock, after the holder has exited", async () => {
    const lock = join(tmp, "a-directory.lock");
    mkdirSync(lock);
    await assert.rejects(holdLock(binary, lock), /holdlock .* (ended before locking|exited with 1).*not a regular file/);
  });

  it("ends a holder that prints anything but the lock line, and waits for it", async () => {
    const liar = join(tmp, "liar");
    writeFileSync(liar, "#!/bin/sh\necho nope\nexec sleep 30\n", { mode: 0o755 });
    await assert.rejects(holdLock(liar, join(tmp, "unused.lock")), /printed "nope\\n"; holdlock .* exited with SIGKILL/);
  });
});
