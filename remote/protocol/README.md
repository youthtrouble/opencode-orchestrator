# Work Trains wire protocol

This directory is the single source of truth for every JSON payload that passes between the Mac (`orch remote`, Go),
the Cloudflare Worker (TypeScript) and the browser page. If one of them disagrees with `v1/schema.json`, the schema
wins and the code is the bug.

| Path | What it is |
|---|---|
| `v1/schema.json` | JSON Schema 2020-12 document. Each payload is a named entry under `$defs`. |
| `v1/examples/valid/*.json` | `{"def": "<DefName>", "value": <payload>}`, which must validate. Every `$defs` entry has at least one. |
| `v1/examples/invalid/*.json` | The same, plus `"expect_path"`: the JSON pointer of the first error the validator must report. |
| `validate.ts` | Dependency-free subset validator. The Worker imports it at runtime and the tests use it. |
| `test/*.test.ts` | Contract tests. |

Run the tests from the repository root (Node 26 strips the types; nothing to install):

```sh
node --test 'remote/protocol/test/*.test.ts'
```

## Payloads

| `$defs` entry | Direction | Purpose |
|---|---|---|
| `PushRequest` | Mac -> Worker | Machine identity, both vendors' budget postures, and up to 50 `ProjectSnapshot`s whose hash changed. |
| `PushResponse` | Worker -> Mac | Per project: `accepted`, `unchanged`, or `deferred` with `retry_after_seconds`. |
| `PullResponse` | Worker -> Mac | Answers and pause/resume actions whose undo window has passed and which are not yet acked, plus `poll_after_seconds`. |
| `AckRequest` | Mac -> Worker | The outcome of applying each pulled answer or action: `applied`, `superseded` or `rejected`. |
| `AckResponse` | Worker -> Mac | How many results changed state (`settled`); re-acking is harmless. |
| `Summary` | Worker -> browser | `GET /api/v1/summary`: machine liveness, budget, undo window, `public_origin` and every project with its open questions and answers. |
| `AnswerRequest` | browser -> Worker | Accept the recommendation, or send typed `text` (present only for `typed`; the Worker enforces the pairing). |
| `AnswerResponse` | Worker -> browser | The stored answer and its delivery `status`. |
| `ActionRequest` | browser -> Worker | Pause or resume a project. |
| `ActionResponse` | Worker -> browser | The stored action and its delivery `status`. |
| `VisibilityRequest` | browser -> Worker | Hide or show a project on the page. |
| `Error` | Worker -> any | Every non-2xx body. |

Shared building blocks: `Iso`, `Uuid`, `ProjectKey`, `Posture`, `ProjectSnapshot`, `Question`, `Event`, `CiRun`,
`Progress`, `Run`. `Iso`, `Posture` and `Progress` each narrow a `Nullable*` entry (`NullableIso`, `NullablePosture`,
`NullableProgress`) to non-null. The keyword subset has no `anyOf`, and in 2020-12 a `$ref` beside sibling keywords
must satisfy both. So a field that may be null refers to the `Nullable*` entry, and the shape is written only once.
Where a browser payload repeats a field of another payload, it refers to that field by JSON pointer (for example
`#/$defs/ProjectSnapshot/properties/name`), so each cap is stated once.

## Versioning

- Every machine payload and every Worker response body except `Error` carries `"v": 1`. The browser request bodies
  (`AnswerRequest`, `ActionRequest`, `VisibilityRequest`) do not: their route is already under `/api/v1/`.
- Machine routes live under `/api/v1/`. The Worker answers a body with any other `v` with
  400 `{"error":"unsupported_version","message":"...","supported":[1]}`.
- A breaking change gets a new `v2/` directory beside `v1/`, with its own schema and examples. `v1/` stays as it is
  for as long as the Worker accepts v1.
- Within v1, the only allowed change is a new optional field. It becomes legal once `v1/schema.json` declares it
  (in `properties`, not in `required`). Every object is `additionalProperties: false`, so a sender must not emit a
  field until the schema, the Worker that imports it and the Go structs all declare it.
- Optional fields added within v1: `Posture.credits` (OpenAI only: ChatGPT credits and banked window resets);
  `Question.ask` and the summary question's `ask` (WT25: `"action"` for steps the owner must do, which carry no
  recommendation; absent for a decision); `Posture.weekly_resets_at` and `Posture.window` (WT22-C: the weekly
  window's reset, and the window that drives `level`, `"weekly"` when the weekly window is red, else
  `"five_hour"`; a reader treats an absent `window` as `"five_hour"`). Go marks an optional field `omitempty`, so it leaves it out when unset and
  its decoder accepts it absent.
- `Question.ask` rollout: a Worker built before it rejects a push that carries it (`additionalProperties: false`), so
  deploy the Worker (with its migration `0003_questions_ask.sql`) before an orch that sends it. An older Worker answers
  a push that holds an action question with 400 `invalid_request` and stores none of its projects. An action question
  carries no recommendation, so the Worker refuses an accept of it (`this question has no recommendation; reply
  instead`).

- `Posture.weekly_resets_at` and `Posture.window` rollout: a Worker built before them rejects a push that carries
  them with 400 `invalid_request` (`additionalProperties: false`) and stores none of its projects, and every push
  carries the budget. Deploy the Worker first, then rebuild `orch`. Until the rebuild the page reads every chip
  from the 5-hour window and shows no weekly reset.

## Accept answers

An `accept` names the question and nothing else: the browser sends `{"kind":"accept"}` and the Worker stores the
recommendation it holds, which the Mac redacted before the push. That copy is what the page shows and what a pulled
accept carries as `text`, but it is not what the owner accepted, so the Mac never posts it: it replies with the
recommendation the question holds in its own mailbox, and rejects the accept (`the question has no recommendation on
the Mac; reply instead`) when there is none. A typed answer's `text` is posted as it is.

The Mac says it does this with the request header `X-Work-Trains-Accept: mailbox` on `GET /api/v1/machine/pull`. An
orch built before the header would post the redacted `text`, so a pull without it settles every due accept as
`rejected` (detail `orch on the Mac is too old to accept a recommendation: ...`), which the page shows, and returns
the rest. The payloads are unchanged, so either side may deploy first: an orch with the header works against a
Worker that ignores it.

## Size caps

- The Worker refuses a request body above 256 KiB with 413 `payload_too_large` before it parses the body.
- Field caps are the schema's `maxLength`/`maxItems`. The Mac cuts free text to these caps before it sends.
  Lengths are counted in UTF-16 code units, as JavaScript `.length` counts them, so an astral emoji counts 2. The
  JSON Schema spec counts code points instead. Go must count UTF-16 units too
  (`len(utf16.Encode([]rune(s)))`).
- List caps: 50 projects per push or summary, 5 CI runs, 10 events and 20 questions per project, 50 answers and
  50 actions per pull, 1 to 100 results per ack.

## The validator

`compileSchema(schemaDoc)` returns `validate(defName, value)`, which gives `{ok: true}` or
`{ok: false, errors: [{path, message}]}`. `path` is the JSON pointer of the failing value (`""` for the root). A
missing required property is reported at the path it should have had. Within an object, errors come in this order:
missing required properties, then declared properties in schema order, then undeclared properties. A value of the
wrong type gets a single type error.

The validator implements only these keywords: `$schema`, `$id`, `$defs` (document root only), `$ref` (local
`#/$defs/...` pointers only), `type`, `properties`, `required`, `additionalProperties` (`false` only), `items`,
`enum`, `const`, `minLength`, `maxLength`, `minItems`, `maxItems`, `minimum`, `maximum`, `pattern` (compiled with
the `u` flag), `description` and `title`. Compiling throws on anything else, including a keyword with a malformed
value:

- an unknown keyword or an unsupported form throws `UnsupportedSchemaKeywordError`;
- a `$ref` that is not local or does not resolve throws `UnresolvedSchemaRefError`.

So the schema can never quietly validate less than it says. Calling `validate` with a name that is not in `$defs`
throws `UnknownDefinitionError`. To use a new keyword, implement it in `validate.ts` with tests first.
