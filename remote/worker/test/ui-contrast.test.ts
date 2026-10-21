// Every colour pair the page draws, checked against WCAG 2.x contrast minimums with the tokens read
// from public/app.css, so a palette edit that breaks AA fails here. Run with --test-reporter=spec to
// see the table.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const TEXT = 4.5; // 1.4.3, normal-size text
const NON_TEXT = 3; // 1.4.11, focus rings and progress fills

/** [foreground, background, minimum, where the pair appears] */
const PAIRS: readonly [string, string, number, string][] = [
  ["ink", "ground", TEXT, "body text, recommended reply"],
  ["ink", "surface", TEXT, "cards, project rows, textarea"],
  ["ink-2", "ground", TEXT, "section labels, Mac seen, empty line"],
  ["ink-2", "surface", TEXT, "meta, updated, eyebrow"],
  ["ink-3", "ground", TEXT, "tertiary text on the page"],
  ["ink-3", "surface", TEXT, "reply counter, event meta"],
  ["accent", "ground", TEXT, "links"],
  ["accent", "surface", TEXT, "Show more link"],
  ["on-accent", "accent", TEXT, "Accept, Send, Retry"],
  ["on-accent", "accent-pressed", TEXT, "primary button pressed"],
  ["surface", "ink", TEXT, "undo toast text"],
  ["toast-link", "ink", TEXT, "undo toast Undo"],
  ["working", "working-tint", TEXT, "Working pill"],
  ["needs", "needs-tint", TEXT, "Needs you pill, blocking and stale badges"],
  ["paused", "paused-tint", TEXT, "Paused and Idle pills"],
  ["failed", "failed-tint", TEXT, "Failed pill, error banner"],
  ["done", "done-tint", TEXT, "Done pill"],
  ["needs", "surface", TEXT, "unconfirmed write line"],
  ["accent", "ground", NON_TEXT, "focus ring on the page"],
  ["accent", "surface", NON_TEXT, "focus ring on a card"],
  ["toast-link", "ink", NON_TEXT, "focus ring on the undo toast"],
  ...["working", "needs", "paused", "failed", "done"].flatMap((tone): [string, string, number, string][] => [
    [tone, "hairline", NON_TEXT, `${tone} progress fill on its track`],
    [tone, "surface", NON_TEXT, `${tone} status dot or CI mark`],
  ]),
];

/** The `--name: #rrggbb` declarations of app.css's first `:root` block. */
function rootColours(): Map<string, string> {
  const css = readFileSync(new URL("../public/app.css", import.meta.url), "utf8");
  const root = css.match(/:root\s*\{([^}]*)\}/);
  assert.ok(root, "app.css has a :root block");
  return new Map([...root[1].matchAll(/--([a-z0-9-]+):\s*(#[0-9a-f]{6})\s*;/gi)].map(([, name, hex]) => [name, hex]));
}

function relativeLuminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((start) => {
    const channel = parseInt(hex.slice(start, start + 2), 16) / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [light, dark] = [relativeLuminance(a), relativeLuminance(b)].sort((x, y) => y - x);
  return (light + 0.05) / (dark + 0.05);
}

test("every colour pair the page draws meets its WCAG AA minimum", (t) => {
  const colours = rootColours();
  const hex = (name: string) => {
    const value = colours.get(name);
    assert.ok(value, `--${name} is a hex colour in :root`);
    return value;
  };
  const rows = PAIRS.map(([fg, bg, minimum, use]) => ({ fg, bg, minimum, use, ratio: contrast(hex(fg), hex(bg)) }));
  t.diagnostic("foreground | background | ratio | minimum | use");
  for (const row of rows) t.diagnostic(`${row.fg} | ${row.bg} | ${row.ratio.toFixed(2)} | ${row.minimum} | ${row.use}`);
  for (const row of rows)
    assert.ok(row.ratio >= row.minimum, `${row.fg} on ${row.bg} is ${row.ratio.toFixed(2)}:1, below ${row.minimum}:1`);
});

test("every colour token in :root is checked in at least one pair", () => {
  const paired = new Set(PAIRS.flatMap(([fg, bg]) => [fg, bg]));
  const unpaired = [...rootColours().keys()].filter((name) => !paired.has(name));
  assert.deepEqual(unpaired, [], "add a pair for each new colour token");
});

test("the contrast formula matches WCAG's reference values", () => {
  assert.equal(contrast("#000000", "#ffffff"), 21);
  assert.equal(contrast("#ffffff", "#ffffff"), 1);
  assert.equal(contrast("#767676", "#ffffff").toFixed(2), "4.54");
});
