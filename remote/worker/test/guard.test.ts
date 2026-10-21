// Public-repo guard (plan D16): no owner identifier may land in a committed file under remote/.
import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const EMAIL = /[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/g;
// Hostnames are case-insensitive, so an upper- or mixed-case Access host names the same team.
const ACCESS_HOST = /([A-Za-z0-9<>_-]+)\.cloudflareaccess\.com/gi;
const ALLOWED_ACCESS_TEAMS = new Set(["<team>", "example", "other-example"]);
const HEX_64 = /[0-9a-fA-F]{64,}/g;
// A Cloudflare account, zone or D1 id is 32 hex digits. Such runs are common in code (digests,
// project keys), so they count only in config or credential contexts. Requiring no adjacent letter
// or digit keeps base64 (lockfile integrity hashes) and longer hex runs out.
const HEX_32 = /(?<![A-Za-z0-9])[0-9a-fA-F]{32}(?![A-Za-z0-9])/g;
const CONFIG_FILE = /(\.jsonc?|\.toml)$|(^|\/)\.env[^/]*$/i;
const CLOUDFLARE_ID_KEY = /(account|database|zone)_?id/i;
const UUID = /[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}/g;
const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
// `wrangler types` appends Cloudflare's published runtime declarations, whose doc examples include
// hex digests and Cloudflare id fields. Only the hex-run checks are skipped there; every other check
// still covers them.
const GENERATED_TYPES = "remote/worker/worker-configuration.d.ts";
const RUNTIME_TYPES_MARKER = "// Begin runtime types";

const HEX_ALPHABET = "0123456789abcdef";

/**
 * A run of one repeated digit, such as the protocol examples' `aaaa...`, or the hex alphabet in
 * order (the examples' project key `0123...cdef`, repeated, either direction) is a placeholder.
 */
function isPlaceholderHex(run: string): boolean {
  const lower = run.toLowerCase();
  const alphabetRun = (alphabet: string) => alphabet.repeat(Math.ceil(lower.length / 16)).startsWith(lower);
  return /^(.)\1*$/.test(lower) || alphabetRun(HEX_ALPHABET) || alphabetRun([...HEX_ALPHABET].reverse().join(""));
}

function isConfigContext(path: string, line: string): boolean {
  return CONFIG_FILE.test(path) || CLOUDFLARE_ID_KEY.test(line);
}

/**
 * The protocol examples number their ids, as in `00000000-0000-4000-8000-000000000001`. A random
 * UUID has about two zero digits of 32; requiring at least half to be zero cannot match a real one.
 */
function isPlaceholderUuid(uuid: string): boolean {
  return (uuid.match(/0/g)?.length ?? 0) >= 16;
}

export function findLeaks(path: string, text: string, privateDomain: string | undefined): string[] {
  const leaks: string[] = [];
  let inRuntimeTypes = false;
  text.split("\n").forEach((line, index) => {
    if (path === GENERATED_TYPES && line.startsWith(RUNTIME_TYPES_MARKER)) inRuntimeTypes = true;
    const where = `${path}:${index + 1}`;
    for (const [email] of line.matchAll(EMAIL)) {
      if (!email.toLowerCase().endsWith("@example.com")) leaks.push(`${where}: email address`);
    }
    for (const [, team] of line.matchAll(ACCESS_HOST)) {
      if (!ALLOWED_ACCESS_TEAMS.has(team.toLowerCase())) leaks.push(`${where}: Access team host`);
    }
    for (const [run] of line.matchAll(HEX_64)) {
      if (!inRuntimeTypes && !isPlaceholderHex(run)) leaks.push(`${where}: 64-hex string`);
    }
    if (!inRuntimeTypes && isConfigContext(path, line)) {
      for (const [run] of line.matchAll(HEX_32)) {
        if (!isPlaceholderHex(run)) leaks.push(`${where}: 32-hex Cloudflare id`);
      }
    }
    for (const [uuid] of line.matchAll(UUID)) {
      if (!isPlaceholderUuid(uuid)) leaks.push(`${where}: UUID`);
    }
    // The owner's own domain cannot be written here (that would leak it), so this check runs only
    // where WORK_TRAINS_PRIVATE_DOMAIN is set, which is the owner's machine and never CI.
    if (privateDomain && line.toLowerCase().includes(privateDomain.toLowerCase())) {
      leaks.push(`${where}: private domain`);
    }
  });
  return leaks;
}

/** Tracked files plus untracked, non-ignored ones: everything the next commit could include. */
function committableFiles(repoRoot: string): string[] {
  const listing = execFileSync("git", ["ls-files", "-z", "--cached", "--others", "--exclude-standard", "remote"], {
    cwd: repoRoot,
    encoding: "utf8",
  });
  return listing.split("\0").filter(Boolean);
}

/** The guard needs git to know what is committable; outside a checkout it fails and says why. */
function gitRepoRoot(): string {
  try {
    return execFileSync("git", ["rev-parse", "--show-toplevel"], { encoding: "utf8", stdio: "pipe" }).trim();
  } catch (error) {
    throw new Error("the public-repo guard must run inside a git checkout of this repository", { cause: error });
  }
}

test("no committable file under remote/ carries an owner identifier", () => {
  const repoRoot = gitRepoRoot();
  const files = committableFiles(repoRoot);
  assert.ok(files.includes("remote/worker/wrangler.jsonc"), "git listed no remote/ files; is the cwd in the repo?");
  const privateDomain = process.env.WORK_TRAINS_PRIVATE_DOMAIN || undefined;
  const leaks = files.flatMap((file) => findLeaks(file, readFileSync(join(repoRoot, file), "utf8"), privateDomain));
  assert.deepEqual(leaks, []);
});

// Built at runtime so this file does not trip its own guard.
const realLookingUuid = ["3f2b9c4e", "7a1d", "4e6b", "9c2f", "8d5e1a3b7c90"].join("-");
const realLookingAccountId = ["3f2b9c4e", "7a1d4e6b", "9c2f8d5e", "1a3b7c90"].join("");

test("the guard catches each kind of identifier", () => {
  // Built at runtime so this file does not trip its own guard.
  const at = "@";
  const lines = [
    `owner${at}example.com is a placeholder`,
    `someone${at}mail.test`,
    `https://<team>.cloudflareaccess.com and https://example.cloudflareaccess.com`,
    `https://${"acme"}.cloudflareaccess.com`,
    `aud ${"ab".repeat(32)} hash ${"a".repeat(64)}`,
    `id ${ZERO_UUID} and ${"00000000-0000-4000-8000-000000000001"}`,
    `id ${realLookingUuid}`,
    "trains.private.test",
  ].join("\n");
  assert.deepEqual(findLeaks("x.md", lines, "PRIVATE.test"), [
    "x.md:2: email address",
    "x.md:4: Access team host",
    "x.md:5: 64-hex string",
    "x.md:7: UUID",
    "x.md:8: private domain",
  ]);
});

test("after the runtime-types marker only the 64-hex check is skipped", () => {
  const at = "@";
  const generated = [
    `ACCESS_AUD: "${"ab".repeat(32)}";`,
    RUNTIME_TYPES_MARKER,
    `digest ${"cd".repeat(32)}`,
    `someone${at}mail.test`,
    `https://${"acme"}.cloudflareaccess.com`,
    `id ${realLookingUuid}`,
  ].join("\n");
  const where = GENERATED_TYPES;
  assert.deepEqual(findLeaks(where, generated, undefined), [
    `${where}:1: 64-hex string`,
    `${where}:4: email address`,
    `${where}:5: Access team host`,
    `${where}:6: UUID`,
  ]);
  // The exemption belongs to the generated file alone.
  assert.deepEqual(findLeaks("other.d.ts", generated, undefined), [
    "other.d.ts:1: 64-hex string",
    "other.d.ts:3: 64-hex string",
    "other.d.ts:4: email address",
    "other.d.ts:5: Access team host",
    "other.d.ts:6: UUID",
  ]);
});

test("an Access team host is caught whatever its case", () => {
  const lines = [
    `https://${"ACME"}.CLOUDFLAREACCESS.COM`,
    `https://${"Acme"}.CloudflareAccess.com`,
    `https://<TEAM>.CLOUDFLAREACCESS.COM and https://${"Example"}.cloudflareaccess.com`,
  ].join("\n");
  assert.deepEqual(findLeaks("x.md", lines, undefined), ["x.md:1: Access team host", "x.md:2: Access team host"]);
});

test("a 32-hex Cloudflare id is caught in config and credential contexts only", () => {
  const id = realLookingAccountId;
  for (const path of ["wrangler.jsonc", "wrangler.toml", "x/config.json", ".env", "x/.env.local"]) {
    assert.deepEqual(findLeaks(path, `"${id}"`, undefined), [`${path}:1: 32-hex Cloudflare id`], path);
  }
  const keyedLines = [`account_id = "${id}"`, `accountId: "${id}"`, `"database_id": "${id}"`, `ZONE_ID=${id}`];
  assert.deepEqual(
    findLeaks("x.ts", keyedLines.join("\n"), undefined),
    keyedLines.map((_, index) => `x.ts:${index + 1}: 32-hex Cloudflare id`),
  );
  const notIds = [
    `const key = "${id}";`, // code outside a config context: digests and project keys are ordinary
    `"key": "${"0123456789abcdef".repeat(2)}", "next": "${"f".repeat(32)}"`, // placeholders
    `"integrity": "sha512-${id}${"Zx".repeat(8)}=="`, // part of a longer base64 run
  ];
  assert.deepEqual(findLeaks("x.ts", notIds[0], undefined), []);
  assert.deepEqual(findLeaks("x.json", notIds.slice(1).join("\n"), undefined), []);
});

test("in the generated types' runtime section a 32-hex id field is not a leak", () => {
  const generated = [`account_id: "${realLookingAccountId}"`, RUNTIME_TYPES_MARKER, `account_id: "${realLookingAccountId}"`];
  assert.deepEqual(findLeaks(GENERATED_TYPES, generated.join("\n"), undefined), [
    `${GENERATED_TYPES}:1: 32-hex Cloudflare id`,
  ]);
});
