import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const runner = fileURLToPath(new URL("../go/parity/run.sh", import.meta.url));

test("parity --only refuses unknown groups before building or extracting the oracle", () => {
  for (const group of ["bogus", "json,bogus", "jsref,bogus", "json.*"]) {
    const result = spawnSync("/bin/sh", [runner, "--only", group], {
      encoding: "utf8",
      env: { ...process.env, ORCH_JS_REF: "missing-reference-must-not-be-read" },
    });
    assert.equal(result.status, 2, group);
    assert.match(result.stderr, /^unknown parity group: /, group);
    assert.equal(result.stdout, "", group);
  }
});
