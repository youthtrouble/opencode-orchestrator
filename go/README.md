# orch: Go runtime of the orchestrator

`orch` is one static binary whose subcommands replace `orchestrator/bin/*.mjs` one for one,
released as [v0.5.0](https://github.com/youthtrouble/opencode-orchestrator/releases/tag/v0.5.0)
(2026-10-19). Every script is ported, `install.sh` installs `orch` (see [Install](#install)), and the
plugin runs on it when its runtime is `go` (see [Plugin runtime](#plugin-runtime)). The JS runtime
stays the default and both share the same `.orchestrator/` and usage state, so they can run side
by side. What remains is removing the JS (S12), planned for 0.6.0 behind a human gate.

For what the orchestrator does and how to use it, see the [root README](../README.md); for the
scripts' own behaviour and state files, [orchestrator/README.md](../orchestrator/README.md).

Commands (every `bin/*.mjs` script):

| Command | Replaces | Behaviour |
|---|---|---|
| `orch version` | — | Release number plus VCS revision. |
| `orch usage [--refresh] [--json]` | `bin/usage.mjs` | Byte-identical output; reads and writes the same usage cache, so either runtime can read what the other wrote. A failed vendor reading (a 429 from the Claude usage endpoint, a codex error) is replaced by the last good cached one, marked `last_good_at` (when it was read) and `read_error`, if the cache was written less than 30 minutes ago. The window rolls: each failed refresh rewrites the cache, so refreshes that keep failing less than 30 minutes apart keep a reading of any age (as in the JS); check `last_good_at`. |
| `orch routing [--refresh \| --cached] [--json]` | `getRouting()` in `lib/usage.mjs` | The routing prompt text, or `{usage, registry, channels, resolved, prompt}` with `--json`. `--cached` never collects and exits 3 when there is no cache; bad flags exit 2. |
| `orch budget gate --vendor anthropic\|openai [--force] [--frontier]` | `budgetGate()` in `lib/budget.mjs` | Compact `{allowed, level, resets_at, note}`; exit 0 whether or not the work is allowed, 2 on bad flags or an unknown vendor. |
| `orch effort --role ROLE [--size SIZE]` | `effortFor()` in `lib/routing.mjs` | The effort-policy value as plain text, or `null`; exit 2 on bad flags or without `--role`. |
| `orch mailbox post\|list\|ack\|reply\|inbox` | `bin/mailbox.mjs` | Byte-identical output and `.orchestrator/mailbox/*.jsonl` lines (one `write` per line on an append-only descriptor, so concurrent JS and Go appenders do not interleave). `post --json` prints the whole message instead of `{id, at}` (PE-3). `inbox` names `orch mailbox reply` in its reply hint when `orch` is on `PATH` (PE-26). A `reply` to the message that the human gate's `awaiting_human.question` (a non-empty string in `flip.json`, a full id or a prefix) resolves to, as the reply's own id is resolved, clears the gate as `orch flip gate --clear` does and prints `cleared the human gate; the supervisor relaunches the <door> conductor within a minute` (`cleared the human gate` when the kept `pending_launch` names no door); a question that resolves to no single message is reported on stderr as `could not match the human gate's question: <ambiguous message id prefix X \| no message with id X>` and clears nothing; a `flip.json` that exists but cannot be read or parsed is reported on stderr as `could not check for a human gate: ...`; the reply still exits 0. Uncaught JS errors print only their error line (see "Crash output and parity exceptions"). |
| `orch manifest create\|verify\|diff` | `bin/manifest.mjs` | Byte-identical output, exit codes and manifest files: the same walk (symlinks, FIFOs and other non-regular entries inside a walked directory are skipped and never opened; an explicit symlink argument is followed), UTF-16 key order and `manifest_sha256`. `create` replaces its output through a private (0600) temp file and a rename, the same in both runtimes, so a symlink at `--out` is replaced, never followed. Each runtime draws its own random temp name, so a failed rename (a directory at `--out`) prints the same error line except for that name's `.tmp-<random>` suffix, which the parity check normalizes. Uncaught JS errors print only their error line. |
| `orch incident record\|list\|report\|share` | `bin/incident.mjs` | Byte-identical output, exit codes, incident log lines and share bundle: the same redaction (home directory, `/Users/<name>`, `/home/<name>`, emails, tokens, uuids, `ses_` ids, with JS white space and a 2000-unit cut), fingerprint (JS `toLowerCase`, final sigma included) and report order (V8's TimSort, `internal/jssort`, so ties and incomparable timestamps order as in Node). Appends to `~/.local/state/opencode/orchestrator/incidents.jsonl` like the mailbox does; an explicitly empty `HOME` is kept as Node keeps it (the log is then relative to the working directory and redaction uses an empty home), never replaced by the account's home. Entries carry `orch`'s version instead of the checkout's git HEAD (PE-4). `share` writes no `project` key and uploads nothing. `record --request -` (orch only, PE-25) reads the whole incident as one JSON object on stdin, so no summary or context reaches argv. |
| `orch policy show\|set key=value...\|unattended [--cwd DIR]` | `bin/policy.mjs` | Byte-identical output, exit codes and `.orchestrator/policy.json`: the registry's `autonomy` (without `$comment`) overridden by the project file; `set` refuses `key=never` except `force_push`, and any `force_push` other than `never`. The registry is read as `JSON.parse` of the file, then `.autonomy`, as the JS does (Node's fs errors, a `SyntaxError`, a `TypeError` for `null`, no autonomy for any other shape); `set` has written its file when that fails. A `policy.json` holding an array makes `set` fail instead (PE-6). |
| `orch claude-exec [--model M] [--effort E] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/claude-exec.mjs` | Byte-identical argv passed to `claude`, stdout JSON, `[claude]` progress lines, exit codes (0, 1, 2, 75), `.orchestrator/runs/*.json` record, incident lines and usage cache: the same option values (a trailing bare value key is the JS `true`: `--model` alone runs model `true`, `--timeout-min` alone is one minute), budget gate and refusal, event handling (lines split as readline splits them, U+2028 and U+2029 included; UTF-16 cuts; `tools_used` property-key quirks), stderr tail, timeout with Node's timer clamp, usage-limit path with its awaited refresh, and `CLAUDECODE` removed from claude's environment. Differences: a bare `--prompt` is refused (PE-8); no Node warnings (PE-9); claude is killed 10 s after a signal it ignores and its pipes are waited on for at most 10 s (PE-10); SIGTERM/SIGINT are forwarded to claude and the run is reported (PE-11); claude runs in a process group of its own, which the timeout and the PE-10 kill reach whole (PE-22); the run record and `runs/` are owner-only and written atomically (PE-21); the post-run usage refresh runs detached and completes (PE-12); U+FFFD where a UTF-16 cut leaves a lone surrogate (PE-1). Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch codex-exec [--model M] [--effort E] [--sandbox S] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/codex-exec.mjs` | Byte-identical argv passed to `codex` (`exec --json ... -`, with `--approve-for-me` instead of `-s` exactly when the sandbox is `workspace-write`, and the `exec`-only options before `resume <id>`, as codex-cli 0.162.1 requires), the prompt written to codex's stdin, codex's working directory and whole inherited environment, stdout JSON, `[codex]` progress lines, exit codes (0, 1, 2, 75), run record, incident lines and usage cache: the same option values (a bare `--model` is the JS `TypeError: model.includes is not a function` before the gate; a bare `--sandbox` is `-s true` without `--approve-for-me`), budget gate (frontier is a case-sensitive `astra` substring) and refusal, event handling (a `thread.started` without `thread_id` leaves the key out of the JSON yet keeps its place; absent `command`, `exit_code`, `path` and `kind` are left out; `turn.failed` serialized with JS key order and numbers), stderr tail, timeout with Node's timer clamp, and the tool-host no-op path with its two incidents. Differences: PE-1, PE-8 to PE-13, PE-21 (the run record), PE-22. Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch opencode-exec [--agent A] [--resume ID] [--model M] ... [--prompt TEXT \| --prompt-file FILE \| TEXT... \| stdin]` | `bin/opencode-exec.mjs` | Byte-identical argv passed to `opencode` (`run --format json --auto --agent A ...` with the prompt last), its working directory and whole inherited environment (stdin on `/dev/null`), stdout JSON, `[opencode]` progress lines, exit codes (0, 1, 2, 75), run record and incident lines: the same option values, budget gate and refusal, event handling (the first truthy `sessionID` unless resuming, the last text part), the bare stderr tail of a failed run (`""` included), and a timeout that sends SIGTERM without a log line. Like the JS it has no `--help` (the flag is ignored and the run proceeds), no `--effort` key (`--effort X` makes `X` the prompt), no start or done line, an incident `context.model` that is always `null`, and no post-run usage refresh. Differences: PE-1, PE-8 to PE-11, PE-21 (the run record), PE-22. Uncaught JS errors print only their error line, after the progress lines printed so far. |
| `orch worker start --brief FILE --harness claude\|codex\|opencode [--lane L] [--model M] [--effort E] [--sandbox S] [--permission-mode P] [--allowed-tools T] [--max-turns N] [--timeout-min N] [--resume ID] [--agent A] [--breadth B] [--run-id ID] [--log-path F] [--force] [--detach] [--cwd DIR] \| status [--lane L] [--cwd DIR] \| stop --lane L [--cwd DIR]` | `bin/worker.mjs` | Byte-identical output and exit codes over the same `.orchestrator/workers/<lane>.json` and `<lane>.lock`. `start` (foreground) validates as the JS does (usage, `brief must be an existing .md file`, `harness must be codex, claude or opencode`, exit 2), reads the brief before it claims the lock (an unreadable brief is the JS error, exit 1, and leaves no lock or state, even on a locked lane), claims the lock (`{"error":"lane locked",...,"hint":...}` on stderr, exit 3, a stale lock is not reclaimed), publishes the state with the JS keys in the JS order (`state_storage` from the second write, `last_state_write_error` after a failed one), runs `orch <harness>-exec` (the JS runs `node <harness>-exec.mjs`) in a session of its own with the same arguments and the same prompt on stdin (the preamble and the brief, or the brief alone for the `conductor` lane), relays the bridge's stderr to stdout while recording `session_id`, `last_progress_at` and `last_tool`, and ends deferred (exit 75 or `budget_red`: limit incident, `deferred` message), finished (no message) or failed (incident, `blocked` message with the run record as ref), then prints `{lane, status, session_id, exit_code}` (exit 0 for finished or deferred, else 1). SIGINT and SIGTERM publish `interrupted` and are passed on to the bridge's process group. `status` lists the given lane, or every `*.json` in byte order, each state spread as the JS spreads it (any JSON value; unreadable or falsy as `{lane, status: "unknown"}`) with `process_alive` (Node's `process.kill(pid, 0)` coercion: an integer in the int32 range, EPERM alive) and `lock_present`; `stop` sends SIGINT to the stored pid's process group, else to the pid. Lane names are the JS values (a bare `--lane` is the lane `true`, no `--lane` on `stop` reads `undefined.json`); a lane containing `/` is refused by `start` (after the harness check), `status` and `stop` with `lane must not contain "/"` on stderr, exit 2, before anything is read or written (both runtimes; `.` and `..` stay names inside `workers/`). Differences: `stop` signals only a pid of at least 2 (PE-14) that the lane's lock holds (PE-23); the state is written owner-only through a private temp and a rename, and `workers/` and `logs/` are 0700 (PE-21); a failed `start` writes `<token>` for token-shaped text in the state's `errors` and the blocked message (PE-24); `start` survives a bridge that never reads its prompt (PE-13), waits at most 10 s for the bridge's pipes once it exits and never kills it (PE-10), records the Go bridge's report of an interrupted run (PE-11) and cuts UTF-16 text as PE-1 says. `start --detach` first refuses a lane whose lock holds a live pid synchronously, as `flip.mjs run` relies on (`{"error":"lane locked","lane":...,"previous":...}` with `alive: true` and no `hint` on stderr, nothing on stdout, exit 3), removes a lock whose holder is gone (or whose text is no pid), creates `.orchestrator/logs/<lane>-<first 8 of the run id>.log` (exclusive, 0600), and launches `orch worker` with the same arguments minus every exact `--detach` token plus `--log-path`, `--run-id` and `--cwd <absolute project root>` (last, so a relative `--cwd` is not applied twice; both runtimes) in the project root, in a session of its own, stdin on `/dev/null` and stdout and stderr on the log; it prints `{lane, runner_pid, log_path, run_id, keep_awake}` and exits 0 (`runner_pid` is left out when the runner cannot be started, as the JS never sees that error). Keep-awake wraps the runner in `caffeinate -i -s` (macOS) or `systemd-inhibit --what=idle:sleep --why=opencode-orchestrator` (Linux) when `which` finds it, unless the registry's `runtime.keep_awake` is `false` (any registry error leaves it on); `keep_awake` reports whether it was wrapped. |
| `orch flip decide --active D \| run --to D [--from D] [--reason R] [--resume ID] [--force] \| pause [--active D] [--reason R] [--brief FILE] \| gate [--active D] [--reason R] [--question ID] [--clear [--no-relaunch]] \| status [--cwd DIR]` | `bin/flip.mjs` | Byte-identical stdout, stderr, exit codes, `.orchestrator/flip.json` (indent 2; both replace it through a temp file and a rename, orch's private and owner-only, the JS's the fixed `flip.json.tmp`, PE-21), mailbox lines and, for `run`, the repository's `info/exclude`, the continue brief and everything the launched conductor worker writes. `flip.json` is read as `JSON.parse` of the file, any failure being no record, so every JSON value is handled as the JS handles it: `status` prints the record or `{"state": "never flipped"}` for a falsy one; `pause` creates `.orchestrator/` and writes `{...previous, paused: {at, door, reason[, brief]}}` (`brief` only for a non-empty `--brief`; a string spreads into index keys, a number or `true` into nothing, an existing `paused` keeps its position, `awaiting_human` is kept) and posts a `handoff` to `supervisor`; `gate` reads `flip.json` first, then creates `.orchestrator/` and sets `awaiting_human: {at, door, reason, question}` (an existing one keeps its position; an array `flip.json` is replaced by an object holding the gate, not by `--clear`, which writes an array back unchanged), deletes `paused` and posts a `handoff` to `human` whose subject keeps 80 UTF-16 units of the reason; `gate --clear` deletes `awaiting_human` and writes any other shape back as it is; when an object held a truthy `awaiting_human`, it also sets `pending_launch: {to, from, reason: "human gate cleared", resume: null, at}` with `to` and `from` the gate's door (`opencode` when it has none), so the supervisor relaunches that conductor, unless a truthy `pending_launch` is already queued (a flip queued before the gate), which is kept; it prints `{"awaiting_human":false,"relaunch":"<door>"}` with the door of the queued launch (when it has a truthy `to`) instead of `{"awaiting_human":false}`; `--no-relaunch` (kickoff) never queues one and removes any queued `pending_launch` and `paused`. `decide` prints `{action, to, reason}` from the cached routing (collecting when the cache is missing or stale, as `getRouting` does) and the time of the last flip (`flip_cooldown_minutes`); `--active` absent or empty prints `--active opencode\|claude required` (exit 2), and a non-door (`true`, `gemini`, `constructor`, `__proto__`) is red with the JS reason text (`undefined`, or the inherited Object.prototype text). `run` refuses a `--to` other than `opencode` or `claude` (`--to opencode\|claude required`, exit 2, nothing written); otherwise, in this order, it adds `.orchestrator/` to the repository's `info/exclude` (`.git/info/exclude`, or for a `.git` file, the linked worktree's common dir or the submodule's git dir) when the project has a git repository (a `.git` file that is not `gitdir: <path>` naming a directory, or whose git dir's `commondir` names no directory, is none, and nothing is written) and no line of the file is `.orchestrator` or `.orchestrator/` (lines end at `\n` and one trailing `\r` is dropped, as Git reads the file; a lone `\r`, U+2028 or U+2029 is part of a line, so such a "line" does not count and the line is appended; a newline is added first unless the file is empty or ends in one), writes `.orchestrator/briefs/conductor-continue.md` (atomically, 0600, PE-21; it launches the brief a kickoff saved instead of writing the continue brief, and names it in the handoff, when the record it replaces has one: `paused.brief`, else `pending_launch.brief`, a non-empty string naming an existing file), replaces `flip.json` with `{at, from, to, reason, previous: {at, to}}` (clearing `paused` and `awaiting_human`), posts a `handoff` from `conductor-<from>` to `conductor-<to>`, and launches the conductor by running this executable as `orch worker start --brief .orchestrator/briefs/conductor-continue.md --lane conductor --harness <to> --agent conductor --breadth broad --cwd <root> --detach` (plus `--permission-mode auto` for claude, `--resume`, `--force`) to completion, so a Go-driven flip runs the conductor on `orch worker` and the Go bridges (the JS runs `node worker.mjs`). It prints `{flipped, to, from, launcher}` with 400 UTF-16 units of the launcher's trimmed output, exit 0 when the launcher exited 0, else 1. A launcher that cannot be started (no executable path, or the start fails) gives `{"flipped":false,...,"launcher":""}` and exit 1 with no diagnostic, as the JS `spawnSync` error result has no status and no output. When the launcher refuses with `lane locked` (usually because the calling conductor holds the lane), `flip.json` gains `pending_launch: {to, from, reason, resume, at[, brief]}` for the supervisor (`brief`: the saved brief this run launched, kept for the retry) and `run` prints `{"flipped":false,"pending":true,...}`, exit 0. Option values are the JS ones: a bare `--active`, `--from`, `--reason`, `--question` or `--resume` is `true` in the file, the mailbox and stdout (`--resume true` reaches the conductor). A routing failure prints `orch flip: <error>` and exits 1, as `orch usage` does (the JS dies of the rejection with Node's crash output). Any other command prints `commands: decide \| run \| pause \| gate [--clear [--no-relaunch]] \| status` (exit 2). Differences: PE-15 (the date formats of `at`), PE-1 (the gate subject and `launcher` cuts), PE-2 (a launcher crash quoted in `launcher` as its error line only), PE-10 (`run` waits at most 10 s for the launcher's pipes), PE-20 (a launcher whose output passes 1 MiB is stopped), PE-21 (`flip.json` and the continue brief) and PE-22 (the launcher runs in a process group of its own). |
| `orch supervisor start \| serve \| status \| stop \| ack --event ID [--by NAME] [--cwd DIR]` | `bin/supervisor.mjs` | Byte-identical stdout, stderr, exit codes and `.orchestrator/supervisor-acks.json` (indent 2, written through a temp file and renamed, as the JS `writeJson` does; PE-21). `status` prints `supervisor-state.json` as `JSON.parse` reads it, whatever the value (`null`, a number, a string, an array), or `{"status": "not_started"}` when it cannot be read or parsed (missing, a directory, unparsable, a BOM), and never writes. `stop` writes an empty `.orchestrator/supervisor.stop` and prints `stop requested; workers are unaffected`, whether or not a supervisor runs. `ack` refuses with `unknown event id` (exit 1) unless `--event` is truthy and names a truthy property of the state's `events`, read as a JS property: an Object.prototype name (`constructor`, `toString`, `__proto__`, ...) is present (mirrored); then it sets `acks[<event>] = {by: <--by or coordinator>, at}` (an existing key keeps its position; a bare `--event` is the key `true`, a bare `--by` writes `true`; `__proto__` without an own key of that name adds nothing and rewrites the file unchanged, as the JS prototype setter does) and prints `acknowledged (receipt, not acceptance)`. Usage: no command prints `commands: start \| serve \| status \| stop \| ack --event ID` (exit 2); any other command prints nothing and exits 0 (mirrored). `start` creates `.orchestrator/logs/` (0700), prints `{"started":false,"reason":"already running","pid":N}` when the pid in `supervisor.lock` is alive, else removes `supervisor.stop`, launches this executable as `orch supervisor serve --cwd <root>` in a session of its own (wrapped in `caffeinate -i -s` or `systemd-inhibit` unless the registry's `runtime.keep_awake` is `false`) with its output appended to `logs/supervisor.log` (0600; a symlink there fails `start` with ELOOP, PE-21), and prints `{"started":true,"supervisor_pid":N,"log":"<path>"}` (`supervisor_pid` left out when the command cannot be started). `serve` claims `supervisor.lock` (created exclusively, 0600, with its pid; a stale lock is replaced atomically through a private temp, PE-21; a lock whose pid is dead is taken over; a live one prints `a supervisor already owns this project`, exit 1), then every 5 s after each tick: looks at every worker lane (phases, the two-phase intent, events), re-dispatches budget-deferred lanes as `orch worker start --detach`, wakes the coordinator through the configured channels (`command`, `codex-queue`, `opencode-run`, `claude-resume` as `orch claude-exec`), evaluates the conductor flip (relaunching through `orch flip decide\|run`), and records the outcome in `supervisor-state.json` (a failed tick: `storage_error` persisted once by the next successful write, `storage_failures` counting). The stop file makes it record `status: "stopped"` and `stopped_at`, release the lock and exit 0; it has no signal handlers of its own, so a killed serve leaves its lock for the next one. While it waits for a child (a channel, a flip, a re-dispatch), RunSync relays a SIGINT, SIGTERM or SIGHUP to that child's process group, and serve then dies of the signal once the child is reaped, at most 10 s later (PE-22), still leaving its lock. A state that is `null` or a primitive crashes as in the JS (C1). Differences: PE-16 (hand-edited non-object shapes), PE-17 (a missing `opencode`), PE-10 (a child that ignores the channel timeout's SIGTERM is killed 10 s later), PE-20 (a child whose output passes 1 MiB is stopped), PE-21 (state, acks, lock and log) and PE-22 (every child it waits for runs in a process group of its own). |
| `orch doctor` | `bin/doctor.mjs` | The same rows in the same order with the same texts (`OK  `/`WARN`/`FAIL`, name, `  —  detail` when the detail is truthy) and exit code (1 if any row is FAIL). Each probe (`opencode`/`codex`/`claude`/`node --version`, `codex login status`, `claude auth status`, `opencode auth list\|plugin list\|debug agents`) runs with a 20 s timeout and reads stdout then stderr; the usage feeds and routing come from a refreshed `orch usage`, rows pushed before a failure are kept and the failure adds `FAIL usage/routing`; the last 400 lines of `~/.local/share/opencode/log/opencode.log` are scanned for orchestrator `ERROR`s, the last three joined, redacted as an incident is (home, emails, tokens, uuids, `ses_` ids) and cut at 300 (cuts in UTF-16 units). Both print `orch binary` after `node present`. Differences: PE-18 (`orch self-test` row; `unit tests` runs the Go tests; the JS warns when it finds no binary); each probe stops at 1 MiB of output (PE-20) and runs in a process group of its own (PE-22). |
| `orch watch [--cwd DIR] [--follow] [--interval 20]` | `bin/watch.mjs` | The same dashboard line for line: header (project, clock, usage time, `STALE`), the first two prompt lines, routing (`role=<model>@effort`/`#variant`/`PAUSED`), supervisor, flip, lanes, questions for the human with the reply command (`orch mailbox reply` when `orch` is on `PATH`, PE-26), the human gate, the last 8 messages, incidents of the last 24 h and the `next:` line of `STATE.md`, with the JS value semantics (`undefined` fields, UTF-16 cuts). Reads the usage cache when fresh, else refreshes it. `--follow` clears the screen and redraws every interval (Node's timer clamp: a non-number, below 1 ms or above 2^31-1 ms waits 1 ms), printing a failed render's message to stderr instead, until killed. A routing failure prints `orch watch: <error>` and exits 1 (PE-2). |
| `orch kickoff --brief FILE [--brief FILE2 ...] [--door auto\|claude\|opencode] [--headless] [--unattended] [--cwd DIR] [--dry-run]` | `bin/kickoff.mjs` | The same stdout, stderr, exit codes and files: the usage line (exit 0 with a brief, 2 without), `brief not found: <b> (looked in <root>; cd into the project or pass --cwd <project dir>)` (exit 2), the first two routing-prompt lines after a refreshed `orch usage`, the door (the home door if its vendor is available and not red, else the other; a given `--door` is not validated), both red (`--dry-run`: the message, exit 75; else the preflight below, the prompt written to `.orchestrator/briefs/conductor-kickoff.md`, `orch flip pause --active <home> --brief .orchestrator/briefs/conductor-kickoff.md` with its answer, and the message, exit 75, so the supervisor's relaunch at the reset starts from that brief; when `orch supervisor start` exits non-zero (before the brief) or the pause does, `Both vendors are red, but the supervisor did not start\|the pause was not recorded: nothing will relaunch at the reset. Fix it and run kickoff again.` on stderr, exit 1), the preflight (`orch policy unattended` with `--unattended`, `orch flip gate --clear --no-relaunch`, `supervisor: ` + the first 120 UTF-16 units of `orch supervisor start`'s trimmed answer, `.orchestrator/` in the repository's `info/exclude` (`.git/info/exclude`, or for a `.git` file, the linked worktree's common dir or the submodule's git dir); `--dry-run` skips it and prints `supervisor: not started (dry-run)` instead, so it changes nothing in the project, though its routing read still refreshes the shared usage cache), the `door:` line, then `prompt: ...` (`--dry-run`), a headless `orch worker start ... --detach` conductor lane on `.orchestrator/briefs/conductor-kickoff.md` (the brief is written atomically, 0600, PE-21; the lane's trimmed answer, the watch hint, its exit code or 1), or the interactive `claude --agent conductor <prompt>` / `opencode --prompt <prompt>` in the project root with this terminal (its exit code, 0 after a signal; a missing binary is `Error: spawn <cmd> ENOENT`, exit 1). The orchestrator children are self-execs of `orch`. A routing failure prints `orch kickoff: <error>` and exits 1 (PE-2). Differences: the captured self-execs stop at 1 MiB of output (PE-20) and run in a process group of their own (PE-22). |

Outside a project: `mailbox list|ack|reply|inbox`, `watch` (with or without `--follow`), `flip
status`, `flip gate --clear`, `worker status|stop` and `supervisor status|stop|ack`, run where
the project root (`--root`/`ORCHESTRATOR_PROJECT`/the working directory for mailbox, `--cwd` or
the working directory elsewhere) has no `.orchestrator/` directory, print `not an orchestrator
project: <root> has no .orchestrator/ directory; cd into the project or pass <--root|--cwd>
<project dir>` on stderr, exit 2 and write nothing, as the JS does. The commands that create a
project (`mailbox post`, `flip run|pause|gate`, `worker start`, `supervisor start|serve`,
`policy`, `kickoff`) are unchanged. In a project, `mailbox ack|reply` with an id that matches no
message print `no message with id <id> in <root>/.orchestrator/mailbox; if it belongs to another
project, cd there or pass --root <project dir>` and exit 1 (an ambiguous prefix is still a C1
crash).

The hidden `orch json-echo [--indent N]` exists for the parity harness.

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
VCS revision Go records at build time, e.g. `0.5.0 (5ae996b)` or `0.5.0 (5ae996b+dirty)`; a build
outside a git checkout (a source tarball or `git archive`) records none and prints `0.5.0`.

## Install

`../install.sh` builds `orch` from the checkout when Go 1.25+ is on `PATH` (`CGO_ENABLED=0 go build
-trimpath -ldflags="-s -w" ./cmd/orch`), checks that `orch version` matches
`internal/version/VERSION`, and places it at `~/.local/bin/orch`, recorded in the install receipt.
`ORCH_BIN_DIR` (absolute) chooses another directory; `--skip-binary` installs without `orch`.

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

Without a checkout:

```sh
go install github.com/youthtrouble/opencode-orchestrator/go/cmd/orch@v0.5.0
```

The module lives in `go/`, so its tags are named `go/vX.Y.Z`, but `go install` takes the bare
version. That binary lands in `$(go env GOBIN)`, or `~/go/bin` when it is unset; the plugin finds
it only through `ORCH_BIN` or `PATH`.

## Plugin runtime

`plugins/orchestrator.ts` has two runtimes. `js` (the default) imports `orchestrator/lib/*.mjs` and
runs `orchestrator/bin/*.mjs`. `go` runs every tool and hook through `orch` and never loads the JS:
routing, effort, the budget gate, the mailbox, manifests and incidents are `orch` subcommands, and
delegate, the workers, the supervisor and the usage-limit flip spawn `orch <script>` with the
arguments the script gets.

- **Select** with `ORCH_RUNTIME=go` in the environment of the opencode process that loads the
  plugin; this is the supported switch. For the background service:
  `opencode service set env ORCH_RUNTIME go`, then `opencode service restart` (undo with
  `opencode service unset env ORCH_RUNTIME` and another restart); the S10 live check confirmed
  both. The value is trimmed and case-insensitive; anything other than `js` or `go` is a
  configuration error.
- **Plugin options (known limitation):** the plugin also reads `runtime` and `binary` options, and
  an option wins over the environment. In the S10 live check, object-form options in a project
  `opencode.jsonc` (`[{ "package": "./plugins/orchestrator.ts", "options": { "runtime": "go" } }]`)
  did not reach the plugin: the js runtime answered and `opencode plugin list` showed no plugins.
  This is probably an upstream opencode issue; use `ORCH_RUNTIME` (and `ORCH_BIN`) until it is
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
- **Rollback:** unset `ORCH_RUNTIME` (or set it, or the option, to `js`) and reload or restart.
- **Which runtime answered:** `orch_usage_status` metadata carries `runtime` and `orch` (the binary
  path); a new incident's `version` is the `orch` version in go mode (`0.5.0 (<rev>)`, or a bare
  `0.5.0` for a binary built outside a checkout), and in js mode the checkout's `.git/HEAD` text
  (for example `ref: refs/heads/main`) or `unknown`.

`../tests/plugin-routing.test.mjs` and `../tests/plugin-tools.test.mjs` drive both runtimes through
a fake opencode context against fakes; `../tests/plugin-orch-parity.test.mjs` runs each lib-backed
tool and hook in both runtimes against the real `orch` (built into a temp dir, or `ORCH_TEST_BIN`)
and compares the results. It skips only when neither `go` nor `ORCH_TEST_BIN` is available.

## Release checklist (maintainer, human-gated)

Tagging and publishing are a human gate: no agent runs these steps.

Written for version `X.Y.Z`; 0.5.0 was released this way on 2026-10-19.

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
| `ORCH_NO_POST_RUN_REFRESH` | unset | `1` stops `orch claude-exec` and `orch codex-exec` from starting their detached post-run usage refresh (PE-12). Set only by the parity harness. |
| `HOME` | OS user home if unset/empty | Per-user state lives in `~/.local/state/opencode/orchestrator`. `orch incident` keeps an explicitly empty `HOME` as Node's `os.homedir()` does (log relative to the working directory). |

## Checks

```sh
cd go
go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...
go vet -tags parity ./parity
# chosen groups; ./parity/run.sh runs them all
./parity/run.sh --only \
  json,usage,mailbox,manifest,incident,policy,bridges,worker,flip,supervisor,doctor,watch,kickoff
```

`.github/workflows/go.yml` runs build, vet, gofmt, tests and the `json`, `mailbox`, `manifest`,
`incident`, `policy`, `bridges`, `worker`, `flip`, `supervisor`, `doctor`, `watch` and `kickoff`
parity groups
on macOS and Linux with `CGO_ENABLED=0` and Node 26 installed. Malformed prompt byte tests also compare
against Node when it is available; fixed byte expectations run even without Node.

## Parity harness

Use Node 26 (`referenceNodeMajor`), as CI does: on Node 22 or 24 one case reports SKIP by
design (PE-5).

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
  (`/bin/sleep`, as its own group leader or not, the pid stored as a number or a string, under
  the lane given, bare or absent, its lock holding its pid), each required to die of SIGINT. The
  PE-14 pids (`1`, `true`, `"1"`, `[1]`, `-1`, `-12345`, `"0"`, `[]`, minus a victim's pid, each
  with a lock holding the pid as `process.kill` coerces it) and the PE-23 cases (a live victim
  recorded as a finished state's pid with no lock, or with a lock holding a dead pid) run `orch`
  only, never the JS, and require the victim and the test process to survive. Normalised: the
  sandbox project path and, for victim cases, the stored `pid` and the lock's pid.
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
  locked the read error still wins, exit 1 not 3); the lane locked with a live or dead
  holder and previous states absent, `null`, `0`, a string or without `status`; a state path that
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
wrapper's argv log ends `--cwd <root>`); a lock held by a live pid (with
  and without a state: exit 3, the lane-locked JSON without `hint` on stderr, empty stdout, no log,
  as `flip.mjs` expects), by a dead pid and holding `abc` (removed, the launch proceeds); and a
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
  directory (PE-21: both fail at the rename, orch's naming its private temp, the JS leaving
  `flip.json.tmp`), and a mailbox directory that is a
  file (no message, same output); `gate` fresh, deleting `paused`, appending and keeping the
  gate's position, `--question` given and bare, a reason over 80 units, a bare `--reason` (no
  message), `--active` given and bare, the TypeErrors of `5`, `1.5`, `"abc"` and `true` (C1), an
  array replaced by the gate, `.orchestrator` a file (C1), and `--clear` over a gate, no gate,
  nothing, a string, a number, an array, `null` and a directory (PE-21, as for `pause`); PE-1
  (the subject cut inside an emoji) as an asserted difference.
  `decide` runs over fresh usage caches built when the group starts (`resets_at` relative to
  that time; no case collects, and each requires the seeded `usage.json` left as it was):
  both green, OpenAI red, both red, unknown, Anthropic red, OpenAI stably green (its window
  reset 180 minutes ago), freshly reset (3 minutes ago) and amber, the plain green fixture
  (its 2099 resets read as freshly reset); `flip.json` absent, `at` 2 and 30 minutes ago,
  unparsable, `null`, a string, an array, a future `2030`, `garbage` and a `paused` record
  without `at`; `--active` absent, empty, bare, `gemini`, `constructor` and `__proto__`. Each
  case also pins the decision printed. PE-15 is asserted (a legacy local date two minutes ago).
  Cross-runtime: `flip.json` written by each runtime's `pause` and `gate` is read by both
  runtimes' `status` and `gate --clear`.
  `run` cases never reach a model: each sandbox's `PATH` holds the fakes first and a fresh
  cache. Pending launches lock the conductor lane with the test process's own pid (only ever
  sent signal 0), so both launchers refuse and nothing runs; each such case requires the
  pending answer from both runtimes. Through that path: `--to` absent, `gemini` or bare and a
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
  `<file>.tmp-<random>`) and `flip.json` a directory (after the brief; both fail at the rename,
  the JS leaving `flip.json.tmp`) are PE-21 differences. Full launches
  run the conductor worker to its end against the fakes and settle its runner as the detached
  worker cases do: claude, opencode handing back with a reason (no `--permission-mode`), claude
  with `--resume`, claude with `--force` over an Anthropic-red cache (finished) and without it
  (deferred), and a stale lock (a reaped pid) the launcher removes; the `launcher` line quoted
  in the answer must be whole (under 400 units) and is parsed as the launch. PE-2 is asserted
  for a launcher that dies because `.orchestrator/logs` is a file. Cross-runtime: a
  `pending_launch` written by each runtime's `run` is read by both runtimes' `status`.
  Normalised: the sandbox project path; an ISO `at` in `flip.json` (any record's, `paused`,
  `awaiting_human`, `pending_launch` or `previous`) that is not earlier than the moment the case
  was built, so seeded and previously written timestamps are compared verbatim; a new mailbox
  line's `id` and `at`; and, for full launches, the worker cases' values, with `runner_pid` also
  in its JSON-escaped form inside `launcher`.
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
  deferred not yet due, unknown status, stale lock, process exited, a primitive lane file and
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
  bridge; the runner's final state replaces the serve's `redispatched_at` in both).
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
  file removed, stale lock, already running (this test's pid), keep-awake through the fake
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
  PE-18 (JS `OK   unit tests  —  pass N`; orch's `orch self-test` row and `WARN unit tests  —  not
  run (...)`, no `go` being on the parity PATH), masks or removes those rows and compares the rest.
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
  launch), with the project's `supervisor.lock` held by the test's pid so neither `supervisor
  start` starts a serve, and both usage feeds failing so each refresh keeps the seeded cache's
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
  show is read from the sandbox's lock, after the stop file is written); normalised: that pid,
  the cut log path and the serve's fresh values.

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
  workers directory and the mailbox; `start --detach` adds the errors of reading or removing the
  lock (`Error: EISDIR: illegal operation on a directory, read` for a directory lock), creating
  `logs/` and creating the log (`Error: EEXIST: file already exists, open '<log>'`). A result whose
  `final_message` is truthy but neither a string nor an array, or whose `errors` is truthy but not
  an array, is C2. From the bridge's start on,
  every crash removes the lock first, as the JS `finally` does.
- `orch flip` prints these C1 lines: the bare `--cwd` TypeError above (before any command, the
  usage error included); `Error: EEXIST: file already exists, mkdir '<root>/.orchestrator'`
  (`pause` or `gate` where `.orchestrator` is a file; any other mkdir errno likewise);
  `Error: EISDIR: illegal operation on a directory, open '<root>/.orchestrator/flip.json'` (the
  write of `pause`, `gate` or `gate --clear`, as other open errors; JS only: orch prints
  `rename '<file>.tmp-<random>' -> '<file>'`, PE-21); and, for `gate` (not
  `--clear`) over a `flip.json` holding a number, a non-empty string or `true`, strict mode's
  `TypeError: Cannot create property 'awaiting_human' on <number|string|boolean> '<value>'`
  (the number as JS prints it, `1e+21`; the string raw and whole), after the mkdir and before
  any write. `run` adds `Error: EISDIR: illegal operation on a directory, read` (the exclude is a
  directory) before any write, then the mkdir of
  `.orchestrator/briefs` and the opens of the brief and of `flip.json` (JS only: orch fails at
  the rename of `<file>.tmp-<random>`, PE-21), each after the writes before it.
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
  '<root>/.orchestrator/supervisor.lock'` in the JS, where orch, taking the lock over as stale,
  gives the rename error of `<lock>.tmp-<random>` (PE-21); `start` gives the open error of its
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
  `supervisor-state.json`, its `lanes`, a worker lane file or `conductor.json` that the
  supervisor rewrites: `orch` reads it as `{}` and writes the object (the JS sets named
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
  parity cases; (1) for `lanes` and a worker file, (2) during a tick and (3) by
  `internal/supervisor` unit tests; (1) for the state file and (2) during a tick also by the
  `serve` parity cases `PE-16-array-state` and `PE-16-null-acks`, and (1) for `conductor.json`
  by the flip evaluation's unit tests (S8b). (5) A worker lane file named
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
  executable` or the first failure (FAIL). Its `unit tests` row runs `go test ./...` in
  `<ORCH_HOME>/../go` (120 s timeout) when `go` is on PATH and `go.mod` exists there, instead of
  `node --test`: OK `go test ./... passed`; FAIL with the first `FAIL` or `--- FAIL` line (or
  `exit N`); WARN `go test timed out after 120 s`; else WARN `not run (no go toolchain or module
  at <dir>)`. With the registry missing both runtimes FAIL their test row (the JS unit tests
  need `models.json`; orch's self-test fails `routing: ENOENT: ...`). Asserted by every
  `doctor` parity case and by `internal/doctor` unit tests.
- PE-19 is reserved for S12 (removing the JS); the gap is deliberate.
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
  same way, so a supervisor never reads a half-written gate: a `flip.json` or brief that is a
  directory fails at the rename (`EISDIR ..., rename '<tmp>' -> '<file>'`), where the JS fails at
  `open` for a brief and, for `flip.json` (which it also replaces through a rename), at the
  rename of `flip.json.tmp`, which it leaves. The supervisor log is opened 0600 without following
  a symlink: a symlinked log fails `start` with ELOOP, after `start` has already removed
  `supervisor.stop`. An existing log keeps its mode (one created by the JS or an older orch stays
  as it was; only the 0700 `logs/` protects it). The supervisor lock is created 0600 and a stale
  one is replaced atomically through a private temp. The temp is not fsynced: the replacement is
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
- PE-23: `orch worker stop` signals only when `<lane>.lock` exists and holds the state's `pid`
  (a live worker holds its lock for its whole run). A finished state, a crashed worker whose lock
  is gone, or a state whose pid is now another process prints
  `{"lane":…,"stopped":false,"reason":"no live worker"}`. The JS signals any live pid in the
  state. Extends PE-14. The lock is read as `Number(text.trim())`, as `start --detach` reads it;
  an unreadable lock holds no pid. Residual: a worker killed with SIGKILL (or a host crash) leaves
  its lock and its `running` state, which still match, so after pid reuse `stop` can still signal
  an unrelated process group; and the check is advisory, not atomic with the signal. Full
  identity (an OS lock held for the worker's lifetime) is post-S12 P1. Asserted by
  `internal/worker` unit tests and the `worker` parity cases `stop/PE-23/no-lock` and `stop/PE-23/lock-of-another-pid`.
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
- PE-26: the reply hint of `orch mailbox inbox` and `orch watch` reads
  `orch mailbox reply <id> "..."` when `orch` resolves on `PATH`, else the JS text
  (`node ~/.config/opencode/orchestrator/bin/mailbox.mjs reply <id> "..."`). Superseded by PE-19
  at S12. Asserted by `internal/cmdref` and `internal/watch` unit tests and the parity cases
  `mailbox/inbox/orch-on-path-PE-26` and `watch/questions/orch-on-path-PE-26`, which put a stub
  `orch` on `PATH`; without it both runtimes stay byte-identical.

Latent JS defects the bridges mirror on purpose: codex-exec ignores codex's top-level `error`
events; opencode-exec ignores `--help`, turns `--effort X` into the prompt `X`, and records `context.model: null` in every failure
incident. Those `orch worker` mirrors: the usage line lists `codex|claude` only; `--effort` is passed to opencode-exec,
where it becomes the prompt; a stale `last_state_write_error` stays after a later successful
write; a stale foreground lock is never reclaimed (only `--detach` reclaims it); `start
--detach` reclaims a stale lock by reading it and then unlinking it, so a lock that another
launcher's runner claims in between is removed too, leaving two runners on one lane;
and the worker posts only `blocked` and `deferred` messages, never the `release` its header
comment mentions (the agent posts its own). These are fixed in JS and Go together after S12
(Q-S6-6).

Those `orch flip` mirrors: the script's header comment lists only decide, run and status;
`gate` dies on a `flip.json` holding a number, string or `true` where `pause` and `run` tolerate
it; a bare `--reason` on `gate` posts no human message (the gate is then invisible in the
mailbox, Q-S7-5); a bare `--active`, `--from`, `--reason`, `--question` or `--resume` writes
`true` into `flip.json` and the mailbox, and `run` passes `--resume true` to the conductor
(Q-S7-4). These are fixed in JS and Go together after S12. (`run` and kickoff dying of ENOTDIR in
a git worktree or submodule, Q-S7-2, is fixed: Sol R13.)

Those `orch supervisor` mirrors (S8a part): any command other than `start`, `serve`, `status`,
`stop` and `ack` prints nothing and exits 0; `ack` accepts Object.prototype names
(`constructor`, `toString`, `__proto__`, ...) as event ids that exist in every state; and the
tick (run by `serve`) keeps the JS order and partial writes: a `null`
`supervisor.json` throws at its first config read only; a primitive worker file aborts every
tick; a redispatch drops every option but brief, harness, lane, breadth, model and session,
writes the old worker state after the launch (over the new runner's first state) and aborts the
tick when its note cannot be posted; a lane entry of a vanished worker is never removed; a
`null` entry in the state's `events` makes every tick throw `Cannot read properties of null
(reading 'created_at')` while pruning, so the supervisor records nothing but that
`storage_error` until the entry is removed by hand. The flip evaluation (S8b) relaunches a
`pending_launch` at every evaluation (once a minute) without a cooldown while `flip run` fails
before clearing it. These are fixed in JS and Go together after S12 (Q-S8-3).

Those `orch doctor` mirrors: the JS row texts (`claude login` has no detail, `codex login` prints
the whole trimmed output, the anthropic row is OK either way); a registry read failure worded as
Node's (`ENOENT: no such file or directory, open '<file>'`, in every command that reads the
registry); a role whose tier has no string `opencode` model fails `usage/routing` with the JS
TypeError (`Cannot read properties of undefined (reading 'split')`) where an empty one prints
`undefined`.

Those `orch watch` mirrors (plan go-s9 3.4, incidents recorded, fixed in JS and Go together after
S12): a `flip.json` holding only `awaiting_human` (what `flip gate` writes on a project that never
flipped) crashes a single render with `TypeError: Cannot read properties of undefined (reading
'slice')` and makes `--follow` print that message every interval without ever drawing a frame; the
`next:` line takes the rest of the `## Next action` heading line, so `## Next action (exact)`
prints `next: (exact)`. A role whose model is not a string throws as in `orch doctor`.

Those `orch kickoff` mirrors (plan go-s9 3.4, incidents recorded, fixed in JS and Go together after
S12): `--door` is not validated, so an unknown door (or a bare `--door`, the text `true`) crashes
with `TypeError: Cannot read properties of undefined (reading 'overall')` after the preflight (with
`--dry-run`, which skips the preflight, before any write); the both-red path says the
supervisor will relaunch but does not start it. One divergence is not mirrored: when `orch`
cannot find its own executable (`os.Executable()` fails), each self-exec has no status and no
output, so orch prints an empty `supervisor: ` line (and, headless, an empty answer and exit 1)
where the JS, whose `spawnSync` result would then hold `null` output, dies of a `TypeError` on
`.trim()`.

`ORCH_JS=/path/to/orchestrator ./parity/run.sh` compares against another JS tree.

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
captured, redacted (`testdata/redact.mjs`) and generated is in its `PROVENANCE.md`. There are
no codex or opencode captures yet; the `codex` and `opencode` bridge subgroups script their
events inline.
