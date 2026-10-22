// Driven by cleanup.test.ts: starts the e2e Worker under `wrangler dev` in the run directory given as its
// argument, owned by a RunCleanup the way run.test.ts does it, prints `ready <url>` once it answers, then
// waits for the signal the test sends. If startup fails it cleans up before failing. The test owns it through
// stdin: when the test closes that pipe (or dies), or after DRIVER_LIFETIME_MS at most, it cleans up and exits 1.
import { rmSync } from "node:fs";
import { RunCleanup } from "../cleanup.ts";
import { freePort, startWorkerDev } from "../worker-dev.ts";

const DRIVER_LIFETIME_MS = 60_000;

const [runDir] = process.argv.slice(2);
const cleanup = new RunCleanup();
cleanup.releaseOnSignals();
cleanup.defer(() => rmSync(runDir, { recursive: true, force: true }));

const abandon = () => {
  cleanup
    .run()
    .catch((error: unknown) => console.error("cleanup failed:", error))
    .finally(() => process.exit(1));
};
process.stdin.on("end", abandon).resume();
setTimeout(abandon, DRIVER_LIFETIME_MS);

try {
  const dev = await startWorkerDev(runDir, await freePort(), {}, cleanup);
  console.log(`ready ${dev.url}`);
} catch (error) {
  await cleanup.run();
  throw error;
}
