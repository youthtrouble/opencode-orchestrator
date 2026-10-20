// Subset JSON Schema 2020-12 validator for the Work Trains wire protocol. No dependencies and no `new Function`
// (Workers forbid it, which rules out ajv), so it runs unchanged in the Worker and under `node --test`.
//
// It implements only the keywords in SUPPORTED_KEYWORDS and refuses, at compile time, any schema that uses
// anything else: a keyword it silently ignored would let invalid payloads through.

export type ValidationError = { path: string; message: string };
export type ValidationResult = { ok: true } | { ok: false; errors: ValidationError[] };
export type Validate = (defName: string, value: unknown) => ValidationResult;

type SchemaNode = Record<string, unknown>;
type Check = (value: unknown, path: string, errors: ValidationError[]) => void;

const SUPPORTED_KEYWORDS = new Set([
  "$schema", "$id", "$defs", "$ref", "type", "properties", "required", "additionalProperties", "items", "enum",
  "const", "minLength", "maxLength", "minItems", "maxItems", "minimum", "maximum", "pattern", "description", "title",
]);
// The JSON shape each keyword's value must have; a malformed value would otherwise be skipped without a word.
const KEYWORD_VALUE_SHAPES: Record<string, (value: unknown) => boolean> = {
  $ref: (value) => typeof value === "string",
  properties: isObject,
  required: (value) => Array.isArray(value) && value.every((name) => typeof name === "string"),
  enum: Array.isArray,
  minLength: Number.isInteger,
  maxLength: Number.isInteger,
  minItems: Number.isInteger,
  maxItems: Number.isInteger,
  minimum: (value) => typeof value === "number",
  maximum: (value) => typeof value === "number",
  pattern: (value) => typeof value === "string",
};
const SUPPORTED_TYPES = new Set(["null", "boolean", "integer", "number", "string", "array", "object"]);
const LOCAL_DEF_REF = "#/$defs/";

/** The schema uses a keyword (or a keyword form) this validator does not implement. */
export class UnsupportedSchemaKeywordError extends Error {
  readonly keyword: string;
  readonly schemaPath: string;
  constructor(keyword: string, schemaPath: string, detail = "is not supported") {
    super(`schema keyword "${keyword}" at ${schemaPath || "/"} ${detail}`);
    this.name = "UnsupportedSchemaKeywordError";
    this.keyword = keyword;
    this.schemaPath = schemaPath;
  }
}

/** A `$ref` is not local (`#/$defs/...`) or points at nothing in the document. */
export class UnresolvedSchemaRefError extends Error {
  readonly ref: string;
  readonly schemaPath: string;
  constructor(ref: string, schemaPath: string) {
    super(`schema $ref "${ref}" at ${schemaPath} does not resolve to a local #/$defs/... target`);
    this.name = "UnresolvedSchemaRefError";
    this.ref = ref;
    this.schemaPath = schemaPath;
  }
}

/** `validate` was asked for a definition the schema does not have (a caller bug, not a bad payload). */
export class UnknownDefinitionError extends Error {
  readonly defName: string;
  constructor(defName: string) {
    super(`schema has no $defs entry named "${defName}"`);
    this.name = "UnknownDefinitionError";
    this.defName = defName;
  }
}

export function compileSchema(schemaDoc: unknown): Validate {
  if (!isObject(schemaDoc)) throw new TypeError("schema document must be a JSON object");
  const compiler = new SchemaCompiler(schemaDoc);
  // Compiling the whole document up front surfaces every unsupported keyword and dangling $ref at load time.
  compiler.compile(schemaDoc, "");
  const defNames = isObject(schemaDoc.$defs) ? Object.keys(schemaDoc.$defs) : [];
  const checksByDef = new Map(defNames.map((name) => [name, compiler.resolve(LOCAL_DEF_REF + escapePointer(name), "")]));

  return (defName, value) => {
    const check = checksByDef.get(defName);
    if (!check) throw new UnknownDefinitionError(defName);
    const errors: ValidationError[] = [];
    check(value, "", errors);
    return errors.length === 0 ? { ok: true } : { ok: false, errors };
  };
}

class SchemaCompiler {
  readonly #doc: SchemaNode;
  readonly #checksByRef = new Map<string, Check>();

  constructor(doc: SchemaNode) {
    this.#doc = doc;
  }

  compile(schema: unknown, schemaPath: string): Check {
    if (!isObject(schema)) throw new UnsupportedSchemaKeywordError("(schema)", schemaPath, "must be an object");
    for (const keyword of Object.keys(schema)) {
      if (!SUPPORTED_KEYWORDS.has(keyword)) throw new UnsupportedSchemaKeywordError(keyword, schemaPath);
      const hasShape = KEYWORD_VALUE_SHAPES[keyword];
      if (hasShape && !hasShape(schema[keyword])) throw new UnsupportedSchemaKeywordError(keyword, schemaPath, "has a malformed value");
    }
    if (schema.$defs !== undefined) this.#compileDefs(schema.$defs, schemaPath);

    const typeGuard = schema.type === undefined ? undefined : typeCheck(schema.type, schemaPath);
    const checks: Check[] = [];
    // As in 2020-12, $ref beside other keywords is a conjunction: the target and the siblings must all hold.
    if (typeof schema.$ref === "string") checks.push(this.resolve(schema.$ref, schemaPath));
    if (schema.const !== undefined) checks.push(constCheck(schema.const));
    if (Array.isArray(schema.enum)) checks.push(enumCheck(schema.enum));
    checks.push(...stringChecks(schema, schemaPath));
    checks.push(...numberChecks(schema));
    checks.push(...this.#arrayChecks(schema, schemaPath));
    checks.push(...this.#objectChecks(schema, schemaPath));
    return (value, path, errors) => {
      // A value of the wrong type gets one error, not a cascade from keywords meant for another type.
      if (typeGuard && !typeGuard(value, path, errors)) return;
      for (const check of checks) check(value, path, errors);
    };
  }

  // Only root-level $defs are addressable by the local refs this validator supports.
  #compileDefs(defs: unknown, schemaPath: string): void {
    if (schemaPath !== "" || !isObject(defs)) {
      throw new UnsupportedSchemaKeywordError("$defs", schemaPath, "is supported only as an object at the document root");
    }
    for (const name of Object.keys(defs)) this.resolve(LOCAL_DEF_REF + escapePointer(name), schemaPath);
  }

  // Refs resolve lazily through a cache so a definition is compiled once and may refer to itself.
  resolve(ref: string, schemaPath: string): Check {
    const cached = this.#checksByRef.get(ref);
    if (cached) return cached;
    const target = ref.startsWith(LOCAL_DEF_REF) ? lookupPointer(this.#doc, ref.slice(1)) : undefined;
    if (target === undefined) throw new UnresolvedSchemaRefError(ref, schemaPath);
    let compiled: Check | undefined;
    this.#checksByRef.set(ref, (value, path, errors) => compiled!(value, path, errors));
    compiled = this.compile(target, ref.slice(1));
    return compiled;
  }

  #arrayChecks(schema: SchemaNode, schemaPath: string): Check[] {
    const checks: Check[] = [];
    const { minItems, maxItems } = schema;
    if (typeof minItems === "number") {
      checks.push(whenArray((items, path, errors) => {
        if (items.length < minItems) errors.push({ path, message: `must have at least ${minItems} items` });
      }));
    }
    if (typeof maxItems === "number") {
      checks.push(whenArray((items, path, errors) => {
        if (items.length > maxItems) errors.push({ path, message: `must have at most ${maxItems} items` });
      }));
    }
    if (schema.items !== undefined) {
      const itemCheck = this.compile(schema.items, `${schemaPath}/items`);
      checks.push(whenArray((items, path, errors) => items.forEach((item, i) => itemCheck(item, `${path}/${i}`, errors))));
    }
    return checks;
  }

  // Order of errors within an object: missing required properties, then declared properties in schema order,
  // then undeclared ones. Tests and the Worker rely on the first error being stable.
  #objectChecks(schema: SchemaNode, schemaPath: string): Check[] {
    const checks: Check[] = [];
    const properties = isObject(schema.properties) ? schema.properties : {};
    if (Array.isArray(schema.required)) {
      const required: string[] = schema.required;
      checks.push(whenObject((object, path, errors) => {
        for (const name of required) {
          if (!Object.hasOwn(object, name)) errors.push({ path: `${path}/${escapePointer(name)}`, message: "is required" });
        }
      }));
    }
    const propertyChecks = Object.entries(properties).map(
      ([name, propertySchema]) => [name, this.compile(propertySchema, `${schemaPath}/properties/${escapePointer(name)}`)] as const,
    );
    if (propertyChecks.length > 0) {
      checks.push(whenObject((object, path, errors) => {
        for (const [name, check] of propertyChecks) {
          if (Object.hasOwn(object, name)) check(object[name], `${path}/${escapePointer(name)}`, errors);
        }
      }));
    }
    if (schema.additionalProperties !== undefined) {
      if (schema.additionalProperties !== false) {
        throw new UnsupportedSchemaKeywordError("additionalProperties", schemaPath, "is supported only as false");
      }
      checks.push(whenObject((object, path, errors) => {
        for (const name of Object.keys(object)) {
          if (!Object.hasOwn(properties, name)) errors.push({ path: `${path}/${escapePointer(name)}`, message: "is not allowed" });
        }
      }));
    }
    return checks;
  }
}

type TypeGuard = (value: unknown, path: string, errors: ValidationError[]) => boolean;

function typeCheck(type: unknown, schemaPath: string): TypeGuard {
  const allowed = Array.isArray(type) ? type : [type];
  for (const name of allowed) {
    if (typeof name !== "string" || !SUPPORTED_TYPES.has(name)) {
      throw new UnsupportedSchemaKeywordError("type", schemaPath, `does not support type ${JSON.stringify(name)}`);
    }
  }
  return (value, path, errors) => {
    if (allowed.some((name) => hasType(value, name))) return true;
    errors.push({ path, message: `must be of type ${allowed.join(" or ")}` });
    return false;
  };
}

function hasType(value: unknown, type: string): boolean {
  switch (type) {
    case "null": return value === null;
    case "integer": return Number.isInteger(value);
    case "number": return typeof value === "number" && Number.isFinite(value);
    case "array": return Array.isArray(value);
    case "object": return isObject(value);
    default: return typeof value === type;
  }
}

function constCheck(expected: unknown): Check {
  return (value, path, errors) => {
    if (!jsonEqual(value, expected)) errors.push({ path, message: `must equal ${JSON.stringify(expected)}` });
  };
}

function enumCheck(options: unknown[]): Check {
  return (value, path, errors) => {
    if (!options.some((option) => jsonEqual(value, option))) {
      errors.push({ path, message: `must be one of ${options.map((option) => JSON.stringify(option)).join(", ")}` });
    }
  };
}

// Lengths are UTF-16 code units (JavaScript `.length`), not code points as the JSON Schema spec says: the caps are
// storage and display budgets, and an astral emoji costs two units in both. The Go side counts the same way.
function stringChecks(schema: SchemaNode, schemaPath: string): Check[] {
  const checks: Check[] = [];
  const { minLength, maxLength, pattern } = schema;
  if (typeof minLength === "number") {
    checks.push(whenString((text, path, errors) => {
      if (text.length < minLength) errors.push({ path, message: `must be at least ${minLength} characters` });
    }));
  }
  if (typeof maxLength === "number") {
    checks.push(whenString((text, path, errors) => {
      if (text.length > maxLength) errors.push({ path, message: `must be at most ${maxLength} characters` });
    }));
  }
  if (typeof pattern === "string") {
    const regex = compilePattern(pattern, schemaPath);
    checks.push(whenString((text, path, errors) => {
      if (!regex.test(text)) errors.push({ path, message: `must match pattern ${pattern}` });
    }));
  }
  return checks;
}

function compilePattern(pattern: string, schemaPath: string): RegExp {
  try {
    return new RegExp(pattern, "u");
  } catch (error) {
    throw new UnsupportedSchemaKeywordError("pattern", schemaPath, `is not a valid regular expression: ${(error as Error).message}`);
  }
}

function numberChecks(schema: SchemaNode): Check[] {
  const checks: Check[] = [];
  const { minimum, maximum } = schema;
  if (typeof minimum === "number") {
    checks.push(whenNumber((n, path, errors) => {
      if (n < minimum) errors.push({ path, message: `must be >= ${minimum}` });
    }));
  }
  if (typeof maximum === "number") {
    checks.push(whenNumber((n, path, errors) => {
      if (n > maximum) errors.push({ path, message: `must be <= ${maximum}` });
    }));
  }
  return checks;
}

function whenString(check: (text: string, path: string, errors: ValidationError[]) => void): Check {
  return (value, path, errors) => {
    if (typeof value === "string") check(value, path, errors);
  };
}

function whenNumber(check: (n: number, path: string, errors: ValidationError[]) => void): Check {
  return (value, path, errors) => {
    if (typeof value === "number") check(value, path, errors);
  };
}

function whenArray(check: (items: unknown[], path: string, errors: ValidationError[]) => void): Check {
  return (value, path, errors) => {
    if (Array.isArray(value)) check(value, path, errors);
  };
}

function whenObject(check: (object: SchemaNode, path: string, errors: ValidationError[]) => void): Check {
  return (value, path, errors) => {
    if (isObject(value)) check(value, path, errors);
  };
}

function isObject(value: unknown): value is SchemaNode {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function jsonEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((item, i) => jsonEqual(item, b[i]));
  if (isObject(a) && isObject(b)) {
    const keys = Object.keys(a);
    return keys.length === Object.keys(b).length && keys.every((key) => Object.hasOwn(b, key) && jsonEqual(a[key], b[key]));
  }
  return false;
}

function escapePointer(token: string): string {
  return token.replaceAll("~", "~0").replaceAll("/", "~1");
}

function lookupPointer(doc: unknown, pointer: string): unknown {
  let node = doc;
  for (const token of pointer.split("/").slice(1)) {
    const key = token.replaceAll("~1", "/").replaceAll("~0", "~");
    if (!isObject(node) || !Object.hasOwn(node, key)) return undefined;
    node = node[key];
  }
  return node;
}
