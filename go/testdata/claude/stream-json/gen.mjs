#!/usr/bin/env node
// Build the synthetic stream-json cases from the committed, redacted `ok` capture.
//
//   node gen.mjs          write the synthetic <case>.jsonl/.stderr files and their cases.json entries
//   node gen.mjs --check  regenerate in memory; exit 1 if any file differs by a byte
//
// Real cases come from ../../redact.mjs and are never touched here, so this needs no raw data.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const DIR = path.dirname(fileURLToPath(import.meta.url));
// 1.5 MiB: one line well past the 1 MiB scanner buffers a line reader might assume.
const LONG_LINE_CHARS = 1_572_864;

const read = (name) => fs.readFileSync(path.join(DIR, name), "utf8");
const parseLines = (text) => text.split("\n").filter(Boolean).map((line) => JSON.parse(line));
const toJsonl = (events) => events.map((e) => JSON.stringify(e) + "\n").join("");

function deterministicAscii(length) {
  const unit = "0123456789abcdefghijklmnopqrstuvwxyz";
  return unit.repeat(Math.ceil(length / unit.length)).slice(0, length);
}

function withResult(events, patch) {
  return events.map((e) => (e.type === "result" ? { ...e, ...patch } : e));
}

function synthesize(cases) {
  const ok = parseLines(read("ok.jsonl"));
  const okStderr = read("ok.stderr");
  const okArgv = cases.ok.argv_redacted;
  const init = ok.find((e) => e.type === "system" && e.subtype === "init");
  const result = ok.find((e) => e.type === "result");

  const longLine = ok.map((e) => {
    if (e.type !== "user") return e;
    const content = e.message.content.map((b) => (b.type === "tool_result" ? { ...b, content: deterministicAscii(LONG_LINE_CHARS) } : b));
    return { ...e, message: { ...e.message, content } };
  });

  return {
    "usage-limit": {
      jsonl: toJsonl([init, { ...result, is_error: true, subtype: "success", result: "You've hit your session limit · resets 3am", num_turns: 1 }]),
      stderr: okStderr,
      entry: {
        exit_code: 1, synthetic: true, argv_redacted: okArgv,
        notes: "mock: ok's system/init plus ok's result with is_error=true, subtype=success, num_turns=1 and a limit message (usage and modelUsage kept from ok). The real limit text and exit code are unknown; this text matches the first branch of claude-exec.mjs LIMIT_TEXT. Exit 1 mirrors the real is_error=true runs (max-turns, bad-model).",
      },
    },
    "long-line": {
      jsonl: toJsonl(longLine),
      stderr: okStderr,
      entry: {
        exit_code: cases.ok.exit_code, synthetic: true, argv_redacted: okArgv,
        notes: `ok with the user tool_result content replaced by ${LONG_LINE_CHARS} deterministic ASCII characters, so one stdout line exceeds 1 MiB.`,
      },
    },
    "success-is-error": {
      jsonl: toJsonl(withResult(ok, { is_error: true, subtype: "success", result: "boom: synthetic failure text" })),
      stderr: okStderr,
      entry: {
        exit_code: 1, synthetic: true, argv_redacted: okArgv,
        notes: "ok with the result's is_error=true, subtype=success and a failure text: the bridge must report the result text as the error (13da780). The real bad-model run has the same subtype/is_error shape.",
      },
    },
  };
}

function expectedFiles() {
  const cases = JSON.parse(read("cases.json"));
  const synthetic = synthesize(cases);
  const real = Object.fromEntries(Object.entries(cases).filter(([, entry]) => !entry.synthetic));
  const allCases = { ...real, ...Object.fromEntries(Object.entries(synthetic).map(([name, c]) => [name, c.entry])) };
  const files = { "cases.json": JSON.stringify(allCases, null, 2) + "\n" };
  for (const [name, { jsonl, stderr }] of Object.entries(synthetic)) {
    files[`${name}.jsonl`] = jsonl;
    files[`${name}.stderr`] = stderr;
  }
  return files;
}

const files = expectedFiles();
if (process.argv.includes("--check")) {
  const stale = Object.entries(files).filter(([name, content]) => {
    const file = path.join(DIR, name);
    return !fs.existsSync(file) || !fs.readFileSync(file).equals(Buffer.from(content));
  }).map(([name]) => name);
  if (stale.length > 0) {
    console.error(`gen.mjs --check: out of date: ${stale.join(", ")}`);
    process.exit(1);
  }
  console.log("gen.mjs --check: synthetic cases up to date");
} else {
  for (const [name, content] of Object.entries(files)) fs.writeFileSync(path.join(DIR, name), content);
  console.log(`gen.mjs: wrote ${Object.keys(files).join(", ")}`);
}
