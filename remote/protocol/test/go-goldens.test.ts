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

// Planted in go/internal/remote/snapshot/testdata/projects/secrets (and the test's fake home):
// credentials, an email and complete filesystem paths.
const plantedSecrets = [
  "sk-test",
  "owner@example.com",
  "deploy-bot",
  "fakepass",
  "fake-hunter2",
  "fake-value-1",
  "/srv/owner-home",
  "/Users/someone",
  "/Volumes/",
  "PrivateClient",
  "Private Client",
  "private customer",
  "private-client.env",
  "fakeKey0a1B2c3D4e5F6g7H8i9J0",
  "/private/tmp",
  "~/",
];

test("the Go snapshot goldens exist, one per fixture project", () => {
  assert.ok(goldens.length >= 10, `found ${goldens.length} goldens`);
  assert.ok(goldens.some(([name]) => name === "secrets.golden.json"));
});

test("path redaction keeps URLs and fractions in the secrets golden", () => {
  const text = goldens.find(([name]) => name === "secrets.golden.json")?.[1] ?? "";
  for (const kept of ["https://trains.example.com/docs/setup", "https://<credentials>@git.example.com/org/repo", "1/2 done"]) {
    assert.ok(text.includes(kept), `secrets.golden.json lost ${kept}`);
  }
});

for (const [name, text] of goldens) {
  test(`Go golden ${name} validates as a PushRequest`, () => {
    assert.deepEqual(validate("PushRequest", JSON.parse(text)), { ok: true });
  });

  test(`Go golden ${name} carries none of the planted secrets`, () => {
    for (const secret of plantedSecrets) assert.ok(!text.includes(secret), `${name} contains ${secret}`);
  });
}
