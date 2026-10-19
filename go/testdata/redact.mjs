#!/usr/bin/env node
// Redact raw Claude Code stream-json captures into committable fixtures, and scan fixtures for leaks.
//
//   redact.mjs --in RAWDIR --out DIR --work CAPTURE_CWD --deny-file FILE
//   redact.mjs --scan DIR [--deny-file FILE]
//
// RAWDIR holds, per case, <case>.jsonl (stdout), <case>.stderr, <case>.exit and <case>.argv (one arg per
// line), as written by the capture helper in claude/stream-json/PROVENANCE.md. Redaction writes
// <case>.jsonl, <case>.stderr and cases.json into DIR, but only after every case has passed: an unparsable
// line, a credential-shaped string anywhere in the input (even in fields that would be discarded) or an
// init apiKeySource other than "none" fails the run with no output.
// The scan walks DIR recursively: JSON and JSONL files are decoded and every key and string value is
// checked, and every file is also checked line by line as raw text, so a duplicate JSON key cannot hide a
// value. Every check also sees each layer of JSON escaping, decoded up to MAX_DECODE_ROUNDS deep; a line
// escaped deeper fails with rule escape-depth. It prints only `file:line rule`, never the matched text,
// and exits 1 on any hit. Without --deny-file the generic rules still apply. Diagnostics never echo input: they name a file, a line and a rule.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { parseArgs } from "node:util";

const FIXTURE_UUID_PREFIX = "00000000-0000-4000-8000-";
const FIXTURE_EMAIL = "fixture@example.com";
const FIXTURE_HOME = "/home/fixture";
const UUID = /\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b/gi;
// API ids are a known prefix, `_`, then letters, digits and underscores. A whole token is either a
// redacted id (`<prefix>_fixtureNN`), a known non-id name, or a leak.
const API_ID = /(?<![A-Za-z0-9])(srvtoolu|toolu|msg|req)_[A-Za-z0-9_]+/g;
const FIXTURE_API_ID = /^(srvtoolu|toolu|msg|req)_fixture\d+$/;
// Claude Code 2.1.287 `system/init` capability names that share an id prefix.
const KNOWN_NON_IDS = new Set(["msg_lifecycle_v1"]);
// The lookbehind starts a match only at the head of a run, which keeps 1.5 MiB lines linear.
const EMAIL = /(?<![A-Za-z0-9._%+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g;
const USER_HOME = /\/(?:Users|home)\/[^/\s"'`\\]+/g;
const DARWIN_TEMP = /\/(?:private\/)?var\/folders(?:\/[^/\s"'`\\]+){0,3}/g;
// A credential in a stream is a stop-and-report event, so these fail the run instead of being redacted.
const CREDENTIAL_RULES = [
  ["anthropic-key", /sk-ant-/i],
  ["jwt", /eyJ/],
  ["bearer-token", /\b(?:sk|rt|at)[-_][A-Za-z0-9._-]{20,}/],
];
// Lists that name the capturing computer's agents, skills, commands, plugins and MCP servers. The bridge
// never reads them, and --safe-mode was shown not to strip user plugins, so they are always emptied.
const INIT_CUSTOMIZATION_KEYS = ["mcp_servers", "plugins", "agents", "skills", "slash_commands", "terminal_slash_commands"];
// Built-in tools of Claude Code 2.1.287 (StructuredOutput appears only with --json-schema). Any other
// name, e.g. an MCP or plugin tool, is dropped. Update this list when re-capturing on a new version.
const BUILT_IN_TOOLS = new Set([
  "Task", "Bash", "CronCreate", "CronDelete", "CronList", "DesignSync", "Edit", "EnterWorktree", "ExitWorktree",
  "ListAgents", "Monitor", "NotebookEdit", "PushNotification", "Read", "RemoteTrigger", "ReportFindings",
  "ScheduleWakeup", "SendMessage", "ShareOnboardingGuide", "Skill", "StructuredOutput", "TaskStop", "ToolSearch",
  "WebFetch", "WebSearch", "Workflow", "Write",
]);
const BUILT_IN_OUTPUT_STYLES = new Set(["default"]);

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

class RedactionError extends Error {}

// Identity literals match anywhere, in any case, however short: recall beats prose. Docs avoid
// collisions by wording, not by weakening the match.
const denyPattern = (literal) => new RegExp(escapeRegExp(literal), "gi");

function readDenyList(file) {
  if (!file) return [];
  return fs.readFileSync(file, "utf8").split("\n").map((s) => s.trim()).filter(Boolean)
    .sort((a, b) => b.length - a.length);
}

// The capture cwd appears both as /private/var/... (realpath) and /var/... (what the shell reports).
function cwdSpellings(work) {
  const bare = path.resolve(work).replace(/^\/private(?=\/var\/)/, "");
  return [...new Set([`/private${bare}`, bare])].sort((a, b) => b.length - a.length);
}

// One round of JSON unescaping, in a single pass as JSON.parse reads it: `\\u002d` becomes `\u002d`, the
// next layer, not `\-`. The control escapes \n \t \b \f \r are not decoded, so they can split a match.
const JSON_ESCAPE = /\\(?:u([0-9a-fA-F]{4})|([/\\"]))/g;
const unescapeOnce = (text) =>
  text.replace(JSON_ESCAPE, (_, hex, char) => (hex ? String.fromCharCode(Number.parseInt(hex, 16)) : char));

// Bounded so a pathological line costs a fixed number of passes; four layers covers any escaping that
// tooling plausibly stacks. The scan fails closed on deeper escaping (see escapedBeyondBound).
const MAX_DECODE_ROUNDS = 4;

// Every distinct layer a reader could see after repeated JSON unescaping, so `\u002d`, `\/` or a doubly
// escaped `\\u002d` cannot hide a match.
function decodedVariants(text) {
  const layers = [text];
  for (let round = 0; round < MAX_DECODE_ROUNDS; round++) {
    const next = unescapeOnce(layers[layers.length - 1]);
    if (next === layers[layers.length - 1]) break;
    layers.push(next);
  }
  return layers;
}

// True when the decoded layers stop at the round bound while the last one still unescapes further, so a
// match could hide below what was checked.
function escapedBeyondBound(layers) {
  const last = layers[layers.length - 1];
  return layers.length > MAX_DECODE_ROUNDS && unescapeOnce(last) !== last;
}

function credentialRule(text) {
  for (const variant of decodedVariants(text)) {
    for (const [rule, pattern] of CREDENTIAL_RULES) if (pattern.test(variant)) return rule;
  }
  return null;
}

// Every key and string of the original event, including fields that sanitizing will discard.
function assertNoCredentials(value, where) {
  const check = (s) => {
    const rule = credentialRule(s);
    if (rule) throw new RedactionError(`${where}: credential-shaped string (rule ${rule}); stop and report`);
  };
  if (typeof value === "string") return check(value);
  if (Array.isArray(value)) return value.forEach((v) => assertNoCredentials(v, where));
  if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value)) { check(k); assertNoCredentials(v, where); }
  }
}

// One redactor per case, so UUID and API-id numbering restarts per case but stays stable within it.
function makeStringRedactor({ cwds, home, deny, counts }) {
  const uuids = new Map();
  const apiIds = new Map();
  const count = (rule) => { counts[rule] = (counts[rule] || 0) + 1; };
  const denyRules = deny.map((literal) => ({
    pattern: denyPattern(literal),
    replacement: literal.includes("@") ? FIXTURE_EMAIL : "fixture",
  }));

  return (input) => {
    let s = input.replace(UUID, (m) => {
      const key = m.toLowerCase();
      if (key.startsWith(FIXTURE_UUID_PREFIX)) return m;
      if (!uuids.has(key)) uuids.set(key, FIXTURE_UUID_PREFIX + String(uuids.size + 1).padStart(12, "0"));
      count("uuid");
      return uuids.get(key);
    });
    s = s.replace(API_ID, (m, prefix) => {
      if (KNOWN_NON_IDS.has(m) || FIXTURE_API_ID.test(m)) return m;
      if (!apiIds.has(m)) apiIds.set(m, `${prefix}_fixture${String(apiIds.size + 1).padStart(2, "0")}`);
      count("api-id");
      return apiIds.get(m);
    });
    for (const cwd of cwds) s = s.replace(new RegExp(escapeRegExp(cwd), "g"), () => { count("capture-cwd"); return "/tmp/fixture"; });
    if (home) s = s.replace(new RegExp(escapeRegExp(home), "g"), () => { count("home"); return FIXTURE_HOME; });
    s = s.replace(USER_HOME, (m) => {
      if (m === FIXTURE_HOME) return m;
      count("user-home");
      return FIXTURE_HOME;
    });
    s = s.replace(DARWIN_TEMP, () => { count("darwin-temp"); return "/tmp"; });
    for (const { pattern, replacement } of denyRules) s = s.replace(pattern, () => { count("deny-literal"); return replacement; });
    s = s.replace(EMAIL, (m) => {
      if (m === FIXTURE_EMAIL) return m;
      count("email");
      return FIXTURE_EMAIL;
    });
    return s;
  };
}

// Keys too: the CLI keys some maps by id (result `wire_tool_inputs` is keyed by tool_use id).
function mapStrings(value, redactString) {
  if (typeof value === "string") return redactString(value);
  if (Array.isArray(value)) return value.map((v) => mapStrings(v, redactString));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [redactString(k), mapStrings(v, redactString)]));
  }
  return value;
}

function sanitizeInit(event, { where, sanitized }) {
  if (event.apiKeySource !== "none") throw new RedactionError(`${where}: system/init apiKeySource is not "none"; stop and report`);
  const note = (what) => { sanitized[what] = (sanitized[what] || 0) + 1; };
  for (const key of INIT_CUSTOMIZATION_KEYS) {
    if (!(key in event)) continue;
    if (Array.isArray(event[key]) && event[key].length === 0) continue;
    event[key] = [];
    note(`${key} emptied`);
  }
  if ("tools" in event) {
    const tools = Array.isArray(event.tools) ? event.tools : [];
    const kept = tools.filter((tool) => typeof tool === "string" && BUILT_IN_TOOLS.has(tool));
    if (!Array.isArray(event.tools) || kept.length !== tools.length) note("tools filtered to built-ins");
    event.tools = kept;
  }
  if ("output_style" in event && !BUILT_IN_OUTPUT_STYLES.has(event.output_style)) {
    event.output_style = "fixture";
    note("output_style replaced");
  }
}

// Thinking signatures and redacted thinking are opaque encrypted blobs: unauditable, and never read by the
// bridge, so they are replaced rather than kept.
function sanitizeThinking(event, { sanitized }) {
  for (const block of Array.isArray(event.message?.content) ? event.message.content : []) {
    if (block?.type === "thinking" && typeof block.signature === "string") {
      block.signature = "fixture-signature";
      sanitized["thinking signature replaced"] = (sanitized["thinking signature replaced"] || 0) + 1;
    } else if (block?.type === "redacted_thinking" && typeof block.data === "string") {
      block.data = "fixture-redacted-thinking";
      sanitized["redacted_thinking replaced"] = (sanitized["redacted_thinking replaced"] || 0) + 1;
    }
  }
}

function describeEvent(event) {
  const name = event.subtype ? `${event.type}/${event.subtype}` : event.type;
  const blocks = event.message?.content;
  if (!Array.isArray(blocks)) return name;
  return `${name}[${blocks.map((b) => (b.name ? `${b.type}:${b.name}` : b.type)).join(",")}]`;
}

function caseNotes(events, exitCode) {
  const sequence = events.map(describeEvent).join(", ") || "(no stdout events)";
  const result = events.find((e) => e.type === "result");
  const outcome = result
    ? `result subtype=${result.subtype} is_error=${result.is_error} num_turns=${result.num_turns} permission_denials=${(result.permission_denials || []).length}`
    : "no result event";
  return `${outcome}; exit ${exitCode}; events: ${sequence}`;
}

function parseLine(line, where) {
  try {
    return JSON.parse(line);
  } catch {
    // The native message can quote the input, so it is deliberately not reported.
    throw new RedactionError(`${where}: unparsable JSON line (rule json-parse)`);
  }
}

function redactCase(rawDir, caseName, context) {
  const raw = (ext) => fs.readFileSync(path.join(rawDir, `${caseName}.${ext}`), "utf8");
  const redactString = makeStringRedactor(context);
  const redactText = (text, file) => text.split("\n").map((line, i) => {
    const rule = credentialRule(line);
    if (rule) throw new RedactionError(`${file}:${i + 1}: credential-shaped string (rule ${rule}); stop and report`);
    return redactString(line);
  }).join("\n");

  const events = raw("jsonl").split("\n").map((line, i) => [line, `${caseName}.jsonl:${i + 1}`])
    .filter(([line]) => line !== "")
    .map(([line, where]) => {
      const event = parseLine(line, where);
      assertNoCredentials(event, where);
      if (event.type === "system" && event.subtype === "init") sanitizeInit(event, { where, ...context });
      if (event.type === "assistant") sanitizeThinking(event, context);
      return mapStrings(event, redactString);
    });
  const exitCode = Number.parseInt(raw("exit").trim(), 10);
  if (!Number.isInteger(exitCode)) throw new RedactionError(`${caseName}.exit: not an integer`);
  const argv = redactText(raw("argv").replace(/\n$/, ""), `${caseName}.argv`).split("\n");
  return {
    jsonl: events.map((e) => JSON.stringify(e) + "\n").join(""),
    stderr: redactText(raw("stderr"), `${caseName}.stderr`),
    entry: { exit_code: exitCode, synthetic: false, argv_redacted: argv, notes: caseNotes(events, exitCode) },
  };
}

function redact({ in: rawDir, out: outDir, work, "deny-file": denyFile }) {
  if (!rawDir || !outDir || !work || !denyFile) throw new RedactionError("redact needs --in, --out, --work and --deny-file");
  const context = { cwds: cwdSpellings(work), home: os.homedir(), deny: readDenyList(denyFile), counts: {}, sanitized: {} };
  const caseNames = fs.readdirSync(rawDir).filter((f) => f.endsWith(".jsonl")).map((f) => f.slice(0, -".jsonl".length)).sort();
  if (caseNames.length === 0) throw new RedactionError("no <case>.jsonl files in --in");
  const redacted = caseNames.map((name) => [name, redactCase(rawDir, name, context)]);

  fs.mkdirSync(outDir, { recursive: true });
  for (const [name, { jsonl, stderr }] of redacted) {
    fs.writeFileSync(path.join(outDir, `${name}.jsonl`), jsonl);
    fs.writeFileSync(path.join(outDir, `${name}.stderr`), stderr);
  }
  const cases = Object.fromEntries(redacted.map(([name, { entry }]) => [name, entry]));
  fs.writeFileSync(path.join(outDir, "cases.json"), JSON.stringify(cases, null, 2) + "\n");
  console.log(`redacted ${caseNames.length} cases: ${caseNames.join(", ")}`);
  console.log(`replacements by rule: ${JSON.stringify(context.counts)}`);
  console.log(`init sanitizing (events): ${JSON.stringify(context.sanitized)}`);
}

// Rules over one decoded string. Each returns true on a leak.
function stringRules(deny) {
  return [
    ...deny.map(denyPattern).map((pattern) => ["deny-literal", (s) => s.search(pattern) !== -1]),
    ["users-path", (s) => s.includes("/Users/")],
    ["user-home", (s) => (s.match(USER_HOME) || []).some((m) => m !== FIXTURE_HOME)],
    ["darwin-temp-path", (s) => s.includes("/var/folders")],
    ["email", (s) => (s.match(EMAIL) || []).some((m) => m !== FIXTURE_EMAIL)],
    ...CREDENTIAL_RULES.map(([rule, pattern]) => [rule, (s) => pattern.test(s)]),
    ["uuid", (s) => (s.match(UUID) || []).some((m) => !m.toLowerCase().startsWith(FIXTURE_UUID_PREFIX))],
    ["api-id", (s) => (s.match(API_ID) || []).some((m) => !KNOWN_NON_IDS.has(m) && !FIXTURE_API_ID.test(m))],
  ];
}

function collectStrings(value, out) {
  if (typeof value === "string") out.push(value);
  else if (Array.isArray(value)) value.forEach((v) => collectStrings(v, out));
  else if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value)) { out.push(k); collectStrings(v, out); }
  }
  return out;
}

function hasUnsafeApiKeySource(value) {
  if (Array.isArray(value)) return value.some(hasUnsafeApiKeySource);
  if (!value || typeof value !== "object") return false;
  if ("apiKeySource" in value && value.apiKeySource !== "none") return true;
  return Object.values(value).some(hasUnsafeApiKeySource);
}

// Rule names that fire for one decoded JSON value (a JSONL line or a whole JSON file). A decoded string
// can still hold escapes when its source was escaped twice, so each one is checked at every layer.
function jsonHits(value, rules) {
  const strings = collectStrings(value, []).flatMap(decodedVariants);
  const hits = rules.filter(([, leaks]) => strings.some(leaks)).map(([rule]) => rule);
  if (hasUnsafeApiKeySource(value)) hits.push("api-key-source");
  return hits;
}

// Each textual `"apiKeySource": <value>` must have exactly the value "none". The value is captured whole
// (quoted string or bare token) before comparing, so whitespace around the colon cannot hide or fake it.
const API_KEY_SOURCE_TEXT = /"apiKeySource"\s*:\s*("(?:[^"\\]|\\.)*"|[^\s,}\]]*)/g;
function hasUnsafeApiKeySourceText(text) {
  return [...text.matchAll(API_KEY_SOURCE_TEXT)].some((m) => m[1] !== '"none"');
}

// Rule names that fire for one text line, read raw and with JSON escapes decoded.
function textHits(line, rules) {
  const variants = decodedVariants(line);
  const hits = rules.filter(([, leaks]) => variants.some(leaks)).map(([rule]) => rule);
  if (variants.some(hasUnsafeApiKeySourceText)) hits.push("api-key-source");
  if (escapedBeyondBound(variants)) hits.push("escape-depth");
  return hits;
}

function decodeJson(text) {
  try { return { value: JSON.parse(text) }; } catch { return { error: true }; }
}

function scanFile(file, rules) {
  const text = fs.readFileSync(file, "utf8");
  const findings = [];
  const add = (line, hits) => hits.forEach((rule) => findings.push(`${file}:${line} ${rule}`));
  if (file.endsWith(".json")) {
    const { value, error } = decodeJson(text);
    add(1, error ? ["json-parse"] : jsonHits(value, rules));
    text.split("\n").forEach((line, i) => add(i + 1, textHits(line, rules)));
  } else if (file.endsWith(".jsonl")) {
    text.split("\n").forEach((line, i) => {
      if (line === "") return;
      const { value, error } = decodeJson(line);
      add(i + 1, error ? ["json-parse"] : jsonHits(value, rules));
      add(i + 1, textHits(line, rules));
    });
  } else {
    text.split("\n").forEach((line, i) => add(i + 1, textHits(line, rules)));
  }
  return [...new Set(findings)];
}

function filesUnder(dir) {
  return fs.readdirSync(dir, { withFileTypes: true, recursive: true })
    .filter((d) => d.isFile()).map((d) => path.join(d.parentPath, d.name)).sort();
}

function scan({ scan: dir, "deny-file": denyFile }) {
  const rules = stringRules(readDenyList(denyFile));
  const findings = filesUnder(dir).flatMap((file) => scanFile(file, rules));
  for (const finding of findings) console.log(finding);
  console.log(findings.length === 0 ? `scan clean: ${dir}${denyFile ? "" : " (no deny file)"}` : `scan FAILED: ${findings.length} hits`);
  return findings.length === 0;
}

const { values } = parseArgs({
  options: { in: { type: "string" }, out: { type: "string" }, work: { type: "string" }, "deny-file": { type: "string" }, scan: { type: "string" } },
});
try {
  if (values.scan) process.exit(scan(values) ? 0 : 1);
  redact(values);
} catch (e) {
  // Native error messages can quote file contents, so only our own messages are printed in full.
  if (e instanceof RedactionError) console.error(`redact.mjs: ${e.message}`);
  else console.error(`redact.mjs: internal error (${e?.name ?? "unknown"}${e?.code ? ` ${e.code}` : ""})`);
  process.exit(1);
}
