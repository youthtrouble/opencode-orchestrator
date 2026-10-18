#!/usr/bin/env node
// Regenerates the jsjson goldens from Node itself: for every <name>.json input in this
// directory, writes <name>.compact.golden = JSON.stringify(JSON.parse(input)) and
// <name>.indent2.golden = JSON.stringify(JSON.parse(input), null, 2), with no trailing newline.
// The Go tests compare jsjson against these bytes. Run: node go/testdata/json/gen.mjs
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const inputs = fs.readdirSync(HERE).filter((f) => f.endsWith(".json"));
for (const input of inputs) {
  const value = JSON.parse(fs.readFileSync(path.join(HERE, input), "utf8"));
  const name = input.slice(0, -".json".length);
  fs.writeFileSync(path.join(HERE, `${name}.compact.golden`), JSON.stringify(value));
  fs.writeFileSync(path.join(HERE, `${name}.indent2.golden`), JSON.stringify(value, null, 2));
}
console.log(`wrote goldens for ${inputs.length} inputs with node ${process.version}`);
