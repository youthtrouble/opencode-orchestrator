// scripts/deploy-plan.sh, the deploy workflow's skip and migration decision, against a scratch git history
// and a stub wrangler that reports which version is live and the commit its message names.
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const PLAN = fileURLToPath(new URL("../scripts/deploy-plan.sh", import.meta.url));
const VERSION = "version-live";

// Answers `deployments status --json` and `versions view <id> --json` from STUB_* variables.
const STUB_WRANGLER = `#!/usr/bin/env bash
case "$1 $2" in
  "deployments status")
    if [[ -n "\${STUB_NO_DEPLOYMENTS:-}" ]]; then echo "The Worker work-trains has no deployments." >&2; exit 1; fi
    if [[ -n "\${STUB_AUTH_FAILS:-}" ]]; then echo "Authentication error [code: 10000]" >&2; exit 1; fi
    echo "$STUB_STATUS" ;;
  "versions view")
    if [[ -n "\${STUB_VIEW_FAILS:-}" ]]; then echo "Authentication error [code: 10000]" >&2; exit 1; fi
    echo "{\\"id\\": \\"$3\\", \\"annotations\\": {\\"workers/message\\": \\"$STUB_MESSAGE\\"}}" ;;
  *) echo "unexpected wrangler $*" >&2; exit 2 ;;
esac
`;

function scratchRepo(t: test.TestContext) {
  const root = mkdtempSync(join(tmpdir(), "deploy-plan-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const repo = join(root, "repo");
  const git = (...args: string[]) =>
    execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", ...args], {
      cwd: repo,
      encoding: "utf8",
    }).trim();
  const commit = (files: Record<string, string>) => {
    for (const [path, text] of Object.entries(files)) {
      mkdirSync(dirname(join(repo, path)), { recursive: true });
      writeFileSync(join(repo, path), text);
    }
    git("add", "-A");
    git("commit", "-q", "-m", "change");
    return git("rev-parse", "HEAD");
  };
  mkdirSync(repo);
  git("init", "-q");
  const bin = join(root, "bin");
  mkdirSync(bin);
  writeFileSync(join(bin, "wrangler"), STUB_WRANGLER);
  chmodSync(join(bin, "wrangler"), 0o755);
  const deployed = commit({
    "remote/worker/src/index.ts": "v1",
    "remote/worker/migrations/0001_init.sql": "create table a (x);",
    "remote/protocol/schema.json": "{}",
    "README.md": "docs",
  });

  function plan(target: string, trigger: string, stub: Record<string, string> = {}) {
    const outputFile = join(root, `output-${Math.random()}`);
    writeFileSync(outputFile, "");
    const status = JSON.stringify({ versions: [{ version_id: VERSION, percentage: 100 }] });
    const result = spawnSync("bash", [PLAN], {
      cwd: join(repo, "remote/worker"),
      encoding: "utf8",
      env: {
        ...process.env,
        PATH: `${bin}:${process.env.PATH}`,
        TARGET_SHA: target,
        TRIGGER: trigger,
        GITHUB_OUTPUT: outputFile,
        STUB_STATUS: status,
        STUB_MESSAGE: deployed,
        ...stub,
      },
    });
    const outputs = Object.fromEntries(
      readFileSync(outputFile, "utf8").split("\n").filter(Boolean).map((line) => line.split(/=(.*)/s).slice(0, 2)),
    );
    return { status: result.status, stdout: result.stdout, stderr: result.stderr, outputs };
  }
  return { deployed, commit, plan };
}

test("a push that changed nothing the Worker ships is skipped with a notice", (t) => {
  const { deployed, commit, plan } = scratchRepo(t);
  const docsOnly = commit({ "README.md": "more docs" });
  const result = plan(docsOnly, "workflow_run");
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.outputs, { previous_version: VERSION, deployed_sha: deployed, skip: "true" });
  assert.match(result.stdout, /^::notice::Nothing under remote\/worker or remote\/protocol changed/m);
});

test("a push that changed the Worker or the protocol deploys", (t) => {
  const { commit, plan } = scratchRepo(t);
  const worker = commit({ "remote/worker/src/index.ts": "v2" });
  assert.equal(plan(worker, "workflow_run").outputs.skip, "false");
  const protocol = commit({ "remote/protocol/schema.json": "{\"v\":2}" });
  assert.equal(plan(protocol, "workflow_run").outputs.skip, "false");
  const added = commit({ "remote/worker/migrations/0002_more.sql": "create table b (y);" });
  assert.equal(plan(added, "workflow_run").outputs.skip, "false");
});

test("a push older than the live commit is skipped, so out-of-order runs never deploy backwards", (t) => {
  const { deployed, commit, plan } = scratchRepo(t);
  const newer = commit({ "remote/worker/src/index.ts": "v2" });
  const newest = commit({ "remote/worker/src/index.ts": "v3" });
  for (const target of [newer, deployed]) {
    const result = plan(target, "workflow_run", { STUB_MESSAGE: newest });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.outputs, { previous_version: VERSION, deployed_sha: newest, skip: "true" });
    assert.match(result.stdout, /^::notice::[0-9a-f]{40} is already part of the live/m);
  }
  assert.equal(plan(newer, "workflow_dispatch", { STUB_MESSAGE: newest }).outputs.skip, "false", "a manual rollback still deploys");
});

test("a manual run deploys even when nothing changed, to redeploy or roll back", (t) => {
  const { deployed, plan } = scratchRepo(t);
  const result = plan(deployed, "workflow_dispatch");
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.outputs.skip, "false");
});

test("an edited migration fails the plan; a deleted one (a rollback) does not", (t) => {
  const { deployed, commit, plan } = scratchRepo(t);
  const edited = commit({ "remote/worker/migrations/0001_init.sql": "create table a (x, y);" });
  const result = plan(edited, "workflow_run");
  assert.equal(result.status, 1);
  assert.match(result.stdout, /^::error::Migrations were edited/m);
  assert.match(result.stdout, /remote\/worker\/migrations\/0001_init\.sql/);
  assert.equal(result.outputs.skip, undefined, "no decision is written");

  const later = commit({ "remote/worker/migrations/0001_init.sql": "create table a (x);", "remote/worker/migrations/0002_more.sql": "b" });
  const rollback = plan(deployed, "workflow_dispatch", { STUB_MESSAGE: later });
  assert.equal(rollback.status, 0, rollback.stderr);
  assert.equal(rollback.outputs.skip, "false");
});

test("a live version that names no known commit deploys with a warning", (t) => {
  const { commit, plan } = scratchRepo(t);
  const target = commit({ "README.md": "docs only" });
  for (const message of ["deployed by hand", "f".repeat(40)]) {
    const result = plan(target, "workflow_run", { STUB_MESSAGE: message });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.outputs, { previous_version: VERSION, deployed_sha: "", skip: "false" });
    assert.match(result.stdout, /^::warning::The live version names no commit/m);
  }
  const gradual = JSON.stringify({ versions: [{ version_id: "a", percentage: 90 }, { version_id: "b", percentage: 10 }] });
  assert.deepEqual(plan(target, "workflow_run", { STUB_STATUS: gradual }).outputs, { previous_version: "", deployed_sha: "", skip: "false" });
  const first = plan(target, "workflow_run", { STUB_NO_DEPLOYMENTS: "1" });
  assert.equal(first.status, 0, first.stderr);
  assert.deepEqual(first.outputs, { previous_version: "", deployed_sha: "", skip: "false" });
});

test("a wrangler failure other than no deployments fails the plan", (t) => {
  const { deployed, plan } = scratchRepo(t);
  const result = plan(deployed, "workflow_run", { STUB_AUTH_FAILS: "1" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Authentication error/);
  assert.deepEqual(result.outputs, {});
});

test("a failed version lookup fails the plan instead of skipping its safeguards", (t) => {
  const { commit, plan } = scratchRepo(t);
  const edited = commit({ "remote/worker/migrations/0001_init.sql": "create table a (x, y);" });
  const result = plan(edited, "workflow_run", { STUB_VIEW_FAILS: "1" });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Authentication error/);
  assert.equal(result.outputs.skip, undefined, "no decision is written");
});
