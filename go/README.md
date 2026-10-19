# orch: Go runtime of the orchestrator

`orch` is one static binary whose subcommands replace `orchestrator/bin/*.mjs` one for one. The
port is incremental (plan: `.orchestrator/plans/go-port.md`); until it is complete the JS runtime
stays the default and both share the same `.orchestrator/` state.

Implemented so far (the rest of `bin/*.mjs` is still JS-only):

| Command | Replaces | Behaviour |
|---|---|---|
| `orch version` | — | Release number plus VCS revision. |
| `orch usage [--refresh] [--json]` | `bin/usage.mjs` | Byte-identical output; reads and writes the same usage cache, so either runtime can read what the other wrote. A failed vendor reading (a 429 from the Claude usage endpoint, a codex error) is replaced by the last good cached one, marked `last_good_at` (when it was read) and `read_error`, if the cache was written less than 30 minutes ago. The window rolls: each failed refresh rewrites the cache, so refreshes that keep failing less than 30 minutes apart keep a reading of any age (as in the JS); check `last_good_at`. |
| `orch routing [--refresh \| --cached] [--json]` | `getRouting()` in `lib/usage.mjs` | The routing prompt text, or `{usage, registry, channels, resolved, prompt}` with `--json`. `--cached` never collects and exits 3 when there is no cache; bad flags exit 2. |
| `orch budget gate --vendor anthropic\|openai [--force] [--frontier]` | `budgetGate()` in `lib/budget.mjs` | Compact `{allowed, level, resets_at, note}`; exit 0 whether or not the work is allowed, 2 on bad flags or an unknown vendor. |
| `orch effort --role ROLE [--size SIZE]` | `effortFor()` in `lib/routing.mjs` | The effort-policy value as plain text, or `null`; exit 2 on bad flags or without `--role`. |
| `orch mailbox post\|list\|ack\|reply\|inbox` | `bin/mailbox.mjs` | Byte-identical output and `.orchestrator/mailbox/*.jsonl` lines (one `write` per line on an append-only descriptor, so concurrent JS and Go appenders do not interleave). `post --json` prints the whole message instead of `{id, at}` (PE-3). Uncaught JS errors print only their error line (see "Crash output and parity exceptions"). |
| `orch manifest create\|verify\|diff` | `bin/manifest.mjs` | Byte-identical output, exit codes and manifest files: the same walk (symlinks, FIFOs and other non-regular entries inside a walked directory are skipped and never opened; an explicit symlink argument is followed), UTF-16 key order and `manifest_sha256`. Uncaught JS errors print only their error line. |
| `orch incident record\|list\|report\|share` | `bin/incident.mjs` | Byte-identical output, exit codes, incident log lines and share bundle: the same redaction (home directory, `/Users/<name>`, `/home/<name>`, emails, tokens, uuids, `ses_` ids, with JS white space and a 2000-unit cut), fingerprint (JS `toLowerCase`, final sigma included) and report order (V8's TimSort, `internal/jssort`, so ties and incomparable timestamps order as in Node). Appends to `~/.local/state/opencode/orchestrator/incidents.jsonl` like the mailbox does; an explicitly empty `HOME` is kept as Node keeps it (the log is then relative to the working directory and redaction uses an empty home), never replaced by the account's home. Entries carry `orch`'s version instead of the checkout's git HEAD (PE-4). `share` writes no `project` key and uploads nothing. |
| `orch policy show\|set key=value...\|unattended [--cwd DIR]` | `bin/policy.mjs` | Byte-identical output, exit codes and `.orchestrator/policy.json`: the registry's `autonomy` (without `$comment`) overridden by the project file; `set` refuses `key=never` except `force_push`, and any `force_push` other than `never`. The registry is read as `JSON.parse` of the file, then `.autonomy`, as the JS does (Node's fs errors, a `SyntaxError`, a `TypeError` for `null`, no autonomy for any other shape); `set` has written its file when that fails. A `policy.json` holding an array makes `set` fail instead (PE-6). |
| `orch claude-exec [--model M] [--effort E] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/claude-exec.mjs` | Byte-identical argv passed to `claude`, stdout JSON, `[claude]` progress lines, exit codes (0, 1, 2, 75), `.orchestrator/runs/*.json` record, incident lines and usage cache: the same option values (a trailing bare value key is the JS `true`: `--model` alone runs model `true`, `--timeout-min` alone is one minute), budget gate and refusal, event handling (lines split as readline splits them, U+2028 and U+2029 included; UTF-16 cuts; `tools_used` property-key quirks), stderr tail, timeout with Node's timer clamp, usage-limit path with its awaited refresh, and `CLAUDECODE` removed from claude's environment. Differences: a bare `--prompt` is refused (PE-8); no Node warnings (PE-9); claude is killed 10 s after a signal it ignores and its pipes are waited on for at most 10 s (PE-10); SIGTERM/SIGINT are forwarded to claude and the run is reported (PE-11); the post-run usage refresh runs detached and completes (PE-12); U+FFFD where a UTF-16 cut leaves a lone surrogate (PE-1). Uncaught JS errors print only their error line, after the progress lines printed so far. |

The hidden `orch json-echo [--indent N]` exists for the parity harness. The plugin does not call
`orch` yet.

## Build

Go 1.25+, no third-party dependencies, no cgo.

```sh
cd go
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/orch ./cmd/orch
./scripts/build-all.sh   # dist/orch_<version>_<os>_<arch> for darwin/linux x arm64/amd64 + checksums.txt
```

Linux binaries are fully static. macOS binaries built with cgo off still link `libSystem`, which
the Go runtime requires on darwin; that is as static as a macOS binary gets. `bin/` and `dist/` are
gitignored.

The release number lives in `internal/version/VERSION` (embedded). `orch version` appends the
VCS revision Go records at build time, e.g. `0.5.0 (5ae996b)` or `0.5.0 (5ae996b+dirty)`.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `ORCH_HOME` | `~/.config/opencode/orchestrator` | The orchestrator tree (the JS `ROOT`). |
| `ORCH_REGISTRY` | `$ORCH_HOME/models.json` | The model registry. |
| `ORCH_NO_POST_RUN_REFRESH` | unset | `1` stops `orch claude-exec` from starting its detached post-run usage refresh (PE-12). Set only by the parity harness. |
| `HOME` | OS user home if unset/empty | Per-user state lives in `~/.local/state/opencode/orchestrator`. `orch incident` keeps an explicitly empty `HOME` as Node's `os.homedir()` does (log relative to the working directory). |

## Checks

```sh
cd go
go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...
go vet -tags parity ./parity
./parity/run.sh --only json,usage,mailbox,manifest,incident,policy,bridges   # chosen groups; ./parity/run.sh runs them all
```

`.github/workflows/go.yml` runs build, vet, gofmt, tests and the `json`, `mailbox`, `manifest`, `incident`, `policy` and `bridges` parity groups on macOS and Linux
with `CGO_ENABLED=0` and Node 26 installed. Malformed prompt byte tests also compare against Node
when it is available; fixed byte expectations run even without Node.

## Parity harness

Use Node 26 (`referenceNodeMajor`), as CI does: on Node 22 or 24 one case reports SKIP by
design (PE-5).

`parity/run.sh` builds `bin/orch`, then runs `go test -tags parity ./parity`. Each case runs the
JS and `orch` with the same argv-equivalent and stdin, each in a fresh temp `HOME` and a fresh
project directory containing `.git/`, with an environment of only `HOME`, `PATH`, `ORCH_HOME` and
`ORCH_REGISTRY` (plus `ORCH_NO_POST_RUN_REFRESH=1` in the bridge cases, PE-12). `PATH` is `parity/fakes` (stand-ins for `claude`, `codex`, `opencode` and `security` that
fail unless a case scripts them, so no case reads the real keychain or calls a real endpoint), then the directories of `node` and `sqlite3`. Exit codes, stdout,
stderr and every file under the project `.orchestrator/` and the HOME state dir must match.

Groups:

- `json`: JSON.stringify compatibility over `testdata/json/*.json` and the checkout's
  `models.json`, compact and indent 2.
- `usage`: `orch usage` against `bin/usage.mjs` over the cache fixtures in
  `testdata/usage/parity-caches.json` and over channel sources (sqlite, `opencode auth list`,
  `auth.json`); re-collection through the scripted codex app-server fake, including the
  last-good cases; the cache round trip in both directions; and `routing`, `budget gate` and
  `effort` against the JS functions they replace. Only `generated_at` is normalised, and only
  when a collection happened.
- `mailbox`: every `orch mailbox` subcommand against `bin/mailbox.mjs` over seeded mailboxes
  (duplicate and `by`-less acks, duplicate ids, hand-edited lines of every JSON shape), all
  filters and `--limit` forms, stdin bodies and raw argv with malformed UTF-8, `--root` and
  `ORCHESTRATOR_PROJECT`, the inbox limit applied before its recipient filter. Fresh uuid v4
  ids, millisecond ISO timestamps and the `replied (xxxxxxxx)` prefix are normalised only
  in newly appended record fields and their write receipts, with exact format checks.
  Seeded records, read-only output and literal user content are compared unchanged.

- `manifest`: `orch manifest` against `bin/manifest.mjs` over a seeded tree holding every
  ignored directory name (as a directory and as a file), NFC/NFD, private-use and astral names
  (UTF-16 order), index-like names, a root `__proto__` file, symlinks to files and directories, a
  symlink loop and a FIFO; explicit ignored directories and symlink arguments, broken symlinks,
  `--root` with `../x`, duplicates, `--baseline`, `--out` outside `.orchestrator`; verify and
  diff over hand-made manifests of every shape (prototype keys such as `constructor`, arrays,
  strings, null). Only `created_at` is normalised, in its exact place and format.
- `incident`: every `orch incident` subcommand against `bin/incident.mjs`: `record` with every
  flag, bare flags, `--detail -` with malformed UTF-8, every `--ctx` form (duplicates, `a=b=c`,
  `=x`, index-like and `__proto__` keys), `--cwd` forms and each crash in the JS order; `list`,
  `report` and `share` over seeded logs (ties, projects, more than 100 entries, hand-edited lines
  of every shape), all filters including bare ones, `--out`, and every command with `HOME=""`
  (the log then lives under the sandbox project), the PE-5 regression case, and a live run of
  `testdata/jssort/gen.mjs` that must reproduce `orders.json` on Node 26 or later. Normalised: a new entry's `at` and
  `version` (PE-4), its `project` when it is the sandbox's temp directory name, the receipt's
  `at`, the bundle's `generated_at` and the HOME prefix of the log path in the usage line.
- `bridges`: `orch claude-exec` against `bin/claude-exec.mjs`, with `parity/fakes/claude` scripted
  per case (`$HOME/fake-claude/case` names files the fake prints as stdout and stderr, its exit
  code, or a signal it kills itself with or a sleep; it logs its argv, one argument per line, to
  `$HOME/fake-log/claude-argv.txt`, which is compared as a file). It replays every fixture in
  `testdata/claude/stream-json/` and covers every flag, the four prompt sources and their file
  errors, `--help`, `--quiet`, the gate (red refusal, `--force`, a fable model against a red Fable
  weekly window, unknown usage, a codex that cannot be spawned), the usage-limit path with a
  scripted refresh and with a failing one, a claude killed by a signal, a stderr tail of more than
  3 lines, the timeout (PE-9), each trailing bare value key, the crashes (bare `--cwd`,
  `--json-schema` and `--add-dir`, a missing schema file, spawn ENOENT and EACCES, a missing
  `--cwd`, a null event or block, non-iterable content, non-string text or result, bare `--resume`
  on the normal path), odd event shapes, events holding raw U+2028 and U+2029, and PE-8 and PE-1
  (the three cuts inside an emoji) as asserted differences. Every case seeds a
  fresh green usage cache unless it needs another, and only the usage-limit cases script the
  codex fake. Normalised: `started_at`, `duration_ms`, the run-record stamp (in paths and file
  names), the seconds in the `done` line, the sandbox project path (`$PROJECT`), a new incident's
  `at`, `version` and temp-directory `project`, and a re-collected cache's `generated_at`.
- `policy`: every `orch policy` command against `bin/policy.mjs` over override files of every
  shape (none, unparsable, BOM, `null`, string, array, own `__proto__`, a directory), `--cwd` in
  every position, refusals and `force_push`, the TypeErrors of a primitive `policy.json`, and
  `show`/`set` against a copy of the JS tree whose `models.json` is missing, a directory, `null`,
  unparsable, BOM-prefixed or of another shape (orch reads the same file through
  `ORCH_REGISTRY`). Nothing is normalised.

A case can add environment entries (`env`), compare files outside `.orchestrator/`
(`extraFiles`), replace the sandbox project path with `$PROJECT` everywhere (`projectPaths`) and
declare that it ends in an uncaught JS exception (`crash`, below), possibly after progress lines
(`progress`).

## Crash output and parity exceptions

Where the JS dies of an uncaught exception, Node prints a source excerpt, `<Name>: <message>`,
a stack and its version, and exits 1. `orch` exits 1 after the same stdout and file writes and
prints only the error line:

- C1, fixed messages (`Error: no message with id zz`, `Error: post needs from and subject`,
  filesystem errors such as EISDIR/ENOTDIR/EACCES, ...): each case pins the expected error
  line; the harness requires that exact nonempty line as `orch`'s entire stderr and as a
  whole line in Node's stderr, with exit 1 from both. Filesystem errnos outside the 20
  that `orch` spells as Node does (EACCES, EEXIST, EISDIR, ELOOP, ENOENT, ENOTDIR, ...) print
  Go's own error text instead; that difference is known and not asserted.
- C1 also covers Node's own coded and type errors with fixed text, such as
  `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received undefined`
  (`manifest verify` without a file) and `TypeError: Cannot convert undefined or null to object`
  (a manifest without `files`), `TypeError: opts.ctx.push is not a function` (an incident `--ctx`
  value after a bare `--ctx`) and `TypeError: Cannot set properties of null (setting 'a')` (a
  `policy.json` holding `null`). Paths in these messages use the physical working directory, as
  `process.cwd()` does, not `$PWD`.
- C2, malformed hand-edited state (a message whose `kind` is not a string, `refs` missing, ...):
  both stderrs must contain a line starting `TypeError: `; V8's wording is not reproduced.
- A crash after progress (`orch claude-exec`, which logs `[claude] ...` lines before it can fail):
  `orch` prints the progress lines, then the error line. Node's stderr must begin with exactly
  those progress lines and print no further one; the rest is compared as C1 or C2. Where the JS
  dies inside its line handler while claude still runs (a `null` event, say), `orch` also sends
  claude SIGTERM before it exits, instead of leaving it running.

The other documented differences, each asserted by its own parity case:

- PE-1: a UTF-16 slice that splits a surrogate pair (the reply subject cut at 140 units, the inbox
  body at 400, an incident summary at 300, redacted text at 2000, the fingerprint input at 200;
  in `orch claude-exec` the result error text at 200, the incident summary at 160 and the run
  record's `prompt_head` at 400, which reach stdout, the run record and the incident) or a
  string message spread into one key per code unit gives U+FFFD in Go. Hashes are identical (Node
  hashes a lone surrogate as U+FFFD). With an empty `HOME` the JS home pattern is empty and
  matches between the halves of every astral character, splitting them the same way. Text
  output is identical (Node prints a lone surrogate as U+FFFD); JSON escapes differ: the JS writes
  `"\ud83d"`, `orch` the raw U+FFFD.
- PE-2: the crash output above. A manifest file or (for `orch policy`) a registry that
  `JSON.parse` rejects prints a `SyntaxError: ` line in both runtimes; the message after the
  class differs.
- PE-3: `orch mailbox post --json` prints the whole message it appended; the JS ignores
  `--json`. As in `list`, only a bare `--json` counts (`--json x` is the value of a key).
- PE-4: an incident's `version` is `orch version` (e.g. `0.5.0 (8b23cd3)`); the JS writes its
  checkout's `.git/HEAD`, trimmed to 40 characters (`ref: refs/heads/main`), or `unknown` when
  there is none.
- PE-5, reference V8 (approved by the human, mailbox 0b533797): the report comparator is
  inconsistent for equal counts whose `last` values are equal or incomparable, so the order
  depends on every comparison the sort makes, and V8's sort differs between Node release lines.
  `orch` reproduces `Array.prototype.sort` of V8 14.6, i.e. Node 26 (`internal/jssort`: binary
  insertion up to 7 elements, then runs, the merge-stack rules and adaptive galloping; goldens from
  v26.5.1, also checked on v26.7.0), against 1611 sorts of up to 6000 elements with four
  inconsistent comparators and a consistent one, and 25 summaries of 60 to 224 groups. Node 22
  (V8 12.4) and Node 24 (V8 13.6) order 13 of those 1611 sorts differently, all with 3 to 7
  elements under an inconsistent comparator. In `incident report` and `share` that happens only
  when a hand-edited log holds `at` values that do not compare (non-ISO strings, numbers,
  booleans, missing): the regression case `"t0"`, `true`, `"2"` gives fp `1,0,2` on Node 26 and
  in `orch`, `2,1,0` on Node 22 and 24. Logs written by either runtime (ISO `at`) order the same on
  22, 24 and 26 (20,000-sort probe). The parity harness and CI use Node 26; the JS tools still
  support Node 22+. Under an older Node the harness skips the live jssort differential with a
  PE-5 message and compares the regression case with the summary put in fp order.
- PE-6: relational comparisons with object or array operands (`--since` against an object `at`)
  are false in `orch`; the JS compares their string forms. `policy set` on a `policy.json`
  holding an array exits 1 with a `TypeError:` line; the JS rewrites the array and succeeds.
- PE-7: `toLowerCase` of code points Go's Unicode 15.0 tables do not know, or whose case
  properties changed since (U+0295, U+1171E), follows Go, not Node's newer ICU data. The test of
  `jsstr.ToLower` against every code point lists them (196 with Node v26.5.1 / Unicode 17.0).
- PE-8: `orch claude-exec` with a trailing bare `--prompt` prints `no prompt: --prompt needs a
  value` and exits 2 before the budget gate. The JS runs claude on the prompt `true` (a real model
  turn), then crashes hashing it on the normal path, or prints its usage-limit JSON. Neither the
  worker nor `flip run` passes a bare `--prompt`; they pipe the brief on stdin. Accepted by the
  human (51e9d79b).
- PE-9: `orch claude-exec` prints no Node process warnings. With a `--timeout-min` whose delay Node
  clamps to 1 ms (NaN, below 1 ms, above 2147483647 ms, e.g. `0`, `-1`, `abc`, `35792`), Node 26
  prints a `TimeoutNaNWarning`, `TimeoutNegativeWarning` or `TimeoutOverflowWarning` on stderr;
  `orch` applies the same 1 ms delay silently. The parity cases drop those lines from the JS stderr.
- PE-10: after `orch claude-exec` signals claude (the timeout, a forwarded signal, a crash), it
  sends SIGKILL if claude has not exited 10 s later, and once claude has exited it waits at most
  10 s for its stdout and stderr to close (a grandchild may hold them). The JS sends one SIGTERM
  and waits for `close` forever. No output changes; a signalled claude still has `exit_code: null`.
  Accepted by the human (51e9d79b).
- PE-11: SIGTERM and SIGINT sent to `orch claude-exec` are forwarded to claude, and the run then
  ends as any run does once claude closes: for a claude the signal killed, `exit_code: null`, the
  `claude exited null: ...` error, a run record, the incident, the JSON and exit 1. The JS dies at once and leaves claude running. claude stays in
  the bridge's process group, so a signal to the whole group reaches it in both runtimes.
- PE-12: after a normal run the JS starts a usage refresh it never awaits; `process.exit` follows
  in the same tick, before any of its awaits resolve, so it never writes `usage.json` (it only
  runs a token lookup and spawns a `codex app-server` that dies with it). `orch claude-exec`
  makes the refresh real (plan 3.10 item 3, kept by the human in 51e9d79b): once the result is
  printed it starts `orch usage --refresh` in a session of its own, stdio on `/dev/null`, and
  exits without waiting, so its exit code and timing are unchanged. The refresh is bounded by the
  timeouts of the feeds it reads (security 30 s, the Claude endpoint 15 s, codex app-server
  20 s, sqlite3 5 s, `opencode auth list` 15 s) and writes `usage.json` as `orch usage
  --refresh` does (which needs a readable registry). The usage-limit path is unchanged: both runtimes await their refresh there.
  The parity harness sets `ORCH_NO_POST_RUN_REFRESH=1` for the bridge cases, so no refresh
  writes into a sandbox after its files are compared. A `usage.json` written during a run comes
  from the budget gate, which both runtimes run alike when they read the same registry: with an
  empty `HOME` and no `ORCH_REGISTRY`, the JS reads its own checkout's `models.json` and its gate
  collects and writes a failed reading, while `orch` finds no registry under `ORCH_HOME` and
  proceeds without collecting (see Environment).

`ORCH_JS=/path/to/orchestrator ./parity/run.sh` compares against another JS tree.

## JSON compatibility

All JSON `orch` writes goes through `internal/jsjson`, which matches Node's `JSON.stringify` byte
for byte: JS property order (canonical array-index keys first, ascending, then insertion order),
ES number formatting, and no escaping of `/ < > &`, U+2028, U+2029 or non-ASCII text. The single
known divergence: a lone UTF-16 surrogate escape (`"\ud800"`) parses to U+FFFD, because a Go
string cannot hold it.

Prompt files, stdin and mailbox argv are decoded like Node's UTF-8 Buffer decoding: malformed sequences
become U+FFFD (an incomplete multi-byte prefix produces one replacement). `jsjson.Unmarshal`
uses `encoding/json`, which replaces invalid input bytes rather than passing them through;
its malformed-sequence grouping is not a claim of Node Buffer decoding parity. `Marshal` and
`MarshalIndent` reject invalid UTF-8 Go strings, object keys and indentation with
`jsjson.ErrInvalidUTF8`, returning no JSON bytes. Decode raw text at its input boundary first.

## Usage feeds

`internal/usage` mirrors `lib/usage.mjs`, `lib/claude-usage.mjs` and `lib/codex-appserver.mjs`.
`internal/budget` mirrors `lib/budget.mjs`. External programs are found on `PATH`, as in the JS:
`security` (macOS keychain, 30 s timeout), `sqlite3` and `opencode` (channel detection), and
`codex app-server` (JSON-RPC, 20 s). The Claude usage request sends Node's fetch headers. Known
JS defects are mirrored on purpose. They are listed in the S3 report, together with the
malformed-input cases that are not mirrored.

Two lifecycle details deliberately differ from the JS without changing any output or cache byte.
A `codex app-server` child that ignores SIGTERM is killed after a 1 s grace and reaped before
`orch` returns; the JS only sends SIGTERM. The cache is written through a temp file unique to the
writer (`usage.json.<pid>.<n>.tmp`, then renamed) rather than the JS's shared `usage.json.tmp`.
Concurrent `orch` refreshes therefore cannot clobber each other's or the JS's temp file. Two JS
writers still can.

## Fixture provenance

`testdata/json/*.golden` are generated by `node testdata/json/gen.mjs` (last run with Node
v26.5.1) from the `*.json` inputs beside them: `<name>.compact.golden` is
`JSON.stringify(JSON.parse(input))` and `<name>.indent2.golden` the same with indent 2, without a
trailing newline. Regenerate after adding an input and commit inputs and goldens together.

`testdata/usage/*.golden` are generated by `node testdata/usage/gen.mjs` through the JS
normalizers, from the redacted feed recordings beside them (no tokens, no account identifiers).

`testdata/incident/corpus.json` is generated by `node testdata/incident/gen.mjs` (last run with
Node v26.5.1) through `lib/incident.mjs`, with `HOME` set to a fixture path inside the script:
redaction and fingerprint cases and seeded, tie-heavy `summarize` inputs with their outputs.
`testdata/incident/mixed-at-80.jsonl` is the cross-vendor reviewer's fixture: Python's
`rng = random.Random(0)`, then for `i in range(80)` the entry `{"fp": str(i), "at": i if
rng.randrange(2) else "t"+str(i), "component": "c", "kind": "note", "summary": "s"}`, then
`rng.shuffle(entries)`, one compact JSON line each. `testdata/jssort/orders.json` is generated by
`node testdata/jssort/gen.mjs`: the sha256 of each order V8 produces.
`testdata/jsstr/lower.json` is generated by `node testdata/jsstr/gen-lower.mjs`: the
`toLowerCase` of every code point and the final-sigma context sets as Node observes them.

`testdata/claude/stream-json/` holds redacted `claude -p --output-format stream-json` captures
(Claude Code 2.1.287) and three synthetic cases built from them by `gen.mjs`; how they were
captured, redacted (`testdata/redact.mjs`) and generated is in its `PROVENANCE.md`.
