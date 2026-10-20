import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { compileSchema } from "../validate.ts";

type Example = { def: string; value: unknown; expect_path?: string };

const v1 = new URL("../v1/", import.meta.url);
const schema = JSON.parse(fs.readFileSync(new URL("schema.json", v1), "utf8"));
const validate = compileSchema(schema);

function readExamples(kind: "valid" | "invalid"): [string, Example][] {
  const dir = new URL(`examples/${kind}/`, v1);
  return fs
    .readdirSync(dir)
    .filter((name) => name.endsWith(".json"))
    .sort()
    .map((name) => [name, JSON.parse(fs.readFileSync(new URL(name, dir), "utf8"))]);
}

const validExamples = readExamples("valid");
const invalidExamples = readExamples("invalid");

for (const [name, example] of validExamples) {
  test(`valid example ${name} validates as ${example.def}`, () => {
    assert.deepEqual(validate(example.def, example.value), { ok: true });
  });
}

for (const [name, example] of invalidExamples) {
  test(`invalid example ${name} fails first at ${example.expect_path}`, () => {
    const result = validate(example.def, example.value);
    assert.equal(result.ok, false);
    if (result.ok) return;
    assert.equal(result.errors[0].path, example.expect_path);
  });
}

test("every $defs entry has at least one valid example", () => {
  const covered = new Set(validExamples.map(([, example]) => example.def));
  const uncovered = Object.keys(schema.$defs).filter((name) => !covered.has(name));
  assert.deepEqual(uncovered, []);
});

test("the payload and shared definitions the Worker and Go name all exist", () => {
  const named = [
    "PushRequest", "PushResponse", "PullResponse", "AckRequest", "AckResponse", "Summary", "AnswerRequest",
    "AnswerResponse", "ActionRequest", "ActionResponse", "VisibilityRequest", "Error", "Iso", "Uuid", "ProjectKey",
    "Posture", "ProjectSnapshot", "Question", "Event", "CiRun", "Progress", "Run",
  ];
  assert.deepEqual(named.filter((name) => !(name in schema.$defs)), []);
});

test("every invalid example names the path of its expected first error", () => {
  assert.ok(invalidExamples.length >= 12);
  for (const [name, example] of invalidExamples) assert.equal(typeof example.expect_path, "string", name);
});

test("every payload that crosses the wire carries v: 1, except the browser request bodies", () => {
  const unversioned = new Set(["AnswerRequest", "ActionRequest", "VisibilityRequest", "Error"]);
  const payloads = ["PushRequest", "PushResponse", "PullResponse", "AckRequest", "AckResponse", "Summary", "AnswerResponse", "ActionResponse"];
  for (const name of payloads) assert.deepEqual(schema.$defs[name].properties.v, { const: 1 }, name);
  for (const name of unversioned) assert.equal(schema.$defs[name].properties.v, undefined, name);
});
