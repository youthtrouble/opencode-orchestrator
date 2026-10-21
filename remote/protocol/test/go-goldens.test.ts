// Contract test (plan D3c): what Go's orch remote would push, pinned by the snapshot goldens,
// must validate as a PushRequest, and the fixtures' planted secrets must not reach it.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { compileSchema } from "../validate.ts";

const schema = JSON.parse(fs.readFileSync(new URL("../v1/schema.json", import.meta.url), "utf8"));
const validate = compileSchema(schema);
const goldenDir = new URL("../../../go/internal/remote/snapshot/testdata/", import.meta.url);

const goldens = fs
  .readdirSync(goldenDir)
  .filter((name) => name.endsWith(".golden.json"))
  .sort()
  .map((name) => [name, fs.readFileSync(new URL(name, goldenDir), "utf8")] as const);

// Planted in go/internal/remote/snapshot/testdata/projects/secrets (and the test's fake home).
const plantedSecrets = ["sk-test", "owner@example.com", "/srv/owner-home", "/Users/someone"];

test("the Go snapshot goldens exist, one per fixture project", () => {
  assert.ok(goldens.length >= 10, `found ${goldens.length} goldens`);
  assert.ok(goldens.some(([name]) => name === "secrets.golden.json"));
});

for (const [name, text] of goldens) {
  test(`Go golden ${name} validates as a PushRequest`, () => {
    assert.deepEqual(validate("PushRequest", JSON.parse(text)), { ok: true });
  });

  test(`Go golden ${name} carries none of the planted secrets`, () => {
    for (const secret of plantedSecrets) assert.ok(!text.includes(secret), `${name} contains ${secret}`);
  });
}
