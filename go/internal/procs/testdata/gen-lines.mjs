// Writes lines.json, the node:readline reference Lines is tested against:
//   node go/internal/procs/testdata/gen-lines.mjs > go/internal/procs/testdata/lines.json
// Each case is a sequence of chunks (hex) pushed one at a time, a macrotask apart and well
// within readline's 100 ms crlfDelay, into a readline interface over a byte stream, as the
// bridge reads claude's stdout; "lines" are the "line" events in order.
import readline from "node:readline";
import { Readable } from "node:stream";
import { setImmediate as nextMacrotask } from "node:timers/promises";

const bytes = (...parts) => Buffer.concat(parts.map((p) => (typeof p === "string" ? Buffer.from(p, "utf8") : Buffer.from(p))));

const cases = [
  ["lf", ["a\nb"]],
  ["crlf", ["a\r\nb"]],
  ["lone-cr", ["a\rb"]],
  ["cr-then-lf-across-writes", ["a\r", "\nb"]],
  ["cr-empty-write-lf", ["a\r", "", "\nb"]],
  ["cr-then-cr-across-writes", ["a\r", "\rb"]],
  ["two-empty-lines", ["\n\n"]],
  ["cr-cr-lf", ["\r\r\n"]],
  ["lf-cr", ["a\n\rb"]],
  ["final-line-unterminated", ["abc"]],
  ["trailing-lf", ["a\n"]],
  ["trailing-cr", ["a\r"]],
  ["trailing-crlf", ["a\r\n"]],
  ["empty-stream", []],
  ["empty-write", [""]],
  ["line-across-writes", ["ab", "cd\ne", "f"]],
  ["many-lines", ["1\n2\r\n3\r4\n"]],
  ["json-events", ['{"type":"system"}\n{"type":"result","result":"x\\ny"}\n']],
  ["malformed-utf8", [bytes([0xff], "a", [0xc3, 0x28], "\n", [0xe2, 0x82], "\n", [0xed, 0xa0, 0x80], "\n", [0xf0, 0x9f, 0x98], "z")]],
  ["multibyte-split", [bytes([0xe2, 0x82]), bytes([0xac], "\n")]],
  ["emoji-split-three-ways", [bytes("x", [0xf0]), bytes([0x9f, 0x98]), bytes([0x80], "y\n")]],
  ["incomplete-before-lf-across-writes", [bytes("a", [0xe2]), "\nb"]],
  ["incomplete-at-end", [bytes("ab", [0xf0, 0x9f])]],
  ["incomplete-at-end-across-writes", [bytes("ab", [0xf0]), bytes([0x9f])]],
  ["incomplete-only-at-end", [bytes([0xe2, 0x82])]],
  ["incomplete-after-line-at-end", [bytes("a\n", [0xc3])]],
  ["overlong-lead-at-end", [bytes("ab", [0xc0])]],
  ["invalid-prefix-at-end", [bytes("ab", [0xe0, 0x80])]],
  ["out-of-range-lead-at-end", [bytes("ab", [0xf5, 0x80])]],
  ["lead-f8-at-end", [bytes("ab", [0xf8, 0x80])]],
  ["invalid-byte-at-end", [bytes("ab", [0xff])]],
  ["four-continuations-at-end", [bytes("a", [0x80, 0x80, 0x80, 0x80])]],
  ["lead-then-four-continuations-at-end", [bytes("a", [0xf0, 0x80, 0x80, 0x80, 0x80])]],
  ["complete-at-end", [bytes("a", [0xe2, 0x82, 0xac])]],
  ["incomplete-cut-by-ascii-across-writes", [bytes([0xf0, 0x9f]), bytes([0x98]), "z\n"]],
  ["bom-kept", [bytes([0xef, 0xbb, 0xbf], "a\n")]],
  ["nul-and-controls", [bytes("a", [0x00], "\tb\x0bc\x0cd\n")]],
  ["line-separator", ["a b"]],
  ["paragraph-separator", ["a b"]],
  ["line-separator-then-lf", ["a \nb"]],
  ["lf-then-line-separator", ["a\n b"]],
  ["cr-then-line-separator", ["a\r b"]],
  ["line-separator-then-cr", ["a \rb"]],
  ["line-separator-then-crlf", ["a \r\nb"]],
  ["cr-then-line-separator-across-writes", ["a\r", " b"]],
  ["trailing-line-separator", ["a "]],
  ["only-line-separator", [" "]],
  ["line-separator-split-after-lead", [bytes("a", [0xe2]), bytes([0x80, 0xa8], "b")]],
  ["line-separator-split-before-last", [bytes("a", [0xe2, 0x80]), bytes([0xa8], "b")]],
  ["paragraph-separator-split-three-ways", [bytes("a", [0xe2]), bytes([0x80]), bytes([0xa9], "b")]],
  ["line-separator-split-then-lf", [bytes("a", [0xe2, 0x80]), bytes([0xa8], "\nb")]],
  ["line-separator-cut-at-end", [bytes("a", [0xe2, 0x80])]],
  ["line-separator-lead-after-invalid", [bytes("a", [0xe2, 0xe2, 0x80, 0xa8], "b")]],
  ["line-separator-in-json-event", ['{"type":"assistant","text":"x y"}\n']],
  ["other-unicode-spaces-kept", ["a\u0085b‧c‪d\u000be\n"]],
];

async function linesOf(chunks) {
  const input = new Readable({ read() {} });
  const lines = [];
  const rl = readline.createInterface({ input });
  rl.on("line", (line) => lines.push(line));
  const closed = new Promise((resolve) => rl.on("close", resolve));
  for (const chunk of chunks) {
    input.push(typeof chunk === "string" ? Buffer.from(chunk, "utf8") : chunk);
    await nextMacrotask();
  }
  input.push(null);
  await closed;
  return lines;
}

const out = [];
for (const [name, chunks] of cases) {
  const buffers = chunks.map((c) => (typeof c === "string" ? Buffer.from(c, "utf8") : c));
  out.push({ name, chunks: buffers.map((b) => b.toString("hex")), lines: await linesOf(buffers) });
}
process.stdout.write(JSON.stringify(out, null, 2) + "\n");
