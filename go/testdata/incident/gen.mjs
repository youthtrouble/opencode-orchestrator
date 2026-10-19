// Writes corpus.json, the lib/incident.mjs reference package incident is tested against:
//   node go/testdata/incident/gen.mjs > go/testdata/incident/corpus.json
// HOME is replaced by a fixture before the library loads, so no real home path is read or
// written; the fixture holds regexp metacharacters and "$" to prove they are matched literally.
// The emptyHome cases set HOME to "" (os.homedir() then returns "") around pure redact and
// fingerprint calls only; nothing touches the incident log.
import fs from "node:fs";
const HOME = "/opt/h o$me.dir";
process.env.HOME = HOME;
const os = await import("node:os");
if (os.homedir() !== HOME) throw new Error(`os.homedir() is ${os.homedir()}, not the fixture`);
const { redact, fingerprint, summarize } = await import("../../../orchestrator/lib/incident.mjs");

const ws = ["\t", "\n", "\v", "\f", "\r", " ", " ", " ", " ", " ", " ", " ", " ", " ", "　", "﻿"];
const notWs = ["\u0085", "᠎", "​"]; // NEL, MONGOLIAN VOWEL SEPARATOR, ZERO WIDTH SPACE

const redactInputs = [
  `${HOME}/x and ${HOME}ty and ${HOME}`,
  "/opt/hXo$meXdir/x", // the fixture as a pattern would match this
  "/Users/bob/x /home/alice/y /Users/ /home//z",
  "/Users/bob@example.com/x", // the home segment swallows the email-like part first
  "/tmp/sk-abcdefghijklmnopqrstuvwxyz0123/f", // a token inside a path
  "/Users/Zoë/x /Users/😀/y",
  ...ws.map((c) => `/Users/x${c}tail`),
  ...notWs.map((c) => `/Users/x${c}tail`),
  "123e4567-e89b-12d3-a456-426614174000 123E4567-E89B-12D3-A456-426614174000 123e4567-E89B-12d3-A456-426614174000",
  "x123e4567-e89b-12d3-a456-426614174000 123e4567-e89b-12d3-a456-4266141740001",
  "ses_abc123XYZ xses_abc ses_ ses_123e4567-e89b-12d3-a456-426614174000",
  "a.b+c@d-e.f.g @x.y me@localhost first.last@sub.example.co.uk.",
  `sk${"x".repeat(19)} sk${"x".repeat(20)} rt-${"y".repeat(20)} eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIx attention_to_detail_matters at_${"z".repeat(19)}`,
  "Bearer sk-proj-ABCDEFGHIJKLMNOPQRSTUV",
  "$& $1 $$ $` $' /Users/bob$&",
  "a".repeat(1999) + "😀tail", // the cut at 2000 splits the pair
  "a".repeat(2000) + "b",
  "/Users/bob/".repeat(300) + "end", // redaction shortens before the cut
  "",
];

const fingerprintInputs = [
  ["bridge", "crash", "exit 2 after 31s"],
  ["bridge", "crash", "EXIT 7 after 90s"],
  ["worker", "crash", "exit 2"],
  [true, "note", "true"],
  ["x", "bug", "a" + ws.join("b") + "c  \t\n d"],
  ["x", "bug", "a" + notWs.join("b") + "c"],
  ...["ΟΔΟΣ", "ΟΔΟΣ.", "ΟΔΟΣ!", "ΆΣ́", "Σ", "ΣΑ", "Α Σ", "ΑΣ:Β", "ΑΣ.Β", "Α'Σ'", "Α.Σ", "ΑΣͅ", "ΑΣͅΒ", "1Σ", "ΑΣ1"].map((m) => ["g", "note", m]),
  ["u", "note", "İ"], ["u", "note", "İΣΑΣ"], ["u", "note", "ПРИВЕТ МИР 123"], ["u", "note", "𐐀𐐁𐐂 Deseret"],
  ["u", "note", "ᲐᲑᲒ Mtavruli"], ["u", "note", "ǅǄǆ"], ["u", "note", "１２３ fullwidth digits"],
  ["u", "note", `${HOME}/ABC /Users/Bob/X ME@EXAMPLE.COM`],
  ["u", "note", "a".repeat(199) + "😀"], // the cut at 200 splits the pair
  ["u", "note", "İ".repeat(150)], // lowercasing doubles the length before the cut
  ["u", "note", "x".repeat(3000)],
];

// A deterministic generator (an LCG), so the corpus is reproducible.
let state = 20261019;
const random = (n) => { state = (state * 1103515245 + 12345) % 2147483648; return state % n; };
const iso = (minute) => `2026-10-18T10:${String(minute).padStart(2, "0")}:00.000Z`;
const summarizeInputs = [];
// Tie-heavy: up to 63 groups (V8's insertion path), timestamps from a set of four.
for (let c = 0; c < 120; c++) {
  const groups = 1 + random(63);
  const entries = [];
  for (let i = 0, n = 1 + random(70); i < n; i++) {
    entries.push({ fp: `f${random(groups)}`, at: `t${random(4)}`, summary: `s${i}`, project: ["p", "q", null, ""][random(4)] });
  }
  summarizeInputs.push(entries);
}
// 80 groups and no two with the same count and last: V8 merges runs here, any correct sort agrees.
summarizeInputs.push(Array.from({ length: 80 }, (_, g) => Array.from({ length: 1 + (g % 5) }, (_, k) => ({ fp: `g${g}`, at: iso((g * 7 + k) % 60) + g, component: "c", kind: "bug", summary: "s", project: `p${g % 3}` }))).flat());
// 64 groups and more (V8 merges runs): `at` values mixing numbers, non-numeric and ISO strings,
// null and absent, so equal counts are often incomparable or tied. Only the resulting fp order
// is stored for these (summarizeOrder), to keep the corpus small.
const orderInputs = [];
// Park-Miller, exact in doubles (the LCG above loses low bits to rounding).
let exactState = 20261019;
const exact = (n) => { exactState = (exactState * 48271) % 2147483647; return exactState % n; };
const atValues = (g) => [g, `t${g}`, iso(g % 60), null, undefined, String(g % 7), `2026`][exact(7)];
for (let c = 0; c < 24; c++) {
  const groups = 64 + exact(200);
  const entries = [];
  for (let i = 0, n = groups * 2; i < n; i++) {
    const g = exact(groups);
    const at = atValues(g);
    entries.push(at === undefined ? { fp: `g${g}` } : { fp: `g${g}`, at });
  }
  orderInputs.push(entries);
}
// The cross-vendor reviewer's fixture (R4 of reviews/go-s4b.md), generated by the Python in
// go/README.md "Fixture provenance": 80 groups, unique `at`, numbers and "t<n>" strings mixed.
orderInputs.push(fs.readFileSync(new URL("mixed-at-80.jsonl", import.meta.url), "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l)));
summarizeInputs.push(["str", 5, [1, 2], { fp: null }, {}, { fp: "a", at: 3 }, { fp: "a", at: "2026" }, { fp: "b", project: 7 }, { fp: "b", project: 7 }, { fp: "b", project: { o: 1 } }]);

const emptyHomeInputs = ["", "abc", "a😀/Users/bob", "/home/x me@example.com ses_abc", "é" + "x".repeat(1500), "😀".repeat(700)];
process.env.HOME = "";
if (os.homedir() !== "") throw new Error("an empty HOME is not honoured");
const emptyHome = {
  redact: emptyHomeInputs.map((input) => ({ input, output: redact(input) })),
  fingerprint: emptyHomeInputs.map((message) => ({ component: "c", kind: "note", message, fp: fingerprint("c", "note", message) })),
};
process.env.HOME = HOME;

console.log(JSON.stringify({
  node: process.version,
  home: HOME,
  redact: redactInputs.map((input) => ({ input, output: redact(input) })),
  fingerprint: fingerprintInputs.map(([component, kind, message]) => ({ component, kind, message, fp: fingerprint(component, kind, message) })),
  summarize: summarizeInputs.map((entries) => ({ entries, summary: summarize(entries) })),
  summarizeOrder: orderInputs.map((entries) => ({ entries, fps: summarize(entries).map((s) => s.fp) })),
  emptyHome,
}));
