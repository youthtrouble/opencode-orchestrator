import test, { describe } from "node:test";
import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn, spawnSync } from "node:child_process";

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
  fs.mkdirSync(path.join(checkout, "go", "internal", "version"), { recursive: true });
  fs.writeFileSync(path.join(checkout, "go", "go.mod"), "module fixture\n");
  fs.writeFileSync(path.join(checkout, "go", "internal", "version", "VERSION"), "0.5.0\n");
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
  // The fake go "builds" a script that reports FAKE_ORCH_VERSION, so no real toolchain runs.
  fs.writeFileSync(
    path.join(bin, "go"),
    `#!/bin/sh
if [ "$1" = "version" ]; then echo "go version go1.25.0 fake"; exit 0; fi
[ "$1" = "build" ] || exit 2
out=""
previous=""
for arg in "$@"; do
  if [ "$previous" = "-o" ]; then out=$arg; fi
  previous=$arg
done
printf '["go","build","%s","%s"]\\n' "$(pwd -P)" "$out" >> "$TEST_LOG"
if [ "\${FAIL_BUILD:-0}" = "1" ]; then exit 1; fi
if [ -n "\${RACE_ORCH:-}" ]; then /bin/mkdir -p "\${RACE_ORCH%/*}"; printf '%s' "user's own orch" > "$RACE_ORCH"; fi
printf '#!/bin/sh\\necho "\${FAKE_ORCH_VERSION:-0.5.0} (fake)"\\n' > "$out"
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
    FAIL_BUILD: "0",
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
  const binary = path.join(home, ".local", "bin", "orch");
  return { root, checkout, home, bin, state, target, log, env, run, receipt, archives, binary };
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

const releaseAsset = `orch_0.5.0_${process.platform}_${{ arm64: "arm64", x64: "amd64" }[process.arch]}`;
const releasedOrch = '#!/bin/sh\necho "0.5.0 (release)"\n';
const sha256 = (content) => crypto.createHash("sha256").update(content).digest("hex");

// Removes go and serves a release from a local directory through a fake curl (no network).
function downloadOnly(f) {
  fs.unlinkSync(path.join(f.bin, "go"));
  fs.writeFileSync(
    path.join(f.bin, "curl"),
    `#!/bin/sh
if [ "$1" = "--version" ]; then exit 0; fi
out=""
previous=""
for arg in "$@"; do
  if [ "$previous" = "--output" ]; then out=$arg; fi
  previous=$arg
done
printf '["curl","%s"]\\n' "$previous" >> "$TEST_LOG"
served="$TEST_RELEASE_DIR/\${previous##*/}"
[ -f "$served" ] || exit 22
/bin/cp "$served" "$out"
`,
    { mode: 0o755 },
  );
  const releaseDirectory = path.join(f.root, "release");
  fs.mkdirSync(releaseDirectory);
  fs.writeFileSync(path.join(releaseDirectory, releaseAsset), releasedOrch);
  fs.writeFileSync(path.join(releaseDirectory, "checksums.txt"), `${sha256(releasedOrch)}  ${releaseAsset}\n`);
  return {
    releaseDirectory,
    env: { TEST_RELEASE_DIR: releaseDirectory, ORCH_RELEASE_BASE_URL: "https://example.invalid/rel" },
  };
}

const logged = (f) => fs.readFileSync(f.log, "utf8").trim().split("\n").map(JSON.parse);

function assertPlacedOrch(f) {
  assert.equal(f.receipt().binary, f.binary);
  assert.equal(fs.statSync(f.binary).mode & 0o777, 0o755);
  assert.equal(spawnSync(f.binary, ["version"], { encoding: "utf8" }).stdout, "0.5.0 (fake)\n");
}

function assertNothingChanged(f) {
  assert.equal(present(path.join(f.home, ".config")), false);
  assert.equal(present(f.state), false);
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
        ["go", "build", path.join(f.checkout, "go"), path.join(f.state, "active", "orch")],
        ["opencode", "reload"],
        ["doctor", path.join(f.target, "orchestrator", "bin", "doctor.mjs")],
      ]);
      assertPlacedOrch(f);
      success(await f.run(path.relative(f.checkout, path.join(f.target, "uninstall.sh"))));
      assert.equal(present(f.target), false);
      assert.equal(present(f.binary), false);
      assert.equal(
        fs.readFileSync(path.join(f.archives()[0], "removed-config", "service.json"), "utf8"),
        "fake runtime state",
      );
      success(await f.run("uninstall.sh"));
      assert.equal(f.archives().length, 1);
      success(await f.run("install.sh", args));
      assertPlacedOrch(f);
      success(await f.run("uninstall.sh"));
      assert.equal(present(f.binary), false);
      assert.equal(f.archives().length, 2);
      for (const archive of f.archives()) assert.equal(present(path.join(archive, "removed-binary")), true);
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

  test("--skip-binary installs without go and records no binary", async (t) => {
    const f = fixture(t);
    fs.rmSync(path.join(f.checkout, "go"), { recursive: true });
    fs.unlinkSync(path.join(f.bin, "go"));
    const installed = await f.run("install.sh", ["--skip-binary"]);
    success(installed);
    assert.match(installed.stdout, /orch: skipped \(--skip-binary\)/);
    assert.equal(f.receipt().binary, null);
    assert.equal(present(f.binary), false);
    assert.equal(fs.readFileSync(f.log, "utf8").includes('"go"'), false);
    success(await f.run("uninstall.sh"));
    assert.equal(present(path.join(f.archives()[0], "removed-binary")), false);
  });

  test("--skip-binary with go present runs no build", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh", ["--skip-binary"]));
    assert.equal(fs.readFileSync(f.log, "utf8").includes('"go"'), false);
    assert.equal(f.receipt().binary, null);
    assert.equal(present(f.binary), false);
  });

  test("no go and no curl refuses before mutations", async (t) => {
    const f = fixture(t);
    fs.unlinkSync(path.join(f.bin, "go"));
    failure(await f.run("install.sh"), /install Go 1\.25\+ or curl, or pass --skip-binary/);
    assertNothingChanged(f);
  });

  test("without go, the release asset is downloaded, verified, placed and archived", async (t) => {
    const f = fixture(t);
    const release = downloadOnly(f);
    fs.unlinkSync(path.join(f.checkout, "go", "go.mod"));
    success(await f.run("install.sh", [], release.env));
    assert.deepEqual(logged(f).slice(0, 2), [
      ["curl", "https://example.invalid/rel/checksums.txt"],
      ["curl", `https://example.invalid/rel/${releaseAsset}`],
    ]);
    assert.equal(f.receipt().binary, f.binary);
    assert.equal(fs.readFileSync(f.binary, "utf8"), releasedOrch);
    assert.equal(fs.statSync(f.binary).mode & 0o777, 0o755);
    success(await f.run("uninstall.sh"));
    assert.equal(present(f.binary), false);
    assert.equal(fs.readFileSync(path.join(f.archives()[0], "removed-binary"), "utf8"), releasedOrch);
    assert.equal(present(path.join(f.archives()[0], "checksums.txt")), true);
  });

  test("without ORCH_RELEASE_BASE_URL the GitHub release of VERSION is used", async (t) => {
    const f = fixture(t);
    const release = downloadOnly(f);
    success(await f.run("install.sh", [], { ...release.env, ORCH_RELEASE_BASE_URL: "" }));
    for (const [, url] of logged(f).slice(0, 2))
      assert.ok(url.startsWith("https://github.com/youthtrouble/opencode-orchestrator/releases/download/v0.5.0/"), url);
  });

  for (const [name, corrupt, pattern] of [
    [
      "checksum mismatch",
      (directory) => fs.writeFileSync(path.join(directory, releaseAsset), "tampered"),
      new RegExp(`Checksum mismatch for ${releaseAsset}; nothing installed\\.`),
    ],
    [
      "missing checksum entry",
      (directory) => fs.writeFileSync(path.join(directory, "checksums.txt"), `${sha256(releasedOrch)} *orch_other\n`),
      new RegExp(`checksums\\.txt has no entry for ${releaseAsset}\\.`),
    ],
    [
      "missing release asset",
      (directory) => fs.unlinkSync(path.join(directory, releaseAsset)),
      new RegExp(`Download failed: https://example\\.invalid/rel/${releaseAsset}\\.`),
    ],
  ]) {
    test(`download: ${name} is nonzero, installs nothing and is recoverable`, async (t) => {
      const f = fixture(t);
      const release = downloadOnly(f);
      corrupt(release.releaseDirectory);
      const original = previousTarget(f, "directory");
      failure(await f.run("install.sh", ["--force"], release.env), pattern);
      assert.equal(f.receipt().phase, "preparing");
      assertOriginal(f, original);
      assert.equal(present(f.binary), false);
      success(await f.run("uninstall.sh"));
      assertOriginal(f, original);
    });
  }

  test("download: a shasum-format entry is accepted", async (t) => {
    const f = fixture(t);
    const release = downloadOnly(f);
    fs.writeFileSync(
      path.join(release.releaseDirectory, "checksums.txt"),
      `${sha256("other")}  orch_other\n${sha256(releasedOrch)} *${releaseAsset}\n`,
    );
    success(await f.run("install.sh", [], release.env));
    assert.equal(fs.readFileSync(f.binary, "utf8"), releasedOrch);
  });

  test("a non-https ORCH_RELEASE_BASE_URL is refused before mutations", async (t) => {
    const f = fixture(t);
    const release = downloadOnly(f);
    const refused = await f.run("install.sh", [], {
      ...release.env,
      ORCH_RELEASE_BASE_URL: "http://example.invalid/rel",
    });
    failure(refused, /ORCH_RELEASE_BASE_URL must be https; nothing changed\./);
    assertNothingChanged(f);
    assert.equal(present(f.log), false);
  });

  test("an architecture without a release asset is refused before mutations", async (t) => {
    const f = fixture(t);
    const release = downloadOnly(f);
    // The installer's Node reports another architecture through a standard preload, not a product hook.
    const preload = path.join(f.root, "arch-ia32.cjs");
    fs.writeFileSync(preload, 'Object.defineProperty(process, "arch", { value: "ia32" });\n');
    const refused = await f.run("install.sh", [], { ...release.env, NODE_OPTIONS: `--require="${preload}"` });
    failure(
      refused,
      new RegExp(
        `No orch release asset for ${process.platform}/ia32; install Go 1\\.25\\+ or pass --skip-binary; nothing changed\\.`,
      ),
    );
    assertNothingChanged(f);
    assert.equal(present(f.log), false);
  });

  test("an existing orch at the destination is refused and left byte-identical", async (t) => {
    const f = fixture(t);
    fs.mkdirSync(path.dirname(f.binary), { recursive: true });
    fs.writeFileSync(f.binary, "user's own orch");
    failure(await f.run("install.sh"), /orch already exists/);
    assert.equal(fs.readFileSync(f.binary, "utf8"), "user's own orch");
    assert.equal(present(path.join(f.home, ".config")), false);
    assert.equal(present(f.state), false);
  });

  test("ORCH_BIN_DIR must be absolute and outside the checkout, config and state", async (t) => {
    const f = fixture(t);
    fs.mkdirSync(path.join(f.checkout, "bin"));
    const linkedIntoCheckout = path.join(f.root, "linked bin");
    fs.symlinkSync(path.join(f.checkout, "bin"), linkedIntoCheckout);
    failure(await f.run("install.sh", [], { ORCH_BIN_DIR: "relative/bin" }), /ORCH_BIN_DIR must be absolute/);
    for (const directory of [
      path.join(f.checkout, "bin"),
      linkedIntoCheckout,
      path.join(f.target, "bin"),
      path.join(f.state, "bin"),
    ])
      failure(await f.run("install.sh", [], { ORCH_BIN_DIR: directory }), /must be outside/);
    assertNothingChanged(f);
  });

  test("an ORCH_BIN_DIR on PATH with a trailing slash gets no PATH note", async (t) => {
    const f = fixture(t);
    const directory = path.join(f.root, "custom bin");
    const installed = await f.run("install.sh", [], { ORCH_BIN_DIR: directory, PATH: `${f.bin}:${directory}/` });
    success(installed);
    assert.doesNotMatch(installed.stdout, /is not on PATH/);
  });

  test("an orch that appears at the destination during the build is never archived", async (t) => {
    const f = fixture(t);
    const original = previousTarget(f, "directory");
    failure(await f.run("install.sh", ["--force"], { RACE_ORCH: f.binary }), /EEXIST/);
    assert.equal(f.receipt().phase, "prepared");
    assert.equal(f.receipt().binary, f.binary);
    const uninstalled = await f.run("uninstall.sh");
    success(uninstalled);
    assert.match(uninstalled.stdout, /not placed by this installation; left in place/);
    assert.equal(fs.readFileSync(f.binary, "utf8"), "user's own orch");
    assert.equal(present(path.join(f.archives()[0], "removed-binary")), false);
    assertOriginal(f, original);
  });

  test("an existing removed-binary leaves orch in place with a message", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    fs.writeFileSync(path.join(f.state, "active", "removed-binary"), "earlier archive");
    const uninstalled = await f.run("uninstall.sh");
    success(uninstalled);
    assert.match(uninstalled.stdout, /left in place: .*removed-binary already exists/);
    assert.equal(present(f.binary), true);
    assert.equal(fs.readFileSync(path.join(f.archives()[0], "removed-binary"), "utf8"), "earlier archive");
  });

  test("a custom absolute ORCH_BIN_DIR is used, recorded and noted when off PATH", async (t) => {
    const f = fixture(t);
    const directory = path.join(f.root, "custom bin");
    const installed = await f.run("install.sh", [], { ORCH_BIN_DIR: directory });
    success(installed);
    const destination = path.join(directory, "orch");
    assert.equal(f.receipt().binary, destination);
    assert.equal(present(destination), true);
    assert.equal(present(f.binary), false);
    assert.match(installed.stdout, new RegExp(`orch: ${destination}`));
    assert.match(installed.stdout, /is not on PATH/);
    success(await f.run("uninstall.sh"));
    assert.equal(present(destination), false);
    assert.equal(present(path.join(f.archives()[0], "removed-binary")), true);
  });

  for (const [name, extraEnv, pattern] of [
    ["failed build", { FAIL_BUILD: "1" }, /go build .* failed/],
    ["version mismatch", { FAKE_ORCH_VERSION: "9.9.9" }, /expected 0\.5\.0/],
  ]) {
    test(`${name} is nonzero, leaves the target untouched and is recoverable`, async (t) => {
      const f = fixture(t);
      const original = previousTarget(f, "directory");
      failure(await f.run("install.sh", ["--force"], extraEnv), pattern);
      assert.equal(f.receipt().phase, "preparing");
      assertOriginal(f, original);
      assert.equal(present(f.binary), false);
      success(await f.run("uninstall.sh"));
      assertOriginal(f, original);
    });
  }

  test("a legacy 0.4.x receipt without binary uninstalls and leaves orch in place", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    const { checkout, target, installedIdentity, fingerprint } = f.receipt();
    // The exact key set written by the 0.4.x installer.
    const legacy = {
      version: 1,
      checkout,
      target,
      mode: "symlink",
      phase: "active",
      hadPrevious: false,
      previousIdentity: null,
      installedIdentity,
      fingerprint,
    };
    fs.writeFileSync(path.join(f.state, "active", "record.json"), JSON.stringify(legacy, null, 2) + "\n");
    success(await f.run("uninstall.sh"));
    assert.equal(present(f.binary), true);
  });

  test("a receipt binary that is not an absolute path to orch is refused", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    const unrelated = path.join(f.home, ".ssh", "id_ed25519");
    fs.mkdirSync(path.dirname(unrelated));
    fs.writeFileSync(unrelated, "private key");
    for (const binary of [42, "relative/orch", unrelated]) {
      const record = f.receipt();
      record.binary = binary;
      fs.writeFileSync(path.join(f.state, "active", "record.json"), JSON.stringify(record));
      failure(await f.run("uninstall.sh"), /Unrecognized installation receipt/);
    }
    assert.equal(present(f.binary), true);
    assert.equal(fs.readFileSync(unrelated, "utf8"), "private key");
  });

  test("a non-regular orch at uninstall is left in place", async (t) => {
    const f = fixture(t);
    success(await f.run("install.sh"));
    fs.unlinkSync(f.binary);
    fs.mkdirSync(f.binary);
    const uninstalled = await f.run("uninstall.sh");
    success(uninstalled);
    assert.match(uninstalled.stdout, /not a regular file/);
    assert.equal(fs.lstatSync(f.binary).isDirectory(), true);
  });
});
