# Claude stream-json fixtures: provenance

Fixtures for the Go port of `orchestrator/bin/claude-exec.mjs` (S5a). Each `<case>.jsonl` is the
redacted stdout of one `claude` run, `<case>.stderr` its redacted stderr, and `cases.json` records
the exit code, the argv and a summary per case. The fake `claude` in `go/parity/fakes` replays them.

## Capture

- Date: 2026-10-19 UTC. Five runs at 03:09–03:11 (capture 1), one run at 03:29 (capture 2,
  `permission-denied`). Claude Code `2.1.287 (Claude Code)` (`claude --version`) for all six.
- Model: `claude-haiku-5-5` for every real case except `bad-model`.
- Auth: the Claude subscription (`claude auth status`: `authMethod=claude.ai`,
  `apiProvider=firstParty`), checked before each capture. No API key, auth token, base URL or
  apiKeyHelper. Every capture's `system/init` reports `apiKeySource: "none"`; the redactor refuses
  any other value.
- Environment: `env -i` with only `HOME PATH LANG=en_US.UTF-8 TERM=dumb USER TMPDIR`. This drops the
  `CLAUDECODE`, `CLAUDE_CODE_*` and messaging variables of the harness that ran the capture.
- cwd: a fresh directory under `$TMPDIR` holding one `README.md`
  (`# Fixture heading`, a blank line, `One line of fixture text.`), outside any git repository.
  Fixtures show it as `/tmp/fixture`.
- Capture-only flags, placed before the bridge's argv. The bridge itself never passes them:
  - `--safe-mode`: no CLAUDE.md, hooks, MCP servers or custom agents, so the capturing computer's
    configuration shapes the run as little as possible. Auth works normally. It does **not** hide
    user-installed plugins from `system/init`; the redactor handles that. `--bare` was not usable,
    because it needs an API key (paid credit).
  - `--no-session-persistence`: the runs leave no session files behind.

Every run was:

```sh
cd /tmp/fixture   # really the $TMPDIR capture directory
env -i HOME="$HOME" PATH="$PATH" LANG=en_US.UTF-8 TERM=dumb USER="$USER" TMPDIR="$TMPDIR" \
  claude --safe-mode --no-session-persistence -p --output-format stream-json --verbose <argv> \
  > raw/<case>.jsonl 2> raw/<case>.stderr < /dev/null
```

`<argv>` is `cases.json`'s `argv_redacted` for the case, in the bridge's order (`claude-exec.mjs`
l.38–49). The fixed prefix `-p --output-format stream-json --verbose` is not repeated in
`argv_redacted`. The six commands:

| case | `<argv>` |
|---|---|
| `ok` | `--permission-mode dontAsk --permission-prompts none --model claude-haiku-5-5 --allowedTools Read --max-turns 3 -- "Read README.md and reply with its first heading only."` |
| `bash-readonly-allowed` | `--permission-mode dontAsk --permission-prompts none --model claude-haiku-5-5 --allowedTools Read --max-turns 3 -- "Use the Bash tool to run: echo pong. Then say what happened in one sentence."` |
| `permission-denied` | `--permission-mode dontAsk --permission-prompts none --model claude-haiku-5-5 --allowedTools Read --max-turns 3 -- "Use the Bash tool to run: touch probe.txt. Then say what happened in one sentence."` |
| `max-turns` | `--permission-mode dontAsk --permission-prompts none --model claude-haiku-5-5 --allowedTools Read --max-turns 1 -- "Read README.md and reply with its first heading only."` |
| `structured-output` | `--permission-mode dontAsk --permission-prompts none --model claude-haiku-5-5 --max-turns 3 --json-schema '{"type":"object","properties":{"word":{"type":"string"}},"required":["word"],"additionalProperties":false}' -- "Return the word pong."` |
| `bad-model` | `--permission-mode dontAsk --permission-prompts none --model claude-not-a-model-0-0 --max-turns 1 -- "Reply with pong."` |

Six real runs, no retries. `--max-turns` is accepted although `claude --help` does not list it
(the binary defines it with `hideHelp()`).

## What the real runs showed

- `ok`: `Read` tool_use, tool_result, text `Fixture heading`; `result/success`, `is_error=false`,
  `num_turns=2`, exit 0.
- `bash-readonly-allowed`: Bash was **allowed** although only `Read` was listed. Under `dontAsk`,
  Claude Code ran `echo pong` (it treats the command as read-only); `permission_denials` is `[]`,
  exit 0. This covers "an unlisted read-only Bash command runs"; it is **not** denial coverage.
  Capture 1 recorded it under the planned name `permission-denied`; it was renamed in v2.
- `permission-denied`: a genuine denial. Bash `touch probe.txt` was refused and no file was
  created. The stream has a `system/permission_denied` event (`tool_name`, `tool_use_id`,
  `decision_reason_type: "mode"`, `message`), then a `user` tool_result with `is_error: true` and
  the same message, plus `tool_result_meta: [{id, non_execution_kind: "permission-rule"}]`. Two
  `system/thinking_tokens` events and a `thinking` block follow, then the text answer.
  `result/success`, `is_error=false`, `num_turns=2`, exit 0, and
  `permission_denials: [{tool_name: "Bash", tool_use_id, tool_input: {command, description}}]`.
- `max-turns`: `result/error_max_turns` with `is_error=true`, `num_turns=2`, no `result` key, and
  `errors: ["Reached maximum number of turns (1)"]`; exit 1.
- `structured-output`: the model first answered in text; the CLI then injected a synthetic `user`
  message (`isSynthetic: true`) demanding the `StructuredOutput` tool. The result carries
  `structured_output: {"word":"pong"}` and `result` as its JSON text; `num_turns=3`, exit 0.
- `bad-model`: a stream, not a bare stderr error. A synthetic `assistant` message
  (`model: "<synthetic>"`, `error: "model_not_found"`, `is_api_error_message: true`), then
  `result/success` with `is_error=true`, `api_error_status=404`, `terminal_reason: "api_error"` and
  the error text in `result`; exit 1.
- Every stderr is one `[claude-code:unrecognized_model] {...}` line naming the model, even for
  `claude-haiku-5-5`, which then ran normally.
- Every run except `bad-model` has a `rate_limit_event` with `status: "allowed"`. That is
  informational and not a stop signal; a limit or refusal **message** would stop a capture.

## Redaction

`go/testdata/redact.mjs` turns the raw files into these fixtures, then scans them. The regression
suite `go/testdata/redact.test.mjs` pins every rule below with fake inputs.

```sh
node go/testdata/redact.mjs --in <rawdir> --out go/testdata/claude/stream-json --work <capture cwd> --deny-file <file>
(cd go/testdata/claude/stream-json && node gen.mjs && node gen.mjs --check)
node go/testdata/redact.mjs --scan go/testdata/claude --deny-file <file>   # must exit 0
node go/testdata/redact.mjs --scan go/testdata/claude                      # generic rules only
node --test go/testdata/redact.test.mjs
```

The deny file held the account email, org id and org name from `claude auth status`, plus `$HOME`,
`$USER` and the short hostname. It lived only next to the raw data and was deleted with it.

Each line is parsed and re-emitted with `JSON.stringify` (key order kept). Before anything is
changed, every key and string of the original event is checked for credentials, including fields
that will be discarded. Diagnostics name only a file, a line and a rule, never input text. The
rules apply to every string, object keys included (the result's `wire_tool_inputs` is keyed by
tool_use id):

- UUIDs map to `00000000-0000-4000-8000-0000000000NN`, numbered per case in order of first
  appearance, so `session_id` stays consistent within a case. Values already in that form are kept.
- API ids are a `msg`, `toolu`, `req` or `srvtoolu` prefix, `_`, then letters, digits and
  underscores. Each becomes `<prefix>_fixtureNN`, per case. Already-redacted ids and the known
  2.1.287 capability name `msg_lifecycle_v1` are kept.
- Paths: the capture cwd (both spellings of the Darwin temp path) becomes `/tmp/fixture`, `$HOME`
  becomes `/home/fixture`, any other user home becomes `/home/fixture`, and any other Darwin temp
  path becomes `/tmp`.
- Deny-file literals are replaced longest first: an email becomes `fixture@example.com`, others
  become `fixture`. Every literal matches anywhere, in any case, however short. A short host name
  can also be part of ordinary words, so this file and the fixtures are worded to avoid such
  collisions; the redactor never weakens the match. Any other email becomes `fixture@example.com`.
- A credential-shaped string (an Anthropic key prefix, a JWT prefix, or a long `sk`/`rt`/`at`
  token, also after JSON unescaping) **fails** the run: it is a stop-and-report event, never
  silently redacted.
- `system/init`: `mcp_servers`, `plugins`, `agents`, `skills`, `slash_commands` and
  `terminal_slash_commands` are always emptied, whatever their names or shape. They describe the
  capturing computer, and the bridge never reads them. `tools` keeps only a pinned list of 2.1.287
  built-in tool names (update it with the CLI). An `output_style` other than `default` becomes
  `fixture`.
- `thinking` block signatures become `fixture-signature`, and `redacted_thinking` data becomes
  `fixture-redacted-thinking`. These are opaque encrypted blobs that cannot be audited.
- Token counts, costs, durations, `num_turns`, model ids, `claude_code_version`, tool names,
  assistant text, timestamps, rate-limit status and utilization, and `apiKeySource` are kept.

The scan decodes every JSON and JSONL file and checks each key and string value. It checks other
files line by line, both raw and with JSON escapes decoded. It fails on a deny-file literal, a
Darwin user-home or non-fixture `/home/` path, a Darwin temp path, an email other than
`fixture@example.com`, a credential-shaped string, a non-fixture UUID, an API id that is neither
`<prefix>_fixtureNN` nor a known capability name, an unparsable JSON line, or any `apiKeySource`
other than `none` at any depth. In text, the value after each quoted apiKeySource key and its colon
is read whole, whatever the spacing, and must be exactly the quoted string none. It prints only `file:line rule`.

### v2 re-sanitizing and its limits

v1's raw captures were deleted after its scan passed, so v2 could not redact capture 1 from raw.
The five capture-1 cases were re-run through the v2 redactor from their **v1 redacted** files,
together with the new raw `permission-denied` capture and a fresh deny file. That emptied the
remaining customization lists in all six inits and the user plugin list in the new one. It also
replaced the new run's thinking signature. Re-redacting the v2 output is byte-identical
(idempotent).

The limits: anything v1's rules missed in capture 1 cannot be recovered or re-checked against the
original. The new deny file came from the same account and computer, so the deny scan does cover
capture-1 text for those literals. The capture-1 inits held only names that Claude Code ships;
v1 had already emptied the one user plugin. Customization names are now gone entirely.

## Synthetic cases

`gen.mjs` builds these from the committed `ok` fixture, so it never needs raw data:

- `usage-limit`: a mock. The real limit text and exit code have never been captured. It is `ok`'s
  `system/init` plus `ok`'s result with `is_error=true`, `subtype: "success"`, `num_turns=1` and
  `result: "You've hit your session limit · resets 3am"` (the first branch of `LIMIT_TEXT`); exit 1.
- `long-line`: `ok` with the tool_result content replaced by 1,572,864 deterministic ASCII
  characters, so one stdout line exceeds 1 MiB.
- `success-is-error`: `ok` with the result's `is_error=true`, `subtype: "success"` and
  `result: "boom: synthetic failure text"`; exit 1. Same shape as the real `bad-model` result.

## Re-capturing after a CLI upgrade

Follow `.orchestrator/briefs/go-s5a-fixtures.md`. In short:
1. Confirm the subscription (`claude auth status`) and check that the Anthropic 5-hour posture is
   not red.
2. Capture into a temp directory outside the repo with the command above.
3. Update `BUILT_IN_TOOLS` in `redact.mjs` for the new version.
4. Run the redaction, generation, scan and test commands.
5. Read every fixture line.
6. Delete the raw directory and deny file only after the scan passes.
7. Update this file (date, version, observed shapes).
