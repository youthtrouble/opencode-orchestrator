#!/usr/bin/env bash
# macOS/Linux, Bash 3.2+, Node 22+. Runtime paths currently require ~/.config/opencode;
# nondefault XDG_CONFIG_HOME is rejected. State and config must share a filesystem.
# Managed-entry symlinks keep service.json/runtime writes out of the checkout.
# Private backups/receipts live in $XDG_STATE_HOME/opencode-orchestrator (0700),
# defaulting to ~/.local/state. Uninstall archives, never recursively deletes.
set -euo pipefail

installer_directory=${BASH_SOURCE[0]%/*}
[[ ${BASH_SOURCE[0]} == */* ]] || installer_directory=.
ORCHESTRATOR_CHECKOUT=$(cd -- "$installer_directory" && pwd -P)

run_installer() {
  if ! command -v node >/dev/null 2>&1; then
    printf '%s\n' 'Node 22+ is required; nothing changed.' >&2
    return 1
  fi
  node - "$ORCHESTRATOR_CHECKOUT" "$@" <<'NODE'
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const { spawnSync } = require("node:child_process");

const [checkout, action, ...args] = process.argv.slice(2);
const entries = [
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
const exists = (file) => {
  try {
    fs.lstatSync(file);
    return true;
  } catch (error) {
    if (error.code === "ENOENT") return false;
    throw error;
  }
};
const requireCondition = (condition, message) => {
  if (!condition) throw new Error(message);
};
const inside = (child, parent) => child === parent || child.startsWith(parent + path.sep);

// Resolve existing parents without following the final target's symlink.
function canonical(file) {
  if (exists(file)) return fs.realpathSync(file);
  return path.join(canonical(path.dirname(file)), path.basename(file));
}

function identity(file) {
  const info = fs.lstatSync(file, { bigint: true });
  return { dev: String(info.dev), ino: String(info.ino), mode: String(info.mode) };
}

function existingDevice(file) {
  while (!exists(file)) file = path.dirname(file);
  return fs.statSync(file).dev;
}

function sameIdentity(file, expected) {
  return exists(file) && JSON.stringify(identity(file)) === JSON.stringify(expected);
}

function fingerprint(directory) {
  const hash = crypto.createHash("sha256");
  function visit(relative) {
    // Runtime additions are preserved in the uninstall archive, not deleted.
    if (["service.json", ".orchestrator", "logs"].includes(relative) || relative.endsWith(".log")) return;
    const file = path.join(directory, relative);
    const info = fs.lstatSync(file);
    hash.update(
      JSON.stringify([
        relative,
        info.mode & 0o777,
        info.isSymbolicLink() ? "link" : info.isDirectory() ? "directory" : "file",
      ]),
    );
    if (info.isSymbolicLink()) hash.update(fs.readlinkSync(file));
    else if (info.isDirectory()) for (const name of fs.readdirSync(file).sort()) visit(path.join(relative, name));
    else {
      requireCondition(info.isFile(), "Unsupported config entry.");
      hash.update(fs.readFileSync(file));
    }
  }
  for (const name of fs.readdirSync(directory).sort()) visit(name);
  return hash.digest("hex");
}

function copyEntry(source, destination) {
  const info = fs.lstatSync(source);
  requireCondition(!info.isSymbolicLink(), "Copy mode refuses symlinks in managed source entries.");
  if (info.isDirectory()) {
    fs.mkdirSync(destination, { mode: info.mode & 0o777 });
    for (const name of fs.readdirSync(source)) {
      if ([".git", ".orchestrator", "service.json", "node_modules", "logs"].includes(name) || name.endsWith(".log"))
        continue;
      copyEntry(path.join(source, name), path.join(destination, name));
    }
    fs.chmodSync(destination, info.mode & 0o777);
  } else {
    requireCondition(info.isFile(), "Unsupported managed source entry.");
    fs.copyFileSync(source, destination, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(destination, info.mode & 0o777);
  }
}

function privateDirectory(directory) {
  if (!exists(directory)) fs.mkdirSync(directory, { recursive: true, mode: 0o700 });
  const info = fs.lstatSync(directory);
  requireCondition(
    info.isDirectory() && !info.isSymbolicLink() && info.uid === process.getuid() && (info.mode & 0o077) === 0,
    "Installer state must be an owned, private directory (0700).",
  );
}

function command(name, argv, options = {}) {
  const result = spawnSync(name, argv, { stdio: "inherit", ...options });
  requireCondition(!result.error && result.status === 0, `${name} ${argv.join(" ")} failed.`);
}

function orchDestination(home) {
  const requested = process.env.ORCH_BIN_DIR || path.join(home, ".local", "bin");
  requireCondition(path.isAbsolute(requested), "ORCH_BIN_DIR must be absolute; nothing changed.");
  const directory = canonical(requested);
  const destination = path.join(directory, "orch");
  requireCondition(
    !exists(destination),
    `orch already exists at ${destination}; remove it, set ORCH_BIN_DIR, or use --skip-binary; nothing changed.`,
  );
  requireCondition(
    !inside(directory, target) && !inside(directory, checkout) && !inside(directory, stateRoot),
    "The orch directory must be outside the config, checkout and installer state; nothing changed.",
  );
  requireCondition(
    existingDevice(directory) === existingDevice(stateRoot),
    "The orch directory and installer state must share a filesystem; set ORCH_BIN_DIR or use --skip-binary.",
  );
  return { directory, destination };
}

// A download needs only VERSION; a build also needs the Go module.
function releaseVersion(source) {
  const versionFile = path.join("go", "internal", "version", "VERSION");
  const required = source === "build" ? [versionFile, path.join("go", "go.mod")] : [versionFile];
  for (const entry of required) requireCondition(exists(path.join(checkout, entry)), `Missing release entry: ${entry}`);
  const version = fs.readFileSync(path.join(checkout, versionFile), "utf8").trim();
  requireCondition(version !== "", `Missing release entry: ${versionFile}`);
  return version;
}

function succeeds(name, argv) {
  const result = spawnSync(name, argv, { stdio: "ignore" });
  return !result.error && result.status === 0;
}

// The single place that decides where orch comes from: a local build, else the release download.
function orchSource() {
  if (succeeds("go", ["version"])) return "build";
  requireCondition(
    succeeds("curl", ["--version"]),
    "orch binary unavailable: install Go 1.25+ or curl, or pass --skip-binary; nothing changed.",
  );
  return "download";
}

const releasePlatforms = ["darwin", "linux"];
const releaseArchitectures = { arm64: "arm64", x64: "amd64" };

function releaseAsset(version) {
  const architecture = releaseArchitectures[process.arch];
  requireCondition(
    releasePlatforms.includes(process.platform) && architecture,
    `No orch release asset for ${process.platform}/${process.arch}; install Go 1.25+ or pass --skip-binary; nothing changed.`,
  );
  const baseUrl = (
    process.env.ORCH_RELEASE_BASE_URL ||
    `https://github.com/youthtrouble/opencode-orchestrator/releases/download/v${version}`
  ).replace(/\/+$/, "");
  requireCondition(baseUrl.startsWith("https://"), "ORCH_RELEASE_BASE_URL must be https; nothing changed.");
  return { baseUrl, asset: `orch_${version}_${process.platform}_${architecture}` };
}

function planOrch(home) {
  const destination = orchDestination(home);
  const source = orchSource();
  const version = releaseVersion(source);
  const release = source === "download" ? releaseAsset(version) : {};
  return { ...destination, version, source, ...release };
}

function buildOrch(file) {
  command("go", ["build", "-trimpath", "-ldflags=-s -w", "-o", file, "./cmd/orch"], {
    cwd: path.join(checkout, "go"),
    env: { ...process.env, CGO_ENABLED: "0" },
  });
}

function download(url, file) {
  const result = spawnSync(
    "curl",
    ["--fail", "--location", "--silent", "--show-error", "--proto", "=https", "--output", file, url],
    { stdio: ["ignore", "ignore", "inherit"] },
  );
  requireCondition(!result.error && result.status === 0, `Download failed: ${url}.`);
}

// Accepts both sha256sum ("<hash>  <name>") and shasum binary ("<hash> *<name>") lines.
function expectedChecksum(checksums, asset) {
  for (const line of checksums.split("\n")) {
    const match = /^([a-f0-9]{64}) [ *](.+)$/.exec(line);
    if (match && match[2] === asset) return match[1];
  }
  throw new Error(`checksums.txt has no entry for ${asset}.`);
}

// Integrity only: checksums.txt comes from the same release, so this catches corruption, not a
// compromised release. checksums.txt stays in active and is archived with it.
function downloadOrch(orch, file) {
  const checksums = path.join(active, "checksums.txt");
  const downloaded = path.join(active, "orch.download");
  download(`${orch.baseUrl}/checksums.txt`, checksums);
  download(`${orch.baseUrl}/${orch.asset}`, downloaded);
  const expected = expectedChecksum(fs.readFileSync(checksums, "utf8"), orch.asset);
  const actual = crypto.createHash("sha256").update(fs.readFileSync(downloaded)).digest("hex");
  requireCondition(actual === expected, `Checksum mismatch for ${orch.asset}; nothing installed.`);
  fs.renameSync(downloaded, file);
}

function acquireOrch(orch, file) {
  if (orch.source === "build") buildOrch(file);
  else downloadOrch(orch, file);
  fs.chmodSync(file, 0o755);
  const reported = spawnSync(file, ["version"], { encoding: "utf8" });
  const firstLine = (reported.stdout || "").split("\n")[0].trim();
  requireCondition(
    !reported.error && reported.status === 0 && firstLine.split(/\s+/)[0] === orch.version,
    `orch version reported "${firstLine}", expected ${orch.version}.`,
  );
}

// Hard link then unlink: never replaces a file that appeared at the destination meanwhile.
function placeOrch(orch, file) {
  fs.mkdirSync(orch.directory, { recursive: true, mode: 0o755 });
  fs.linkSync(file, orch.destination);
  fs.unlinkSync(file);
}

// PATH entries may carry a trailing slash or a symlinked spelling of the canonical directory.
function onPath(directory) {
  return (process.env.PATH || "")
    .split(":")
    .filter((entry) => entry !== "")
    .some((entry) => {
      const resolved = path.resolve(entry);
      return resolved === directory || (fs.existsSync(resolved) && fs.realpathSync(resolved) === directory);
    });
}

function reportOrch(orch) {
  if (!orch) {
    console.log("orch: skipped (--skip-binary)");
    return;
  }
  console.log(`orch: ${orch.destination}`);
  if (!onPath(orch.directory))
    console.log(
      `Note: ${orch.directory} is not on PATH; the plugin looks in ~/.local/bin/orch itself, other locations need ORCH_BIN=${orch.destination}.`,
    );
}

// Archives, never deletes. Before installation finished (preparing/prepared) the destination may
// hold a file that appeared after the pre-check, so only the file we placed (same dev+ino) moves.
// Later phases archive any regular file there: the README update rebuilds orch in place, which
// legitimately replaces the inode we recorded.
function archiveOrch(record) {
  if (typeof record.binary !== "string" || !exists(record.binary)) return;
  if (!fs.lstatSync(record.binary).isFile()) {
    console.log(`orch at ${record.binary} is not a regular file; left in place.`);
    return;
  }
  if (["preparing", "prepared"].includes(record.phase) && !sameIdentity(record.binary, record.binaryIdentity)) {
    console.log(`orch at ${record.binary} was not placed by this installation; left in place.`);
    return;
  }
  const removed = path.join(active, "removed-binary");
  if (exists(removed)) {
    console.log(`orch at ${record.binary} left in place: ${removed} already exists.`);
    return;
  }
  fs.renameSync(record.binary, removed);
  console.log(`orch archived from ${record.binary}`);
}

function writeRecord(record) {
  const temporary = path.join(active, "record.tmp");
  fs.writeFileSync(temporary, JSON.stringify(record, null, 2) + "\n", { mode: 0o600 });
  fs.renameSync(temporary, receipt);
}

function readRecord() {
  const record = JSON.parse(fs.readFileSync(receipt, "utf8"));
  requireCondition(
    record.version === 1 &&
      typeof record.checkout === "string" &&
      path.isAbsolute(record.checkout) &&
      record.target === target &&
      ["symlink", "copy"].includes(record.mode) &&
      ["preparing", "prepared", "backed-up", "installed", "active"].includes(record.phase) &&
      typeof record.hadPrevious === "boolean" &&
      (record.phase === "preparing" || (/^[a-f0-9]{64}$/.test(record.fingerprint) && record.installedIdentity)) &&
      (record.binary === undefined ||
        record.binary === null ||
        (typeof record.binary === "string" &&
          path.isAbsolute(record.binary) &&
          path.basename(record.binary) === "orch")),
    "Unrecognized installation receipt; nothing changed.",
  );
  return record;
}

function install(orch) {
  requireCondition(!exists(active), "An installation or recovery receipt already exists; run uninstall.sh first.");
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.mkdirSync(active, { mode: 0o700 });
  const hadPrevious = exists(target);
  const mode = args.includes("--copy") ? "copy" : "symlink";
  const record = {
    version: 1,
    checkout,
    target,
    mode,
    phase: "preparing",
    hadPrevious,
    previousIdentity: hadPrevious ? identity(target) : null,
  };
  writeRecord(record);
  const staged = path.join(active, "staged");
  fs.mkdirSync(staged, { mode: 0o700 });
  for (const entry of entries) {
    const from = path.join(checkout, entry),
      to = path.join(staged, entry);
    if (mode === "copy") copyEntry(from, to);
    else fs.symlinkSync(from, to);
  }
  const acquired = path.join(active, "orch");
  if (orch) acquireOrch(orch, acquired);
  Object.assign(record, {
    phase: "prepared",
    installedIdentity: identity(staged),
    fingerprint: fingerprint(staged),
    binary: orch ? orch.destination : null,
    // A hard link keeps dev+ino, so uninstall can tell our placed file from one that raced in.
    binaryIdentity: orch ? identity(acquired) : null,
  });
  writeRecord(record);
  if (orch) placeOrch(orch, acquired);
  if (hadPrevious) fs.renameSync(target, backup);
  record.phase = "backed-up";
  writeRecord(record);
  fs.renameSync(staged, target);
  record.phase = "installed";
  writeRecord(record);
  command("opencode", ["reload"]);
  command("node", [path.join(target, "orchestrator", "bin", "doctor.mjs")]);
  record.phase = "active";
  writeRecord(record);
  console.log(`Installed (${mode}). Private receipt: ${receipt}`);
  reportOrch(orch);
}

function uninstall() {
  if (!exists(active)) {
    console.log("No installation receipt; nothing changed.");
    return;
  }
  privateDirectory(active);
  const record = readRecord();
  const removed = path.join(active, "removed-config");
  const alreadyRemoved = record.installedIdentity && sameIdentity(removed, record.installedIdentity);
  const alreadyRestored =
    record.hadPrevious && !exists(backup) && alreadyRemoved && sameIdentity(target, record.previousIdentity);
  if (exists(backup))
    requireCondition(
      record.hadPrevious && sameIdentity(backup, record.previousIdentity),
      "Backup ownership changed; nothing changed.",
    );
  else
    requireCondition(
      !record.hadPrevious ||
        alreadyRestored ||
        (["preparing", "prepared"].includes(record.phase) && sameIdentity(target, record.previousIdentity)),
      "Expected backup is missing; nothing changed.",
    );

  if (record.installedIdentity && sameIdentity(target, record.installedIdentity)) {
    requireCondition(
      fingerprint(target) === record.fingerprint,
      "Installed config changed; nothing changed. Preserve/reconcile it before uninstalling.",
    );
    requireCondition(!exists(removed), "Unrecognized removal archive; nothing changed.");
    fs.renameSync(target, removed);
  } else if (exists(target)) {
    requireCondition(
      alreadyRestored ||
        (["preparing", "prepared"].includes(record.phase) &&
          !exists(backup) &&
          sameIdentity(target, record.previousIdentity)),
      "Target is unrecognized or changed; nothing changed.",
    );
  } else {
    requireCondition(record.phase !== "active" || alreadyRemoved, "Installed target is missing; nothing changed.");
  }
  if (exists(backup)) {
    requireCondition(!exists(target), "Restore target is occupied; backup preserved.");
    fs.renameSync(backup, target);
  }
  archiveOrch(record);
  const archive = path.join(stateRoot, `uninstalled-${Date.now()}-${crypto.randomBytes(6).toString("hex")}`);
  fs.renameSync(active, archive);
  console.log(`Uninstalled; previous config restored when present. Private archive: ${archive}`);
  console.log("Reload opencode separately when ready; no service commands were run during uninstall.");
}

let stateRoot, active, receipt, backup, target;
try {
  requireCondition(Number(process.versions.node.split(".")[0]) >= 22, "Node 22+ is required.");
  requireCondition(["darwin", "linux"].includes(process.platform), "Supported platforms: macOS and Linux.");
  const allowed = action === "install" ? ["--force", "--copy", "--skip-binary", "--help"] : ["--help"];
  requireCondition(
    ["install", "uninstall"].includes(action) && args.every((arg) => allowed.includes(arg)),
    "Unknown argument; use --help.",
  );
  if (args.includes("--help")) {
    console.log(
      action === "install" ? "Usage: ./install.sh [--copy] [--force] [--skip-binary]" : "Usage: ./uninstall.sh",
    );
    process.exit(0);
  }
  const home = os.homedir();
  requireCondition(path.isAbsolute(home), "HOME must be absolute.");
  const configHome = path.join(home, ".config");
  requireCondition(
    !process.env.XDG_CONFIG_HOME || path.resolve(process.env.XDG_CONFIG_HOME) === configHome,
    "Nondefault XDG_CONFIG_HOME is unsupported by the current runtime; nothing changed.",
  );
  target = path.join(canonical(configHome), "opencode");
  const stateHome = process.env.XDG_STATE_HOME || path.join(home, ".local", "state");
  requireCondition(path.isAbsolute(stateHome), "XDG_STATE_HOME must be absolute.");
  stateRoot = path.join(canonical(stateHome), "opencode-orchestrator");
  requireCondition(
    (action === "uninstall" || (!inside(target, checkout) && !inside(checkout, target))) &&
      !inside(stateRoot, checkout) &&
      !inside(stateRoot, target) &&
      !inside(target, stateRoot),
    "Checkout, config and private installer state must not overlap; nothing changed.",
  );
  active = path.join(stateRoot, "active");
  receipt = path.join(active, "record.json");
  backup = path.join(active, "backup");
  if (action === "uninstall" && !exists(stateRoot)) {
    console.log("No installation receipt; nothing changed.");
  } else {
    // Dependencies and target refusal are checked before even creating private state.
    let orch = null;
    if (action === "install") {
      requireCondition(!exists(active), "An installation or recovery receipt already exists; run uninstall.sh first.");
      requireCondition(
        !exists(target) || args.includes("--force"),
        "Config target already exists; use --force to preserve it.",
      );
      for (const name of ["opencode", "claude", "codex"]) {
        const result = spawnSync(name, ["--version"], { stdio: "ignore" });
        requireCondition(!result.error && result.status === 0, `${name} is required; nothing changed.`);
      }
      for (const entry of entries)
        requireCondition(exists(path.join(checkout, entry)), `Missing release entry: ${entry}`);
      requireCondition(
        exists(path.join(checkout, "orchestrator", "bin", "doctor.mjs")),
        "Release doctor is missing; nothing changed.",
      );
      requireCondition(
        existingDevice(path.dirname(target)) === existingDevice(stateRoot),
        "Config and installer state must share a filesystem for atomic backup/restore.",
      );
      orch = args.includes("--skip-binary") ? null : planOrch(home);
    }
    privateDirectory(stateRoot);
    const lock = path.join(stateRoot, "operation.lock");
    const descriptor = fs.openSync(lock, "wx", 0o600);
    fs.writeFileSync(descriptor, String(process.pid));
    fs.closeSync(descriptor);
    try {
      if (action === "install") install(orch);
      else uninstall();
    } finally {
      fs.unlinkSync(lock);
    }
  }
} catch (error) {
  console.error(error.message);
  if (active && exists(active))
    console.error(
      `Recovery state preserved at ${active}. Use uninstall.sh; do not delete the backup. A stale operation.lock after an interruption requires manual confirmation that no installer is running.`,
    );
  process.exitCode = 1;
}
NODE
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  run_installer install "$@"
fi
