// Writes orders.json, the Array.prototype.sort reference package jssort is tested against:
//   node go/testdata/jssort/gen.mjs > go/testdata/jssort/orders.json
// Each case sorts items {id, k} (id = input position) with one of three comparators, two of
// them inconsistent, so the resulting order depends on every comparison V8 makes. The keys come
// from the generator below, which jssort_test.go reproduces; each case stores only the sha256 of
// the resulting ids joined by ",", to keep the file small (rerun this script to see an order).
import crypto from "node:crypto";
const comparators = {
  "never-equal": (a, b) => (a.k < b.k ? -1 : 1),
  hash: (a, b) => ((a.id * 7919 + b.id * 104729) % 5) - 2,
  "key-hash": (a, b) => ((a.k * 31 + b.k * 17 + (a.id % 3)) % 7) - 3,
  "tie-hash": (a, b) => a.k - b.k || ((a.id * 7 + b.id * 5) % 3) - 1,
  consistent: (a, b) => a.k - b.k,
};

// keys(pattern, n, seed): a Park-Miller generator (exact in doubles) seeded per case.
function keys(pattern, n, seed) {
  let state = 20261019 + n * 7 + seed * 1000003;
  const random = (m) => { state = (state * 48271) % 2147483647; return state % m; };
  let run = 0; // ragged-runs: natural ascending runs of random length from random keys
  let base = 0, j = 0; // ragged-ties: the same, with many keys shared across runs
  return Array.from({ length: n }, (_, i) => {
    switch (pattern) {
      case "random-small": return random(4);
      case "random-wide": return random(1000);
      case "ascending-blocks": return i % 40;
      case "descending-blocks": return 40 - (i % 40);
      case "two-runs": return i < n >> 1 ? i : i - (n >> 1);
      case "ragged-runs": return (run = random(60) === 0 ? random(100) : run + 1);
      case "ragged-ties":
        if (random(60) === 0) { base = random(5); j = 0; }
        return base + Math.floor(j++ / 10);
      case "nearly-sorted": return i % 50 === 49 ? i - 25 : i;
      case "sorted-plus-one": return i === n - 1 ? -1 : i;
    }
    throw new Error(pattern);
  });
}

const sizes = [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 33, 63, 64, 65, 100, 129, 257, 500, 1000, 2500, 6000];
// More seeds where the outcome is most sensitive: pseudo-random comparators on mid sizes, and
// tie-hash on ragged runs (the merge-stack rules).
const seedsFor = (comparator, pattern, n) =>
  n < 64 ? 1 : comparator === "key-hash" && n <= 257 ? 6 : comparator === "tie-hash" && pattern.startsWith("ragged") ? 4 : 2;
const cases = [];
for (const comparator of Object.keys(comparators))
  for (const pattern of ["random-small", "random-wide", "ascending-blocks", "descending-blocks", "two-runs", "ragged-runs", "ragged-ties", "nearly-sorted", "sorted-plus-one"])
    for (const n of sizes)
      for (let seed = 0; seed < seedsFor(comparator, pattern, n); seed++) {
        const items = keys(pattern, n, seed).map((k, id) => ({ id, k }));
        items.sort(comparators[comparator]);
        const ids = items.map((item) => item.id);
        const sha256 = crypto.createHash("sha256").update(ids.join(",")).digest("hex");
        cases.push({ comparator, pattern, n, seed, sha256 });
      }
console.log(JSON.stringify({ node: process.version, cases }));
