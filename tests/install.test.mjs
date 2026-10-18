import test, { describe } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";

const release = new URL("../", import.meta.url);
const managed = [
  "AGENTS.md",
  "opencode.jsonc",
  "agents",
  "commands",
  "skills",
  "plugins",
  "orchestrator",
  "install.sh",
  "uninstall.sh",
];
const present = (file) => {
  try {
    fs.lstatSync(file);
    return true;
  } catch (error) {
    if (error.code === "ENOENT") return false;
    throw error;
  }
};

function fixture(t) {
  const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "orchestrator install ")));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const checkout = path.join(root, "release checkout");
  const home = path.join(root, "test home");
  const bin = path.join(root, "fake bin");
  const state = path.join(root, "private state", "opencode-orchestrator");
  const target = path.join(home, ".config", "opencode");
  const log = path.join(root, "commands.jsonl");
  for (const directory of [checkout, home, bin]) fs.mkdirSync(directory, { recursive: true });
  for (const script of ["install.sh", "uninstall.sh"])
    fs.copyFileSync(new URL(script, release), path.join(checkout, script));
  for (const entry of managed) {
    if (entry.endsWith(".sh")) continue;
    if (entry.endsWith(".md") || entry.endsWith(".jsonc")) fs.writeFileSync(path.join(checkout, entry), entry);
    else {
      fs.mkdirSync(path.join(checkout, entry));
      fs.writeFileSync(path.join(checkout, entry, "example.txt"), entry);
    }
  }
  fs.mkdirSync(path.join(checkout, "orchestrator", "bin"));
  fs.writeFileSync(
    path.join(checkout, "orchestrator", "bin", "doctor.mjs"),
    "// Placeholder: the fixture Node command stubs doctor invocation.\n",
  );
  // Only fixtures are installed; external commands never reach real services/auth.
  const external = `#!/bin/sh
if [ "$1" = "--version" ]; then exit 0; fi
printf '%s\\n' '["opencode","reload"]' >> "$TEST_LOG"
[ "\${FAIL_RELOAD:-0}" != "1" ]
`;
  for (const name of ["opencode", "claude", "codex"]) fs.writeFileSync(path.join(bin, name), external, { mode: 0o755 });
  fs.writeFileSync(
    path.join(bin, "node"),
    `#!/bin/sh
if [ "$1" = "-" ]; then exec "$TEST_NODE" "$@"; fi
[ "$1" = "$HOME/.config/opencode/orchestrator/bin/doctor.mjs" ] || exit 2
printf '["doctor","%s"]\\n' "$1" >> "$TEST_LOG"
printf '%s' 'fake runtime state' > "$HOME/.config/opencode/service.json"
exit "\${FAIL_DOCTOR:-0}"
`,
    { mode: 0o755 },
  );
  const env = {
    ...process.env,
    HOME: home,
    XDG_CONFIG_HOME: path.join(home, ".config"),
    XDG_STATE_HOME: path.dirname(state),
    PATH: bin,
    TEST_LOG: log,
    TEST_NODE: process.execPath,
    FAIL_RELOAD: "0",
    FAIL_DOCTOR: "0",
  };
  const run = (script, args = [], extraEnv = {}) =>
    new Promise((resolve, reject) => {
      const child = spawn("/bin/bash", [path.join(checkout, script), ...args], {
        env: { ...env, ...extraEnv },
        stdio: ["ignore", "pipe", "pipe"],
      });
      let stdout = "",
        stderr = "";
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
      });
      child.stderr.on("data", (chunk) => {
        stderr += chunk;
      });
      child.on("error", reject);
      child.on("close", (status) => resolve({ status, stdout, stderr }));
    });
  const receipt = () => JSON.parse(fs.readFileSync(path.join(state, "active", "record.json"), "utf8"));
  const archives = () =>
    fs
      .readdirSync(state)
      .filter((name) => name.startsWith("uninstalled-"))
      .map((name) => path.join(state, name));
  return { root, checkout, home, bin, state, target, log, env, run, receipt, archives };
}

function success(result) {
  assert.equal(result.status, 0, result.stdout + result.stderr);
}

function failure(result, pattern) {
  assert.notEqual(result.status, 0, result.stdout + result.stderr);
  if (pattern) assert.match(result.stderr, pattern);
}

function previousTarget(f, type) {
  fs.mkdirSync(path.dirname(f.target), { recursive: true });
  const destination = path.join(f.root, "original symlink destination");
  if (type === "directory") {
    fs.mkdirSync(f.target);
    fs.writeFileSync(path.join(f.target, "personal-config.txt"), "preserve previous config");
  } else if (type === "file") fs.writeFileSync(f.target, "preserve previous file");
  else {
    if (type === "symlink") fs.mkdirSync(destination);
    fs.symlinkSync(destination, f.target);
  }
  const info = fs.lstatSync(f.target);
  return { ino: info.ino, dev: info.dev, link: info.isSymbolicLink() ? fs.readlinkSync(f.target) : null };
}

function assertOriginal(f, original) {
  const info = fs.lstatSync(f.target);
  assert.equal(info.ino, original.ino);
  assert.equal(info.dev, original.dev);
  if (original.link) assert.equal(fs.readlinkSync(f.target), original.link);
}

describe("installer (isolated temporary homes)", { concurrency: 16 }, () => {
  for (const mode of ["symlink", "copy"]) {
    const args = mode === "copy" ? ["--copy"] : [];
    test(`${mode}: install, installed-path doctor, runtime isolation, uninstall and repeat`, async (t) => {
      const f = fixture(t);
      fs.mkdirSync(path.join(f.checkout, ".git"));
      fs.mkdirSync(path.join(f.checkout, ".orchestrator"));
      fs.writeFileSync(path.join(f.checkout, ".orchestrator", "private.txt"), "not installed");
      success(await f.run("install.sh", args));
      assert.equal(fs.lstatSync(f.target).isDirectory(), true);
      assert.equal(f.receipt().mode, mode);
      assert.equal(f.receipt().phase, "active");
      assert.equal(fs.statSync(path.join(f.state, "active")).mode & 0o777, 0o700);
      assert.equal(fs.statSync(path.join(f.state, "active", "record.json")).mode & 0o777, 0o600);
      for (const entry of managed)
        assert.equal(fs.lstatSync(path.join(f.target, entry)).isSymbolicLink(), mode === "symlink");
      assert.equal(present(path.join(f.target, ".git")), false);
      assert.equal(present(path.join(f.target, ".orchestrator")), false);
      assert.equal(present(path.join(f.checkout, "service.json")), false);
      const commands = fs.readFileSync(f.log, "utf8").trim().split("\n").map(JSON.parse);
      assert.deepEqual(commands, [
        ["opencode", "reload"],
        ["doctor", path.join(f.target, "orchestrator", "bin", "doctor.mjs")],
      ]);
      success(await f.run(path.relative(f.checkout, path.join(f.target, "uninstall.sh"))));
      assert.equal(present(f.target), false);
      assert.equal(
        fs.readFileSync(path.join(f.archives()[0], "removed-config", "service.json"), "utf8"),
        "fake runtime state",
      );
      success(await f.run("uninstall.sh"));
      assert.equal(f.archives().length, 1);
      success(await f.run("install.sh", args));
      success(await f.run("uninstall.sh"));
      assert.equal(f.archives().length, 2);
    });

    for (const type of ["directory", "file", "symlink", "broken symlink"]) {
      test(`${mode}: refuses ${type}; force preserves and restores its identity`, async (t) => {
        const f = fixture(t);
        const original = previousTarget(f, type);
        failure(await f.run("install.sh", args), /already exists/);
        assertOriginal(f, original);
        assert.equal(present(f.state), false);
        success(await f.run("install.sh", [...args, "--force"]));
        assert.equal(fs.lstatSync(path.join(f.state, "active", "backup")).ino, original.ino);
        success(await f.run("uninstall.sh"));
        assertOriginal(f, original);
        assert.equal(present(path.join(f.state, "active")), false);
      });
    }

    for (const failedStep of ["RELOAD", "DOCTOR"]) {
      test(`${mode}: failed ${failedStep.toLowerCase()} is nonzero and recoverable`, async (t) => {
        const f = fixture(t);
        const original = previousTarget(f, "directory");
        failure(await f.run("install.sh", [...args, "--force"], { [`FAIL_${failedStep}`]: "1" }), /failed/);
        assert.equal(f.receipt().phase, "installed");
        if (failedStep === "RELOAD") assert.equal(fs.readFileSync(f.log, "utf8").includes("doctor"), false);
        success(await f.run("uninstall.sh"));
        assertOriginal(f, original);
      });
    }

    test(`${mode}: changed managed entry refuses uninstall without moving config or backup`, async (t) => {
      const f = fixture(t);
      const original = previousTarget(f, "directory");
      success(await f.run("install.sh", [...args, "--force"]));
      const changed = path.join(f.target, "AGENTS.md");
      fs.unlinkSync(changed);
      fs.writeFileSync(changed, "locally changed");
      failure(await f.run("uninstall.sh"), /config changed/);
      assert.equal(fs.readFileSync(changed, "utf8"), "locally changed");
      assert.equal(fs.lstatSync(path.join(f.state, "active", "backup")).ino, original.ino);
    });
  }

  for (const prerequisite of ["node", "opencode", "claude", "codex"]) {
    test(`missing ${prerequisite} refuses before mutations`, async (t) => {
      const f = fixture(t);
      fs.unlinkSync(path.join(f.bin, prerequisite));
      failure(await f.run("install.sh"), /required/);
      assert.equal(present(path.join(f.home, ".config")), false);
      assert.equal(present(f.state), false);
    });
  }

  test("copy preparation failure preserves original and can be uninstalled", async (t) => {
    const f = fixture(t);
    const original = previousTarget(f, "directory");
    fs.symlinkSync("missing", path.join(f.checkout, "agents", "unsupported-link"));
    failure(await f.run("install.sh", ["--copy", "--force"]), /refuses symlinks/);
    assert.equal(f.receipt().phase, "preparing");
    assertOriginal(f, original);
    success(await f.run("uninstall.sh"));
    assertOriginal(f, original);
  });

  test("unrecognized replacement directory and missing active target are refused", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    fs.chmodSync(f.target, 0o755);
    failure(await f.run("uninstall.sh"), /unrecognized or changed/);
    fs.chmodSync(f.target, 0o700);
    fs.renameSync(f.target, path.join(f.root, "moved config"));
    failure(await f.run("uninstall.sh"), /target is missing/);
    fs.mkdirSync(f.target);
    fs.writeFileSync(path.join(f.target, "unowned.txt"), "do not touch");
    failure(await f.run("uninstall.sh"), /unrecognized/);
    assert.equal(fs.readFileSync(path.join(f.target, "unowned.txt"), "utf8"), "do not touch");
  });

  test("missing or replaced backup refuses before moving installed config", async (t) => {
    const f = fixture(t);
    previousTarget(f, "directory");
    success(await f.run("install.sh", ["--force"]));
    const backup = path.join(f.state, "active", "backup");
    fs.renameSync(backup, path.join(f.root, "saved backup"));
    failure(await f.run("uninstall.sh"), /backup is missing/);
    fs.mkdirSync(backup);
    failure(await f.run("uninstall.sh"), /Backup ownership changed/);
    assert.equal(fs.lstatSync(f.target).isDirectory(), true);
  });

  test("repeat install, corrupt receipt and concurrent-operation lock are safe refusals", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    failure(await f.run("install.sh", ["--force"]), /receipt already exists/);
    fs.writeFileSync(path.join(f.state, "operation.lock"), "another operation");
    failure(await f.run("uninstall.sh"), /EEXIST/);
    assert.equal(fs.readFileSync(path.join(f.state, "operation.lock"), "utf8"), "another operation");
    fs.unlinkSync(path.join(f.state, "operation.lock"));
    fs.writeFileSync(path.join(f.state, "active", "record.json"), "{}");
    failure(await f.run("uninstall.sh"), /Unrecognized installation receipt/);
    assert.equal(present(f.target), true);
  });

  for (const restored of [false, true]) {
    test(`interrupted uninstall resumes ${restored ? "after" : "before"} backup restoration`, async (t) => {
      const f = fixture(t);
      const original = previousTarget(f, "file");
      success(await f.run("install.sh", ["--force"]));
      fs.renameSync(f.target, path.join(f.state, "active", "removed-config"));
      if (restored) fs.renameSync(path.join(f.state, "active", "backup"), f.target);
      success(await f.run("uninstall.sh"));
      assertOriginal(f, original);
    });
  }

  test("interrupted install resumes before target deployment", async (t) => {
    const f = fixture(t);
    const original = previousTarget(f, "directory");
    failure(await f.run("install.sh", ["--force"], { FAIL_RELOAD: "1" }));
    const receipt = f.receipt();
    receipt.phase = "prepared";
    fs.writeFileSync(path.join(f.state, "active", "record.json"), JSON.stringify(receipt));
    fs.renameSync(f.target, path.join(f.state, "active", "staged"));
    success(await f.run("uninstall.sh"));
    assertOriginal(f, original);
  });

  test("state inside checkout and nondefault XDG config are rejected", async (t) => {
    const f = fixture(t);
    failure(await f.run("install.sh", [], { XDG_STATE_HOME: f.checkout }), /must not overlap/);
    failure(
      await f.run("install.sh", [], { XDG_CONFIG_HOME: path.join(f.home, "other config") }),
      /Nondefault XDG_CONFIG_HOME/,
    );
    assert.equal(present(f.target), false);
    assert.equal(present(f.state), false);
  });

  test("uninstall requires only Node, and an unowned target without receipt is left alone", async (t) => {
    const f = fixture(t);
    const original = previousTarget(f, "broken symlink");
    success(await f.run("uninstall.sh"));
    assertOriginal(f, original);
    success(await f.run("install.sh", ["--force"]));
    for (const name of ["opencode", "claude", "codex"]) fs.unlinkSync(path.join(f.bin, name));
    success(await f.run("uninstall.sh"));
    assertOriginal(f, original);
  });
});
