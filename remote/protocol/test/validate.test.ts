import test from "node:test";
import assert from "node:assert/strict";
import {
  compileSchema,
  UnknownDefinitionError,
  UnresolvedSchemaRefError,
  UnsupportedSchemaKeywordError,
  type ValidationResult,
} from "../validate.ts";

function validatorFor(defs: Record<string, unknown>) {
  return compileSchema({ $schema: "https://json-schema.org/draft/2020-12/schema", $defs: defs });
}

function firstError(result: ValidationResult) {
  assert.equal(result.ok, false, "expected a validation failure");
  return result.ok ? undefined : result.errors[0];
}

test("type: each JSON type, integer vs number, and type arrays with null", () => {
  const validate = validatorFor({
    Int: { type: "integer" },
    Num: { type: "number" },
    MaybeText: { type: ["string", "null"] },
    Obj: { type: "object" },
    List: { type: "array" },
    Flag: { type: "boolean" },
  });
  assert.equal(validate("Int", 3).ok, true);
  assert.deepEqual(firstError(validate("Int", 3.5)), { path: "", message: "must be of type integer" });
  assert.equal(validate("Num", 3.5).ok, true);
  assert.equal(validate("Num", "3").ok, false);
  assert.equal(validate("MaybeText", null).ok, true);
  assert.equal(validate("MaybeText", "x").ok, true);
  assert.equal(validate("MaybeText", 1).ok, false);
  assert.equal(validate("Obj", []).ok, false);
  assert.equal(validate("Obj", null).ok, false);
  assert.equal(validate("List", {}).ok, false);
  assert.equal(validate("Flag", 0).ok, false);
});

test("a wrong type reports one error, not the keywords meant for the right type", () => {
  const validate = validatorFor({ Short: { type: "string", maxLength: 1, pattern: "^a$" } });
  assert.deepEqual(validate("Short", 7), { ok: false, errors: [{ path: "", message: "must be of type string" }] });
});

test("minLength and maxLength count UTF-16 code units: an astral emoji counts 2", () => {
  const validate = validatorFor({ Two: { type: "string", minLength: 2, maxLength: 2 } });
  assert.equal(validate("Two", "\u{1F682}").ok, true);
  assert.equal(validate("Two", "ab").ok, true);
  assert.equal(firstError(validate("Two", "a"))?.message, "must be at least 2 characters");
  assert.equal(firstError(validate("Two", "\u{1F682}a"))?.message, "must be at most 2 characters");
});

test("pattern applies to strings only, so a nullable patterned string accepts null", () => {
  const validate = validatorFor({ Hex: { type: ["string", "null"], pattern: "^[0-9a-f]+$" } });
  assert.equal(validate("Hex", "c0ffee").ok, true);
  assert.equal(validate("Hex", null).ok, true);
  assert.equal(firstError(validate("Hex", "C0FFEE"))?.message, "must match pattern ^[0-9a-f]+$");
});

test("minimum and maximum are inclusive", () => {
  const validate = validatorFor({ Pct: { type: "number", minimum: 0, maximum: 100 } });
  assert.equal(validate("Pct", 0).ok, true);
  assert.equal(validate("Pct", 100).ok, true);
  assert.equal(firstError(validate("Pct", -0.1))?.message, "must be >= 0");
  assert.equal(firstError(validate("Pct", 100.1))?.message, "must be <= 100");
});

test("enum and const compare JSON values, including null", () => {
  const validate = validatorFor({ Door: { enum: ["opencode", null] }, V: { const: 1 }, Pair: { const: [1, { a: 2 }] } });
  assert.equal(validate("Door", null).ok, true);
  assert.equal(validate("Door", "claude").ok, false);
  assert.equal(validate("V", 1).ok, true);
  assert.deepEqual(firstError(validate("V", "1")), { path: "", message: "must equal 1" });
  assert.equal(validate("Pair", [1, { a: 2 }]).ok, true);
  assert.equal(validate("Pair", [1, { a: 3 }]).ok, false);
});

test("arrays: minItems, maxItems and items report JSON pointer paths", () => {
  const validate = validatorFor({ Tags: { type: "array", minItems: 1, maxItems: 2, items: { type: "string" } } });
  assert.equal(validate("Tags", ["a"]).ok, true);
  assert.equal(firstError(validate("Tags", []))?.message, "must have at least 1 items");
  assert.equal(firstError(validate("Tags", ["a", "b", "c"]))?.message, "must have at most 2 items");
  assert.deepEqual(firstError(validate("Tags", ["a", 2])), { path: "/1", message: "must be of type string" });
});

test("objects: missing required, then declared properties in schema order, then undeclared ones", () => {
  const validate = validatorFor({
    Item: {
      type: "object",
      additionalProperties: false,
      required: ["a", "b"],
      properties: { a: { type: "string" }, b: { type: "string" }, "x/y": { type: "string" } },
    },
  });
  assert.equal(validate("Item", { a: "1", b: "2" }).ok, true);
  assert.deepEqual(validate("Item", { extra: 1, b: 2 }), {
    ok: false,
    errors: [
      { path: "/a", message: "is required" },
      { path: "/b", message: "must be of type string" },
      { path: "/extra", message: "is not allowed" },
    ],
  });
  assert.equal(firstError(validate("Item", { a: "1", b: "2", "x/y": 3 }))?.path, "/x~1y");
});

test("$ref resolves local defs and JSON pointers into them, and is a conjunction with its siblings", () => {
  const validate = validatorFor({
    MaybeIso: { type: ["string", "null"], pattern: "^\\d{4}$" },
    Iso: { type: "string", $ref: "#/$defs/MaybeIso" },
    Doc: {
      type: "object",
      properties: { at: { $ref: "#/$defs/Iso" }, year: { $ref: "#/$defs/Doc/properties/at" } },
    },
  });
  assert.equal(validate("MaybeIso", null).ok, true);
  assert.equal(validate("Iso", "2026").ok, true);
  assert.equal(firstError(validate("Iso", null))?.message, "must be of type string");
  assert.equal(firstError(validate("Iso", "26"))?.message, "must match pattern ^\\d{4}$");
  assert.deepEqual(firstError(validate("Doc", { at: "2026", year: "x" })), { path: "/year", message: "must match pattern ^\\d{4}$" });
});

test("$ref may be recursive", () => {
  const validate = validatorFor({
    Tree: { type: "object", additionalProperties: false, properties: { children: { type: "array", items: { $ref: "#/$defs/Tree" } } } },
  });
  assert.equal(validate("Tree", { children: [{ children: [] }] }).ok, true);
  assert.equal(firstError(validate("Tree", { children: [{ leaf: true }] }))?.path, "/children/0/leaf");
});

test("an unknown keyword anywhere in the schema throws UnsupportedSchemaKeywordError at compile time", () => {
  assert.throws(() => validatorFor({ A: { type: "string", format: "date-time" } }), (error: unknown) => {
    assert.ok(error instanceof UnsupportedSchemaKeywordError);
    assert.equal(error.keyword, "format");
    assert.equal(error.schemaPath, "/$defs/A");
    return true;
  });
  assert.throws(
    () => validatorFor({ A: { type: "object", properties: { b: { anyOf: [{ type: "string" }] } } } }),
    UnsupportedSchemaKeywordError,
  );
  assert.throws(() => compileSchema({ $defs: {}, oneOf: [] }), UnsupportedSchemaKeywordError);
});

test("supported keywords in unsupported forms also throw UnsupportedSchemaKeywordError", () => {
  assert.throws(() => validatorFor({ A: { type: "object", additionalProperties: true } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { type: "object", additionalProperties: { type: "string" } } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { type: "date" } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { $defs: { B: { type: "string" } } } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { type: "string", pattern: "(" } }), UnsupportedSchemaKeywordError);
});

test("a malformed keyword value throws instead of being skipped", () => {
  assert.throws(() => validatorFor({ A: { type: "string", maxLength: "5" } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { type: "object", required: "a" } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { enum: "a" } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { type: "array", minItems: 1.5 } }), UnsupportedSchemaKeywordError);
  assert.throws(() => validatorFor({ A: { $ref: 7 } }), UnsupportedSchemaKeywordError);
});

test("a $ref to a missing def, or a non-local $ref, throws UnresolvedSchemaRefError at compile time", () => {
  assert.throws(() => validatorFor({ A: { $ref: "#/$defs/Missing" } }), (error: unknown) => {
    assert.ok(error instanceof UnresolvedSchemaRefError);
    assert.equal(error.ref, "#/$defs/Missing");
    return true;
  });
  assert.throws(() => validatorFor({ A: { $ref: "https://example.com/schema.json" } }), UnresolvedSchemaRefError);
  assert.throws(() => validatorFor({ A: { $ref: "#/properties/x" } }), UnresolvedSchemaRefError);
});

test("asking for a definition the schema does not have throws UnknownDefinitionError", () => {
  const validate = validatorFor({ A: { type: "string" } });
  assert.throws(() => validate("B", "x"), UnknownDefinitionError);
});
