// Writes lower.json, the String.prototype.toLowerCase reference jsstr.ToLower is tested against:
//   node go/testdata/jsstr/gen-lower.mjs > go/testdata/jsstr/lower.json
// For every code point except surrogates: its lowercase where that is not the code point itself,
// and the two sets the Final_Sigma rule depends on, observed through toLowerCase itself:
//   cased:      "c" + "Σ" lowers to final sigma (c is cased and not case-ignorable)
//   ignorable:  "A" + c + "Σ" lowers to final sigma but c + "Σ" does not
// and checks that the rule looks ahead with the same sets: "AΣ" + c keeps final sigma unless c is cased.
const FINAL = "ς";
const lower = {};
const cased = [];
const ignorable = [];
const addTo = (ranges, cp) => {
  const last = ranges[ranges.length - 1];
  if (last && last[1] === cp - 1) last[1] = cp;
  else ranges.push([cp, cp]);
};
for (let cp = 0; cp <= 0x10ffff; cp++) {
  if (cp >= 0xd800 && cp <= 0xdfff) continue;
  const c = String.fromCodePoint(cp);
  const l = c.toLowerCase();
  if (l !== c) lower[cp] = [...l].map((ch) => ch.codePointAt(0));
  const afterCased = ("A" + c + "Σ").toLowerCase().endsWith(FINAL);
  const afterC = (c + "Σ").toLowerCase().endsWith(FINAL);
  if (afterC) addTo(cased, cp);
  else if (afterCased) addTo(ignorable, cp);
  const beforeC = ("A\u03a3" + c).toLowerCase()[1] === FINAL;
  if (beforeC === afterC) throw new Error(`U+${cp.toString(16)}: look-ahead disagrees with look-behind`);
}
console.log(JSON.stringify({ node: process.version, icu: process.versions.icu, unicode: process.versions.unicode, lower, cased, ignorable }));
