// scripts/roll-back.sh, the deploy workflow's rollback step, against a stub wrangler that keeps the live version in
// a file: `deploy` can make a new version live and then fail, as wrangler does when a custom domain or trigger
// fails after the upload, and `rollback` puts the named version back.
import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const ROLL_BACK = fileURLToPath(new URL("../scripts/roll-back.sh", import.meta.url));
const TARGET_SHA = "a".repeat(40);
const PREVIOUS = "version-previous";
const NEW = "version-new";

const STUB_WRANGLER = `#!/usr/bin/env bash
case "$1 $2" in
  "deployments status")
    if [[ -n "\${STUB_STATUS_FAILS:-}" ]]; then echo "Authentication error [code: 10000]" >&2; exit 1; fi
    echo "{\\"versions\\": [{\\"version_id\\": \\"$(cat "$STUB_LIVE")\\", \\"percentage\\": 100}]}" ;;
  "deploy -c")
    if [[ -n "\${STUB_DEPLOY_GOES_LIVE:-}" ]]; then echo "$STUB_NEW" >"$STUB_LIVE"; fi
    echo "Custom domains: failed" >&2; exit 1 ;;
  "rollback "*)
    echo "$2" >"$STUB_LIVE"; echo "$*" >"$STUB_ROLLBACK" ;;
  *) echo "unexpected wrangler $*" >&2; exit 2 ;;
esac
`;

function stubAccount(t: test.TestContext) {
  const root = mkdtempSync(join(tmpdir(), "roll-back-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const bin = join(root, "bin");
  mkdirSync(bin);
  writeFileSync(join(bin, "wrangler"), STUB_WRANGLER);
  chmodSync(join(bin, "wrangler"), 0o755);
  const files = { STUB_LIVE: join(root, "live"), STUB_ROLLBACK: join(root, "rollback") };
  writeFileSync(files.STUB_LIVE, `${PREVIOUS}\n`);
  const env = (stub: Record<string, string>) => ({
    ...process.env,
    PATH: `${bin}:${process.env.PATH}`,
    TARGET_SHA,
    PREVIOUS_VERSION: PREVIOUS,
    STUB_NEW: NEW,
    ...files,
    ...stub,
  });

  function deploy(stub: Record<string, string> = {}) {
    return spawnSync("wrangler", ["deploy", "-c", "wrangler.deploy.jsonc", "--message", TARGET_SHA], { cwd: root, env: env(stub) }).status;
  }
  function rollBack(deployOutcome: string, stub: Record<string, string> = {}) {
    const result = spawnSync("bash", [ROLL_BACK], { cwd: root, encoding: "utf8", env: env({ DEPLOY_OUTCOME: deployOutcome, ...stub }) });
    return { status: result.status, stdout: result.stdout, stderr: result.stderr };
  }
  const live = () => readFileSync(files.STUB_LIVE, "utf8").trim();
  const rollbackCommand = () => (existsSync(files.STUB_ROLLBACK) ? readFileSync(files.STUB_ROLLBACK, "utf8").trim() : undefined);
  return { deploy, rollBack, live, rollbackCommand };
}

test("a deploy that went live and then failed is rolled back to the previous version", (t) => {
  const { deploy, rollBack, live, rollbackCommand } = stubAccount(t);
  assert.notEqual(deploy({ STUB_DEPLOY_GOES_LIVE: "1" }), 0);
  assert.equal(live(), NEW);

  const result = rollBack("failure");
  assert.equal(result.status, 0, result.stderr);
  assert.equal(live(), PREVIOUS);
  assert.equal(rollbackCommand(), `rollback ${PREVIOUS} -c wrangler.deploy.jsonc -y -m rolled back: deploy of ${TARGET_SHA} failed`);
  assert.match(result.stdout, /^::error::Rolled back to version version-previous: the deploy of a{40} failed after its version went live\./m);
});

test("a deploy that failed before going live rolls nothing back", (t) => {
  const { deploy, rollBack, live, rollbackCommand } = stubAccount(t);
  assert.notEqual(deploy(), 0);

  const result = rollBack("failure");
  assert.equal(result.status, 0, result.stderr);
  assert.equal(live(), PREVIOUS);
  assert.equal(rollbackCommand(), undefined);
  assert.match(result.stdout, /^::notice::The deploy of a{40} failed before a new version went live/m);
});

test("a failure after a successful deploy rolls back without reading the live version", (t) => {
  const { rollBack, live, rollbackCommand } = stubAccount(t);
  const result = rollBack("success", { STUB_STATUS_FAILS: "1" });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(live(), PREVIOUS);
  assert.match(rollbackCommand() ?? "", /^rollback version-previous /);
  assert.match(result.stdout, /^::error::Rolled back to version version-previous: a step after the deploy of a{40} failed\./m);
});

test("a failed deploy whose live version cannot be read fails for a rollback by hand", (t) => {
  const { rollBack, rollbackCommand } = stubAccount(t);
  const result = rollBack("failure", { STUB_STATUS_FAILS: "1" });
  assert.equal(result.status, 1);
  assert.equal(rollbackCommand(), undefined);
  assert.match(result.stderr, /Authentication error/);
  assert.match(result.stdout, /^::error::The deploy of a{40} failed and the live version could not be read/m);
});

test("a new version live with no single previous version fails for a rollback by hand", (t) => {
  const { deploy, rollBack, rollbackCommand } = stubAccount(t);
  deploy({ STUB_DEPLOY_GOES_LIVE: "1" });
  const result = rollBack("failure", { PREVIOUS_VERSION: "" });
  assert.equal(result.status, 1);
  assert.equal(rollbackCommand(), undefined);
  assert.match(result.stdout, /^::error::Not rolled back: the deploy of a{40} failed after its version went live, and there is no single previous version/m);
});
