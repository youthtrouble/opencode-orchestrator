# orch: Go runtime of the orchestrator

`orch` is the only runtime in **0.6.0 (unreleased)**: one Go binary, installed by `install.sh`
(see [Install](#install)), that runs every plugin tool and hook. The JS runtime, libraries and
their tests are removed. The last JS release is
[v0.5.0](https://github.com/youthtrouble/opencode-orchestrator/releases/tag/v0.5.0) (2026-10-19);
the parity oracle is the later pre-S12 commit `9e3a456`, pinned in `go/parity/js-ref`.

For what the orchestrator does and how to use it, see the [root README](../README.md); for the
scripts' own behaviour and state files, [orchestrator/README.md](../orchestrator/README.md).

Commands (ported from the JS at that parity reference; differences are listed below):

| Command | Ported from (JS) | Behaviour |
|---|---|---|
| `orch version` | — | Release number plus VCS revision. |
| `orch usage [--refresh] [--json]` | `bin/usage.mjs` | Byte-identical output; reads and writes the same usage cache, so either runtime can read what the other wrote. A failed vendor reading (a 429 from the Claude usage endpoint, a codex error) is replaced by the last good cached one, marked `last_good_at` (when it was read) and `read_error`, if the cache was written less than 30 minutes ago. The window rolls: each failed refresh rewrites the cache, so refreshes that keep failing less than 30 minutes apart keep a reading of any age (as in the JS); check `last_good_at`. |
| `orch routing [--refresh \| --cached] [--json]` | `getRouting()` in `lib/usage.mjs` | The routing prompt text, or `{usage, registry, channels, resolved, prompt}` with `--json`. `--cached` never collects and exits 3 when there is no cache; bad flags exit 2. |
| `orch budget gate --vendor anthropic\|openai [--force] [--frontier]` | `budgetGate()` in `lib/budget.mjs` | Compact `{allowed, level, resets_at, note}`; exit 0 whether or not the work is allowed, 2 on bad flags or an unknown vendor. It never draws ChatGPT credits: only `orch codex-exec` does (see `openai_credits` in [orchestrator/README.md](../orchestrator/README.md#openai-credits)). |
| `orch effort --role ROLE [--size SIZE]` | `effortFor()` in `lib/routing.mjs` | The effort-policy value as plain text, or `null`; exit 2 on bad flags or without `--role`. |
| `orch mailbox post\|list\|ack\|reply\|inbox` | `bin/mailbox.mjs` | Byte-identical output and `.orchestrator/mailbox/*.jsonl` lines (one `write` per line on an append-only descriptor, so concurrent JS and Go appenders do not interleave). `post --kind question` requires a non-empty `--recommended TEXT` and takes `--blocking true\|false` (default and bare: true), written as `"recommended"` and `"blocking"` after `"thread"`; a missing recommendation, another `--blocking` value or either option on another kind prints `mailbox post: <usage>` on stderr and exits 2 with nothing written. `--ask decision\|action` (WT25): `decision` is the default and is not written; an `action` question takes no `--recommended` and is written as `"blocking"` then `"ask":"action"`; `--recommended` with `--ask action`, a recommendation that opens (after the JS `trim()`, ASCII case aside) with a completion claim (`completed`, `all done`, `i ran`, `i've done`, `i have done` or `i did`, then a word boundary; `done` then `:` `,` `.` `!` `—`, the end or a clause cue; `ran`, `created`, `installed`, `finished`, `deployed` or `rebuilt` then `:` `,` or a clause cue; a clause cue is `[ \t]+` then `it`, `the`, `that`, `this`, `successfully`, `ok` or `all` and a word boundary; a guard rail, not a guarantee), another `--ask` value, or `--ask` on another kind exits 2 the same way; `inbox` ends a blocking question's header with ` (blocking)` and prints its `recommended:` line (first 200 UTF-16 units) under the body. `post --json` prints the whole message instead of `{id, at}` (PE-3). `inbox` names `orch mailbox reply` in its reply hint (PE-19). A `reply` to the message that the human gate's `awaiting_human.question` (a non-empty string in `flip.json`, a full id or a prefix) resolves to, as the reply's own id is resolved, clears the gate as `orch flip gate --clear` does and prints `cleared the human gate; the supervisor relaunches the <door> conductor within a minute` (`cleared the human gate` when the kept `pending_launch` names no door); a question that resolves to no single message is reported on stderr as `could not match the human gate's question: <ambiguous message id prefix X \| no message with id X>` and clears nothing; a `flip.json` that exists but cannot be read or parsed refuses gate clearing with an error naming that file and exit 1 (PE-32); a reply note and ack already posted remain recorded, and a remote retry finishes the same reply after repair. Uncaught JS errors print only their error line (see "Crash output and parity exceptions"). |
| `orch manifest create\|verify\|diff` | `bin/manifest.mjs` | Byte-identical output, exit codes and manifest files: the same walk (symlinks, FIFOs and other non-regular entries inside a walked directory are skipped and never opened; an explicit symlink argument is followed), UTF-16 key order and `manifest_sha256`. `create` replaces its output through a private (0600) temp file and a rename, the same in both runtimes, so a symlink at `--out` is replaced, never followed. Each runtime draws its own random temp name, so a failed rename (a directory at `--out`) prints the same error line except for that name's `.tmp-<random>` suffix, which the parity check normalizes. Uncaught JS errors print only their error line. |
| `orch incident record\|list\|report\|share` | `bin/incident.mjs` | Byte-identical output, exit codes, incident log lines and share bundle: the same redaction (home directory, `/Users/<name>`, `/home/<name>`, emails, tokens, uuids, `ses_` ids, with JS white space and a 2000-unit cut), fingerprint (JS `toLowerCase`, final sigma included) and report order (V8's TimSort, `internal/jssort`, so ties and incomparable timestamps order as in Node). Appends to `~/.local/state/opencode/orchestrator/incidents.jsonl` like the mailbox does; an explicitly empty `HOME` is kept as Node keeps it (the log is then relative to the working directory and redaction uses an empty home), never replaced by the account's home. Entries carry `orch`'s version instead of the checkout's git HEAD (PE-4). `share` writes no `project` key and uploads nothing. `record --request -` (orch only, PE-25) reads the whole incident as one JSON object on stdin, so no summary or context reaches argv. |
| `orch policy show\|set key=value...\|unattended [--cwd DIR]` | `bin/policy.mjs` | Byte-identical output, exit codes and `.orchestrator/policy.json`: the registry's `autonomy` (without `$comment`) overridden by the project file; `set` refuses `key=never` except `force_push`, any `force_push` other than `never`, an `openai_credits` other than `off` or `on`, and an `openai_credits_floor` that is not a number, 0 or more. The registry is read as `JSON.parse` of the file, then `.autonomy`, as the JS does (Node's fs errors, a `SyntaxError`, a `TypeError` for `null`, no autonomy for any other shape); `set` has written its file when that fails. A `policy.json` holding an array makes `set` fail instead (PE-6). |
| `orch claude-exec [--model M] [--effort E] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/claude-exec.mjs` | Byte-identical argv passed to `claude`, stdout JSON, `[claude]` progress lines, exit codes (0, 1, 2, 75), `.orchestrator/runs/*.json` record, incident lines and usage cache: the same option values (a trailing bare value key is the JS `true`: `--model` alone runs model `true`, `--timeout-min` alone is one minute), budget gate and refusal, event handling (lines split as readline splits them, U+2028 and U+2029 included; UTF-16 cuts; `tools_used` property-key quirks), stderr tail, timeout with Node's timer clamp, usage-limit path with its awaited refresh, and `CLAUDECODE` removed from claude's environment. Differences: a bare `--prompt` is refused (PE-8); no Node warnings (PE-9); claude is killed 10 s after a signal it ignores and its pipes are waited on for at most 10 s (PE-10); SIGTERM/SIGINT are forwarded to claude and the run is reported (PE-11); a failed run stopped at the `--timeout-min` cap is a `timeout` incident (PE-45); claude runs in a process group of its own, which the timeout and the PE-10 kill reach whole (PE-22); the run record and `runs/` are owner-only and written atomically (PE-21); the post-run usage refresh runs detached and completes (PE-12); U+FFFD where a UTF-16 cut leaves a lone surrogate (PE-1). Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch codex-exec [--model M] [--effort E] [--sandbox S] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/codex-exec.mjs` | Byte-identical argv passed to `codex` (`exec --json ... -`, with `--approve-for-me` instead of `-s` exactly when the sandbox is `workspace-write`, and the `exec`-only options before `resume <id>`, as codex-cli 0.162.1 requires), the prompt written to codex's stdin, codex's working directory and whole inherited environment, stdout JSON, `[codex]` progress lines, exit codes (0, 1, 2, 75), run record, incident lines and usage cache: the same option values (a bare `--model` is the JS `TypeError: model.includes is not a function` before the gate; a bare `--sandbox` is `-s true` without `--approve-for-me`), budget gate (frontier is a case-sensitive `astra` substring) and refusal, event handling (a `thread.started` without `thread_id` leaves the key out of the JSON yet keeps its place; absent `command`, `exit_code`, `path` and `kind` are left out; `turn.failed` serialized with JS key order and numbers), stderr tail, timeout with Node's timer clamp, and the tool-host no-op path with its two incidents. With `openai_credits=on` in the project's (`--cwd`'s) policy, a red OpenAI window whose routing is credit-funded is allowed once a fresh usage reading confirms it (refused when that reading fails), and the result and run record gain `credits: {before, after}` (the balance at the gate and from a fresh usage reading after the run; `after` is `null` when that reading failed). Differences: PE-1, PE-8 to PE-13, PE-21 (the run record), PE-22, PE-44 (top-level `error` events are collected) and PE-45 (a cap stop is a `timeout` incident). Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch opencode-exec [--agent A] [--resume ID] [--model M] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/opencode-exec.mjs` | Byte-identical argv passed to `opencode` (`run --format json --auto --agent A ...` with the prompt last), its working directory and whole inherited environment (stdin on `/dev/null`), stdout JSON, `[opencode]` progress lines, exit codes (0, 1, 2, 75), run record and incident lines: the same option values, budget gate and refusal, event handling (the first truthy `sessionID` unless resuming, the last text part), the bare stderr tail of a failed run that reported no error event (`""` included), and a timeout without a log line. Like the JS it has no start or done line and no post-run usage refresh. Differences: PE-1, PE-8 to PE-11, PE-21 (the run record), PE-22, PE-44 (`error` events are collected, so a usage limit is a `limit` incident with its text), PE-45 (a cap stop is a `timeout` incident), PE-46 (`--help` prints help; `--effort X` is accepted and ignored), PE-47 (the failure incident's `context.model` is `--model`) and PE-65 (opencode is stopped with SIGINT, not SIGTERM). Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch worker start --brief FILE --harness claude\|codex\|opencode [--lane L] [--model M] [--effort E] [--sandbox S] [--permission-mode P] [--allowed-tools T] [--max-turns N] [--timeout-min N] [--resume ID] [--agent A] [--breadth B] [--run-id ID] [--log-path F] [--force] [--detach] [--cwd DIR] \| status [--lane L] [--cwd DIR] \| stop --lane L [--cwd DIR]` | `bin/worker.mjs` | Byte-identical output and exit codes over the same `.orchestrator/workers/<lane>.json` and `<lane>.lock`. `start` (foreground) validates as the JS does (usage, `brief must be an existing .md file`, `harness must be codex, claude or opencode`, exit 2), reads the brief before it claims the lock (an unreadable brief is the JS error, exit 1, and leaves no lock or state, even on a locked lane), claims the lane with a kernel record lock on `<lane>.lock` held for the whole run (`{"error":"lane locked",...,"hint":...}` on stderr, exit 3, while another runner holds it; a lock file no process holds is reclaimed, PE-28), publishes the state with the JS keys in the JS order plus `launch_options` after `model` (PE-62; `state_storage` from the second write, `last_state_write_error` after a failed one), runs `orch <harness>-exec` (the JS runs `node <harness>-exec.mjs`) in a session of its own with the same arguments and the same prompt on stdin (the preamble and the brief, or the brief alone for the `conductor` lane), relays the bridge's stderr to stdout while recording `session_id`, `last_progress_at` and `last_tool`, and ends deferred (exit 75 or `budget_red`: limit incident, `deferred` message), finished (no message) or failed (incident, `blocked` message with the run record as ref), then prints `{lane, status, session_id, exit_code}` (exit 0 for finished or deferred, else 1). SIGINT and SIGTERM publish `interrupted` and are passed on to the bridge's process group. `status` lists the given lane, or every `*.json` in byte order, each state spread as the JS spreads it (any JSON value; unreadable or falsy as `{lane, status: "unknown"}`) with `process_alive` (Node's `process.kill(pid, 0)` coercion: an integer in the int32 range, EPERM alive) and `lock_present`; `stop` sends SIGINT to the stored pid's process group, else to the pid. Lane names are the JS values (a bare `--lane` is the lane `true`, no `--lane` on `stop` reads `undefined.json`); a lane containing `/` is refused by `start` (after the harness check), `status` and `stop` with `lane must not contain "/"` on stderr, exit 2, before anything is read or written (both runtimes; `.` and `..` stay names inside `workers/`). Differences: `stop` signals only a pid of at least 2 (PE-14) that the kernel reports as the holder of the lane's lock (PE-23); a lock file left by a dead runner is reclaimed and the lane-locked hint never says to remove the lock (PE-28); the state is written owner-only through a private temp and a rename, and `workers/` and `logs/` are 0700 (PE-21); a failed `start` writes `<token>` for token-shaped text in the state's `errors` and the blocked message (PE-24); `start` survives a bridge that never reads its prompt (PE-13), waits at most 10 s for the bridge's pipes once it exits and never kills it (PE-10), records the Go bridge's report of an interrupted run (PE-11), cuts UTF-16 text as PE-1 says, and its usage line names `codex|claude|opencode` (PE-48). `start --detach` first refuses a lane whose lock a runner holds synchronously, as `flip.mjs run` relies on (`{"error":"lane locked","lane":...,"previous":...}` with `alive: true` and no `hint` on stderr, nothing on stdout, exit 3), leaves a lock file no process holds for its runner to reclaim (PE-29), creates `.orchestrator/logs/<lane>-<first 8 of the run id>.log` (exclusive, 0600), and launches `orch worker` with the same arguments minus every exact `--detach` token plus `--log-path`, `--run-id` and `--cwd <absolute project root>` (last, so a relative `--cwd` is not applied twice; both runtimes) in the project root, in a session of its own, stdin on `/dev/null` and stdout and stderr on the log; it prints `{lane, runner_pid, log_path, run_id, keep_awake}` and exits 0 (`runner_pid` is left out when the runner cannot be started, as the JS never sees that error). Keep-awake wraps the runner in `caffeinate -i -s` (macOS) or `systemd-inhibit --what=idle:sleep --why=opencode-orchestrator` (Linux) when `which` finds it, unless the registry's `runtime.keep_awake` is `false` (any registry error leaves it on); `keep_awake` reports whether it was wrapped. |
| `orch flip decide --active D \| run --to D [--from D] [--reason R] [--resume ID] [--force] [--unless-gated] \| pause [--active D] [--reason R] [--brief FILE] \| gate [--active D] [--reason R] [--question ID] [--clear [--no-relaunch]] \| status [--cwd DIR]` | `bin/flip.mjs` | Byte-identical stdout, stderr, exit codes, `.orchestrator/flip.json` (indent 2; both replace it through a temp file and a rename, orch's private and owner-only, the JS's the fixed `flip.json.tmp`, PE-21), mailbox lines and, for `run`, the repository's `info/exclude`, the continue brief and everything the launched conductor worker writes. `flip.json` is read as `JSON.parse` of the file; only ENOENT is absent state. Invalid JSON or unreadable state makes `decide` print `{"action":"gate","reason":"flip state unreadable: <code>; fix or remove .orchestrator/flip.json"}` (exit 0, no usage collection), makes every writer refuse with exit 1 and the file untouched, and blocks supervisor relaunches with one log entry (PE-32). Each writer reads and atomically replaces the data under a transaction on the separate, persistent `flip.json.lock`; no child is launched while it is held. Valid JSON values retain their previous handling except primitive gate records (PE-34): `status` prints the record or `{"state": "never flipped"}` for a falsy one; `pause` creates `.orchestrator/` and writes `{...previous, paused: {at, door, reason[, brief]}}` (`brief` only for a non-empty `--brief`; a string spreads into index keys, a number or `true` into nothing, an existing `paused` keeps its position, `awaiting_human` is kept) and posts a `handoff` to `supervisor`; `gate` creates `.orchestrator/`, then transactionally reads `flip.json` and sets `awaiting_human: {at, door, reason, question}` (an existing one keeps its position; an array or primitive `flip.json` is replaced by an object holding the gate, not by `--clear`, which writes truthy non-object values back unchanged), deletes `paused` and posts a `handoff` to `human` whose subject keeps 80 UTF-16 units of the reason; `gate --clear` deletes `awaiting_human` and writes any other shape back as it is; when an object held a truthy `awaiting_human`, it also sets `pending_launch: {to, from, reason: "human gate cleared", resume: null, at}` with `to` and `from` the gate's door (`opencode` when it has none), so the supervisor relaunches that conductor, unless a truthy `pending_launch` is already queued (a flip queued before the gate), which is kept; it prints `{"awaiting_human":false,"relaunch":"<door>"}` with the door of the queued launch (when it has a truthy `to`) instead of `{"awaiting_human":false}`; `--no-relaunch` (kickoff) never queues one and removes any queued `pending_launch` and `paused`. `decide` prints `{"action":"gate","to":null,"reason":"awaiting human: <reason>"}` when `flip.json` is an object with a truthy `awaiting_human` (`<reason>` is `${awaiting_human.reason}`, so `undefined` without one), without reading or collecting usage; otherwise it prints `{action, to, reason}` (`flip`, `handback`, `stay` or `pause`) from the cached routing (collecting when the cache is missing or stale, as `getRouting` does) and the time of the last flip (`flip_cooldown_minutes`); `--active` absent or empty prints `--active opencode\|claude required` (exit 2), and a non-door string (`gemini`, `constructor`, `__proto__`) is red with the JS reason text (`undefined`, or the inherited Object.prototype text). `run` refuses a `--to` other than `opencode` or `claude` (`--to opencode\|claude required`, exit 2, nothing written); otherwise it first reads state under the transaction, then adds `.orchestrator/` to the repository's `info/exclude` (`.git/info/exclude`, or for a `.git` file, the linked worktree's common dir or the submodule's git dir) when the project has a git repository (a `.git` file that is not `gitdir: <path>` naming a directory, or whose git dir's `commondir` names no directory, is none, and nothing is written) and no line of the file is `.orchestrator` or `.orchestrator/` (lines end at `\n` and one trailing `\r` is dropped, as Git reads the file; a lone `\r`, U+2028 or U+2029 is part of a line, so such a "line" does not count and the line is appended; a newline is added first unless the file is empty or ends in one), writes `.orchestrator/briefs/conductor-continue.md` (atomically, 0600, PE-21; it launches the brief a kickoff saved instead of writing the continue brief, and names it in the handoff, when the record it replaces has one: `paused.brief`, else `pending_launch.brief`, a non-empty string naming an existing file), replaces `flip.json` with `{at, from, to, reason, previous: {at, to}}` (clearing `paused` and `awaiting_human`), posts a `handoff` from `conductor-<from>` to `conductor-<to>`, and launches the conductor by running this executable as `orch worker start --brief .orchestrator/briefs/conductor-continue.md --lane conductor --harness <to> --agent conductor --breadth broad --cwd <root> --detach` (plus `--permission-mode auto` for claude, `--resume`, `--force`) to completion, so a Go-driven flip runs the conductor on `orch worker` and the Go bridges (the JS runs `node worker.mjs`). It prints `{flipped, to, from, launcher[, run_id, runner_pid]}` with 400 UTF-16 units of the launcher's trimmed output, exit 0 when the launcher exited 0, else 1; a launch whose launcher exited 0 with a last stdout line holding a string `run_id` and a numeric `runner_pid` (the detach answer) also carries those two, after `launcher` (PE-63). A launcher that cannot be started (no executable path, or the start fails) gives `{"flipped":false,...,"launcher":""}` and exit 1 with no diagnostic, as the JS `spawnSync` error result has no status and no output. When the launcher refuses with `lane locked` (usually because the calling conductor holds the lane), `flip.json` gains `pending_launch: {to, from, reason, resume, at[, brief]}` for the supervisor (`brief`: the saved brief this run launched, kept for the retry) and `run` prints `{"flipped":false,"pending":true,...}`, exit 0. A bare `--active`, `--from`, `--reason`, `--question`, `--resume`, `--to` or `--brief` is a one-line usage error (exit 2, no writes, PE-35). With `--unless-gated`, `run` checks `awaiting_human` under the lock and refuses with `{"flipped":false,"reason":"awaiting human"}` (exit 1, no state, brief, exclude or handoff write); every supervisor run passes it. Explicit human and kickoff runs keep clearing gates. A failed lane launch adds `pending_launch` in a second transaction against current state, preserving a gate installed during launch (PE-33). A routing failure prints `orch flip: <error>` and exits 1, as `orch usage` does (the JS dies of the rejection with Node's crash output). Any other command prints `commands: decide \| run \| pause \| gate [--clear [--no-relaunch]] \| status` (exit 2). Differences: PE-15 (the date formats of `at`), PE-1 (the gate subject and `launcher` cuts), PE-2 (a launcher crash quoted in `launcher` as its error line only), PE-10 (`run` waits at most 10 s for the launcher's pipes), PE-20 (a launcher whose output passes 1 MiB is stopped), PE-21 (`flip.json` and the continue brief), PE-22 (the launcher runs in a process group of its own), PE-32 (transactions and unreadable state), PE-33 (automatic runs preserve gates), PE-34 (primitive gate records and default gate reasons), PE-35 (required option values) and PE-63 (the launched run's `run_id` and `runner_pid`). |
| `orch supervisor start \| serve \| status \| stop \| ack --event ID [--by NAME] [--cwd DIR]` | `bin/supervisor.mjs` | Byte-identical stdout, stderr, exit codes and `.orchestrator/supervisor-acks.json` except the documented differences (indent 2, written through a temp file and renamed, as the JS `writeJson` does; PE-21). `status` prints `supervisor-state.json` as `JSON.parse` reads it, whatever the value (`null`, a number, a string, an array), or `{"status": "not_started"}` when it cannot be read or parsed (missing, a directory, unparsable, a BOM), and never writes. `stop` writes an empty `.orchestrator/supervisor.stop` and prints `stop requested; workers are unaffected`, whether or not a supervisor runs. `ack` refuses with `unknown event id` (exit 1) unless `--event` is truthy and names a truthy own property of the state's `events` (inherited Object.prototype names are refused, PE-39); then it sets `acks[<event>] = {by: <--by or coordinator>, at}` under a transaction on the separate persistent `supervisor-acks.json.lock`, retaining concurrent receipts (an existing key keeps its position; a bare `--event` is the key `true`, a bare `--by` writes `true`; `__proto__` without an own ack key of that name adds nothing and rewrites the file unchanged, as the JS prototype setter does) and prints `acknowledged (receipt, not acceptance)`. Usage: no command prints `commands: start \| serve \| status \| stop \| ack --event ID` (exit 2); any other command prints the same line (exit 2, PE-49). `start` creates `.orchestrator/logs/` (0700), prints `{"started":false,"reason":"already running","pid":N}` when a process holds the kernel lock on `supervisor.lock` (`N` is the kernel's holder, PE-30), else removes `supervisor.stop`, launches this executable as `orch supervisor serve --cwd <root>` in a session of its own (wrapped in `caffeinate -i -s` or `systemd-inhibit` unless the registry's `runtime.keep_awake` is `false`) with its output appended to `logs/supervisor.log` (0600; a symlink there fails `start` with ELOOP, PE-21), and prints `{"started":true,"supervisor_pid":N,"log":"<path>"}` (`supervisor_pid` left out when the command cannot be started). `serve` claims `supervisor.lock` with a kernel record lock it holds for its life, its pid written into the file as metadata (created 0600, PE-21; a lock file no process holds is claimed in place, PE-30; a held one prints `a supervisor already owns this project`, exit 1), then every 5 s after each tick: looks at every worker lane (phases, the two-phase intent, events), re-dispatches budget-deferred lanes as `orch worker start --detach`, wakes the coordinator through the configured channels (`command`, `codex-queue`, `opencode-run`, `claude-resume` as `orch claude-exec`), evaluates the conductor flip (relaunching through `orch flip decide\|run`), and records the outcome in `supervisor-state.json` (a failed tick: `storage_error` persisted once by the next successful write, `storage_failures` counting). The stop file makes it record `status: "stopped"` and `stopped_at`, release the lock and exit 0; it has no signal handlers of its own, so a killed serve leaves its lock file, which the kernel stops holding when the process exits and the next serve or start treats as free. While it waits for a child (a channel, a flip, a re-dispatch), RunSync relays a SIGINT, SIGTERM or SIGHUP to that child's process group, and serve then dies of the signal once the child is reaped, at most 10 s later (PE-22), still leaving its lock file. A state that is `null` or a primitive crashes as in the JS (C1). Differences: PE-16 (hand-edited non-object shapes), PE-17 (a missing `opencode`), PE-10 (a child that ignores the channel timeout's SIGTERM is killed 10 s later), PE-20 (a child whose output passes 1 MiB is stopped), PE-21 (state, acks, lock and log), PE-22 (every child it waits for runs in a process group of its own), PE-30 (ownership is the kernel lock: one serve per project; the tick's lane `stale_lock` and the flip evaluation's free conductor lock ask the kernel, not the file) PE-36 (supervisor-owned records), PE-37 (confirmation, backoff and five-failure stop), PE-38 (best-effort notes), PE-39 (serialized ack writes and own event keys), PE-49 (an unknown command is a usage error), PE-50 (a non-object `supervisor.json`), PE-51 (a non-object worker file), PE-52 (vanished lanes and null events), PE-59 (no next check for an ended lane), PE-60 (conductor restarts, relaunch notes, slow retry and stale confirmations) and PE-64 (failed lane redispatch subject). |
| `orch doctor` | `bin/doctor.mjs` | The same rows in the same order with the same texts (`OK  `/`WARN`/`FAIL`, name, `  —  detail` when the detail is truthy) and exit code (1 if any row is FAIL). Each probe (`opencode`/`codex`/`claude`/`node --version`, `codex login status`, `claude auth status`, `opencode auth list\|plugin list\|debug agents`) runs with a 20 s timeout and reads stdout then stderr; the usage feeds and routing come from a refreshed `orch usage`, rows pushed before a failure are kept and the failure adds `FAIL usage/routing`; the last 400 lines of `~/.local/share/opencode/log/opencode.log` are scanned for orchestrator `ERROR`s, the last three joined, redacted as an incident is (home, emails, tokens, uuids, `ses_` ids) and cut at 300 (cuts in UTF-16 units). Both print `orch binary` after `node present`. Differences: PE-18 (a missing `node` is a WARN, `needed only by install.sh and uninstall.sh`; an `orch on PATH` row after `orch binary`; `orch self-test` row; `unit tests` runs the Go tests; the JS warns when it finds no binary); each probe stops at 1 MiB of output (PE-20) and runs in a process group of its own (PE-22). |
| `orch watch [--cwd DIR] [--follow] [--interval 20]` | `bin/watch.mjs` | The same dashboard line for line: header (project, clock, usage time, `STALE`), the first two prompt lines, routing (`role=<model>@effort`/`#variant`/`PAUSED`), supervisor, flip, lanes, questions for the human with the reply command (`orch mailbox reply`, PE-19), the human gate, the last 8 messages, incidents of the last 24 h and the `next:` line of `STATE.md`, with the JS value semantics (`undefined` fields, UTF-16 cuts). Reads the usage cache when fresh, else refreshes it. `--follow` clears the screen and redraws every interval (Node's timer clamp: a non-number, below 1 ms or above 2^31-1 ms waits 1 ms), printing a failed render's message to stderr instead, until killed. A routing failure prints `orch watch: <error>` and exits 1 (PE-2). |
| `orch kickoff --brief FILE [--brief FILE2 ...] [--door auto\|claude\|opencode] [--headless] [--unattended] [--cwd DIR] [--dry-run]` | `bin/kickoff.mjs` | The same stdout, stderr, exit codes and files: the usage line (exit 0 with a brief, 2 without), `brief not found: <b> (looked in <root>; cd into the project or pass --cwd <project dir>)` (exit 2), the first two routing-prompt lines after a refreshed `orch usage`, the door (the home door if its vendor is available and not red, else the other; a given `--door` is not validated), both red (`--dry-run`: the message, exit 75; else the preflight below, the prompt written to `.orchestrator/briefs/conductor-kickoff.md`, `orch flip pause --active <home> --brief .orchestrator/briefs/conductor-kickoff.md` with its answer, and the message, exit 75, so the supervisor's relaunch at the reset starts from that brief; when `orch supervisor start` exits non-zero (before the brief) or the pause does, `Both vendors are red, but the supervisor did not start\|the pause was not recorded: nothing will relaunch at the reset. Fix it and run kickoff again.` on stderr, exit 1), the preflight (`orch policy unattended` with `--unattended`, `orch flip gate --clear --no-relaunch`, `supervisor: ` + the first 120 UTF-16 units of `orch supervisor start`'s trimmed answer, `.orchestrator/` in the repository's `info/exclude` (`.git/info/exclude`, or for a `.git` file, the linked worktree's common dir or the submodule's git dir); `--dry-run` skips it and prints `supervisor: not started (dry-run)` instead, so it changes nothing in the project, though its routing read still refreshes the shared usage cache), the `door:` line, then `prompt: ...` (`--dry-run`), a headless `orch worker start ... --detach` conductor lane on `.orchestrator/briefs/conductor-kickoff.md` (the brief is written atomically, 0600, PE-21; the lane's trimmed answer, the watch hint, its exit code or 1), or the interactive `claude --agent conductor <prompt>` / `opencode --prompt <prompt>` in the project root with this terminal (its exit code, 0 after a signal; a missing binary is `Error: spawn <cmd> ENOENT`, exit 1). The orchestrator children are self-execs of `orch`. A routing failure prints `orch kickoff: <error>` and exits 1 (PE-2). A refused gate clear stops the preflight with exit 1 (PE-61). Differences: the captured self-execs stop at 1 MiB of output (PE-20) and run in a process group of their own (PE-22), and PE-61. |
| `orch session beat --door opencode\|claude [--state working\|waiting\|done\|ended] [--now TEXT] [--slice ID] [--progress N/M] [--session ID] [--hook] [--cwd DIR] \| show [--cwd DIR]` | `bin/session.mjs` | Byte-identical stdout, stderr, exit codes and `.orchestrator/sessions/<door>.json` (indent 2; the directory 0700, an existing one this user owns tightened to 0700, the file 0600, written through a private temp file and a rename in both runtimes). `beat` keeps each field not given, clears `--now`, `--slice` or `--session` given as `""` (`--progress` must always be `N/M`), defaults the state to `working` and refreshes `updated_at`; `--now` is cut to 200 UTF-16 units and `--slice` to 40 (a split surrogate pair becomes U+FFFD in both). Bad options exit 2. `--hook` reads a Claude Code hook's JSON on stdin (`session_id`, `cwd`, `hook_event_name` only; never the prompt): `UserPromptSubmit` and `SessionStart` record `working`, `Stop` `waiting`, `SessionEnd` `ended`, any other event writes nothing; it never exits 2 (every failure is one `session beat: ...` line and exit 1; a `cwd` with a NUL character is refused before any filesystem call) and never creates `.orchestrator/`. `show` prints `{door: record}` (indent 2) and refuses to run outside a project. |

Comments citing `bin/*.mjs` or `lib/*.mjs` name the JS at parity reference `9e3a456`
(`go/parity/js-ref`), not files in this checkout; the last JS release is tag `v0.5.0`.

On weekly-only Codex plans, usage and doctor show `5h n/a`; the weekly window alone drives
routing, flip grace, headroom and the remote snapshot. Amber steers and red blocks; unreadable
data stays unknown. See PE-27 for the deliberate post-oracle difference.

Outside a project: `mailbox list|ack|reply|inbox`, `watch` (with or without `--follow`), `flip
status`, `flip gate --clear`, `worker status|stop`, `supervisor status|stop|ack` and `session show`, run where
the project root (`--root`/`ORCHESTRATOR_PROJECT`/the working directory for mailbox, `--cwd` or
the working directory elsewhere) has no `.orchestrator/` directory, print `not an orchestrator
project: <root> has no .orchestrator/ directory; cd into the project or pass <--root|--cwd>
<project dir>` on stderr, exit 2 and write nothing, as the JS does. The commands that create a
project (`mailbox post`, `flip run|pause|gate`, `worker start`, `supervisor start|serve`,
`policy`, `kickoff`, `session beat` without `--hook`) are unchanged. In a project, `mailbox ack|reply` with an id that matches no
message print `no message with id <id> in <root>/.orchestrator/mailbox; if it belongs to another
project, cd there or pass --root <project dir>` and exit 1 (an ambiguous prefix is still a C1
crash).

The hidden `orch json-echo [--indent N]` exists for the parity harness.

### orch remote (Go-only)

`orch remote` is the Mac side of Work Trains, the remote status page. It has **no JS twin** (no
`bin/*.mjs` script) and so no parity group: it uses the standard `flag` package and plain
`encoding/json`. Usage errors exit 2 with the usage text; runtime errors exit 1 with one line
`orch remote: <message>`. It sets up the machine, shows what it would push, and runs the sync loop
with the Worker (`once`, `serve`), which launchd keeps running (`launchd`). Setting up, rotating and
turning off Work Trains is in the [root README](../README.md#remote-status-work-trains) and
[remote/README.md](../remote/README.md).

| Command | Behaviour |
|---|---|
| `orch remote init --url URL [--root DIR]... [--exclude NAME]... [--force]` | Writes the config (refuses to replace it without `--force`) and creates the state file with a random machine id, kept across later `init --force` runs. `--url` must be `https://` (plain `http://` only for `127.0.0.1`, `localhost` or `[::1]`). `--root` defaults to `~/Developer`; `--exclude` takes directory names or absolute paths. Prints the config path, the machine id and the next steps; never a secret. |
| `orch remote status [--json]` | The config, the machine id, whether each Keychain item exists (`yes`/`no`, or `unknown` with the error when the Keychain cannot be asked), the last push, pull and error, and the discovered projects: their count and names, plus each root or directory that could not be examined (`projects`, `project_names`, `discovery_errors` in JSON; `null` without a usable config). Never reads a secret. Exits 0 without a config. A refused config (insecure mode, inside a git work tree, invalid) is shown as `refused: <reason>` (`config_error` in JSON, `null` otherwise) with everything else still reported, and status exits 1. |
| `orch remote push --dry-run [--json]` | Discovers the projects and prints, indented, the exact `PushRequest` a push would send (machine id, the release number from `VERSION` without the build's git revision, the cached budget and one snapshot per project), so the owner can audit what leaves the Mac. With `ci` on it uses the cached CI runs, or asks `gh` for a project whose cache is older than 5 minutes (read only; why a project's CI was left out goes to stderr as `orch remote: ci left out for <name>: <reason>`). It sends nothing and writes nothing, not even the CI it checked. Without `--json` two summary lines come first. A project whose state cannot be read, a root that cannot be scanned, or an unreadable usage registry is reported on stderr (`orch remote: skipped ...`) and left out; the exit is still 0. Needs the config and the machine id (`orch remote init`). Without `--dry-run` it is a usage error: `orch remote once` sends. |
| `orch remote once [--json]` | Runs one cycle of the sync loop (below) and prints `pulled N, applied N, pushed N, deferred N`; `--json` prints `{pulled, applied, pushed, deferred, error}` (`error` null on success). Log lines go to stderr. Exits 0 when the cycle succeeded, 1 otherwise (the error, redacted as in `last_error`, is printed and stored as `last_error`). It holds `serve.lock` for its cycle, so it never overlaps a `serve` cycle (both could pull and apply the same answer): while `serve` runs it exits 1 with `orch remote serve is running; it syncs on its own (pid N)` and sends nothing. Needs the config, the machine id and the three Keychain items; a missing item is named with the command that sets it. |
| `orch remote serve` | Runs the sync loop until SIGINT or SIGTERM, which cancel the HTTP call in flight, save the state and exit 0. Logs one line per change (an applied answer or action, a push, a skipped project, an error, a recovery) to stdout, never one per quiet cycle. Holds `~/.local/state/opencode/orchestrator/remote/serve.lock`: a second `serve` exits 1 with `another orch remote serve is running (pid N)` (also while an `orch remote once` holds it for its cycle; launchd retries 30 s later). The lock is an `flock(2)`, which the kernel drops when its holder dies, so a lock file left by a dead serve is simply taken over. The config is read once at start; restart `serve` after changing it. |
| `orch remote launchd [--label LABEL]` | Prints a LaunchAgent plist (Label `org.opencode-orchestrator.work-trains` unless `--label`; ProgramArguments this `orch`'s absolute path, `remote`, `serve`; `RunAtLoad`, `KeepAlive`, `ThrottleInterval` 30; stdout and stderr to `~/.local/state/opencode/orchestrator/remote/remote.log`, expanded; `PATH` `/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:/usr/local/bin`, for `security` and `gh`), then on stderr, as comments, where to save it and the `launchctl bootstrap gui/$(id -u) '<path>'` / `launchctl bootout gui/$(id -u)/<label>` commands (the path single-quoted for the shell, so a home with spaces or quotes copies whole). It writes and installs nothing. |
| `orch remote token rotate` | Stores a new machine token (32 random bytes, unpadded base64url) in the Keychain and prints only its SHA-256 hex and the `wrangler secret put MACHINE_TOKEN_SHA256` line to run, noting that it deploys the Worker at once. This Mac sends the new token at once, and the Worker accepts it only once `MACHINE_TOKEN_SHA256` holds the new hash, so replace the secret before the next push or pushes get 403. |
| `orch remote token revoke` | Deletes the three Keychain items (absent ones are reported as `not present`, so it is idempotent) and says what to revoke on the Worker side. |
| `orch remote service-token set` | Reads the Access service token from stdin, client id then client secret, one per line (on a terminal it prompts with echo off, and turns echo back on however the prompt ends, Ctrl-C included), checks both values before storing either, and stores both. If the second write fails it says the client id changed and the secret did not, so run it again. Never echoes them. |

Discovery: for each root in the config, the root itself and each direct child directory that holds a
`.orchestrator/` directory, deduplicated by real path and sorted by name. A linked git worktree (a
`.git` file whose git dir has a `commondir`) is skipped unless `include_worktrees`; an `exclude` entry
matches a directory's basename, or its absolute path as found or resolved; the as-found match is
made before the directory is read, so an excluded directory you cannot read is not reported.

What a snapshot carries, read only from the project's `.orchestrator/` (flip.json, the `conductor`
lane, `sessions/*.json`, the mailbox) and the cached usage file (never collected or refreshed by
`orch remote`):

- `key`: the first 32 hex digits of `sha256("work-trains/v1\0" + machine id + "\0" + real path)`;
  `name`: the directory basename. Paths are never sent.
- `state`, the first that applies: `needs_you` (an open blocking question to the human, counted
  over all open questions, not only the 20 sent, or a gate waiting on a question), `failed` (the
  conductor lane `failed`, or `running` with a dead pid), `paused` (a gate without a question,
  which includes a pause from the page, or a budget pause), `working` (a live conductor lane, or a
  `working` heartbeat under 15 minutes old and at most 2 minutes in the future, the clock-skew
  allowance), `done` (the newest heartbeat is `done`), else `idle`; `state_reason` is a short
  sentence. `flip.json`'s `awaiting_human` and `paused` count when JavaScript-truthy, as the
  supervisor reads them (a hand-edited `true` is a gate or a pause). A lane is live when its pid
  answers signal 0, so a pid the system has since given to another process reads as live.
- `door` (the live lane's harness, else `flip.json`'s `to`, else the newest heartbeat's door),
  `run` (`headless`, `interactive` for a heartbeat under 15 minutes old that has not ended, or
  `none`; `since` is the lane's start; `last_activity_at` the latest lane progress, heartbeat or
  message), and `slice`, `now`, `progress` from the newest heartbeat.
- `questions`: the newest 20 questions to `human` or `*` not acked by `human`, newest first; a
  question posted without `recommended` has none and never blocks, except an action question
  (`"ask": "action"`), which carries `ask`, never a recommendation (even one written into its line),
  and blocks as posted. `events`: the newest 10
  messages as `{id, at, kind, from, subject}`, never a body. A message id
  is sent as is, so a message whose id is not a lowercase uuid (the form orch writes) is left out.
- `ci` (when the config's `ci` is true, the default): the newest GitHub Actions run of each
  workflow on the default branch, newest first, at most 5, as `{repo, workflow, status,
  conclusion, url, at}`. For a project whose `git remote get-url origin` is a github.com
  repository (`https://`, `ssh://` or `git@github.com:` form; only `owner/name` is sent), orch runs
  `gh run list --repo owner/name --branch <branch> --limit 10 --json
  workflowName,status,conclusion,url,createdAt` in the project with your own `gh` login; the branch
  is origin's HEAD (`git symbolic-ref refs/remotes/origin/HEAD`), else `main`. GitHub's waiting
  states are sent as status `unknown`, a `startup_failure` as conclusion `failure`, and `stale` as
  `null`; a `url` is sent only when it starts with `https://github.com/` and redaction leaves it
  intact. gh always runs with `GH_HOST=github.com` (an enterprise `GH_HOST` in your environment is
  ignored). The check (both git calls and gh) times out after 10 s, killing gh and anything it
  started, and is cached per project in `state.json` for 5 minutes, failures included, so neither
  `serve`'s 15 s checks nor repeated `once` runs ask gh more often. All checks of one cycle share
  a 20 s budget: a project not checked within it keeps its previous runs (or `ci: []`) and is
  checked next cycle, so hung `gh` calls never delay a cycle by more than 20 s. A project with no
  `origin` (git's exit 2) or that is not inside a git checkout, or whose origin is not on
  github.com, has `ci: []` and nothing to report. Any other failure (git refusing the repository,
  such as a `safe.directory` error, gh not on PATH, not authenticated, a gh error, a timeout,
  unexpected output) also gives
  `ci: []`, never fails a cycle, is logged once while it lasts (`ci left out for <name>:
  <reason>`) and, after a cycle that otherwise succeeded, is the `last_error` note of kind `ci`
  (the first such project; its time changes only when its message does). With `ci` false gh is
  never run and the cached runs are dropped from `state.json`.
- Every free-text field is scrubbed, then redacted, then cut to the schema's cap, ending in `…`
  when cut. The scrub (Work Trains only; `incident.Redact` keeps its JS twin's rules) replaces
  credentials: GitHub (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`), AWS access key ids,
  Slack, Google API, Stripe and npm tokens (matched wherever the prefix starts, even glued to a
  word as in `backup_ghp_...`), JWTs, PEM private key blocks and `Bearer` values become
  placeholders, the value of a credential assignment (`password=`, `token:`, `secret=`,
  `api_key=`, ...) becomes `<redacted>`, and URL user info becomes `<credentials>@`. Any other run
  of `A-Z a-z 0-9 _ -` with a piece (between `-` and `_`) of 24 or more characters mixing letters
  and digits becomes `<token>`: this takes full 40-hex commit SHAs too, while short SHAs, uuids,
  words, slugs and `ses_` ids stay. It also replaces every absolute filesystem path with `<path>`:
  a quoted path (`"..."`, `'...'`, `` `...` ``) whole, spaces included; an unquoted Unix path
  (`/x`, `/x/y`, `file://...`, `~/...`, escaped spaces included), Windows drive path (`C:\x`,
  `C:/x`) or UNC share (`\\server\share`), together with the rest of its clause: everything up to
  the next newline, comma, semicolon, quote, closing bracket or parenthesis, or the end of the text,
  whatever its case, because a final component may hold a space (`see /Volumes/Private client now`
  is sent as `see <path>`). An `http://` or `https://` URL ends the clause early and is kept. A path
  counts only where one can start, so a lone `/`, URLs (`https://host/a/b`), words joined by a slash
  (`and/or`, `w/o`, `1/2`) and relative paths (`go/internal`) stay; the prose swallowed after an
  unquoted path is a loss D11 accepts.
  `incident.Redact` then replaces the home directory, user paths, emails, tokens, uuids and
  session ids.
- `budget`: both vendors' postures from the cached usage reading and the registry's thresholds;
  `null` when there is no cache or it is more than 30 minutes old. Each posture carries both
  windows' resets (`resets_at` for the 5-hour one, `weekly_resets_at`) and `window`, the one that
   drives its level: `weekly` when the weekly window is red or Codex is weekly-only, else `five_hour`. It runs no program: unlike
  `orch routing`, it does not detect opencode's channels (`sqlite3`, `opencode auth list`).

The sync loop (`once` runs one cycle; `serve` repeats them):

1. Pull the answers and actions whose undo window has passed (`GET /api/v1/machine/pull`).
2. Apply each one exactly as `orch mailbox reply`, `flip gate` and the gate's clearing would (an
   answer becomes the human's reply with `via: remote`; a pause sets the human gate; a resume clears
   it and starts the supervisor if none runs). Projects are matched by key from one discovery pass.
3. Ack every outcome in one request. An outcome is acked as it is, whatever error came with it (an
   answer that was posted but whose gate could not be checked is acked `applied`, the error only
   logged). If the ack is lost, the next cycle pulls the same items again; applying them a second
   time changes nothing (the reply carries the answer id; a pause or resume resends the outcome
   its project's action journal recorded, so a pause or resume made on the Mac since stands) and
   they are acked again. An item the apply layer could not settle (a journal or mailbox it could
   not read, a receipt it could not write: `apply.RetryableError`) is not acked: the Worker sends
   it again with the next pull, and its failed step is logged once while it lasts.
4. Push the projects that are due, all in one request: a project is due when its snapshot hash
   differs from the last one the Worker took, its last accepted push is at least 60 s old, and any
   `retry_after` from a `deferred` result has passed (a `retry_after_seconds` outside the
   schema's 1..300 counts as 60). The schema caps a push at 50 projects; any
   beyond that stay due and go 15 s later. `accepted` records the hash and time, `unchanged` the
   hash, `deferred` the retry time. Every push carries the budget, and the Worker stores it only
   on push: when no project is due but the budget differs from the one last pushed (a new
   reading, or `null` once the cached one is over 30 minutes old), the push goes without projects,
   at least 60 s after the last push and never before a first push has landed (a projectless first
   push would end the Worker's first-push backlog window).
5. Save the state (private temp file, then rename), with the cycle's error as `last_error`
   (redacted), else the `ci` note when a project's CI was left out, else none.

`serve` pulls again after `poll_after_seconds` (the Worker's value clamped to 15..300 s; the config's
`poll_seconds` until the first pull) and checks local state every 15 s in between, touching the network
only when a project or the budget is due. After a failed cycle it backs off: a random wait between half the
ceiling and the ceiling (equal jitter, so a retry never comes at once), where the ceiling starts at
5 s and doubles per consecutive failure up to 5 minutes, reset by a good cycle. A project or root that cannot be read is logged once while it lasts and never fails a cycle.

Each request carries `Authorization: Bearer <machine token>`, `CF-Access-Client-Id`,
`CF-Access-Client-Secret` (read from the Keychain once per cycle, and only when the cycle sends
something), `Content-Type: application/json`, `User-Agent: orch/<version>` and
`X-Work-Trains-Machine: <machine id>`, which the Worker ignores while one Mac is the only machine
([one deployment per Mac](../remote/README.md#before-you-start));
a push adds `Idempotency-Key: <sha256 of the body>`. Requests time out after 15 s and redirects are
never followed, nor their `Location` parsed (requests go through the HTTP transport, not an
`http.Client`). Errors are typed: a redirect, or a 2xx/4xx that is not JSON, came from Access, not the
Worker (`access denied ...; check the Access service token (orch remote service-token set) and the
Access Service Auth policy`); a Worker error is `HTTP <code> <error code>` (its free-text message is
dropped, and a code outside the protocol's `Error` enum shows as `unknown`; a `wrong_issuer` adds
`(the Worker expects Access team <expected_issuer>)` when that field is exactly a team domain); a response that breaks
the schema is a protocol error saying how in fixed words (`a field not in the schema`, `a field of
the wrong type`, `a field missing, null or not in the schema`), never which field or value; no
usable response is a network error with a fixed cause (`could not connect`, `timed out`,
`cancelled`, `the host name did not resolve`, `the TLS certificate was not trusted`, `the
connection closed before the response ended`, else `no valid HTTP response`). No error, log line or
state field carries a header value, a request body or text from a response: a response can echo
what was sent, and the parsers quote what they refuse. A pulled answer's or action's id is logged
only when it is a UUID.

The snapshot goldens in `internal/remote/snapshot/testdata/*.golden.json` are built from the fixture
projects under `testdata/projects/` with a fixed clock and machine id; `go test` compares them byte
for byte and `go test ./internal/remote/snapshot -update` rewrites them.
`remote/protocol/test/go-goldens.test.ts` validates every golden against the `PushRequest` schema and
checks that none carries the fixtures' planted secrets.

Files:

- Config: `~/.config/orch/remote.json` (directory 0700, file 0600; override with `ORCH_REMOTE_CONFIG`).
  Fields `url`, `roots`, `exclude`, `include_worktrees` (default false), `ci` (default true),
  `poll_seconds` (15..300, default 30); unknown fields are refused. `url` is https (plain http
  only to 127.0.0.1, localhost or [::1]) with no user info, query or fragment; it may end in a
  slash or carry a base path, under which every route goes. It is deliberately **not** under
  `~/.config/opencode/orchestrator`, which is a symlink into the checkout: orch refuses a config whose
  real path lies inside any git work tree, and one readable or writable by group or others. It holds
  no secrets.
- State: `~/.local/state/opencode/orchestrator/remote/state.json` (0600): `machine_id`, per-project
  `last_hash`, `last_accepted_at` and `retry_after`, `last_push_at`, `last_pull_at`, `last_error`
  (`at`, `kind`: `network`, `access_denied`, `http_status`, `protocol`, `keychain` or `local`, or
  `ci` for the note above, which is not a failure, and a redacted `message`), `last_budget_hash`
  (the sha256 of the budget last pushed; a file without it still loads), and, while `ci` is
  on, `ci`: per project key the last CI check's `checked_at`, `runs` and redacted `note` (empty
  when the project simply has no GitHub CI), kept only for the projects of the last cycle. The file is created
  complete and only if absent (written to a private temp, then hard-linked into place), so
  concurrent `orch remote init` runs all report the one machine id that was stored. A state file
  that does not parse, has unknown fields, a `machine_id` that is not a lowercase UUID v4, or a
  missing or null `projects` (or a null or empty-keyed entry) is refused as corrupt and never
  regenerated: move it aside and run `orch remote init` again.
- Secrets: the login Keychain, service `opencode-orchestrator.work-trains`, accounts `machine-token`,
  `access-client-id` and `access-client-secret`. They are written by feeding the
  `add-generic-password` command line to `security -i` on stdin, so no secret appears in argv (`ps`).
  Values with whitespace, quotes or backslashes are refused.
- `serve.lock` and `remote.log` (the LaunchAgent's output) live next to `state.json`. `remote.log`
  is appended to and never rotated. A quiet cycle logs nothing, so it grows mainly while the Worker
  is unreachable: one line per failed cycle, at most one per 5 minutes once the backoff is at its
  cap (about 300 lines a day). Truncate it by hand, or add a `newsyslog.d` rule, if it matters.
- Not yet verified: a `resume` applied by `serve` may start `orch supervisor start`, which runs
  detached in a session of its own. Whether launchd still ends it when it stops or restarts the
  agent (launchd may kill the job's whole process group unless the plist sets
  `AbandonProcessGroup`) has not been tested: the end-to-end test runs `once` without launchd. If
  the supervisor dies with `serve`, the plist needs `AbandonProcessGroup`.

The `claude-exec`, `codex-exec`, `opencode-exec`, `worker start` and `supervisor` rows above
also differ from JS by PE-58: when setting a child's directory, orch replaces its `PWD`.
References above to the whole inherited environment exclude that normalized `PWD`.

## Ownership primitive (S13a; not yet used by commands)

`internal/ownlock` provides non-blocking `Acquire`, non-acquiring `Probe`, idempotent
`Release` and blocking `Transaction` using POSIX record locks on regular files opened
without following the final symlink. Kernel locks, not decimal PID metadata, determine
ownership; process death releases them. An inode-keyed process registry excludes local
owners and transactions, handles hard-link aliases, and prevents package-internal closes
from dropping another owner's lock. Acquisition rechecks pathname identity, retrying at
most eight times; release refuses an already replaced pathname with an error and anchors
relative paths at acquisition, independently of later working-directory changes. Probe
re-resolves a pending reservation that ends without acquiring. Transactions retain their
lock file and release on callback errors or panics. This is a cooperative protocol: all
same-process access to lock inodes must use this package (even closing an unrelated read
descriptor drops POSIX locks); callers must not rename/unlink active lock paths, and
transaction data must be a separate file. Identity checking cannot make check-and-unlink
atomic against an unrelated replacer. `orch worker` lanes use it (S13b: PE-23, PE-28,
PE-29), and so do `orch supervisor`'s lock and every reader of a lane or supervisor lock
(S13c: PE-30), flip writers (S13d: PE-32) and supervisor acks (S13f: PE-39).

## Build

Go 1.25+, no third-party dependencies, no cgo.

```sh
cd go
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/orch ./cmd/orch
# dist/orch_<version>_<os>_<arch> for darwin/linux x arm64/amd64 + checksums.txt
./scripts/build-all.sh
```

Linux binaries are fully static. macOS binaries built with cgo off still link `libSystem`, which
the Go runtime requires on darwin; that is as static as a macOS binary gets. `bin/` and `dist/` are
gitignored.

The release number lives in `internal/version/VERSION` (embedded). `orch version` appends the
VCS revision Go records at build time, e.g. `0.6.0 (5ae996b)` or `0.6.0 (5ae996b+dirty)`; a build
outside a git checkout (a source tarball or `git archive`) records none and prints `0.6.0`.

## Install

`../install.sh` builds `orch` from the checkout when Go 1.25+ is on `PATH` (`CGO_ENABLED=0 go build
-trimpath -ldflags="-s -w" ./cmd/orch`), checks that `orch version` matches
`internal/version/VERSION`, and places it at `~/.local/bin/orch`, recorded in the install receipt.
`ORCH_BIN_DIR` (absolute) chooses another directory; `--skip-binary` requires an existing executable
`orch` (`ORCH_BIN`, else `~/.local/bin/orch`, else `PATH`) whose `version` succeeds, before any
mutation. Its receipt records no binary and uninstall leaves that existing binary alone.

Without Go it downloads, with `curl`, `orch_<VERSION>_<darwin|linux>_<arm64|amd64>` and
`checksums.txt` from
`https://github.com/youthtrouble/opencode-orchestrator/releases/download/v<VERSION>` (override the
base with `ORCH_RELEASE_BASE_URL`, which must be `https://`), and places the asset only when its
sha256 matches its `checksums.txt` entry. That is an integrity check only: the checksums come from
the same release and nothing is signed.

The installer refuses before any change when `orch` already exists at the destination;
`ORCH_BIN_DIR` is relative, inside the config, checkout or installer state, or on another
filesystem than the installer state; neither `go` nor `curl` is available; or there is no release
asset for the platform. A failed download or checksum leaves a recoverable `preparing` receipt.
If the directory is not on `PATH`, the plugin still finds `~/.local/bin/orch`; other locations
need `ORCH_BIN=<path>`. Uninstall moves a recorded `orch` into its archive; see
[orchestrator/README.md](../orchestrator/README.md#install-update-uninstall-and-recovery) for
uninstall, upgrading from 0.4.x and recovery.

Without a checkout, the last published release (not this unreleased checkout):

```sh
go install github.com/youthtrouble/opencode-orchestrator/go/cmd/orch@v0.5.0
```

The module lives in `go/`, so its tags are named `go/vX.Y.Z`, but `go install` takes the bare
version. That binary lands in `$(go env GOBIN)`, or `~/go/bin` when it is unset; the plugin finds
it only through `ORCH_BIN` or `PATH`.

## Plugin runtime

`plugins/orchestrator.ts` runs every tool and hook through `orch` and never loads the removed JS:
routing, effort, the budget gate, the mailbox, manifests and incidents are `orch` subcommands, and
delegate, the workers, the supervisor and the usage-limit flip spawn `orch <script>` with the
arguments the script gets.

- **Runtime:** `go` is the default and only accepted value (trimmed, case-insensitive).
  `ORCH_RUNTIME=js` or a `runtime: "js"` option is a configuration error naming its removal in
  0.6.0 and telling you to remove the option or variable. For a service previously set to JS,
  run `opencode service unset env ORCH_RUNTIME`, then `opencode service restart`.
- **Plugin options (known limitation):** the plugin also reads `runtime` and `binary` options, and
  an option wins over the environment. In the S10 live check, object-form options in a project
  `opencode.jsonc` (`[{ "package": "./plugins/orchestrator.ts", "options": { "runtime": "go" } }]`)
  did not reach the plugin: the js runtime answered and `opencode plugin list` showed no plugins.
  This is probably an upstream opencode issue; use `ORCH_BIN` until it is
  resolved.
- **Binary:** the option `binary` (a leading `~` is expanded), then `ORCH_BIN`, then
  `~/.local/bin/orch`, then each `PATH` entry. An explicit `binary` or `ORCH_BIN` that is not an
  executable file is an error, never skipped. The binary is resolved once when the plugin loads, so
  reload opencode after installing or moving `orch`. In go mode `orch` reads the registry
  `ORCH_REGISTRY`, else `$ORCH_HOME/models.json`, else the installed `models.json`.
  A relative `ORCH_BIN`, `PATH` entry, `ORCH_HOME` or `ORCH_REGISTRY` is made absolute against the
  plugin's working directory when it loads; a bare `ORCH_BIN=orch` therefore means `./orch`, not a
  `PATH` lookup.
- **Failure policy:** fail loudly, never fall back to JS. With an unknown runtime or no binary the
  plugin still loads, every `orch_*` tool returns the configuration error (`orch binary not found
  (looked in: ...)`), and the hooks degrade as when usage is unavailable (no budget block, no
  subagent gate). The `execute.after` incident for that error cannot be recorded.
- **Go-mode deviations** (accepted in the S10 plan):
  - D-S10-1: `manifest_create` with no paths is a tool error (JS writes an empty manifest).
  - D-S10-2: an `execute.after` incident without a known project directory names the plugin's
    working directory as its project (JS records none).
  - D-S10-3 (retired by H12): incident context values now travel as JSON on stdin
    (`orch incident record --request -`), so values are no longer cut at a second `=`. Go mode
    therefore needs an `orch` that has `incident record --request -` (this release or later).
  - D-S10-4: free text beginning with `--` that would travel as an argument is refused before
    `orch` runs (mailbox and manifest text); an incident's text is exempt, since the whole
    incident goes on stdin, as are bodies and details.
  - D-S10-5 (PE-4): an incident's `version` is the `orch` version, not the checkout's HEAD.
  - D-S10-6 (PE-11): an aborted or timed-out delegate shows the Go bridge's FAILED result instead
    of the stderr tail.
  - D-S10-7 (both runtimes, a fix): a command that cannot be started is a failed run (in go mode
    an `orch <sub>: could not start ...` tool error) instead of an uncaught crash of the host.
  - D-S10-8: a `mailbox_post` question without `recommended` fails with orch's usage line
    (`orch mailbox post exited 2: mailbox post: a question needs --recommended ...`); JS mode
    says `a question needs a recommended answer`. Go mode needs an `orch` from this release or
    later: questions pass `mailbox post --recommended/--blocking`, and the conductor heartbeat runs
    `orch session beat`. An older binary fails both (a tool error; a recorded incident per beat).
- **Rollback:** JS selection is not a rollback; it fails. Restoring JS requires restoring a
  pre-0.6.0 checkout and restarting the service under the human cutover/rollback runbook.
- **Which runtime answered:** `orch_usage_status` metadata carries `runtime` and `orch` (the binary
  path); `runtime` is `go`. A new incident's `version` is the `orch` version (`0.6.0 (<rev>)`, or
  a bare `0.6.0` for a binary built outside a checkout).

`../tests/plugin-routing.test.mjs` and `../tests/plugin-tools.test.mjs` drive the plugin through
a fake opencode context and a fake `orch`, with pinned routing documents and argv expectations.
The former JS-vs-Go plugin differential is deleted; the command parity harness remains.

## Release checklist (maintainer, human-gated)

Tagging and publishing are a human gate: no agent runs these steps.

For 0.6.0, complete the human cutover and live smoke before release; deferred release-blocking
findings must be closed by cross-vendor review. Keep `## 0.6.0 — unreleased` until publishing.
The steps below use `X.Y.Z = 0.6.0` (0.5.0 was released this way on 2026-10-19).

1. Bump `internal/version/VERSION` to `X.Y.Z`.
2. Date the release heading in `../CHANGELOG.md` (`## X.Y.Z — <YYYY-MM-DD>`).
3. On a clean tree, run `./scripts/build-all.sh`: it writes the four `dist/orch_X.Y.Z_*` assets
   (darwin and linux, arm64 and amd64) and `dist/checksums.txt`.
4. Tag the commit `vX.Y.Z` (the installer's download path) and `go/vX.Y.Z` (the Go module tag,
   since the module lives in `go/`).
5. Publish a GitHub release for `vX.Y.Z` with the four `orch_X.Y.Z_*` assets and `checksums.txt`.
   The installer fetches
   `https://github.com/youthtrouble/opencode-orchestrator/releases/download/vX.Y.Z/<asset>`.
6. In a throwaway HOME with no `go` on `PATH`, check that `./install.sh` downloads `orch`, verifies
   its checksum and places it; then check that
   `go install github.com/youthtrouble/opencode-orchestrator/go/cmd/orch@vX.Y.Z` works (the bare
   version, not the `go/` tag name).
7. Reinstall into your own live config (`./uninstall.sh` then `./install.sh`; see "Upgrading from
   0.4.x" in [orchestrator/README.md](../orchestrator/README.md#install-update-uninstall-and-recovery))
   yourself; it is not part of any automated run.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `ORCH_HOME` | `~/.config/opencode/orchestrator` | The orchestrator tree (the JS `ROOT`). |
| `ORCH_REGISTRY` | `$ORCH_HOME/models.json` | The model registry. |
| `ORCH_REMOTE_CONFIG` | `~/.config/orch/remote.json` | The `orch remote` config file. |
| `ORCH_NO_POST_RUN_REFRESH` | unset | `1` stops `orch claude-exec` and `orch codex-exec` from starting their detached post-run usage refresh (PE-12). Set only by the parity harness. |
| `ORCH_ARTIFACT_ROOT` | child cwd | Bridge project root for run records and incident project attribution; `--artifact-root DIR` wins (PE-42). |
| `ORCH_JS` | unset | Parity only: the JS `orchestrator/` tree `parity/run.sh` compares against. Set, it wins over `ORCH_JS_REF` and nothing is extracted. |
| `ORCH_JS_REF` | the commit in `go/parity/js-ref` | Parity only: the commit-ish (e.g. `v0.5.0`) whose `orchestrator/` `parity/run.sh` extracts from git when `ORCH_JS` is unset. |
| `HOME` | OS user home if unset/empty | Per-user state lives in `~/.local/state/opencode/orchestrator`. `orch incident` keeps an explicitly empty `HOME` as Node's `os.homedir()` does (log relative to the working directory). |

## Checks

```sh
cd go
go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...
go vet -tags parity ./parity
# chosen groups; ./parity/run.sh runs them all
./parity/run.sh --only \
  json,usage,mailbox,manifest,incident,policy,bridges,worker,flip,supervisor,doctor,watch,kickoff,session,jsref
```

The unit tests need no JS tree. The parity reference is the pre-S12 JS: `orchestrator/` at the
commit in `go/parity/js-ref` (`9e3a456`), extracted from git by `parity/run.sh` (see
[Parity harness](#parity-harness)).

`.github/workflows/go.yml` runs build, vet, gofmt, tests and the `json`, `mailbox`, `manifest`,
`incident`, `policy`, `bridges`, `worker`, `flip`, `supervisor`, `doctor`, `watch`, `kickoff` and `session`
parity groups and `jsref`
on macOS and Linux with `CGO_ENABLED=0` and Node 26 installed; it fetches the pinned commit into its
shallow checkout first. Malformed prompt byte tests also compare
against Node when it is available; fixed byte expectations run even without Node.

## Parity harness

Use Node 26 (`referenceNodeMajor`), as CI does: on Node 22 or 24 one case reports SKIP by
design (PE-5).

The JS side is the reference, not the checkout: `parity/run.sh` extracts `orchestrator/` at the commit
in `go/parity/js-ref` (`9e3a456`, the last main commit before S12, the JS the port was checked
against) with `git archive` into a temp directory, sets `ORCH_JS` to it and removes it on exit. It is
a full SHA, not the `v0.5.0` tag, because the JS changed after the tag (`git diff v0.5.0 9e3a456 --
orchestrator/` is 34 files) and the Go port follows the later JS. `ORCH_JS` (a JS tree) or
`ORCH_JS_REF` (any commit-ish) override it. A shallow clone lacks the commit; fetch it with
`git fetch --no-tags --depth=1 origin "$(cat go/parity/js-ref)"` (on a full clone, drop `--depth=1`).
Without `--only`, `run.sh` also runs `TestJSReference`, which checks the texts the unit tests pin as
literals (the flip brief, the exec help headers, kickoff's prompt, usage and watch hint, and
`runrecord.Write` against `lib/run-record.mjs`) against that JS source. With `--only`, name `jsref`
to run it: `--only jsref` runs it alone, `--only json,jsref` runs it after the `json` group (two
`go test` invocations; `run.sh` exits with the first non-zero status).

`parity/run.sh` builds `bin/orch`, then runs `go test -tags parity ./parity`. Each case runs the
JS and `orch` with the same argv-equivalent and stdin, each in a fresh temp `HOME` and a fresh
project directory containing `.git/`, with an environment of only `HOME`, `PATH`, `ORCH_HOME` and
`ORCH_REGISTRY` (plus `ORCH_NO_POST_RUN_REFRESH=1` in the bridge, worker, flip and kickoff cases,
PE-12). `PATH` is `parity/fakes` (stand-ins for `claude`, `codex`, `opencode` and `security` that
fail unless a case scripts them, so no case reads the real keychain or calls a real endpoint, and
for `which`, `caffeinate` and `systemd-inhibit`, so no case reaches the real keep-awake tools),
then a private directory holding only links to `node` and `sqlite3` (S4/R10: nothing else installed
beside them, an `orch` included, can change a case). Cleanup signals refuse pids below 2 (S3/R13).
Exit codes, stdout,
stderr and every file under the project `.orchestrator/`, the HOME state dir and the fakes'
`$HOME/fake-log/` must match.

Groups:

- `json`: JSON.stringify compatibility over `testdata/json/*.json` and the reference's
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
  `testdata/jssort/gen.mjs` that must reproduce `orders.json` on Node 26 or later. PE-25:
  `record --request -` runs `orch` only (a full object, defaults, a missing summary, invalid JSON, a
  conflicting flag), and its line and receipt are compared with the JS `record` given the same
  fields as flags. Normalised: a new
  entry's `at` and `version` (PE-4), its `project` when it is the sandbox's temp directory name, the
  receipt's
  `at`, the bundle's `generated_at` and the HOME prefix of the log path in the usage line.
- `bridges`: each bridge against its script, with the parity fakes scripted per case. The fakes
  use POSIX shell builtins only (CI's `/bin/sh` is dash) and log under `$HOME/fake-log/`, which is
  compared as files.
  - `claude`: `orch claude-exec` against `bin/claude-exec.mjs`, with `parity/fakes/claude`
    scripted by `$HOME/fake-claude/case` (files the fake prints as stdout and stderr, its exit
    code, or a signal it kills itself with or a sleep; it logs its argv, one argument per line,
    to `claude-argv.txt`). It replays every fixture in `testdata/claude/stream-json/` and covers
    every flag, the four prompt sources and their file errors, `--help`, `--quiet`, the gate (red
    refusal, `--force`, a fable model against a red Fable weekly window, unknown usage, a codex
    that cannot be spawned), the usage-limit path with a scripted refresh and with a failing one,
    a claude killed by a signal, a stderr tail of more than 3 lines, the timeout (PE-9), each
    trailing bare value key, the crashes (bare `--cwd`, `--json-schema` and `--add-dir`, a missing
    schema file, spawn ENOENT and EACCES, a missing `--cwd`, a null event or block, non-iterable
    content, non-string text or result, bare `--resume` on the normal path, a NUL byte in the
    prompt), odd event shapes, events holding raw U+2028 and U+2029, and PE-8 and PE-1 (the three
    cuts inside an emoji) as asserted differences. Only the usage-limit cases script the codex
    app-server fake.
  - `codex`: `orch codex-exec` against `bin/codex-exec.mjs`, with the `exec` mode of
    `parity/fakes/codex` scripted by `$HOME/fake-codex-exec/case` (as the claude fake, plus
    `<case>.nostdin`; it logs `codex-argv.txt`, `codex-cwd.txt`, `codex-env.txt` and the prompt
    it reads on stdin, `codex-stdin.txt`). It covers every flag at once, the default sandbox (the
    argv log shows `--approve-for-me` without `-s`), `read-only`, `danger-full-access`,
    `--resume`, `--resume` with `--add-dir` (before `resume`) and `--schema` (after it), empty
    values, unknown keys, `--quiet`, `--help`, the prompt sources and their file errors, the gate
    (red refusal, `--force`, unknown usage, Anthropic red only, a codex app-server that cannot be
    spawned), a codex killed by a signal, a stderr tail, no output, the timeout (PE-9), a limit
    word in the errors, each trailing bare value key, the crashes (bare `--model`, `--cwd`,
    `--schema` and `--add-dir`, spawn ENOENT and EACCES, a missing `--cwd`, a null event or
    change, non-string text or command, non-iterable or string changes, a bare `--resume` without
    a `thread.started`), odd event shapes (a `thread.started` without an id, then with one;
    commands and changes with absent fields; `turn.failed` with a string or no error; top-level
    `error` events), raw U+2028, the tool-host no-op path, `CLAUDECODE` reaching codex, and PE-8,
    PE-13 and PE-1 (the three cuts inside an emoji) as asserted differences.
  - `opencode`: `orch opencode-exec` against `bin/opencode-exec.mjs`, with the `run` mode of
    `parity/fakes/opencode` scripted by `$HOME/fake-opencode-run/case` (as the claude fake; it
    logs `opencode-argv.txt`, `opencode-cwd.txt` and `opencode-env.txt`). It covers the default
    agent and every flag, an ignored `--help`, `--effort X` becoming the prompt, `--quiet`,
    `--force`, unknown keys, the prompt sources and their file errors, the gate, a non-zero exit
    with and without stderr, a signal, the silent timeout (PE-9), the session and text rules, odd
    part shapes, raw U+2028, the crashes (a null event, non-string text also under `--quiet`,
    spawn ENOENT and EACCES, a missing `--cwd`, a bare `--cwd` or `--resume`, a NUL byte in a
    stdin prompt), `CLAUDECODE` reaching opencode, and PE-8 and PE-1 (`prompt_head`) as asserted
    differences.
  - `split-utf8`: each of the three bridges with a CLI whose stderr splits a character across
    two pipe reads (`split-character`) or ends inside one (`ends-inside-character`). The fakes
    write the first line of `<case>.stderr-head` to stderr without its newline, sleep 0.2 s, then
    write `<case>.stderr`; before the byte comparison the JS output, run record and incident must
    hold the whole character or a single U+FFFD.

  Every bridge case seeds a fresh green usage cache unless it needs another (both vendors red,
  unknown, Fable red, or Anthropic red with OpenAI green); no codex or opencode case scripts the
  codex app-server fake, so the JS post-run refresh meets the unscripted one. Normalised:
  `started_at`, `duration_ms`, the run-record stamp (in paths and file names), the seconds in the
  `done` line (claude and codex; opencode has none), the sandbox project path (`$PROJECT`), a new
  incident's `at`, `version` and temp-directory `project`, and a re-collected cache's
  `generated_at`. There are no codex or opencode captures yet: those subgroups script every
  event inline.
- `policy`: every `orch policy` command against `bin/policy.mjs` over override files of every
  shape (none, unparsable, BOM, `null`, string, array, own `__proto__`, a directory), `--cwd` in
  every position, refusals and `force_push`, the TypeErrors of a primitive `policy.json`, and
  `show`/`set` against a copy of the JS tree whose `models.json` is missing, a directory, `null`,
  unparsable, BOM-prefixed or of another shape (orch reads the same file through
  `ORCH_REGISTRY`). Nothing is normalised.
- `worker`: `orch worker start|status|stop` against `bin/worker.mjs`. `status` over no
  `.orchestrator/`, an empty `workers/`, lane order (`b a 1 10 2`, other files ignored), pids that
  are live (the
  test process), dead (a reaped child), a string, `true`, `-1`, `1.5`, an array, an object,
  beyond int32 or missing, a lock with and without a state, states that are unparsable, `null`,
  `0`, `""`, a string, a number, an array or already hold `process_alive`/`lock_present`, a
  directory `x.json`, `--lane` given, empty, bare, `..` or with a `/` (`../flip`, `/etc/hosts`:
  refused), `workers` as a regular file (C1), a bare and a relative `--cwd`. Status only sends
  signal 0. `stop` over no state, a dead pid, a falsy state, an empty lane, the lane `../flip`
  (refused, `flip.json` and `flip.lock` untouched), and live victims each runtime's seed starts
  (a helper process holding the lane's kernel lock with its own pid, as a runner does, as its
  own group leader or not, the pid stored as a number or a string, under the lane given, bare
  or absent), each required to die of SIGINT. The PE-14 pids (`1`, `true`, `"1"`, `[1]`, `-1`,
  `-12345`, `"0"`, `[]`, minus a victim's pid, each with a lock file holding the pid as
  `process.kill` coerces it) and the PE-23 cases (a live `/bin/sleep` recorded as the state's
  pid with no lock, with a lock file naming a dead pid, with a lock file naming the victim that
  no process holds, or with the lock held by another helper) run `orch` only, never the JS, and
  require the victim and the test process to survive. Normalised: the sandbox project path and,
  for victim cases, the stored `pid` and the lock's pid. A live lock holder in any parity case
  is such a helper (contract CA2); a lock file with pid text alone models a stale lock.
  Foreground `start` runs each runtime's own bridge (`node <harness>-exec.mjs` for the JS,
  `orch <harness>-exec` for `orch`) over the bridges' scripted fakes, with
  `ORCH_NO_POST_RUN_REFRESH=1`: claude finished (the `ok` capture; the fake's argv log holds the
  preamble byte for byte), the `conductor` lane (the brief alone), permission denials, failed
  (`bad-model`, an exit with stderr, a `rate_limited` result: a limit incident), deferred by a red
  cache (the gate's refusal: two incidents, no `last_progress_at`) and by the usage-limit path
  (exit 75 with `resets_at`), every passthrough option, bare `--model`, `--resume` and `--lane`
  (lane `true` posting from `true`), final messages over 2000 units, an array and a number (C2
  after the relay: the state stays `running`, the lock is removed), and a bridge that dies on a
  `null` event (`bridge produced no result`); codex with its `thread` and `ran:` lines, the
  default and a read-only sandbox, `--agent` not passed; opencode with `--agent` and with
  `--effort X`; the validation refusals (a lane with `/` among them, with and without `--detach`; an
  unknown harness is reported first), a bare `--brief` or `--cwd` (C1) and a brief that is a
  directory (C1 EISDIR read before the lane is claimed: no lock and no state, and with the lane
  locked the read error still wins, exit 1 not 3); the lane locked by a helper holding its
  kernel lock, with previous states naming a live or dead pid, absent, `null`, `0`, a string or
  without `status` (PE-28: orch's hint is rewritten to the JS's for the comparison); PE-28 as an
  asserted difference (a lock file naming a reaped pid that no process holds: the JS refuses the
  lane, pinned, `orch` runs to `finished` and removes the lock); a state path that
  is a directory (the final line; PE-21 as an asserted difference: the JS leaves `b.json.tmp`, orch no temp); a lane written by one runtime's `start`
  read by both runtimes' `status`; PE-24 as an asserted difference (claude exits 1 with a
  synthetic credential on stderr: the JS state and blocked message hold it, `orch`'s hold
  `<token>`; the rest, run record and incident included, is compared); PE-13 as an asserted difference (opencode with `--effort X`
  and a 1 MiB brief: `orch` finishes, the JS dies of `Error: write EPIPE` leaving its lock and a
  `running` state). Normalised besides the
  bridges' fresh values: the state's `pid`, `worker_pid`, `run_id`, `updated_at` and
  `last_progress_at`, the lock's pid, mailbox `id` and `at`, the run record's `prompt_sha256`
  (the prompt names the sandbox; the fake's logs compare the prompt itself), the run file stamp
  in the blocked message's ref and in the incident detail, and, only in the cases marked as a
  bridge crash, Node's crash block, which a JS bridge's stderr carries into the JS worker's
  stdout, reduced to its error line (the line `orch`'s bridge prints alone): a `file://` line,
  the source line and its caret line, a blank line, the error line, its `    at` lines and a
  blank line to `Node.js v<version>`; a subtest of the group pins that shape.
  `start --detach` cases are multi-step: the launcher runs, then (for some cases) steps run while
  the runner runs, then the harness settles the runner (polls until `runner_pid` is gone, at most
  30 s; on a timeout it kills the runner's process group and fails the case) and collects the
  files again. Cases: the claude `ok` capture without keep-awake (the fake `which` finds nothing:
  `keep_awake: false`) and with it (`$HOME/fake-which/caffeinate` on macOS, `systemd-inhibit` on
  Linux: `keep_awake: true` and the wrapper's argv log); the conductor launch `flip.mjs` makes;
  `--detach` repeated, and as `--lane`'s value (the launcher's lane is `--detach`, the runner's
  `--lane` takes `--harness` and the runner logs the usage line); a launch with `--cwd sub` from
the sandbox, with and without keep-awake (the runner must work in `sub/`, not `sub/sub/`, and the
wrapper's argv log ends `--cwd <root>`); a lock held by a helper process (with
  and without a state: exit 3, the lane-locked JSON without `hint` on stderr, empty stdout, no log,
  as `flip.mjs` expects), and lock files naming a dead pid and holding `abc` that no process
  holds (the launch proceeds; the JS launcher removes the file, `orch`'s runner reclaims it,
  PE-29); and a
  worker stopped while its bridge runs the sleeping fake claude: launch, wait for the state's
  `running`, `worker_pid` and `session_id`, `status` (`process_alive` and `lock_present` true),
  `stop` (only once the state's pid is the launched runner), settle. Its PE-11 differences are
  asserted per runtime (state `exit_code`, `errors` and `record`; the log's final lines; the
  blocked message's body and refs; the incidents; `orch`'s run record) and the rest is compared;
  the fake claude must have been signalled away. Each runtime's log must be 0600 and its state must
  name the launch's `run_id` and `log_path`. Normalised besides the start cases' values: the run
  id (also in the log's name, as `<lane>-<run>.log`), `runner_pid`, and the runner's command in a
  wrapper's argv log (`<node> <ORCH_JS>/bin/worker.mjs` and `<orch> worker` become `<runner>`).
  The fakes `which`, `caffeinate` and `systemd-inhibit` (shell builtins only) keep every case off
  the real keep-awake tools: `which NAME` exits 0 only when a case created `$HOME/fake-which/NAME`.
  Both runtimes decode the relayed stderr as a UTF-8 stream; read sizes are up to the pipe in
  both, so each case relays at most one `tool:`/`ran:` line and the unit tests pin the per-chunk
  rules.
- `flip`: every `orch flip` command against `bin/flip.mjs`, always in the sandbox project. Usage
  errors (no or an unknown command, a bare `--cwd` with and without a command, C1) and a
  relative `--cwd`; `status` over no `.orchestrator/`, a run
  record, `0`, `false`, `null`, `""`, a string, an array, a number, unparsable, BOM-prefixed and
  malformed UTF-8 files, a directory and `__proto__`/`constructor` keys; `pause` fresh, over kept
  keys (`paused` in place, `awaiting_human` kept), `--active`, `--reason`, both bare, a string,
  array, number and prototype-key record, `.orchestrator` a regular file (C1) and `flip.json` a
  directory (PE-32: orch refuses unreadable state; the JS reaches its failed rename and leaves
  `flip.json.tmp`), and a mailbox directory that is a
  file (no message, same output); `gate` fresh, deleting `paused`, appending and keeping the
  gate's position, `--question` given and bare, a reason over 80 units, a bare `--reason` (PE-35 usage refusal; the frozen JS posts no
  message), `--active` given and bare, primitive states `5`, `1.5`, `"abc"` and `true` (PE-34: orch gates, the JS throws), an
  array replaced by the gate, `.orchestrator` a file (C1), and `--clear` over a gate, no gate,
  nothing, a string, a number, an array, `null` and a directory (PE-32, as for `pause`); PE-1
  (the subject cut inside an emoji) as an asserted difference.
  `decide` runs over fresh usage caches built when the group starts (`resets_at` relative to
  that time; no case collects, and each requires the seeded `usage.json` left as it was):
  both green, OpenAI red, both red, unknown, Anthropic red, OpenAI stably green (its window
  reset 180 minutes ago), freshly reset (3 minutes ago) and amber, the plain green fixture
  (its 2099 resets read as freshly reset); `flip.json` absent, `at` 2 and 30 minutes ago,
  unparsable, `null`, a string, an array, a future `2030`, `garbage` and a `paused` record
  without `at`; a human gate with and without a reason, over a both-red cache (gate, not pause), `awaiting_human: false` and an array holding a gate (both routed as usual); `--active` absent, empty, bare, `gemini`, `constructor` and `__proto__`. Each
  case also pins the decision printed; unreadable state gates under PE-32 and a bare active is
  a PE-35 usage refusal. PE-15 is asserted (a legacy local date two minutes ago).
  Cross-runtime: `flip.json` written by each runtime's `pause` and `gate` is read by both
  runtimes' `status` and `gate --clear`.
  `run` cases never reach a model: each sandbox's `PATH` holds the fakes first and a fresh
  cache. Pending launches have a helper process hold the conductor lane's lock with its own pid
  (only ever sent signal 0 by the JS), so both launchers refuse and nothing runs; each such case requires the
  pending answer from both runtimes except PE-32/35 refusals. Through that path: `--to` absent, `gemini` or bare and a
  bare `--cwd` (C1), with nothing written; a seeded `conductor.json`; `--reason` and `--resume`
  carried into `pending_launch`; bare `--from`, `--reason` and `--resume`; `--force`; a mailbox
  that is a file; `previous` over a run record, a `paused`-only record, a record whose gate and
  pause are cleared, a string, an array, `0`, `null`, unparsable and `{"at":5,"to":true}`;
  `.git/info/exclude` empty, `foo`, `foo\n`, `foo\r`, holding `.orchestrator` or
  `.orchestrator/`, CRLF lines (recognised), CR and U+2028 lines, ` .orchestrator/` and
  `.orchestrator//` (appended), malformed UTF-8 at the end, no `.git`, and a `.git` file that
  is malformed or names a missing git dir (nothing written); a linked-worktree layout (a relative
  `gitdir:` whose `commondir` is `../..`: the common dir's exclude is appended). Crashes before
  the launch (C1): the exclude a directory (EISDIR read); the brief
  a directory (after the exclude; the JS fails at open, orch at the rename of
  `<file>.tmp-<random>`, PE-21). A directory `flip.json` is unreadable state (PE-32): orch
  refuses before the exclude or brief, the JS leaves `flip.json.tmp` after its failed rename. Full launches
  run the conductor worker to its end against the fakes and settle its runner as the detached
  worker cases do: claude, opencode handing back with a reason (no `--permission-mode`), claude
  with `--resume`, claude with `--force` over an Anthropic-red cache (finished) and without it
  (deferred), and a stale lock (a reaped pid) the JS launcher removes and `orch`'s runner reclaims (PE-29); the `launcher` line quoted
  in the answer must be whole (under 400 units) and is parsed as the launch. PE-2 is asserted
  for a launcher that dies because `.orchestrator/logs` is a file. Cross-runtime: a
  `pending_launch` written by each runtime's `run` is read by both runtimes' `status`.
  Normalised: the sandbox project path; an ISO `at` in `flip.json` (any record's, `paused`,
  `awaiting_human`, `pending_launch` or `previous`) that is not earlier than the moment the case
  was built, so seeded and previously written timestamps are compared verbatim; a new mailbox
  line's `id` and `at`; and, for full launches, the worker cases' values, with `runner_pid` also
  in its JSON-escaped form inside `launcher`. Only an unseeded, empty `flip.json.lock` is
  masked for writer cases, including full launches and kickoff writes (PE-32); status and
  dry runs retain all files in the comparison. PE-33 pins the frozen JS clearing a gate
  despite `--unless-gated`, while orch refuses without writing or launching.
- `supervisor` (S8a part): `orch supervisor status|stop|ack` against `bin/supervisor.mjs`, always
  in the sandbox project. Usage: no command (exit 2), `foo` (nothing, exit 0), a bare `--cwd`
  with and without a command (C1) and a relative `--cwd`; `status` over no `.orchestrator/`,
  `{}`, a realistic state (events of every status, an index-like lane `"1"`, `checked_at`,
  `pid`, `status`, a stale `stopped_at`, `last_flip_check`, `last_relaunch_at`,
  `awaiting_human_logged`), `null`, `5`, `"x"`, `[]`, unparsable, BOM-prefixed and a directory;
  `stop` fresh, over an existing stop file, `.orchestrator` a file and the stop file a directory
  (C1); `ack` of a known id (new acks file), an unknown id, a bare and no `--event`, no state
  file, `--by X` and a bare `--by`, an existing ack for the same id (position kept) and for
  another id, `constructor`, `__proto__` with and without an own key, an event stored as `0`,
  state `null` with and without `--event` (C1 with), acks `null` and `5` (C1), unparsable acks
  and an acks directory (C1, the rename line, with PE-21 asserted). PE-16(4) (`--event length` over events `"ab"`)
  and PE-16(2) (array acks, `--event 0`) are asserted differences. Cross-runtime: acks written
  by each runtime's `ack` are extended by both runtimes' `ack` of another id. Normalised: the
  sandbox project path and an `at` written by the run (seeded ones are compared verbatim).
  `serve` (S8c part 1) runs as a daemon in each runtime's sandbox: the harness waits until the
  lock names the started process (at most 10 s), lets it tick once for 1 s, writes the stop
  file and waits for the exit (at most 20 s; on timeout it kills the pid it started and fails).
  Cases: no workers, a realistic state, a stale lock, a restart over a stopped state with a
  `storage_error`; lanes finished (command channel exit 0 and 3), failed, interrupted,
  deferred not yet due, unknown status, stale lock (held by a helper process), process exited, a primitive lane file and
  primitive `lanes`; events acknowledged, a failed one past `retry_after`, a queued one not
  retried, `logs/` absent (the ENOENT path) and the mailbox directory a file; channels
  `codex-queue` (the fake's `queue` mode: exit 0, 1, unscripted 97), `opencode-run`, unknown
  type, no type, `null`, two channels; redispatch disabled and already done; states `null` and
  `5` (C1); a live lock (exit 1); (S8c part 2) the stop file present at the first loop over a
  `null` and a `"x"` state (C1, `status`), a finished and a running lane whose pid is alive,
  a `null` config with a deferred lane not yet due; (S8c part 2b) running lanes (this test's
  pid) quiet at the standard and focused cadences, not quiet with a check due and not due, and
  quiet with a delivered reminder pending, their progress minutes back with margins of at
  least ten minutes around the quiet threshold and the 30-minute re-alert bucket. The flip
  evaluation without a launch, each case required to reach it (`last_flip_check` stamped, no
  `storage_error`; a conductor lane is seeded with `logs/`, without which its own event throws
  at the log append before the evaluation): a human gate (first note, and already logged), a
  live conductor, a held conductor lock with a pending launch, a finished-early conductor, a
  paused project, a `decide` flip and a hand-off before the lane started inside their
  cooldowns, a never-started flip target in its backoff, a paused project `decide` keeps
  paused (both red), a conductor that handed off, a never-started and a pending launch to
  `gemini` (refused by `flip run`, exit 2, logged), and an evaluation already done this
  minute; `decide` reads a usage cache seeded fresh (green, both red or OpenAI red) so its
  action is known. Launches, each runner settled (waited for by the pid its lane records)
  before the files are compared and the expected log line, mailbox note or claude argv
  required: a paused project relaunched on its own door (opencode bridge), a pending launch, a
  `decide` flip to claude, a finished-early claude conductor restarted with `--resume` of its
  session, a never-started flip target launched, and a due deferred lane re-dispatched (claude
  bridge). PE-36/37 assert supervisor-owned attempt records before comparing the remaining
  files; the frozen JS still writes `redispatched_at` into a stale lane snapshot.
  `channel/claude-resume` runs the claude
  bridge detached over the fake and compares once a run record exists and the sandbox is
  quiet for 300 ms; the record's `prompt_sha256` must be the digest of the wake text posted
  to the mailbox (the text names the sandbox, so the digest is rewritten). `restart/after-js`
  and `restart/after-orch`: a serve ticks and stops in one sandbox, the stop file is removed
  (as `start` does), and a second serve of the other runtime must leave what a second serve of
  the same runtime leaves (and must tick: its `checked_at` changes). Slow, opt-in with
  `ORCH_PARITY_SLOW=1`: `slow/paused-uncached-decide`, a paused project whose `decide` must
  collect usage. `channel/delivered` checks that the event reaches the command, the mailbox
  and the log in both runtimes. Asserted differences: PE-16(1) (`[]` state), PE-16(2)
  (`null` acks with a failed event due) and PE-17 (no `opencode` on PATH). `start`: fresh, stop
  file removed, stale lock, already running (a helper process holding the lock; its pid is
  written `<holder>`), keep-awake through the fake
  `which` (the wrapper's argv checked per runtime); each launched serve is stopped through its
  stop file and waited for by the pid `start` printed. Normalised, only inside the run's time
  window: `checked_at`, `created_at`, `stopped_at`, log line times, `next_check`, `retry_after`
  and `next_check_at` (as tick time plus 120, 300 or 600 s), `last_flip_check` and
  `last_relaunch_at` (as tick time), the state's `pid`, a claimed lock's pid, mailbox ids, an
  incident's `at`, `version` and project name; for launches also the worker cases' values
  and the end of a log line quoting `flip run`'s answer from its `log_path` (the 200-unit cut
  falls inside the sandbox path); in the slow case the collected usage's `generated_at`.
  Why the paused case needs a cache: without one, `decide` collects usage, and both
  runtimes' `codex app-server` readers wait out their 20 s timeout although the unscripted
  fake exits at once (incident `5f5f6ff3449b`); `serve` only checks the stop file between
  ticks, so it holds the stop for about 25 s, past the harness's 20 s. The slow case waits 45 s.
  Not yet compared: a crashing `flip run`, `flip decide` or `worker start` child (PE-2
  extension), the 200-unit cut over an astral character (PE-1 extension), a channel child killed
  by the RunSync grace (PE-10 extension), serves of more than one tick.

- `doctor`: `orch doctor` against `bin/doctor.mjs` over the fakes' canned answers
  (`$HOME/fake-answers/<cli>/<argv joined by "-">`): everything healthy (claude auth as JSON),
  nothing scripted (every probe fails, the feeds fail, exit 1), claude auth as text and unclear
  with 200 characters (cut at 120), auth list without anthropic with the plugin missing, agents
  with missing ids (a number, an array id, `{}`), not JSON (cut at 200) and with a `null`
  element, a log with errors among 500 lines (outside the last 400, the last 3 joined, the 300
  cut through an astral character: PE-1), a plugin error quoting a credential, the sandbox home
  and an email (`log-errors-redacted`: both print `<token>`, `~` and `<email>`), the log a directory (no row) and the registry missing
  (a copied JS tree without `models.json`: Node's `ENOENT` line as `FAIL usage/routing`). Each
  case passes `ORCH_BIN=<orch>` to the JS, so both `orch binary` rows are compared, and asserts
  PE-18 (JS `OK   unit tests  —  pass N`; orch's `WARN orch on PATH  —  not on PATH; ...`, no `orch`
  being on the parity PATH, its `orch self-test` row and `WARN unit tests  —  not run (...)`, no `go`
  being on it either), masks or removes those rows and compares the rest.
  `no-binary` runs the JS without `ORCH_BIN`, with no `~/.local/bin/orch` and no `orch` on the parity
  PATH: its row is `WARN orch binary  —  not found (runtime js only; see README "Go runtime")`,
  masked against orch's own OK row. Normalised: the usage cache's `generated_at`.

- `watch`: `orch watch` against `bin/watch.mjs` over a fresh usage cache (no feed refresh), with
  every seeded time relative to one instant: an empty project; the supervisor alive (the test's
  pid), dead (a reaped pid) and without `checked_at`; flip flipped, paused, awaiting the human with
  `at` and awaiting the human only (the mirrored TypeError crash, C1); lanes running alive, running
  dead, finished with `last_tool`, a file holding `7` and an unparsable one; questions to the human,
  to `*`, to someone else and with an astral subject cut at 100 (PE-1), a question acked by the
  human, 21 questions (limit 20); 9 messages (last 8, `padEnd`); incidents inside and outside 24 h;
  `STATE.md` with `## Next action`, `## Next action (exact)` and none; a stale cache with both
  feeds failing (the last good reading; no `STALE`, which `getRouting({})` never sets).
  Normalised: the header's project basename and clock (and, for the stale cache, the re-collected
  usage time). `--follow --interval 1` runs each runtime until its second frame
  begins (5 s bound), SIGTERMs the pid it started and compares the first frame and the signal
  death; the gate-only `flip.json` compares the first stderr line instead. A supervisor-state pid of `0` is
  alive in both (no falsy guard in `watch.mjs`'s `alive()`).

- `kickoff`: `orch kickoff` against `bin/kickoff.mjs` in cases that never launch a conductor (each
  stops at the usage, the brief check, the both-red path or `--dry-run`, or crashes before the
  launch), with the project's `supervisor.lock` held by a helper process (its kernel lock and its
  pid as text, written `<holder>` in the answer) so neither `supervisor start` starts a serve, and both usage feeds failing so each refresh keeps the seeded cache's
  readings: no arguments, `--help` with and without a brief, `--brief ""`, a missing brief, `--brief
  --brief x`, a directory brief, a bare `--cwd` (C1); dry runs with the home door usable, the home
  door red, `--door claude`, `--door auto` with Anthropic red, `--headless`, side briefs,
  `--unattended`, a human gate (kept) and `.git` a file, and one headless, unattended dry run
  asserted to leave the project as seeded (no `policy.json`, the gate kept, no supervisor state or
  conductor brief, the exclude untouched); the preflight, reached by `--door gemini` without
  `--dry-run`, with `--unattended` (`policy.json`), a human gate cleared, the exclude appended,
  already present, without `.git`, with a `.git` file naming a missing git dir (nothing
  written) and in a linked-worktree layout (the common dir's exclude appended), the last also
  after a dry run in the same sandbox, which leaves it alone; a registry with `conductor.home: "claude"` (a copied JS tree); `--door
  gemini` and a bare `--door` with `--dry-run` (the mirrored TypeError after the dry-run
  supervisor line, C2, files compared); both red
  with `--dry-run` (exit 75, no side effects) and without (`flip pause`: `flip.json`, the handoff,
  exit 75); the registry missing (PE-2). Each case first checks that the JS printed its expected
  line, so no case passes by both runtimes failing alike. Normalised: `generated_at`, fresh
  flip and mailbox times and ids, the project path.
  (S9c-2) Then the launches, each asserting on both sides that it happened before comparing:
  headless on claude (the `ok` fixture) and opencode (`opencode run` scripted), each runner
  settled by the `runner_pid` its kickoff printed and everything it wrote compared
  (`conductor-kickoff.md`, the lane state and log, the mailbox, the run record, the fake's argv);
  the JS claude bridge's post-run refresh opens a codex app-server session orch's does not
  (PE-12), so the app-server log must be orch's one session followed by at most the first whole
  lines of one more like it, and is then dropped. Interactive claude and opencode (the opencode
  fake's `--prompt` mode, scripted by `$HOME/fake-opencode-tui/`; the fake's argv log, exit 3
  and 4 passed through; a fake that kills itself with SIGTERM or SIGINT, exit 0). A dry run
  without the lock, where each `supervisor start` really starts a serve in its sandbox: the
  serve's tick is checked by the pid the kickoff printed, the serve stopped through its stop file
  and waited for (killed by that pid after 20 s, failing the case; a pid the output does not
  show is found after the stop file is written, polling up to 10 s: orch's through the kernel
  lock, the frozen JS's from the lock's text, as its serve holds no record lock; the case ends
  only after that serve has exited, so its sandbox is never removed under it); normalised: that
  pid, the cut log path and the serve's fresh values. `cleanup/unread-serve/{js,orch}/{missing,
  malformed}` asserts that failure path against each runtime's real `supervisor start`.

- `session`: `orch session` against `bin/session.mjs`: usage errors (no or unknown command, door,
  state, progress forms, a missing value, unknown options and positionals); beats that create the
  project, set every field, cut `--now`/`--slice` (a split surrogate pair, malformed UTF-8 argv,
  JSON-special text), take huge and infinite progress numbers, merge over and clear a seeded record
  (a lone surrogate escape in it, odd field types, unparsable, an array, null, a BOM), and fail on a
  directory record, a `sessions` file or an `.orchestrator` file; hook mode for every followed event,
  unfollowed and non-string events, the session id from the hook or the flag, relative and
  `--cwd`-overridden roots, outside a project, bad, empty and non-object input, no cwd, and usage
  errors that exit 1, plus one case asserting the prompt and transcript path reach no output or
  file; `show` empty, with both doors, any JSON, an unparsable or directory record, `--cwd`, an
  unknown option and outside a project. Only `updated_at` is normalised, where it has its exact
  ISO-ms format; `show` prints stored records as parsed, so a hand-edited lone surrogate escape is
  the `jsjson` divergence in [JSON compatibility](#json-compatibility).

A case can add environment entries (`env`), compare files outside `.orchestrator/`
(`extraFiles`), replace the sandbox project path with `$PROJECT` everywhere (`projectPaths`) and
declare that it ends in an uncaught JS exception (`crash`, below), possibly after progress lines
(`progress`).

## Crash output and parity exceptions

Where the JS dies of an uncaught exception, Node prints a source excerpt, `<Name>: <message>`,
a stack and its version, and exits 1. `orch` exits 1 after the same stdout and file writes and
prints only the error line:

- C1, fixed messages (`Error: ambiguous message id prefix aaaaaaaa`, `Error: post needs from and subject`,
  filesystem errors such as EISDIR/ENOTDIR/EACCES, ...): each case pins the expected error
  line; the harness requires that exact nonempty line as `orch`'s entire stderr and as a
  whole line in Node's stderr, with exit 1 from both. Errnos outside the 21
  that `orch` spells as Node does (EACCES, EEXIST, EISDIR, ELOOP, ENOENT, ENOTDIR, ...) print
  Go's own error text instead; that difference is known and not asserted.
- `orch worker` prints these C1 lines: `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]"
  argument must be of type string. Received type boolean (true)` (a bare `--cwd`, before any
  command), `Error: ENOTDIR: not a directory, scandir '<dir>'` (or EACCES: a `workers` that
  cannot be listed), `Error: kill <CODE>` (`stop` when neither the group nor the pid can be
  signalled), and for `start`: the `"paths[1]"` form of the first (a bare `--brief`), `Error:
  EISDIR: illegal operation on a directory, read` (a brief that is a directory, before the lock
  and the state), `TypeError [ERR_INVALID_ARG_TYPE]: The "to" argument must be of type
  string. Received ...` (a failed result whose `record` is not a string), `Error: post needs from
  and subject` (an empty lane, from a brief named `.md`) and the filesystem errors of the
  workers directory and the mailbox; `start --detach` adds the error of probing the lock (a
  directory lock: the JS's `Error: EISDIR: illegal operation on a directory, read`, `orch`'s
  ownlock error, PE-29), creating
  `logs/` and creating the log (`Error: EEXIST: file already exists, open '<log>'`). A result whose
  `final_message` is truthy but neither a string nor an array, or whose `errors` is truthy but not
  an array, is C2. Once the lane is claimed,
  every crash releases the lock first (PE-28); the JS `finally` removes it from the bridge's
  start on.
- `orch flip` prints these C1 lines: the bare `--cwd` TypeError above (before any command,
  the usage error included); `Error: EEXIST: file already exists, mkdir '<root>/.orchestrator'`
  when `.orchestrator` is a file; an exclude directory's EISDIR read; and the mkdir/rename
  failures of the continue brief (PE-21). An unreadable or invalid `flip.json` now fails
  closed with `Error: flip state unreadable: <path>: <code>` before other run writes (PE-32).
  Gate assignment to a valid primitive now succeeds (PE-34); bare value options are usage
  errors rather than JS values (PE-35).
- `orch supervisor` prints these C1 lines: the bare `--cwd` TypeError above (before any command,
  the usage error included); `Error: EEXIST: file already exists, mkdir '<root>/.orchestrator'`
  (`stop` where `.orchestrator` is a file) and `Error: EISDIR: illegal operation on a directory,
  open '<root>/.orchestrator/supervisor.stop'` (the stop file a directory); for `ack`, `TypeError:
  Cannot read properties of null (reading 'events')` (a state file holding `null`, with a
  truthy `--event`), `TypeError: Cannot set properties of null (setting '<event>')` (acks
  `null`), `TypeError: Cannot create property '<event>' on <number|string|boolean> '<value>'`
  (acks a primitive) and `Error: EISDIR: illegal operation on a directory, rename
  '<acks>.tmp' -> '<acks>'` (the acks file a directory; the `.tmp` file stays), as other rename
  and open errors of the acks write; orch's line names its own `<acks>.tmp-<random>`, which it
  removes (PE-21). `serve` with a state file holding `null` prints `TypeError:
  Cannot set properties of null (setting 'storage_error')` (`'status'` when the stop file is
  already there), with a primitive `TypeError: Cannot create property 'storage_error'|'status'
  on <number|string|boolean> '<value>'`, its lock left (asserted for `null` and `5`); a lock
  that is a directory gives `Error: EISDIR: illegal operation on a directory, open
  '<root>/.orchestrator/supervisor.lock'` in the JS, where orch's `serve` and `start` fail with
  `lock "supervisor.lock" is not a regular file` (PE-30); `start` gives the open error of its
  log, in orch also ELOOP for a symlinked log (PE-21).
- C1 also covers Node's own coded and type errors with fixed text, such as
  `TypeError [ERR_INVALID_ARG_TYPE]: The "paths[0]" argument must be of type string. Received
  undefined` (`manifest verify` without a file) and `TypeError: Cannot convert undefined or null to
  object`
  (a manifest without `files`), `TypeError: opts.ctx.push is not a function` (an incident `--ctx`
  value after a bare `--ctx`) and `TypeError: Cannot set properties of null (setting 'a')` (a
  `policy.json` holding `null`). Paths in these messages use the physical working directory, as
  `process.cwd()` does, not `$PWD`.
- C2, malformed hand-edited state (a message whose `kind` is not a string, `refs` missing, ...):
  both stderrs must contain a line starting `TypeError: `; V8's wording is not reproduced.
- A crash after progress (the bridges, which log `[claude] ...`, `[codex] ...` or
  `[opencode] ...` lines before they can fail): `orch` prints the progress lines, then the error
  line. Node's stderr must begin with exactly those progress lines and print no further one; the
  rest is compared as C1 or C2. Where the JS dies inside its line handler while the CLI still
  runs (a `null` event, say), `orch` also sends the CLI SIGTERM before it exits, instead of
  leaving it running. An argument holding a NUL byte (a prompt read from a file or stdin) fails
  before the CLI starts with Node's `TypeError [ERR_INVALID_ARG_VALUE]: The argument
  'args[<i>]' must be a string without null bytes. Received <inspected value>`.

The other documented differences, each asserted by its own parity case:

- PE-1: a UTF-16 slice that splits a surrogate pair (the reply subject cut at 140 units, the inbox
  body at 400, an incident summary at 300, redacted text at 2000, the fingerprint input at 200;
  in `orch claude-exec` the result error text at 200, the incident summary at 160 and the run
  record's `prompt_head` at 400, which reach stdout, the run record and the incident; in `orch
  codex-exec` the incident summary at 160, the tool-host no-op incident detail at 400 and
  `prompt_head` at 400; in `orch opencode-exec` `prompt_head` at 400; in `orch worker start` the
  state's `result_summary` at 2000, the failure incident's summary at 160 and detail (the last
  1500 units of the bridge's stdout) and the blocked message body at 1500; in `orch flip gate`
  the human's message subject at 80, in `orch flip run` the `launcher` text at 400; in `orch
  supervisor` (drafted in S8b, not yet asserted by a parity case) a flip log line's quoted
  output at 200 and the redispatch note body at 500) or a string message spread into one key per
  code unit gives U+FFFD in Go. Hashes are identical (Node hashes a lone surrogate as U+FFFD). With
  an empty `HOME` the JS home pattern is empty and
  matches between the halves of every astral character, splitting them the same way. Text
  output is identical (Node prints a lone surrogate as U+FFFD); JSON escapes differ: the JS writes
  `"\ud83d"`, `orch` the raw U+FFFD.
- PE-2: the crash output above. A manifest file or (for `orch policy`) a registry that
  `JSON.parse` rejects prints a `SyntaxError: ` line in both runtimes; the message after the
  class differs. Extended to `orch flip run`: when the worker launcher dies, `run`'s `launcher`
  field (`(stdout || stderr).trim()`, cut at 400 units) holds Node's crash block in the JS (its
  throwing location, source excerpt, error line and stack) and only the error line in `orch`.
  Extended to `orch supervisor` (drafted in S8b, not yet asserted by a parity case): a
  crashing `flip run`, `flip decide` or `worker start` child quoted in a log line or the redispatch
  note shows Node's crash block in the JS and the error line in `orch`; so does `serve`'s own crash
  in its log.
- PE-3: `orch mailbox post --json` prints the whole message it appended; the JS ignores
  `--json`. As in `list`, only a bare `--json` counts (`--json x` is the value of a key).
- PE-4: an incident's `version` is `orch version` (e.g. `0.5.0 (8b23cd3)`, or `0.5.0` when built
  outside a git checkout); the JS writes its checkout's `.git/HEAD`, trimmed to 40 characters
  (`ref: refs/heads/main`), or `unknown` when there is none.
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
  Extended to `orch supervisor` (drafted in S8b): the flip evaluation's handoff scan (`m.at >=
  conductorLane.started_at`) is false in `orch` when either value is an object or an array.
- PE-7: `toLowerCase` of code points Go's Unicode 15.0 tables do not know, or whose case
  properties changed since (U+0295, U+1171E), follows Go, not Node's newer ICU data. The test of
  `jsstr.ToLower` against every code point lists them (196 with Node v26.5.1 / Unicode 17.0).
- PE-8: the bridges with a trailing bare `--prompt` print `no prompt: --prompt needs a value` and
  exit 2 before the budget gate. The JS uses the prompt `true`: claude-exec runs claude on it (a
  real model turn), then crashes hashing it on the normal path, or prints its usage-limit JSON;
  codex-exec logs its start line, spawns codex and dies of `stdin.end(true)` (`TypeError
  [ERR_INVALID_ARG_TYPE]: The "chunk" argument ...`); opencode-exec runs opencode on it, then
  crashes hashing it. Neither the worker nor `flip run` passes a bare `--prompt`; they pipe the
  brief on stdin. Accepted by the human for claude-exec (51e9d79b); extended to codex-exec and
  opencode-exec as an assumption (Q-S5b-2).
- PE-9: the bridges print no Node process warnings. With a `--timeout-min` whose delay Node
  clamps to 1 ms (NaN, below 1 ms, above 2147483647 ms, e.g. `0`, `-1`, `abc`, `35792`), Node 26
  prints a `TimeoutNaNWarning`, `TimeoutNegativeWarning` or `TimeoutOverflowWarning` on stderr;
  `orch` applies the same 1 ms delay silently. The parity cases drop those lines from the JS stderr.
- PE-10: after a bridge signals its CLI (the timeout, a forwarded signal, a crash), it sends
  SIGKILL if the CLI has not exited 10 s later, and once the CLI has exited it waits at most
  10 s for its stdout and stderr to close and for codex's stdin to take the prompt (a grandchild
  may hold them). orch reads those pipes itself rather than through `os/exec`, marks the CLI
  reaped as soon as it exits and only then waits, closing its own ends of the pipes when the 10 s
  run out, so no signal of this escalation can reach a reused pid or process group during the
  wait. The JS sends one SIGTERM and waits for `close` forever. No output changes; a
  signalled CLI still has `exit_code: null`. Accepted by the human (51e9d79b). Extended to
  `orch worker start`: once its bridge has exited it waits at most 10 s for the bridge's pipes
  (the JS waits for `close` forever); it never SIGKILLs the bridge, which bounds its own CLI.
  Assumption (Q-S6-4). Extended to `orch flip run`: once the worker launcher has exited it waits
  at most 10 s for the launcher's pipes; `spawnSync` waits for them forever. No output changes
  (the detached runner writes to its log, not to the launcher's pipes). Extended to `orch
  supervisor` (drafted in S8b, not yet asserted by a parity case): every child it waits for
  (`codex queue`, a `command` channel, `flip decide`, `flip run`, `worker start`) runs through
  `procs.RunSync`, which SIGKILLs a child that ignores the 30 s channel timeout's SIGTERM 10 s later
  and waits at
  most 10 s for pipes held by grandchildren; `spawnSync` waits for both forever. No output
  changes (a killed channel is `exit null` either way). Every signal of this escalation goes to
  the child's whole process group (PE-22).
- PE-11: SIGTERM and SIGINT sent to a bridge are forwarded to its CLI, and the run then ends as
  any run does once the CLI closes: for a CLI the signal killed, `exit_code: null`, the
  `claude exited null: ...` or `codex exited null: ...` error (opencode: the bare stderr tail), a
  run record, the incident, the JSON and exit 1. The JS dies at once and leaves the CLI running.
  In the JS the CLI stays in the bridge's process group, so a signal to the whole group reaches it
  there; in orch the CLI has a group of its own (PE-22) and is reached through the forward.
  PE-65 amends "as received" for opencode-exec, which forwards a SIGTERM as SIGINT.
  Extended to `orch worker start`: a worker interrupted while its bridge runs (`worker stop`,
  Ctrl-C, SIGTERM) records what the Go bridge reports after forwarding the signal: status
  `failed`, `exit_code: 1`, the bridge's `claude exited null: ...` (codex: `codex exited null:
  ...`, opencode: the stderr tail) in `errors`, the run record in `record` and in the blocked
  message's `refs`, a run record file and the bridge's own failure incident. The JS bridge dies
  at once, so the JS worker records `exit_code: null`, `errors: ["bridge produced no result"]`,
  `record: null`, the incident summary `... failed on <harness>: no result` and no run record.
  The CLI runs in a process group of its own (PE-22), so a signal to the bridge's group reaches
  only the bridge, and the CLI receives it once, through the bridge's forward; no output
  changes. The same holds for a detached worker stopped by `worker stop`, whose log then ends, in
  `orch`, with the bridge's `done ok=false` line and a final line with `exit_code: 1` (the JS:
  `exit_code: null`, no done line); both end `failed`, the `interrupted` status the signal
  handler publishes being overwritten when the bridge closes. Assumption (Q-S6-2).
- PE-12: after a normal run claude-exec and codex-exec start a usage refresh they never await;
  `process.exit` follows in the same tick, before any of its awaits resolve, so it never writes
  `usage.json` (it only runs a token lookup and spawns a `codex app-server` that dies with
  it). `orch claude-exec` and `orch codex-exec` make the refresh real
  (plan 3.10 item 3, kept by the human in 51e9d79b; extended to codex as an assumption,
  Q-S5b-3): once the run record is written, where the JS starts its refresh, they start
  `orch usage --refresh` in a session of its own, stdio on `/dev/null`, and exit without
  waiting, so their exit code and timing are unchanged. A run record the JS throws on (a bare
  `--resume` with no thread or session id to replace it) ends both runtimes before the refresh,
  so orch starts none.
  The refresh is bounded by the timeouts of the feeds it reads (security 30 s, the Claude
  endpoint 15 s, codex app-server 20 s, sqlite3 5 s, `opencode auth list` 15 s) and writes
  `usage.json` as `orch usage --refresh` does (which needs a readable registry).
  `bin/opencode-exec.mjs` has no refresh, and `orch opencode-exec` starts none. The claude
  usage-limit path is unchanged: both runtimes await their refresh there. The parity harness sets
  `ORCH_NO_POST_RUN_REFRESH=1` for the bridge cases, so no refresh writes into a sandbox after its
  files are compared. A `usage.json` written during a run comes from the budget gate, which both
  runtimes run alike when they read the same registry: with an empty `HOME` and no
  `ORCH_REGISTRY`, the JS reads its own checkout's `models.json` and its gate collects and writes
  a failed reading, while `orch` finds no registry under `ORCH_HOME` and proceeds without
  collecting (see Environment).
- PE-13: when codex exits without reading its prompt (its own argv rejection, say), `orch
  codex-exec` ignores the EPIPE on codex's stdin and reports the run like any failed run: the
  `codex exited <code>: <stderr tail>` error, a run record, the incident, the JSON and exit 1. The
  JS has no error listener on codex's stdin and may die of an uncaught `Error: write EPIPE` with
  no JSON and no record: racy for a small prompt, certain for one larger than the pipe buffer.
  Assumption (Q-S5b-4). Extended to `orch worker start`: a bridge that exits without reading
  its prompt (opencode-exec given `--effort X`, which makes `X` its prompt) does not crash the
  worker; the EPIPE is ignored and the run ends like any other. The JS worker has no error
  listener on the bridge's stdin and dies of an uncaught `Error: write EPIPE` (certain for a brief
  over the pipe buffer), leaving its lock and a `running` state. Assumption (Q-S6-3).
- PE-14: `orch worker stop` signals only a stored `pid` that coerces (as `process.kill` coerces
  it: an array or object through its string form, then an integer in the int32 range) to an
  integer of at least 2; anything else (`1`, `true`, `"1"`, `[1]`, `-1`, any negative, `"0"`,
  `[]`) prints `{"lane":…,"stopped":false,"reason":"no live worker"}` and exits 0. The JS sends
  SIGINT to every process the user may signal (pid `1` or `true`: `kill(-1)`), to its own
  process group (`"0"`, `[]`) or to process `|pid|` (a negative pid). Parity cases run `orch`
  only for these values and assert its output; the JS side is never executed. Assumption
  (Q-S6-1).
- PE-15: `orch flip decide` reads `flip.json`'s `at` (when truthy) as `Date.parse(String(at))`
  in the ECMAScript date-time formats only (`jsjson.ParseDate`: ISO with `Z` or an offset,
  date-only, year-month, year, and local date-time without a zone). Where V8's legacy fallback
  parser accepts other text (`2026-10-19 10:00:00`, `Oct 19 2026 10:00`, `+0100` offsets, a bare
  `5`), `orch` treats it as unparsable: no previous flip, so no cooldown. Both runtimes write
  `at` with `toISOString`, so only a hand-edited `flip.json` can differ, and only in whether a
  flip less than `flip_cooldown_minutes` ago (or in the future) holds back a hand-back.
  Asserted by one case (the legacy local form of two minutes ago: the JS stays with `cooldown
  after a recent flip`, `orch` hands back). The same parser already reads the usage cache's
  `resets_at` (`orch usage`, `routing`, `budget`, the bridges' gate and `decide`'s freshly-reset
  test), where those producers write ISO timestamps only. Assumption (Q-S7-1). Extended to
  `orch supervisor` (drafted in S8b): `flip.json`'s `at` and `conductor.json`'s `started_at`
  (flip target never started), a worker's `resume_after` (redispatch) and
  `last_progress_at`/`updated_at`/`started_at`, and an event's `created_at`.
- PE-16: hand-edited supervisor inputs of shapes the JS mutates invisibly. (1) A JSON array as
  `supervisor-state.json` or its `lanes` that the supervisor rewrites: `orch` reads it as `{}`
  and writes the object (the JS sets named
  properties on the array, which `JSON.stringify` drops, and keeps rewriting the array). (2)
  `supervisor-acks.json` holding `null` during a tick: `orch` reads no acks (the JS throws a
  TypeError at the first ack lookup, so the lane's event pipeline stalls with `storage_error`);
  and an array `supervisor-acks.json` in `ack`: `orch` writes `{<event>: …}` (the JS writes the
  array, with the entry only for an index-like event). (3) A worker `breadth` naming an
  Object.prototype key: `orch` uses the standard cadence (the JS uses the inherited function as
  the interval: string concatenation, then `RangeError: Invalid time value`). (4) `ack` with a
  state `events` that is a string, array, number or boolean: `orch` reads it as `{}` (the JS
  also accepts index keys, `length` and the primitive's prototype methods as event ids). Both
  runtimes write objects, so only hand edits reach these. Asserted: (2) for `ack` and (4) by
  parity cases; (1) for `lanes`, (2) during a tick and (3) by
  `internal/supervisor` unit tests; (1) for the state file and (2) during a tick also by the
  `serve` parity cases `PE-16-array-state` and `PE-16-null-acks`. PE-36 supersedes the old
  array-conductor rewrite: no worker-owned lane file is written. (5) A worker lane file named
  `__proto__.json`: like the JS, `orch` writes no `lanes.__proto__` entry (the assignment is the
  prototype setter), but a later tick of the same JS process reads the previous entry back
  through the prototype as the lane's state, where `orch` reads none. Assumption (Q-S8-2).
- PE-17: when the `opencode-run` wake channel's `opencode` cannot be found or executed, or its
  working directory is missing, `orch supervisor serve` records what the JS records (the channel
  `queued`, the event status, the log line, the state) and keeps serving. The JS spawns it
  without an `'error'` listener and dies right after the tick (`Error: spawn opencode ENOENT`,
  exit 1, lock left; the next `start` reclaims the lock). Asserted by the `supervisor` parity
  case `serve/PE-17-no-opencode`. Assumption (Q-S8-1).
- PE-18: both runtimes print `orch binary` right after `node present`. The JS resolves `ORCH_BIN`
  (when set, with no fall-through), else `~/.local/bin/orch`, else each `PATH` entry, and prints
  `OK   orch binary  —  <orch version output> at <path>`, `FAIL` when `ORCH_BIN` is not an
  executable file or `version` exits nonzero, and `WARN orch binary  —  not found (runtime js only;
  see README "Go runtime")` when nothing is found; orch always reports itself (`<orch version> at
  <executable>`), so the rows are byte-identical when both name the same file. The remaining
  differences: orch adds `orch self-test` right before `unit tests`: in-process checks that write
  nothing (a JSON round trip against embedded golden bytes, the registry resolving every role
  over an embedded all-green usage document, the executable path), detail `json, routing,
  executable` or the first failure (FAIL). Without Node, orch prints `WARN node missing  —  needed
  only by install.sh and uninstall.sh` where the JS prints `FAIL node missing  —  install it`:
  orch runs without Node, which only the installer scripts still need. Right after `orch binary`
  (when its executable is known) orch adds `orch on PATH`, which resolves `orch` through `PATH`
  and compares it with its executable, both through `filepath.EvalSymlinks`: `OK` with the PATH
  path when they are the same file; `WARN not on PATH; agent commands "orch ..." will fail; add
  <dir of the executable> to PATH` when nothing resolves; `WARN PATH orch is <path> (<first line
  of its version>, or version failed), not <executable>` when another binary resolves (its
  `version` runs with the 20 s probe timeout). The row never FAILs, so the installer's
  post-install doctor does not fail on a PATH layout. Parity has no case without `node` (the JS
  needs it to run), so the node WARN is asserted by unit tests only; every case asserts orch's
  `not on PATH` warning, there being no `orch` on the parity PATH, and removes it. Its `unit tests` row runs `go test ./...` in
  `<ORCH_HOME>/../go` (120 s timeout) when `go` is on PATH and `go.mod` exists there, instead of
  `node --test`: OK `go test ./... passed`; FAIL with the first `FAIL` or `--- FAIL` line (or
  `exit N`); WARN `go test timed out after 120 s`; else WARN `not run (no go toolchain or module
  at <dir>)`. With the registry missing both runtimes FAIL their test row (the JS unit tests
  need `models.json`; orch's self-test fails `routing: ENOENT: ...`). Asserted by every
  `doctor` parity case and by `internal/doctor` unit tests.
- PE-19: every command line orch writes for agents and humans reads `orch <cmd> ...` (package
  `internal/cmdref`): supervisor reminders, the worker preamble, `flip run`'s
  `conductor-continue.md`, the `flip gate` message, kickoff's usage line and watch hint, the reply
  and clear hints of `mailbox inbox` and `watch`, and the usage lines of `manifest create`,
  `mailbox reply` and `worker`. The JS writes `node ~/.config/opencode/orchestrator/bin/<cmd>.mjs`
  or `<cmd>.mjs`. Why: 0.6.0 removes the JS, so its command strings would name deleted files. The
  harness rewrites the JS side only (stdout, stderr and file contents, after the `$PROJECT`
  placeholder and before each case's normalizers): first
  `node ~/.config/opencode/orchestrator/bin/(<name>)\.mjs`, then `\b(<name>)\.mjs\b`, to
  `orch <name>`, where `<name>` is one of claude-exec, codex-exec, opencode-exec, worker,
  supervisor, flip, mailbox, manifest, incident, usage, doctor, kickoff, watch and policy.
  Special cases, each asserted:
  - `--help` of `claude-exec` and `codex-exec` prints the JS header as orch help: the trailing
    `import` line dropped, each line's leading `// ` or `//` stripped, the names mapped and the
    usage line's continuation lines re-aligned under its first `[`. The `bridges` `help` cases
    derive the expected text from the JS output, and `TestJSReference` from the JS source.
  - The bridge budget-red incident's summary is `orch <bridge> refused: budget red` (was
    `<bridge>.mjs refused: budget red`), so its `fp` changes. The harness masks `fp` on incident
    lines with `"component":"bridge","kind":"limit"` and that summary only, on both sides, and
    asserts that orch's is `incident.Fingerprint` of its summary; every other `fp` stays compared.
  - A run record's `prompt_head` keeps 400 UTF-16 units of the prompt, so the JS head, cut from
    a prompt with longer command strings, is shorter after the mapping or ends inside a command
    it could not map. When it holds a mapped command or such a partial one, the partial is
    dropped and both heads are compared as the JS's remaining text, which must be a strict
    prefix of orch's.

  Unit tests in `go/parity/pe19_test.go` (no build tag) pin the mapping (the 14 names in both
  forms; `notascript.mjs`, `session.mjs` and `orchestrator/models.json` untouched), the help
  derivation, the fingerprint mask and the `prompt_head` alignment. PE-19 normalizes nothing
  beyond the name mapping, the help derivation, the budget-red `fp` mask and the `prompt_head`
  alignment above.
- PE-20: `procs.RunSync` (every spawnSync orch mirrors: `flip run`'s launcher, kickoff's
  children, the supervisor's channels and `flip`/`worker start` children, the doctor's probes;
  orch also captures kickoff's `policy unattended` and `flip gate --clear`, which the JS runs
  with stdio ignore and so without a limit, but both print a few bytes, so the cap is unreachable
  there; the usage feeds' execFileSync calls, `sqlite3`, `opencode auth list` and `security`,
  run through it too) stops a child whose stdout and stderr together exceed 1 MiB (Node's
  default maxBuffer, counted over both streams as Node 26 counts it). orch sends SIGTERM
  (SIGKILL after the PE-10 grace, 1 s for the usage feeds), reports `status null` for a child the
  signal killed and marks the result overflowed (ENOBUFS). orch keeps the first 1 MiB and
  discards the rest while draining; Node keeps every whole read made before it noticed
  (1,114,112 bytes in a probe), then closes the pipes. A TERM-ignoring child ends by SIGKILL in
  orch, by EPIPE/SIGPIPE in Node, `status null` in both. The usage feeds count stdout only (their
  stderr goes to `/dev/null`), as execFileSync does, and an overflow is a failed feed. Asserted
  by `internal/procs` and `internal/usage` unit tests and the
  `supervisor/serve/channel/command-overflow` parity case.
- PE-21: private state is written owner-only and atomically: run records, worker state and the
  supervisor's state, acks, lane and conductor files. Each is written to `<name>.tmp-<random>`
  (created exclusively, mode 0600, in the same directory), then renamed over the target, so a
  symlink at the target is replaced, not followed, and concurrent writers never share a temp.
  `runs/`, `workers/` and `logs/` are 0700 (an existing one owned by the user is tightened; a
  symlinked one is not followed, so its target keeps its own mode). The JS writes 0666/0777 less
  the umask, in place or through the fixed `<file>.tmp`. A failed rename names
  `<file>.tmp-<random>` and orch removes it, where the JS leaves `<file>.tmp`; a missing
  directory likewise names `<file>.tmp-<random>` where the JS names `<file>.tmp`. The suffix adds
  up to 15 bytes to the name, so a name near the file system's limit can fail with ENAMETOOLONG
  in orch where the JS succeeds. `flip.json` and the continue and kickoff briefs are replaced the
  same way, so a supervisor never reads a half-written gate. A brief that is a directory fails
  at the rename (`EISDIR ..., rename '<tmp>' -> '<file>'`), where the JS fails at `open`.
  A directory `flip.json` now refuses at its read before any replacement (PE-32); the frozen JS
  reaches the failed rename of `flip.json.tmp`, which it leaves. The supervisor log is opened 0600 without following
  a symlink: a symlinked log fails `start` with ELOOP, after `start` has already removed
  `supervisor.stop`. An existing log keeps its mode (one created by the JS or an older orch stays
  as it was; only the 0700 `logs/` protects it). The supervisor lock is created 0600; a lock file
  no process holds is claimed in place and keeps its mode (PE-30). The temp is not fsynced: the replacement is
  atomic for concurrent readers, not durable across a power loss. Asserted by the `worker` parity
  case `start/state/PE-21-json-is-a-directory`, the `supervisor` case `ack/PE-21-acks-directory`
  and the `flip` cases `pause/PE-21-flip-is-directory`, `gate/clear-PE-21-directory`,
  `run/crash/PE-21-brief-is-a-directory` and `run/crash/PE-21-flip-is-a-directory`; the log and
  lock modes by `internal/supervisor` unit tests.
- PE-22: the bridges' CLIs and every `procs.RunSync` child (the supervisor's channels and
  `flip`/`worker start` children, `flip run`'s launcher, kickoff's self-execs, the doctor's probes,
  the usage feeds' `sqlite3`, `opencode auth list` and `security`) run in a process group of their
  own (setpgid); the JS leaves them in its own group. The timeout, a forwarded signal and the PE-10
  SIGKILL go to that whole group, so a CLI's tools stop with it. Once a child that orch cancelled
  (timeout, signal, overflow) has exited, and before orch reaps it (the exited leader still holds
  the group id, so the id cannot have passed to another group), its group is SIGKILLed once more,
  so a member that ignored SIGTERM does not outlive it. orch waits for the exit without reaping
  through `waitid(WNOWAIT)` on Linux and kqueue's `NOTE_EXIT` on macOS; on another system, or
  should that wait fail, it reaps first and kills after, as before. A child that exits on its own
  leaves its descendants running, as in the JS. A descendant that starts a session of its own
  (`setsid`, a detached worker or usage refresh) leaves the group and survives in both runtimes.
  The group is a background job of the terminal: RunSync children read `/dev/null` and write to
  pipes, so none stops on SIGTTIN/SIGTTOU, but a child that opens `/dev/tty` itself (ssh, a git
  credential prompt, sudo) would stop until its timeout, or forever without one. A Ctrl-C on an
  interactive `orch doctor` or kickoff therefore reaches orch alone, and so does a SIGINT, SIGTERM
  or SIGHUP sent to orch's pid: while a RunSync child runs, orch forwards each of the three that
  it does not ignore (a nohup'd orch keeps ignoring SIGHUP) to the child's group, SIGKILLs the
  group one PE-10 grace later if the child has not exited, and once the child is reaped, ends by
  the first signal it received, as node ends on a Ctrl-C. A signal sent to orch's pid alone thus
  stops the child too, where the JS orphans it. No current caller needs the terminal; kickoff's
  interactive conductor and its inherited self-exec keep orch's group. Asserted by
  `internal/procs`, `internal/usage` and `internal/bridge` unit tests.
- PE-23: `orch worker stop` signals only the runner that holds the lane: the kernel must report
  a record lock on `<lane>.lock` (`F_GETLK`) whose holder is the state's `pid` (PE-14 still
  applies to that pid). A running worker holds the lock for its whole run, and the kernel drops
  it when the runner exits, SIGKILL and host crashes included, so a finished state, a killed
  runner whose lock file and `running` state remain, a lock file naming the pid as text only, or
  a lock held by another process prints `{"lane":…,"stopped":false,"reason":"no live worker"}`,
  and a reused pid is never signalled. A lock that cannot be probed holds no pid. The JS signals
  any live pid in the state. Extends PE-14. Residual: the microseconds between the probe and the
  `kill`, in which the runner can exit and its pid be reused (pidfd is Linux-only; macOS has no
  equivalent). The runner owns the lane, not its bridge's process group (contract C6): a runner
  killed with SIGKILL frees the lane even if its bridge's group lives on, and `stop` then leaves
  that group alone. Asserted by `internal/worker` unit tests (a helper process holding the lock:
  its group is interrupted; killed holder, text-only lock and another holder: nothing is
  signalled) and the `worker` parity cases `stop/PE-23/*`.
- PE-24: when a run fails, `orch worker start` replaces token-shaped text
  (`\b(?:sk|rt|at|eyJ)[A-Za-z0-9._-]{20,}`, the incident log's `<token>` rule) with `<token>` in
  every string of the state's `errors`, at any depth (inside nested arrays and objects), and in
  the blocked message body. The JS persists both verbatim. `result_summary` is unchanged in
  both, and so is the run record file, which keeps the errors and the model's text verbatim: both
  are owner-only (0600, PE-21). Asserted by the `worker` parity case
  `start/PE-24/failed-run-scrubs-tokens`.
- PE-25: `orch incident record --request -` reads one JSON object `{component, kind, summary,
  detail, context, project}` from stdin and records it as the flags would, so no free text reaches
  argv. Combining it with `--component`, `--kind`, `--summary`, `--detail`, `--ctx` or `--cwd` is a
  usage error (exit 2). `bin/incident.mjs` has no such option. An absent, `null` or empty text
  field takes the flags' default; `context` values are strings, numbers, booleans or `null`; an
  unknown key, a value of another type, invalid JSON or a non-object is a usage error (exit 2, one
  line on stderr); a missing summary fails as `record` does. Asserted by the `incident` parity cases
  `record/request-PE-25` and `record/request-equals-flags-PE-25`.
- PE-26: the reply hint of `orch mailbox inbox` and `orch watch` read `orch mailbox reply` only
  when `orch` resolved on `PATH`. Superseded by PE-19: the hint is always
  `orch mailbox reply <id> "..."`. The parity cases
  `mailbox/inbox/orch-reply-hint-unconditional-PE-19` and
  `watch/questions/orch-reply-hint-unconditional-PE-19` keep a stub `orch` on `PATH` and compare
  the pair byte for byte under PE-19, hint included.
- PE-27: Codex windows are mapped by duration (owner bug a90ac3eb), which the `9e3a456` JS
  predates. A duration-labeled single-window reading appends `applicable: false` to its missing
  slot; two-window and unreadable outputs are unchanged. This applies equally to `rateLimits`
  and `rateLimitsByLimitId.codex`. The only existing oracle golden affected is `codex-by-id`:
  `postOracleDeltas` in `internal/usage/codex_test.go` first asserts the golden has no
  `weekly.applicable`, then adds `false` for comparison. The golden stays the immutable oracle
  output; do not regenerate it from later JS. Hand-written unit tests and raw fixtures cover
  weekly-only, five-hour-only and swapped windows, routing, prompt, doctor, flip, budget reset
  and snapshot behavior. The parity reference and generated caches are unchanged.
- PE-28: `orch worker start` (foreground) claims its lane with a kernel record lock on
  `<lane>.lock` (`internal/ownlock`), held by the runner process for its whole run, and writes
  its pid into the file as metadata only. A lane is locked only while a runner holds that lock: a
  lock file left by a runner that died (SIGKILL or a host crash included) is reclaimed by the
  next start, where the JS refuses any existing file until a person deletes it. Any other failure
  to claim is still the lane-locked refusal, exit 3, as in the JS. The lock is released on every
  path once claimed, before the final line; a failed release is reported on stderr as
  `orch worker: releasing the lane lock: <error>` and does not change the exit code (the kernel
  lock goes with the process anyway, and a left file is reclaimed). The refusal's hint is
  `inspect worker status; the lane frees itself when its runner exits`; the JS's says to remove
  the lock once the process is gone, and a lock file is never to be removed by hand (contract
  C8): deleting a held one would let a second runner claim the lane. Two runners started on one
  lane at once: exactly one runs, the other exits 3. Asserted by `internal/worker` helper-process
  tests and the `worker` parity cases `start/locked/*` (hint rewritten for the comparison) and
  `start/PE-28/stale-lock-is-reclaimed`.
- PE-29: `orch worker start --detach` only probes the lane's lock (`F_GETLK`) and never touches
  it: a lock a runner holds is the synchronous lane-locked refusal (exit 3, same JSON); a lock
  file no process holds is left for the runner's own claim, where the JS unlinks it after reading
  a dead pid, so two JS launchers could remove each other's fresh lock and leave two runners on
  the lane. The probe is advisory (contract C9) and the runner's claim decides: when two
  launchers both find the lane free, both print their launch and exit 0, and the runner that
  loses writes the lane-locked refusal into its log and exits 3. The losing launch's `run_id` is
  still sound for a caller waiting for the lane to be served, since the winning runner started
  after the loser's probe. A lock path that is not a regular file fails the launch with the
  probe's error (exit 1), where the JS fails reading it (EISDIR). Asserted by `internal/worker`
  unit tests (a SIGKILLed holder's lane is reclaimed by a detached runner) and the `worker`
  parity cases `detach/locked/*`.
- PE-30: `orch supervisor` owns its project with a kernel record lock on `supervisor.lock`
  (`internal/ownlock`), held by `serve` for its life and released when it stops; the pid in the
  file is metadata only. Of any number of concurrent serves exactly one claims the project and
  the others print `a supervisor already owns this project` (exit 1); the JS claims with an
  exclusive create, else reads the pid and replaces a dead one's file by rename, so two
  reclaimers could both win (sol-5 R2). Every reader asks the kernel (`F_GETLK`), never the file
  or the liveness of the pid it names: `start` reports the kernel's holder as `already running`;
  the tick's lane phase `stale_lock` needs a runner to hold `<lane>.lock`; the flip evaluation's
  pending or never-started relaunch needs no runner to hold `workers/conductor.lock`; and
  `remote apply`'s resume waits for a process to hold `supervisor.lock`. So a lock file no process
  holds (a serve or runner killed with SIGKILL, or pid text a pre-S13 process left) is free: serve
  and start claim it in place (it keeps its mode), a finished lane with such a lock file is
  `finished`, not `stale_lock`, and it no longer blocks a conductor relaunch, where the JS treats
  the file's presence (or, for the supervisor lock, a live pid in it) as held. A lock that cannot
  be probed (not a regular file) is the error: `serve` and `start` exit 1 with `lock
  "supervisor.lock" is not a regular file`, a tick records it as its `storage_error`, and `remote
  apply` rejects the resume. `orch worker status`'s `lock_present` keeps its file-exists meaning
  (contract C5). Asserted by `internal/supervisor` tests (helper-process serves: exactly one of
  four concurrent claimers serves; a helper-held lock refuses serve and is reported by start;
  pid-text lock files are claimed; a stale `conductor.lock` relaunches), `internal/remote/apply`
  and `internal/cli` tests, and the `supervisor` parity cases `serve/PE-30-unheld-live-pid-lock`,
  `serve/PE-30-unheld-lane-lock` and `start/PE-30-unheld-live-pid-lock` (the JS output pinned).

- PE-58: every child orch starts in a directory (`--cwd`, a lane's or supervisor's project
  root, or a bridge's `cwd`) gets one `PWD` set to that directory, replacing any inherited or
  explicit `PWD`. Relative directories resolve against the physical cwd; other environment
  entries, including claude's removal of `CLAUDECODE`, are preserved. The JS left the parent's
  `PWD`, so a supervisor started from another checkout made a `$PWD`-trusting CLI (OpenCode)
  attach to that checkout. Asserted by `TestWithPWD`, `TestStartSetsPWDToDir`,
  `TestDetachedSetsPWDToDir`, ci's `TestRunSetsPWDToDir`, and
  `TestRunClaudeChildPWDIsTheProject`; the process tests observe non-shell children.

- PE-32: flip state reads distinguish absence (ENOENT), a present JSON record (including
  null and primitives), and `*flipcmd.StateError` (filesystem code or `INVALID_JSON`).
  `decide` fails closed with `{"action":"gate","reason":"flip state unreadable: <code>; fix or remove .orchestrator/flip.json"}`,
  exit 0 without routing. Gate, clear, pause, run, reply gate clearing and remote apply refuse
  unreadable state and leave its bytes unchanged; the supervisor logs once and does not relaunch.
  All writers use `Project.Update` on the separate, persistent `flip.json.lock` and atomically
  replace the data with `WritePrivateFile`. Callback errors write nothing; `ErrNoUpdate` skips
  a write successfully without masking lock-release errors. Callbacks neither
  nest an Update nor launch children. Reply question matching and clearing happen in the same
  transaction. An unreadable gate makes terminal reply exit 1 even if its note/ack was already
  posted; a remote reply remains retryable and is finished without duplicating its note after
  repair. Remote resume validates the gate under a transaction, starts/waits for the supervisor
  outside it, then transactionally rechecks and clears that gate; a changed gate supersedes the
  resume and a failed supervisor start leaves it set. Unit tests cover corrupt and mode-0000
  state, exact decisions/exits, untouched bytes, serial gate/clear/pause helper processes and
  `ownlock.Probe` at each launch boundary. Parity pins the frozen JS's fail-open behaviour and
  masks only the writer's empty, unseeded transaction sidecar.
- PE-33: `orch flip run --unless-gated` checks for `awaiting_human` inside its write transaction.
  A gate returns `{"flipped":false,"reason":"awaiting human"}`, exit 1, without writing state,
  a continue brief, the git exclude or a handoff, and without launching. All three supervisor
  relaunch paths pass this flag, so a gate installed after the supervisor's snapshot survives.
  Explicit human and kickoff runs keep clearing gates. A lane-locked launch adds its pending
  record in a second Update against current state, preserving a gate installed during launch.
  The fake launcher probes the sidecar without opening it and proves the lock is released.
  The frozen JS ignores the flag and clears the gate; the parity case pins that old result.
- PE-34: `gate` treats valid non-object JSON as an empty object rather than throwing when
  assigning `awaiting_human`. The internal gate operation defaults a non-string reason to
  `human approval required` and posts the usual `awaiting human: approval` handoff. CLI bare
  reasons are refused first by PE-35. Unit tests pin the fallback and notification; primitive
  parity cases pin the JS TypeErrors and assert the Go gate and handoff.
- PE-35: a trailing bare `--active`, `--from`, `--reason`, `--question`, `--resume`, `--to` or
  `--brief` prints `--<option> requires a value` (one stderr line), exits 2 and writes nothing.
  The frozen JS's boolean-valued records, omitted bare-reason handoff and `--resume true`
  launch remain pinned in the affected parity cases; they no longer reach the CLI's writers.
- PE-36 (S13e, sol-5 R5): the supervisor never writes `workers/<lane>.json` or
  `conductor.json`. Relaunch bookkeeping lives in `supervisor-state.json` as
  `redispatch: {<run key>: {attempts, last_attempt_at, confirmed_run_id}}`, using the lane's
  `run_id || "<pid>:<lane>"` as text; a pending launch records under the conductor slot key
  (S13o v3 retired `pending:<at>` keys, PE-60). Entries with no current worker run or conductor
  slot key are pruned after the lane loop, a leftover `pending:` key included. Legacy truthy `redispatched_at`
  remains a read-only marker that the lane's restart or redispatch is done: it gates deferred-lane
  redispatch and the conductor restart conditions (failed, deferred, interrupted, finished,
  exited without a final status; PE-60) only, never a decided `flip`/`handback` or a paused-project relaunch (S13r). Fake launchers publish replacement final state before
  returning; tests assert byte-identical lane files, including on the conductor path.
- PE-37 (S13e, sol-5 R7, L24): an attempted launch records its outcome after the child returns
  and before diagnostics. Success needs exit 0 and the last stdout JSON object to contain a
  string `run_id` and numeric `runner_pid`; a conductor/pending answer additionally needs
  `flipped: true` and those fields at the answer's top level (PE-63); its 400-unit `launcher`
  quote is display text and is not parsed (S13r). Success confirms once.
  An unconfirmed key retries after `min(60 s × 2^(attempts-1), 30 min)`; a worker-lane key
  stops after five failures (conductor and pending keys then retry slowly, PE-60) with one
  `blocked` note: `human` for conductor/pending keys, `coordinator` for other
  lanes, with 500 UTF-16 units of the last output and a manual retry command. The decided
  conductor path also retains its project-wide 600 s cooldown; never-started launches retain
  their separate 120 s backoff. `flipped: false` with `pending: true` or `reason: "awaiting human"`
  is not an attempt and stamps neither attempts nor `last_relaunch_at`. Fake clocks verify
  60/120/240/480 s waits, recovery, confirmation, the stop, recipients and pruning. Parity
  asserts affected launch records before comparing against the frozen JS's old behavior.
  When the conductor value is absent, null or primitive, its slot key is `conductor:none`.
  Pruning always retains the current conductor slot key, using the same read as flip
  evaluation; an array is read as an empty object and keeps the normal object derivation.
  The synthetic key is pruned on the next tick after a real conductor run is published.
  S13p's kickoff gate refusal and S13q's launch-option carry-over remain open.
- PE-38 (S13e, L21): a failed redispatch or blocked-note post adds one supervisor diagnostic
  line and continues the tick; a failed diagnostic append is ignored because the attempt
  is already in state. A normal relaunch-log error still fails that tick, with the in-memory
  record persisted on the next successful state write. Unit tests cover both storage paths;
  the frozen JS instead aborts on a redispatch post error.
- PE-63 (S13r, E-L2): a `flip run` launch answer gains top-level `run_id` (string) and
  `runner_pid` (number) after `launcher`, copied from the launcher's last stdout line when the
  launcher exited 0 and that line is an object holding both; otherwise neither is added.
  Pending, gated and failed answers are unchanged. The supervisor confirms a conductor or
  pending launch from these fields alone, so a project root long enough to cut the 400-unit
  `launcher` quote no longer counts a real launch as failed. The supervisor self-execs the same
  binary, so it has no fallback to the nested quote. Parity: every successful `flip/run/launch`
  case asserts the two fields on the orch side (matching the detach line in `launcher`), the
  frozen JS answer without them as the old behaviour, then compares the rest; unit tests cover
  a 600-character root.
- PE-64 (S13r, E-N1): a failed deferred-lane redispatch attempt 1-4 posts the note subject
  `<lane> re-dispatch failed (attempt <n> of 5)` with the same body (500 UTF-16 units of the
  launcher's output); a confirmed redispatch keeps `<lane> re-dispatched after budget reset`, and
  the fifth failure posts only the PE-37 `blocked` note. The frozen JS posted the success subject
  whatever the launcher returned. No parity case launches a failing redispatch (the
  `redispatch/deferred-due` case confirms and keeps the success subject); unit tests pin the
  subjects.
- PE-62 (S13q, L19): `orch worker start` records `launch_options` after `model` in the lane
  state: each of `effort`, `timeout-min`, `agent`, `sandbox`, `permission-mode`,
  `allowed-tools` and `max-turns` that was given (truthy), as its JS value, in that order; then,
  for a `--root` lane whose child cwd is not its project root, `root` and `cwd` (both absolute).
  `force`, `run-id`, `log-path` and `detach` belong to one run and are never recorded. A
  budget-deferred lane's redispatch appends `--<key> <value>` for each entry, in key order, after
  the brief, harness, lane, breadth, `--cwd <root>`, `--detach`, model and session it passed before,
  so a `--root` lane is redispatched with `--root R --cwd C` (last wins). A worker state without an
  object `launch_options` (written by an older `orch`) is redispatched as before. The frozen JS
  dropped every other option. Parity: every worker start, detach and supervisor launch case that
  writes a state pins its exact `launch_options` on the orch side (the frozen JS states have none)
  before comparing the rest; `supervisor/serve/launch/redispatch/PE-62-replays-launch-options`
  pins the replayed options in orch's claude argv and replacement state, and the PE-42 `--root`
  cases pin `root`/`cwd`.
- PE-49 (S13k, L15): `orch supervisor` with a command other than `start`, `serve`, `status`,
  `stop` and `ack` prints `commands: start | serve | status | stop | ack --event ID` on stderr
  and exits 2, as with no command; the frozen JS prints nothing and exits 0. Parity
  `supervisor/usage/PE-49-unknown-command` pins the JS and asserts orch; a unit test covers it.
- PE-50 (S13k, L17): the tick reads `supervisor.json` once; a value that is not an object or
  array (`null`, a number, a string, a boolean) reads as `{}` (no wake channels, redispatch on,
  home `opencode`) and the state records `config_error: "supervisor.json is not an object; read
  as {}"`, removed by the first tick that reads an object or array. The frozen JS throws at the
  first read of a `null` config (the deferred lane's `auto_redispatch_deferred`, an event's
  wake or the flip evaluation's `home`), so the tick records only that `storage_error`. Parity
  `supervisor/serve/hand-edited/PE-50-null-config-deferred-lane` pins the JS storage error and
  asserts orch's note; unit tests cover `null`, `5`, `"x"` and `true`.
- PE-51 (S13k, L18): a worker file holding a truthy primitive (`5`, `"x"`, `true`) skips its
  lane only: its `state.lanes` entry becomes `{error: "worker file is not an object; lane
  skipped", checked_at}` and the other lanes tick. The frozen JS throws `Cannot create property
  'lane' on ...` and aborts every tick. Falsy and unparsable files are still skipped without an
  entry change, and arrays read as `{}` (PE-16). Parity
  `supervisor/serve/hand-edited/PE-51-primitive-worker-file`; a unit test.
- PE-52 (S13k, L22, L23): each tick removes the `state.lanes` entries whose `workers/<lane>.json`
  no longer exists (a lane with a falsy or unparsable file keeps its entry), and drops `null`
  entries from `state.events` while pruning. The frozen JS keeps vanished lanes forever and
  throws `Cannot read properties of null (reading 'created_at')` on a `null` event, recording
  nothing but that `storage_error` until the entry is removed by hand. Parity
  `supervisor/serve/hand-edited/PE-52-null-event` and `PE-52-vanished-lane`; the
  `realistic-state-falsy-workers` case gives its seeded lanes falsy worker files so both runtimes
  keep them; unit tests.
- PE-59 (S13o, ee50690d D1b): a `state.lanes` entry whose phase is not `running` has
  `next_check: null` and `next_check_at: null`, and an event emitted for it does not set them:
  an ended lane has no check ahead, where the frozen JS showed a time that never comes. A
  running lane is unchanged, including its mirrored freeze while a delivered reminder is
  unacknowledged. Parity (`withPE59`, by side, on every serve outcome): for each lane entry the
  run wrote (`checked_at` at or after the serve started) whose phase is not `running`, orch must
  hold literal `null` in both fields or the case fails, and only the JS's epoch/ISO pair there is
  rewritten to null; running and seeded entries and any other JS value are compared as written.
  `TestPE59EndedLaneCheck` proves a numeric orch entry fails; unit tests pin the nulls across ticks.
- PE-60 (S13o, ee50690d D1a/D1c, owner rules `5b18cc1c` and `779604e2`): an idle conductor is
  restarted unless a gate (`awaiting_human`, unreadable flip state) or a pause (`decide` answering
  `pause`) holds it. Beyond the JS's `failed`/`deferred`/`interrupted`/early `finished`, a lane
  that `finished` after a bare `handoff` (reason suffix `(previous conductor handed off without
  flip run, pause or gate)`) and a lane still saying `running`/`starting` whose runner is dead and
  whose kernel lock is free (`(previous conductor exited without a final status)`) restart through
  the same `decide` path; the frozen JS never restarts either. The first attempt of each restarted
  run key posts one `note` to `human`, `<door> conductor relaunched by the supervisor: <cause>`,
  naming the previous run, the outcome and `orch flip gate` (record `relaunch_noted`). After
  five failures a conductor slot key (pending launches included) retries every 30 min indefinitely; its `blocked` note
  repeats as `conductor relaunch still failing after <n> attempts` at most every 6 h
  (`blocked_noted_at`, stamped even when the post fails). Worker lanes keep the PE-37 stop. A
  conductor launch confirmed by its launcher whose runner never published a lane (the slot still
  shows the confirmed key, idle and unlocked, 600 s after the attempt) loses its confirmation and
  counts as a failed attempt (D1d). Conductor launches have one owner, the flip evaluation: the
  lane loop never re-dispatches the `conductor` lane, so a deferred conductor resumes through
  `flip run` (its gate, pause and launch-time gate recheck; the saved, continue or kickoff brief
  and `--resume <session>` on the same door) instead of `worker start` with the lane's own brief,
  which the frozen JS uses. Retries belong to the conductor slot key, pending launches included,
  because `flip run` replaces `flip.json` (dropping `paused` and `pending_launch`) before it
  launches. A slot record with attempts and no confirmation is itself a restart cause,
  `(previous supervisor launch did not start)`, so the slot's door is retried after the writer
  cleared what triggered it; without such a record an empty slot launches nothing. Parity:
  `flip/handed-off` runs in the cooldown, where neither side launches;
  `launch/conductor/finished-early-resume` asserts and removes orch's note before comparing; the
  pending cases assert the slot key. Unit tests in `relaunch_test.go` and
  `relaunch_lifecycle_test.go` (full `Tick` with the real `flipcmd.Project.Run` writer and a fake
  launcher) cover the schedules on a fake clock.
- PE-61 (S13p, plan 1.7 D2): kickoff's preflight stops when its `flip gate --clear --no-relaunch`
  is refused. It carries on when the clear exits 0, or exits 2 (not a project) while the root
  still has no `.orchestrator/` directory, as in a first kickoff. Any other answer (exit 1, as
  for an unreadable `flip.json` since PE-32, another code, or no status) prints
  `kickoff: the human gate could not be cleared: <first non-empty line of the clear's stderr,
  else stdout>; nothing was launched` on stderr and exits 1 before `supervisor start`, the
  exclude, the brief, the both-red pause or any launch, for usable-vendor and both-red runs
  alike; dry runs never clear. The frozen JS discards the clear's result and goes on. Kickoff
  still discards `policy unattended`'s result, as the JS does. Parity: `kickoff/PE-61/*` asserts
  orch alone (exit 1, the line, project files as seeded) and pins the JS's old behaviour: a
  corrupt `flip.json` with a usable door is rewritten to `{}` and the preflight runs on; a
  `flip.json` directory with both vendors red ends at `the pause was not recorded` with the
  brief saved (formerly `both-red/pause-fails`).
- PE-39: `orch supervisor ack` requires a truthy own key in the state's `events`.
  Inherited Object.prototype names (`constructor`, `toString`, `__proto__`, ...) now
  print `unknown event id` and exit 1 without changing receipts; the frozen JS accepts
  them. Explicit own event keys still work, including the existing safe `__proto__`
  assignment semantics: it adds no ack key unless that key already exists in the acks
  object. The entire acks read-modify-write uses `ownlock.Transaction` on the persistent
  `supervisor-acks.json.lock`, never on the data file. Concurrent goroutines and processes
  retain all receipts; the tick still reads the atomically replaced data file without a
  lock. Asserted by `TestAck`, `TestAckConcurrentGoroutines` (16 writers),
  `TestAckConcurrentProcesses` (two helpers, FIFO data-read barrier and kernel holder
  observation), and parity `ack/PE-39-own-events-only`.

- PE-40 (S13g, sol-5 R14): PATH search attempts execution of each candidate, rather than
  selecting the first file with execute permission. A script whose `#!` interpreter is missing
  no longer hides a later working executable. `Start`, `Detached` and `RunSync` continue on
  ENOENT, ENOTDIR and EACCES; if none starts, EACCES wins when encountered, otherwise ENOENT.
  Errors other than ENOENT, ENOTDIR and EACCES stop the search (for example ELOOP), unlike
  the old Go pre-check which skipped stat failures such as a symlink loop; ENOEXEC already
  stopped after the old pre-check passed. A child that starts is never retried, even when it
  exits nonzero. Names containing `/` remain single candidates with their original
  error spelling. This corrects Go's search to match Node/libuv, not a latent JS defect or a
  divergence from the frozen JS oracle. Asserted by `internal/procs`'s `TestPATH*` tests,
  including missing-interpreter fallback at all three entry points, error precedence,
  non-retry after exit 7, intact pipes and before/after fd counts on darwin and Linux.

- PE-41 (S13h, incident 9a191c0fe00d): all project-root selection uses
  `paths.ProjectRoot`: resolve absolutely and clean lexically, then truncate at the first
  exact `.orchestrator` component. Running from `<project>/.orchestrator/briefs` therefore
  reads/writes `<project>/.orchestrator`, not nested state. Explicit `--cwd`, `--root`,
  mailbox's `ORCHESTRATOR_PROJECT`, session hook cwd, incident request project and routing's
  project policy use the same rule. No symlink resolution, other ancestor search, migration
  or deletion is performed; ordinary prompt/schema/manifest input and output file paths
  retain their cwd-relative semantics. Bridge children receive the resolved project cwd,
  with the existing child `PWD` rule unchanged. Every affected parity group (usage, mailbox,
  manifest, incident, policy, bridges, worker, flip, supervisor, watch, kickoff, session)
  pins the frozen JS's nested-state behaviour and asserts Go's corrected result, including
  ignoring a stale nested flip gate. The latent nesting defect is no longer mirrored.

- PE-42 (S13i, 8cecd17d): `claude-exec`, `codex-exec` and `opencode-exec` accept
  `--artifact-root DIR` and `ORCH_ARTIFACT_ROOT` (flag wins; default is cwd), normalized by
  `paths.ProjectRoot`. The child cwd and result's `cwd` stay unchanged. Run records go to
  that root's `.orchestrator/runs` and include `artifact_root` only when different from cwd;
  refusal, usage-limit, failure and tool-host incidents identify the artifact project. Incidents
  remain in the user-wide incident log, and the detached post-run usage refresh remains
  user-wide, not a second project write. Plugin delegation forwards the session root for a
  different child cwd. `worker start|status|stop --root DIR` explicitly selects the owning
  project for lane state, locks, logs and run records. With `--root`, the child's `--cwd`
  defaults to the project root; otherwise it resolves independently, and differing roots
  generate the bridge's `--artifact-root`. Detached runners receive absolute `--root` and
  `--cwd` selectors while their process runs in the owning project. Without `--root` (or
  with `--root ""`), legacy project selection, bridge argv and `.orchestrator` normalization
  are unchanged; a bare `--root` is the same error as bare `--cwd`. Plugin `worker_start`
  supplies `--root <session project>` for a different child cwd; `worker_status` still inspects
  its cwd project. `--artifact-root`, `ORCH_ARTIFACT_ROOT`, `--root` and `--cwd` resolve against
  the invoking process's directory; pass absolute paths. Frozen JS ignores the new bridge
  environment variable and has no worker `--root`; bridges/worker parity pins these differences.
- PE-43 (S13i, 31e490b1): `manifest verify` exits 1 on a missing manifest with
  `manifest not found: <abs>; manifests live in the checkout that created them: pass an absolute path or run from that checkout (--root selects the source tree, not the manifest)`.
  The plugin preserves this diagnostic and accepts absolute manifest paths; verify source files
  from the checkout the manifest describes (`--root` in the CLI, `cwd` in the plugin).
  Manifest input paths retain their process-cwd-relative semantics; `--root` selects source
  files, not the manifest's directory. Malformed JSON, permissions, directories and absent
  arguments keep their distinct errors. Frozen JS reports a generic ENOENT instead.
- PE-44 (S13j, P6 soak 593b0b34/2370ffed, L1): bridge error events reach `errors`. codex-exec
  collects a top-level `{"type":"error","message":...}` (the message, else the event as JSON)
  and logs `[codex] error: <message>`; it does not change `ok`, as codex also reports stream
  retries this way. opencode-exec collects `{"type":"error",...}` as `error.data.message ||
  error.message || JSON.stringify(error || event)` and logs `[opencode] error: <message>`; the
  shape is opencode 2.0.32's `run --format json` failure report (`{type, timestamp, sessionID,
  error: {type: "unknown", message}}`, read from the installed bundle; `data.message` covers a
  session error object). A non-zero exit then adds the stderr tail only when no error event
  arrived. A ChatGPT usage limit is filed kind `limit` with its text, not `crash` with
  `opencode bridge failed: `. The JS ignored both events. Parity: `bridges/codex/PE-44/*` and
  `bridges/opencode/PE-44/PE-47/*` pin the JS and rewrite orch's differing text; the top-level
  error left `codex/events/odd-shapes`.
- PE-45 (S13j, Q-S13-5): a failed run that the bridge itself stopped at its `--timeout-min` cap
  is filed kind `timeout` with summary `<harness> bridge timed out after <n> min` (`n` is
  `Number(--timeout-min || default)`), not kind `crash`; the result, run record and the
  incident's detail are unchanged. A run that ends at the same time for another reason is still
  filed as the cap. Parity: the `PE-9/PE-45/timeout-*` cases of all three bridges.
- PE-46 (S13j, L2/L3/L6): `opencode-exec --help` prints its help and exits 0 without running
  anything; `--effort X` is read as a value and ignored, because `opencode run` has no effort
  option (its `--variant` is model-specific), and `orch worker` passes `--effort` to every
  harness. The JS ignored `--help` and ran opencode with `X` as the prompt. Parity:
  `bridges/opencode/PE-46/*` and `worker/start/opencode/PE-46/effort-is-not-the-prompt`; the
  worker's PE-13 unread-prompt case keeps the JS pinned with a 100 KiB brief, under Linux's
  128 KiB limit on one argument, because orch's opencode-exec now reads the prompt.
- PE-47 (S13j, L4): opencode-exec's failure incident records `context.model` from `--model`
  (or `null` without it); its result still has no `model` key. The JS always recorded `null`.
- PE-48 (S13j, L5): `orch worker`'s usage line names `--harness codex|claude|opencode`; the JS
  listed `codex|claude` though it accepts opencode. Parity: the worker `usage/*` cases and
  `detach/detach-as-a-value` rewrite this text only.
- PE-65 (S13j amended, plan go-s13s 3.4, D3): opencode-exec stops opencode with SIGINT: at the
  `--timeout-min` cap, after a crash in its line handler, and in place of a SIGTERM it forwards
  (SIGINT stays SIGINT). Evidence: a SIGTERM left the session running in `opencode serve
  --service` for 8 and 13 minutes while the supervisor had already relaunched a second conductor;
  SIGINT (`orch worker stop`) interrupted it at once, 3 times out of 3. claude-exec and
  codex-exec keep SIGTERM. Not visible in parity (the fake dies of either);
  `TestBridgeStopSignals` traps INT and TERM separately.

Latent JS defects `orch worker` mirrors on purpose: a stale `last_state_write_error` stays after a later successful
write; and the worker posts only `blocked` and `deferred` messages, never the `release` its header
comment mentions (the agent posts its own). These are to be fixed in Go (S13 plan)
(Q-S6-6).

Those `orch supervisor` mirrors (S8a part): the tick (run by `serve`) keeps the JS order and
partial writes. This is to be fixed in Go (S13 plan, Q-S8-3).

Those `orch doctor` mirrors: the JS row texts (`claude login` has no detail, `codex login` prints
the whole trimmed output, the anthropic row is OK either way); a registry read failure worded as
Node's (`ENOENT: no such file or directory, open '<file>'`, in every command that reads the
registry); a role whose tier has no string `opencode` model fails `usage/routing` with the JS
TypeError (`Cannot read properties of undefined (reading 'split')`) where an empty one prints
`undefined`.

Those `orch watch` mirrors (plan go-s9 3.4, incidents recorded, to be fixed in Go (S13 plan)):
a `flip.json` holding only `awaiting_human` (what `flip gate` writes on a project that never
flipped) crashes a single render with `TypeError: Cannot read properties of undefined (reading
'slice')` and makes `--follow` print that message every interval without ever drawing a frame; the
`next:` line takes the rest of the `## Next action` heading line, so `## Next action (exact)`
prints `next: (exact)`. A role whose model is not a string throws as in `orch doctor`.

Those `orch kickoff` mirrors (plan go-s9 3.4, incidents recorded, to be fixed in Go (S13 plan)):
`--door` is not validated, so an unknown door (or a bare `--door`, the text `true`) crashes
with `TypeError: Cannot read properties of undefined (reading 'overall')` after the preflight (with
`--dry-run`, which skips the preflight, before any write). The formerly mirrored both-red
supervisor-start defect is fixed: kickoff preflights and records a pause naming its saved brief,
or exits 1 if either step fails. The ignored gate clear is fixed too (PE-61). One divergence is not mirrored: when `orch`
cannot find its own executable (`os.Executable()` fails), each self-exec has no status and no
output, so the gate clear counts as refused (PE-61): orch prints `kickoff: the human gate could
not be cleared: <the os.Executable error>; nothing was launched` and exits 1 before any
supervisor line, exclude, brief, pause or launch, where the JS, whose `spawnSync` result would
then hold `null` output, dies of a `TypeError` on `.trim()`.

`ORCH_JS=/path/to/orchestrator ./parity/run.sh` compares against another JS tree, and
`ORCH_JS_REF=<commit-ish> ./parity/run.sh` against `orchestrator/` at another commit.

## JSON compatibility

All JSON `orch` writes goes through `internal/jsjson`, which matches Node's `JSON.stringify` byte
for byte: JS property order (canonical array-index keys first, ascending, then insertion order),
ES number formatting, and no escaping of `/ < > &`, U+2028, U+2029 or non-ASCII text. The single
known divergence: a lone UTF-16 surrogate escape (`"\ud800"`) parses to U+FFFD, because a Go
string cannot hold it.

Prompt files, stdin and mailbox argv are decoded like Node's UTF-8 Buffer decoding: malformed
sequences become U+FFFD (an incomplete multi-byte prefix produces one replacement).
`jsjson.Unmarshal`
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
malformed-input cases that are not mirrored. One is not: a `codex app-server` that exits before
reading its first request makes the JS die of an uncaught `Error: write EPIPE` (no error listener
on the child's stdin; incident 1bb84b8fe3dd), where `orch` ignores the write error and times out
as the JS otherwise would; the parity fake codex reads that request first, so no case races on it.

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

`testdata/usage/*.golden` are generated by `ORCH_JS=<tree> node testdata/usage/gen.mjs` through the JS
normalizers, from the redacted feed recordings beside them (no tokens, no account identifiers).
The `routing`, `usage` and `incident` generators load the JS libs from `ORCH_JS`, an `orchestrator/`
tree such as the reference: `git archive $(cat go/parity/js-ref) orchestrator | tar -x -C <dir>`,
then `ORCH_JS=<dir>/orchestrator`.

`testdata/incident/corpus.json` is generated by `ORCH_JS=<tree> node testdata/incident/gen.mjs` (last run with
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
captured, redacted (`testdata/redact.mjs`) and generated is in its `PROVENANCE.md`. There are
no codex or opencode captures yet; the `codex` and `opencode` bridge subgroups script their
events inline.
