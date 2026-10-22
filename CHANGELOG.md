# Changelog

## Unreleased

- Fixed: map Codex windows by duration, including weekly-only plans (`5h n/a`); their weekly usage alone drives routing and flip, while unreadable data stays unknown.

- plugin: `orch_delegate` gives the verifier write access by default, as it does the coder (incident 3ad1e76b).
  A verifier on Claude Code ran with `--permission-mode plan`, which blocks Bash, so it returned a plan instead of
  running the checks; on Codex it got a `read-only` sandbox, which blocks its report and the temp dirs tests
  create. It now gets `--permission-mode auto` / `--sandbox workspace-write`; an explicit `write: false` still
  wins, and the other read-only roles are unchanged. `tests/plugin-tools.test.mjs` pins all three cases.
- remote: an interrupted Work Trains e2e run cleans up after itself (Sol review R1). SIGINT or SIGTERM ended the
  test process without its `after` hook, so the detached `wrangler dev` kept listening and the run's temp directory
  stayed. One idempotent cleanup (`remote/e2e/cleanup.ts`) now serves both the hook and the signals, owns the dev
  server from the moment it is spawned (also while it is still starting), and then ends the process by the same
  signal. `remote/e2e/cleanup.test.ts` interrupts a run with a fake wrangler, before and after readiness, and covers a
  failed start and an abandoned run; it owns its fakes through a socket lifeline, never by signalling a stored pid.
- docs: the README's update recipe ([Updating](README.md#updating)) rebuilds `orch` beside the installed binary,
  swaps it in with `mv`, checks `orch version` and restarts the Work Trains LaunchAgent; a Worker-first rollout
  still deploys the Worker before that (Sol review R2). Before, it said only `git pull`, tests, `opencode reload`
  and `orch doctor`, which left `orch` and `orch remote serve` on the old build. `orchestrator/README.md`'s update
  recipe swaps the binary the same way, and `remote/README.md` section 4 restarts the agent with `launchctl
  kickstart -k` after an in-place replacement and keeps `bootout` + `bootstrap` for a moved binary (rerunning
  `bootstrap` on a loaded agent fails). The `node .../<name>.mjs` fallback now says `orch remote` is Go-only (R3).
  Docs only.

- remote: the deploy workflow rolls back a `wrangler deploy` that fails after its version went live. Wrangler
  4.118 makes the new version live before it sets up the custom domain and triggers, so a failure there exited
  nonzero with the new code serving, and neither the smoke check nor the rollback ran. The rollback step
  (`remote/worker/scripts/roll-back.sh`) now also runs after a failed deploy, reads the live version back, and
  rolls back to the plan's previous version if it changed. The plan step now fails when `wrangler versions view`
  fails; before, it read as a version naming no commit and deployed without the migration check or the
  older-commit guard. See `remote/README.md`, "Deploy from GitHub Actions".
- remote: a pull from an orch built before `X-Work-Trains-Accept` can no longer receive an accept (Sol review B).
  Its pull settled only the first 50 due accepts as rejected, so with a larger backlog the same pull selected the
  next ones and delivered them, and the old orch would post the redacted recommendation as the owner's reply. Such a
  pull now never selects or stamps an accept, however many are due: each pull refuses the next 50 and returns the
  other answers. **Rollout:** Worker only, v1-compatible. See `remote/protocol/README.md`, "Accept answers".
- remote: the Worker enforces that an action question carries no recommendation (Sol review D1). A push whose
  `ask: "action"` question has a non-null `recommended` is refused with 400 `invalid_request` and stores none of its
  projects, and an accept of an action question is refused whatever its row holds, so no completion claim can be
  accepted from the page. `orch` already sends `recommended: null` for one. **Rollout:** Worker only.
- remote: a Work Trains budget chip shows the credits the Mac reported even when the vendor's usage has no reading
  (Sol review D2): `OpenAI · no reading · credits 195.62`, and the credits line when it opens. Before, both said
  only "no reading".

- remote: the setup docs say one Work Trains deployment serves one Mac (SI-1). A pull has no machine filter, so a
  second Mac on the same deployment can pull the first Mac's answers and actions, reject them as an unknown project
  and settle that rejection with its ack, and the answer is lost. Use one deployment per Mac. See `remote/README.md`,
  "Before you start". Docs only.
- remote: Worker secrets are set with one `wrangler secret bulk` instead of chained `wrangler secret put`s (SI-2).
  Each `secret put` deploys the Worker at once, so six of them made six deployments, each live with a partial set.
  Section 2 now fills a new private file (`mktemp` under `umask 077`, outside the repository, deleted after) and
  sends it in one request; the notification and `wrong_issuer` fixes set their two secrets the same way. Moving to
  another hostname stays one `secret put PUBLIC_ORIGIN` plus the `wrangler deploy` its routes need. `orch remote
  token rotate` notes that its `secret put` line deploys at once. See `remote/README.md`, "Reference".

- remote: a Work Trains push now fits the Workers Free plan's 50 D1 queries per invocation (SI-3). Before, a push
  that needed more failed with 500 part-way: a machine's first push of 24 question-less projects needed 51, and with 50
  projects the Mac's retries never got through at all (re-reading the projects that had landed used the whole
  budget), so nothing more synced and no notification was sent. The Worker now takes the projects that fit,
  keeping the queries the first-push stamp and the notifier need, and answers `deferred` with
  `retry_after_seconds: 1` for the rest, which the Mac sends at its next cycle (one `{"push":"query_budget"}` log
  line per such push). A first push's backlog stays suppressed until every project the Worker will retry has landed (a key another machine
  owns is refused, not retried, and does not hold it open). Notifications
  for projects that landed in a failed push were never lost: the notifier claims whatever D1 says is owed at the
  next push that accepts a project. **Rollout:** Worker only, v1-compatible; every `orch remote` already retries a
  deferred project. See `remote/README.md`, "First sync".

- remote: the Work Trains budget chips show when each vendor resets (WT22-C). A chip reads the window that sets its
  colour, the 5-hour one unless the weekly one is red: `Anthropic 28% · resets in 2 h 10 min`, `OpenAI weekly 100% ·
  resets Mon 08:04 · credits 195.62`. Times are the viewer's time zone, 24-hour, relative within a day, then weekday
  and time (with the date from six days out); a passed reset reads "resets due". Tapping a chip opens both windows,
  `5-hour 28% · resets 22:50 (in 2 h 10 min)` and `Weekly 41% · resets Mon 08:04`, with the credits in full (they no
  longer have a chip of their own); it stays open across the 15 s refresh. Chips are neutral pills whose dot carries
  the level and a chevron that turns up while open, with the level in words for screen readers, and stack full
  width on a phone; an open chip and its detail join into one card with a rule in the level colour. A reset time
  that is not a date is left out rather than breaking the page. The v1 protocol's
  `Posture` gains two optional fields, `weekly_resets_at` and `window`, which `orch remote` now sends. **Rollout:**
  deploy the Worker first, then rebuild `orch`: an older Worker answers a push that carries them with 400 and stores
  nothing. Until the rebuild the page falls back to the 5-hour window. Shots, with the new `budget-expanded` state:
  `remote/docs/design/after/`; `screens.ts` pins Europe/London, en-GB and the clock, so a re-run is byte-identical.
- remote: the Work Trains page is restyled, light only (WT22-B): a warm off-white ground, one accent for Accept,
  Send, Retry, links and the focus ring, and colour only for status (a dot and a word on a tint per project, the
  progress bar in the project's state colour, a dot per CI run that is red only for a real failure). The Needs-you
  count is a plain number (`3`, not `(3)`); beside a recommendation the second button reads **Type something
  else**; the undo window is a dark bar inside its card; a stale Mac is amber, not red. Projects sit as rows on one
  grouped surface. **Recent events** stay open across the 15 s refresh. The dark scheme is gone. The page fills the
  iPhone's safe area (`viewport-fit=cover`), sets its theme colour, and has a 180x180 `apple-touch-icon.png` for the
  home screen. `test/ui-contrast.test.ts` checks every colour pair against WCAG AA from the tokens in `app.css`.
  Shots: `remote/docs/design/after/`.
- mailbox, remote: Accept can no longer tell the conductor that work the owner was asked to do is done (WT25, incident
  2026-10-21: a question asked the owner to run setup scripts, its recommended answer read "Done: token created, ...
  succeeded", and one tap on Accept reported work nobody had run). **Action questions:** `mailbox post --kind question
  --ask action` (and the `mailbox_post` tool's `ask: "action"`) asks the owner to DO something; it takes no
  `--recommended`, the body carries the steps, and the line records `"ask": "action"`. `--ask decision` is the default
  and is not written, so existing questions are unchanged. **Lint:** a recommended answer that reads as a completion
  claim (after trimming, ASCII case aside: `completed`, `all done`, `I ran`, `I've done`, `I have done` or `I did` as
  whole words; `done` followed by `:` `,` `.` `!` `—`, the end or a clause cue; `ran`, `created`, `installed`,
  `finished`, `deployed` or `rebuilt` followed by `:` `,` or a clause cue, where a clause cue is spaces or tabs then it, the,
  that, this, successfully, ok or all) is refused with exit 2 (JS and `orch`, with parity cases), as is
  `--recommended` with `--ask action`; `post()` treats an empty `ask` as a decision, as `orch` does. The lint is a
  guard rail, not a guarantee ("Yes, done" passes); the real defences are no Accept for an action question and the
  conductor verifying the owner's reply. "Done is better than perfect" and "Ran Kirsh" pass. **Work Trains:** the
  snapshot carries `ask` (a new optional field of `Question` and of the summary's questions) and never a
  recommendation for an action question; the Worker stores it (migration `0003_questions_ask.sql`); the page shows an
  action question's steps in full with a **Copy** button (it says "Copied" or "Copy failed: ..."), **I've done it**
  and Reply, no Accept, and **I've done it** sends the typed answer "Owner reports done (unverified).". **Guidance:**
  AGENTS.md and the Claude conductor's "Questions for the human" now say recommended answers are for decisions only
  and that a reply to an action question is a claim to verify. **Rollout:** deploy the Worker first; an older Worker
  rejects a push that carries `ask`.
- remote: the Work Trains page's weight budget (`test/ui-assets.test.ts`) is 64 KiB, up from 48 KiB, so WT25's
  action questions fit beside the WT22-B restyle and its home-screen icon (owner approved, question 6bf313bf).
- remote: `remote/e2e/screens.ts` also shoots an `action-question` state (a blocking action question with Copy,
  **I've done it** and Reply); its shots are in `remote/docs/design/after/`.
- remote: the deploy workflow leaves the `send_email` binding out of the rendered `wrangler.deploy.jsonc` unless
  the `work-trains-production` environment sets the new optional variable `WORK_TRAINS_SEND_EMAIL` to `true`, so an
  account without Email Routing deploys. Without the binding the Worker skips email notifications, as it does when
  `NOTIFY_FROM`/`NOTIFY_TO` are unset. Any value other than `true`, `false` or unset fails the render.
- remote: `node remote/e2e/screens.ts --out <dir>` takes screenshots of the Work Trains page from fixtures
  (WT22-A): five states (needs you, calm, stale Mac, failed CI and paused, OpenAI on credits) on a phone and a
  desktop viewport, through headless Chrome on loopback, with no Worker, orch or real data. Requests off loopback go
  to a refusing proxy and fail the run before any shot is written; a Chrome that cannot start fails it at once, and
  Ctrl-C or SIGTERM stops Chrome and removes its profile. `remote/docs/design/before/` holds the current page's
  shots, the baseline for the redesign.
- remote: the Work Trains page's size budget (`remote/worker/test/ui-assets.test.ts`) is now 48 KiB for
  everything in `public/`, up from 40 KiB: the OpenAI credits chip and the WT21c error messages together
  take the page past the old cap. The budget still fails the tests when crossed.
- remote: after a hostname cut-over the old address no longer fails mysteriously, and Accept delivers what the
  conductor wrote (WT21c). **Accept** now replies on the Mac with the recommendation from the project's mailbox, not
  the copy the snapshot redacted for the Worker (which had turned `/v1/installations/me` and any path into
  `<path>`); a question with no recommendation on the Mac is rejected with a reason the page shows, never answered
  with placeholder text. `orch remote` marks its pull with `X-Work-Trains-Accept: mailbox`; a Worker pulled by an
  older orch refuses each due accept as rejected ("orch on the Mac is too old to accept a recommendation") rather
  than let it post the redacted copy. **Redaction** keeps API routes (`/v1/...`, `/api/...`, quoted or not) and
  still removes absolute paths and `~/` paths. **Old hostname:** a GET of the page or an asset on any host other
  than `PUBLIC_ORIGIN`'s gets a 302 to the same path and query there (after the Access gate); API and machine
  routes still answer on every host. **Error messages:** a browser-route 403 body carries `reason`, the code of the failed check
  (new optional field of the protocol's `Error`; never an email or a token; machine routes keep one opaque body for
  every failure), and the summary carries
  `public_origin` (new optional field). The page maps the codes: a write from another address says "This address
  can't send answers" and the banner links to `PUBLIC_ORIGIN`; an unlisted email says so; a 403 without a reason says "Not
  allowed"; only a lapsed sign-in says "Signed out. Reload to sign in". Rollout: rebuild `orch` first, then deploy the Worker; in the other order,
  accepts tapped in between are refused, not misapplied.
- remote: GitHub Actions deploys Work Trains (WT24). The new `.github/workflows/remote-deploy.yml` runs after the
  Remote workflow passes on a push to main, and on demand with an optional `sha` (redeploy or roll back). It reads
  everything from a GitHub environment, `work-trains-production` (secrets `CLOUDFLARE_API_TOKEN` and
  `CLOUDFLARE_ACCOUNT_ID`; variables `WORK_TRAINS_D1_ID`, `WORK_TRAINS_HOSTNAME` and optional
  `WORK_TRAINS_WORKERS_DEV`). A push in a repository without that environment skips with a notice; a manual run
  there, or an incomplete environment, fails. The environment must allow deploys from `main` only. A run renders `wrangler.deploy.jsonc` from the example (`remote/worker/scripts/render-deploy-config.ts`),
  and skips when nothing under `remote/worker/` or `remote/protocol/` changed since the live commit, or when the
  live commit already contains the pushed one. Each deploy
  records its commit as the version message, which is how the next run finds the live commit. The run refuses
  an edited migration (`scripts/deploy-plan.sh`), applies D1 migrations, deploys, and runs a credential-free
  smoke check (`scripts/smoke-check.ts`): the page must redirect to Access, and a machine route must refuse. If
  the smoke check, or reading the live version after the deploy, fails, the run rolls back to the previous
  version and fails. The smoke check proves the deploy completed and Access still fronts the hostname, not that
  the Worker is healthy: Access answers before the Worker runs, so a crashing Worker passes and is rolled back by
  hand. Only the steps that run wrangler against the account see the Cloudflare credential. Worker secrets,
  Access and DNS stay manual. `wrangler` 4.118.0 is now a devDependency of `remote/worker`, pinned by its
  lockfile, and the e2e CI job uses it instead of `npm i -g`. Token scope, environment setup and rollback are in
  [remote/README.md](remote/README.md#deploy-from-github-actions). The conductor contract (AGENTS.md and
  `claude/agents/conductor.md`, "Keeping the live checkout current", now covered by the word-identical test) no
  longer counts the deploy this repository's `remote-deploy.yml` runs after a push as an `ask` item, once its
  environment is configured: conductors push and watch the run. No other workflow inherits the exception.
- budget: OpenAI credits. `orch usage` (text and `--json`), `orch watch`, the injected budget,
  `orch doctor` and the Work Trains page now show the ChatGPT credits Codex reports ("credits 215.68
  left; plan limit reached") and its banked resets, labelled as window resets, not money; the
  window's colour is unchanged. A new policy key, `openai_credits` (`off` by default, or `on`) with an
  optional `openai_credits_floor`, lets OpenAI roles whose plan limit is reached run on credits
  through `codex-exec`, which records the balance before and after in the run record. With the
  default nothing changes: a red Codex window means no OpenAI work. Before drawing, the gate re-reads
  usage and refuses if it cannot; a negative floor counts as 0. The v1 protocol's `Posture` gains an
  optional `credits` object, which a Worker older than this release rejects: deploy the Worker
  before updating `orch` on the Mac. See
  [OpenAI credits](orchestrator/README.md#openai-credits).
- remote: fixed the Work Trains header reading "Mac seen 24 s agonull" when the Mac is not stale
  (WT21b). The DOM's `replaceChildren` renders a `null` child as the text "null"; the page now fills
  every node through one helper that drops absent children the way `el` does. A new page test,
  `remote/worker/test/ui-page.test.ts`, runs `public/app.js` against a small fake DOM and checks the
  header for both a fresh and a stale Mac.
- remote: Work Trains for more than one sign-in and a new hostname (WT21). `OWNER_EMAIL` is now a
  comma-separated allowlist (trimmed, lower-cased, de-duplicated; any empty or malformed entry fails closed
  with 503 `not_configured`, as a missing secret does); a single address works as before. An Access JWT
  whose `iss` is another Zero Trust team domain is refused with 403 `wrong_issuer` and `expected_issuer`, the
  team domain the Worker expects (new in the v1 protocol's `Error`; a team domain is public, the AUD and
  emails are never shown), instead of the opaque 403, and `orch remote status` shows it as `HTTP 403
  wrong_issuer (the Worker expects Access team https://<team>.cloudflareaccess.com)`. `PUBLIC_ORIGIN` stays
  one exact origin: [remote/README.md](remote/README.md#moving-to-another-hostname) has the cut-over steps
  to a Custom Domain (dashboard, `routes` in the deploy config, `PUBLIC_ORIGIN`, `orch remote init --url
  --force`, LaunchAgent restart) and the wrong-team fix. The end-to-end test now signs in two owners,
  refuses an unlisted email and a write from the old hostname, and checks `wrong_issuer` on the phone and
  in `orch remote status`.
- Work Trains (WT1-WT17): an optional, private, self-hosted status page for every orchestrator project
  on a Mac, built for a phone. A Cloudflare Worker with D1 behind Cloudflare Access holds the latest
  snapshots; the Mac runs `orch remote serve` (Go-only, kept running by a LaunchAgent), which pushes
  scrubbed snapshots and pulls answers, pauses and resumes. The page answers questions, pauses,
  resumes and hides projects, nothing else. It fits Cloudflare's free tier. Setup, privacy, cost,
  credential rotation, turning it off and troubleshooting are in the README's
  [Remote status (Work Trains)](README.md#remote-status-work-trains) section, with the operator detail
  in the new [remote/README.md](remote/README.md). The per-slice entries below give the details.
- remote: an end-to-end test of Work Trains (WT16), `node --test 'remote/e2e/*.test.ts'`, and a CI job
  `e2e` running it. It starts the real Worker under `wrangler dev --local` (miniflare and a temp D1, no
  Cloudflare account) through a new test-only entry, `remote/worker/test/e2e-entry.ts` with
  `wrangler.e2e.jsonc` (Access keys from the run's own key set, a recording notifier at
  `GET /__e2e/notifications`, a 1 s undo window), puts a local Access emulator in front of it, and drives
  it with the real `orch remote once` in a sandbox HOME and a simulated phone. It checks the 403 gate
  on every route (`/__e2e/notifications` included), that Access replaces any assertion a caller sends, Accept and typed answers landing once after the undo window (and a double tap refused),
  an undo applying nothing, one notification without the question's text, an Access outage and a lost
  ack response, pause and resume with the 60 s push spacing, and that neither secret appears in any
  output or file. Wrangler runs on a temp copy of the Worker, so no `.wrangler/` lands in the
  repository. `src/index.ts` now builds the app through `createApp({ keysFor, notifierFor })`, which
  both entries use.

- go: Work Trains snapshots now carry each project's latest CI (WT15): with the config's `ci` on
  (the default), a project whose origin is a github.com repository gets the newest GitHub Actions
  run of each workflow on its default branch (at most 5), read with the owner's own `gh` (`gh run
  list --repo owner/name --branch <origin HEAD, else main>`, with `GH_HOST=github.com`). Each
  check times out after 10 s (killing gh's whole process group), all checks of a cycle share a 20 s
  budget (projects not reached keep their previous runs and are checked next cycle), and checks
  are cached per project for 5 minutes in `state.json` (new `ci` field), failures included. Run
  links are sent only under `https://github.com/`. A failure (git refusing the repository, gh
  missing or not logged in, an error, a timeout) never fails a cycle: the project gets
  `ci: []`, the log says `ci left out for <name>: <reason>` once while it lasts, and a cycle that
  otherwise succeeded records it as a `last_error` of kind `ci` that changes only with its message.
  `orch remote push --dry-run` shows the same CI (and those notes on stderr) and still writes
  nothing. Turning `ci` off runs no gh and drops the cached runs.

- go: `orch remote once [--json]`, `orch remote serve` and `orch remote launchd [--label L]` (WT14).
  The Mac now syncs with the Work Trains Worker: each cycle pulls the page's answers and actions,
  applies them through the WT13 apply layer, acks every outcome in one request (an applied answer
  whose gate check failed is still acked `applied`; a lost ack is re-sent next cycle and the answer
  is not posted twice, nor a pause or resume applied over a local decision made since; an item the
  apply layer could not settle, `apply.RetryableError`, is not acked, so the Worker sends it again,
  and its failed step is logged once while it lasts), then pushes, in one request of at most 50, the projects whose snapshot changed
  and whose last accepted push is 60 s old (honouring the Worker's `deferred` `retry_after` within the schema's 1..300 s, else 60 s). `serve`
  pulls every `poll_after_seconds` (15..300 s), checks local state every 15 s, backs off with equal
  jitter (half to all of a ceiling from 5 s to 5 min) on failure, logs only changes and errors, stops cleanly on SIGINT/SIGTERM
  and holds a single-instance `flock` on `remote/serve.lock`, which `once` also takes for its
  cycle (while `serve` runs, `once` exits 1 and sends nothing; its error output is redacted as
  `last_error` is). `launchd` prints the LaunchAgent plist
  (`RunAtLoad`, `KeepAlive`, `ThrottleInterval` 30, log in `remote/remote.log`, never rotated) and, on stderr, the
  `launchctl bootstrap`/`bootout` commands, the plist path single-quoted for the shell; it installs
  nothing. The HTTP client never follows or even parses a redirect (reported as an Access denial
  with a hint), types every failure (access denied, HTTP status, protocol, network), sends
  `X-Work-Trains-Machine: <machine id>` (ignored by the Worker for now) and an `Idempotency-Key` on
  push, and keeps header values, request bodies and response text out of every error, log line,
  `last_error` and `once --json`: a Worker error code outside the protocol's enum is `unknown`, a
  schema or transport failure is a fixed phrase that names no field, value or header, and a pulled
  id is logged only when it is a UUID. `orch remote push` without `--dry-run` now points at `once`.
  A budget that changed or expired while no project did goes in a push without projects (60 s
  spacing, only after a first push has landed; `state.json` gains `last_budget_hash`). The
  config's `url` is now refused with a query or fragment (`url must not have a query or a
  fragment`), and routes are joined to its path, so a trailing slash or base path works.

- go: `orch remote push --dry-run [--json]` prints the exact Work Trains `PushRequest` this Mac
  would send, built only from local `.orchestrator/` state and the cached usage reading, and sends
  and writes nothing (WT12). Projects are discovered under the config's roots (the root and its
  direct children with a `.orchestrator/` directory; linked worktrees and `exclude` matches
  skipped; one per real path). Each snapshot derives the project state (`needs_you`, `failed`,
  `paused`, `working`, `done`, `idle`), door, run, slice and progress, the open questions to the
  human and the last 10 message headlines; free text is scrubbed of credentials (provider tokens,
  JWTs, private keys, `Bearer` values, `password=`-style assignments, URL user info, any other long
  letter-and-digit run) and every absolute filesystem path (quoted, with spaces, single-component,
  Windows and UNC, an unquoted one with the rest of its clause), then redacted and capped, a
  message whose id is not a lowercase uuid is left out, the budget is read without running any program, `orch_version` is the bare release number
  and the project is keyed by an opaque hash. `orch remote status` now shows the discovered
  projects' count and names and any root it could not scan. Snapshot goldens are checked against
  the protocol schema by `remote/protocol/test/go-goldens.test.ts`.
- go: `orch remote` apply layer (WT13). A Work Trains answer is posted through the same reply path
  as `orch mailbox reply` (now `internal/reply`, output unchanged), and its line gains, after
  `thread`, `"via": "remote"` and `"remote": {"answer_id", "kind": "accept|typed"}`, acked with the
  note `replied via Work Trains`; JS readers ignore the extra keys. A retried answer whose reply is
  already posted is not posted again but finishes what an interruption left (the ack, the gate). A
  page pause sets the human gate (`paused from Work Trains`); a resume starts the project's
  supervisor when none runs and clears the gate only once a live supervisor holds its lock
  (waiting up to about 5 s), else keeps the gate for the owner's next resume. Each pause and resume
  is recorded in the project's `.orchestrator/remote/actions.json` (Go-only, newest 256) before
  `flip.json` is touched and its outcome after, so an action pulled again after a lost ack resends
  its outcome and never undoes a pause or resume made on the Mac since. One interrupted mid-apply
  is never applied again: it is `applied` if the project is already as it asked, else `superseded`
  (`interrupted; send it again`), so the owner sends a new one. An answer or action whose project
  cannot be resolved is rejected and recorded in `unresolved.json` in the remote state directory
  (newest 256), so a replay after discovery finds the project resends the rejection. A journal
  that cannot be read or is not a valid array of distinct entries, or a receipt (the in-flight
  action, the unknown-project rejection) that cannot be written, applies nothing and returns
  `apply.RetryableError` with no outcome: the caller must not ack it, so the Worker sends it
  again. So does an answer whose mailbox cannot be read, whose reply cannot be posted or whose
  ack cannot be written; a reply already posted carries the answer's id, so the resend acks it
  and clears the gate without posting it twice. Once the in-flight receipt is written, an
  action's outcome is final even if recording it fails.
  A failed step sends the page a fixed detail (for example `pause: could not write the gate`)
  and keeps the error for the local log.
- flip/kickoff: reading a `.git` file's `gitdir:` path or a `commondir` path now strips only the
  trailing CR/LF line ending, as git does, not all trailing white space (JS and Go, C4-SOL-R1).
  Before, a git dir literally named `meta ` was read as `meta`, so `.orchestrator/` went unexcluded
  or, if a `meta` sibling existed, that other directory's `info/exclude` was written.
- go: `orch remote` state and wire fixes (WT11 review R1-R3). Concurrent `orch remote init` runs
  (separate processes included) now all get the same machine id: `state.json` is published
  complete and only if absent, and a run that loses reads the winner's id. Before, each could
  report a different id and the last write won. A state file that is valid JSON but has a missing,
  null, empty or non-canonical `machine_id`, or a missing or null `projects` (or a null or
  empty-keyed entry), is now refused as corrupt instead of yielding a blank machine id; it is
  never regenerated. Wire decoding refuses a property whose name is only a case variant of a
  declared one (`{"settled":0,"Settled":99}`), at any depth; before, it overrode the declared value.
- remote/worker machine API fixes (WT8 review R1, R2): `GET /api/v1/machine/pull` now reads the due
  answers and actions and stamps their first delivery in one D1 batch (one transaction), so an Undo
  either lands first (the row is never delivered) or after (the Undo is 409 `too_late`). Before, an
  Undo that reached D1 between the pull's read and its stamp succeeded and the Mac still received
  the row. Still 5 D1 queries per pull. Two identical pushes for the same key on the same Worker
  millisecond no longer fail with a 500: each push's question writes are keyed on a per-push id
  (new nullable `projects.push_id`, migration `0002_projects_push_id.sql`) rather than on the
  snapshot hash and receive time, so the loser is `deferred`.
- remote/worker: an Access JWT without `exp` is now refused (403) instead of never expiring, and the public-repo guard matches Access team hosts in any case and flags 32-hex Cloudflare account/zone/D1 ids in config files and `account_id`-style lines.
- conductors: AGENTS.md and the Claude conductor share a word-identical "Questions for the human"
  section: every question goes to the mailbox with `--recommended` and `--blocking`, an interactive
  session under the unattended policy also posts the questions it asks in chat (and acks them when
  answered there), a Work Trains reply (`"via": "remote"`) counts as a human reply, an interactive
  session defers to a running headless conductor, and conductors keep `session beat` current. `/away`
  step 1 posts the open chat questions; `flip decide` answering `gate` means stop (an interactive
  conductor reports it and keeps working with the human). The continue brief `flip run` writes (JS and
  Go) shows the question form with `--recommended` and `--blocking` (WT6).
- plugin: `orch_mailbox_post` takes `recommended` and `blocking`, so a conductor can post a contract-complete
  question (both runtimes; a question without `recommended` is a tool error). An opencode `conductor` session
  now beats `.orchestrator/sessions/opencode.json`: `working` from the per-call context hook, `waiting` on
  opencode's `session.idle` event (read from the promise API's `event.subscribe` async iterable). One write
  at a time, at most one per 30 s per session; a change inside the interval is written once it ends, latest
  state wins. An idle from a session that no longer owns the door is ignored. Unloading the plugin waits for
  the beat in flight and starts no new one, so a reloaded plugin's heartbeat is never overwritten late.
  Only in a directory that already has `.orchestrator/`; a failed beat is recorded as an incident and never
  holds up the turn (WT5).
- go: new Go-only `orch remote init|status|token rotate|token revoke|service-token set` sets up
  this Mac for Work Trains (no network yet). The config lives in `~/.config/orch/remote.json`
  (owner-only, refused inside a git work tree or when group/other can access it; override
  `ORCH_REMOTE_CONFIG`), the machine id in `~/.local/state/opencode/orchestrator/remote/state.json`,
  and the machine token and Access service token in the login Keychain, written through
  `security -i` on stdin so no secret appears in argv. `token rotate` prints only the token's
  SHA-256 for the Worker's `MACHINE_TOKEN_SHA256`; this Mac switches to the new token at once, so
  pushes get 403 until that secret holds the new hash. `status` still reports everything when the
  config is refused (`config_error`) and exits 1. Typed Go structs for the v1 wire payloads
  (`internal/remote/wire`) decode every protocol example, refusing unknown and missing properties
  and a null where the schema does not allow one. See `go/README.md`.
- remote/worker browser API (local only, not yet deployed): `GET /api/v1/summary` lists every
  project (hidden ones marked `hidden: true`) needs_you first, then failed, paused, working, done
  and idle, newest first within a state, each with its open questions, each question's live answer
  (`undo_window`, `sent`, or the Mac's outcome) and the project's pending action, plus the machine
  last seen and `undo_seconds` (`UNDO_SECONDS`, default 10, clamped to 1..60).
  `POST /api/v1/questions/:id/answer` accepts the Worker's stored recommendation (client text is
  ignored; none stored is 400) or typed text (trimmed, control characters other than newline and
  tab removed, 1..4000 UTF-16 units). A retried tap with the same `client_key` returns the same
  answer with 200, even after the question has closed; the same `client_key` with a different
  question, kind or text, a second answer to the question, or a new one to a closed question is 409
  `conflict`.
  `POST /api/v1/projects/:key/actions` pauses or resumes the same way (pause on a paused project,
  or resume on one that is neither paused nor needs_you, is 409; a retried tap is 200 whatever the
  project's state is now). `DELETE /api/v1/answers/:id` and
  `DELETE /api/v1/actions/:id` undo while the undo window is open and the Mac has not pulled the
  row, else 409 `too_late`. `POST /api/v1/projects/:key/visibility` hides or shows a project (204).
- remote/worker email notifier: after a push, a newly seen blocking question (not one from a
  machine's first push) emails "Work Trains: <project> needs you", and a project entering `paused`
  or `failed` emails "... is paused" / "... failed", each with the page link and never the
  question text. Each is claimed in D1 first, so it is sent at most once and a failed send is not
  retried; at most 20 emails a UTC day. The notifier adds at most 5 D1 queries to a push however
  many it sends, and runs only when the Worker's own count of the invocation's queries leaves all 5
  under the Free plan's 50 (it logs `{"notify":"deferred"}` otherwise). Anything still owed (a
  deferred push, or more than 20 at once) is taken up by a later accepted push, within the same
  daily cap. It sends through the `send_email` binding `EMAIL` to
  `NOTIFY_TO` from `NOTIFY_FROM`; without all three it sends nothing and logs `{"notify":"disabled"}`
  once per isolate.

- remote/worker page (local only, not yet deployed; the browser API it calls is WT9): a
  mobile-first, dependency-free page in `remote/worker/public/` (plain HTML, CSS and ES2022
  modules, no build step, under 40 KB). It polls `GET /api/v1/summary` every 15 s while visible,
  shows when the Mac was last seen (a `stale` badge after 3 min, `never` without a pull), budget
  chips, a "Needs you" stack (blocking first, then oldest) with Accept and Reply, a 10 s undo
  countdown on the server's clock, and a card per project with Pause/Resume and Hide/Unhide.
  A stored answer or action shows its Undo at once from the write's own response. Each write keeps
  its `client_key` with its exact body: after an unconfirmed send (network error, 5xx) the card
  offers only Retry of that same request until a response settles it (the stored row or a 4xx);
  a summary never does. An unreadable summary keeps the last good one, shows the banner and
  polling carries on. Every Worker response now carries a strict Content-Security-Policy,
  `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` and a `Permissions-Policy`.
- remote/worker machine API (local only, not yet deployed): `POST /api/v1/machine/push` stores
  changed project snapshots (an unchanged `snapshot_hash` writes nothing; a project accepted under
  55 s ago is `deferred` with `retry_after_seconds`; a key pushed by another machine is `deferred`
  and logged) and keeps each project's questions in step (new, edited, closed, reopened; a
  machine's first push marks its questions `suppressed`, and a first push that fails part-way still
  counts as first on retry; a push that loses a race with a concurrent push for the same key is
  `deferred`, never a 500). `GET /api/v1/machine/pull` returns the answers and actions past their
  undo window that are not cancelled or settled, oldest first, with `poll_after_seconds: 30`.
  `POST /api/v1/machine/ack` settles each pulled one once (`settled` counts only rows that changed).
  Each push costs about 2 D1 queries plus 1-3 per project and one per question written; the Workers
  Free plan allows 50 per invocation, so large pushes need the Workers Paid plan (1000). Every body is checked against `remote/protocol/v1/schema.json`; another `v`
  is 400 `unsupported_version` with `supported: [1]`, and a schema failure is 400 `invalid_request`
  naming the first bad path, never the value.
- remote/worker scaffold (not yet deployable): a TypeScript Cloudflare Worker that verifies the
  Cloudflare Access JWT and pins the owner's or the Mac's identity on every request (machine routes
  also need the machine token), fails closed with 503 when a secret is missing, the D1 migration,
  a node:sqlite D1 test double, a public-repo guard test and the `Remote` CI workflow.
- go (Linux): after relaying a Ctrl-C (or SIGTERM/SIGHUP) to a captured child, orch now dies of
  the signal before RunSync returns. It used to send the signal to its own pid, which Linux could
  deliver to another thread later, so orch sometimes ran on and exited 0. That was the ubuntu CI
  flake in `TestRunSyncRelaysSIGINTAndThenEndsByIt` (test reliability).
- go (macOS): the same now holds on macOS. orch raised the relayed signal there with a kill(2) of
  its own pid too, which darwin hands to a thread of its choosing, so under load orch ran on after
  RunSync and exited 0 (the macOS flakes of
  `TestRunSyncKillsAChildIgnoringARelayedSIGTERMBeforeEndingByIt` and
  `TestRunSyncRelaysSIGINTAndThenEndsByIt`). It now raises the signal on its own thread with
  libSystem's `raise(3)`, as Go's runtime does. `TestWaitExitedReturnsAtOnceForAZombie` now waits
  for its child to exit instead of sleeping 200 ms, which a loaded Mac took over a second to do.
- remote: the Work Trains wire protocol (`remote/protocol/`) is versioned as a v1 JSON Schema with a
  dependency-free validator that rejects schema keywords it does not support, valid and invalid
  examples for every payload, and contract tests (`node --test 'remote/protocol/test/*.test.ts'`).
- session (JS, Go): new `session.mjs beat|show` (`orch session`) keeps a conductor heartbeat in
  `.orchestrator/sessions/<door>.json` (`door`, `session_id`, `state`, `now`, `slice`, `progress`,
  `updated_at`), written owner-only through a temp file and a rename in both runtimes. `beat` clears
  `--now`, `--slice` or `--session` given as `""`; `--progress` must always be `N/M`. `beat --hook`
  reads a Claude Code hook's JSON, maps its event to a state, never stores the prompt, never creates
  `.orchestrator/` and never exits 2. New parity group `session`.
- claude conductor: frontmatter hooks run `session.mjs beat --door claude --hook` on
  `UserPromptSubmit`, `Stop` and `SessionEnd`, so an interactive Claude conductor session leaves a
  heartbeat automatically.
- manifest (JS): the private temp-file write moved to `lib/replace-file.mjs`, shared with the
  session heartbeat; behaviour unchanged.
- flip: `decide` answers `{"action":"gate","to":null,"reason":"awaiting human: <reason>"}` while
  `flip.json` holds a human gate (`awaiting_human`), before reading any usage, so a pause recorded
  while a headless conductor runs takes effect at its next brief boundary; the continue brief
  `flip run` writes tells the conductor to update `STATE.md` and stop on `gate`. An interactive
  conductor reports the gate and keeps working with the human (JS and Go, WT4).
- mailbox (JS, Go): `post --kind question` needs `--recommended "<the answer you recommend>"` and takes
  `--blocking true|false` (default true; bare means true). The line gains `"recommended"` and `"blocking"`
  after `"thread"`; other kinds are unchanged and refuse both options. A question without a non-empty
  recommendation, a bad `--blocking` or either option on another kind exits 2 with a usage line and writes
  nothing; `post()` / `Mailbox.Post` refuse a question without one (`QuestionNeedsRecommendationError`,
  `ErrQuestionNeedsRecommendation`). `inbox` marks blocking questions ` (blocking)` and shows the
  recommended answer; older questions print as before. Callers that post questions without a
  recommendation (the plugin's `mailbox_post`, the conductor docs) are now refused until they pass one.
- flip/kickoff: `.orchestrator/` is excluded in linked worktrees (the common dir's `info/exclude`)
  and submodules (the git dir's) instead of failing with ENOTDIR; a `.git` file that is malformed or
  names a missing git dir, or whose `commondir` names a missing directory, counts as no repository
  and nothing is written. The JS exclude step now
  has one implementation (`lib/flip.mjs`) for flip and kickoff (Sol R13).
- kickoff: the docs now say `--dry-run` changes nothing in the project; it still refreshes the
  shared usage cache (Sol R18).
- kickoff/flip: a kickoff now supersedes any queued relaunch or pause (`gate --clear --no-relaunch`
  removes `pending_launch` and `paused`), so the supervisor cannot start an older conductor first
  (Sol R9).
- kickoff: with both vendors red, a real kickoff runs the preflight (starting the supervisor),
  saves its prompt as `.orchestrator/briefs/conductor-kickoff.md` and pauses with `flip pause
  --brief`. At the reset, `flip run` launches that brief, and a launch the lane lock leaves pending
  keeps it for the retry. If the supervisor does not start or the pause is not recorded, kickoff
  exits 1 instead of 75 (Sol R10).
- plugin: a failed spawn no longer leaves a two-hour deadline timer behind (Sol R8).
- manifest (JS, Go, plugin): a manifest is written through a private temp file and a rename, so a symlink at the
  destination is replaced, never followed. A temp-name collision draws a new name and never removes another writer's
  temp. The plugin refuses a manifests directory outside the project, checked before any directory is created
  (Sol R15).
- tests: the plugin test fakes end by themselves; the cleanup that killed logged pids is gone (Sol R16).
- go: Ctrl-C (or SIGTERM/SIGHUP) during a captured child (doctor probes, kickoff self-execs, flip's
  launcher, supervisor channels, usage feeds) now stops the child's process group and then ends
  orch by the same signal; a signal orch ignores (nohup) stays ignored. Leftover cleanup of a
  cancelled child's group now happens before the child is reaped (waitid WNOWAIT on Linux, kqueue
  NOTE_EXIT on macOS), so it can no longer signal a reused group id; usage feeds (`sqlite3`,
  `opencode auth list`, `security`) stop their whole process group on timeout or overflow
  (PE-20, PE-22; Sol R1, R6, R11).
- go (tests): process tests no longer SIGKILL a recorded pid at cleanup; the descendants they
  start end with the test through a FIFO lifeline, and a dead but unreaped descendant counts as
  gone (review go-c1 F1, F3).
- AGENTS.md and the Claude Code conductor: a standing rule keeps the live checkout current. After
  each push to main the conductor fast-forwards the main checkout when it is clean and on main (else
  posts a mailbox note) and runs `opencode reload` when `plugins/` or `opencode.jsonc` changed.
- docs: the READMEs document parity exceptions PE-20 to PE-26 (PE-19 stays reserved for S12),
  including owner-only state files under `orch`.
- mailbox/watch/flip/worker/supervisor: run outside a project (no `.orchestrator/`), the read and
  stop commands say which directory they looked in and how to fix it (`cd` or `--root`/`--cwd`),
  exit 2 and create nothing; `mailbox reply|ack` with an unknown id name the project searched.
- `/away` (opencode command and Claude Code skill) hands an interactive session to one headless,
  unattended conductor and ends the interactive turn; saying "I'm away" does the same after one
  confirmation. `/away --dry-run` shows the door and prompt without changing anything.
- kickoff: `--dry-run` no longer sets the policy, clears gates, starts the supervisor or edits
  `.git/info/exclude` (it prints `supervisor: not started (dry-run)`); a missing brief names the
  directory searched; the usage line lists `--unattended`.
- flip/mailbox: clearing a human gate (`orch flip gate --clear`, or `orch mailbox reply` to the
  gate's question) now queues a relaunch; the supervisor restarts the gated door's conductor within
  a minute. Clearing when no gate is set changes nothing; `kickoff` clears with the new
  `--no-relaunch` so it never starts a second conductor.
- codex-exec: works with codex-cli 0.162.1. `workspace-write` is now requested with
  `--approve-for-me` alone (the CLI rejects it next to `-s`), and `exec`-only options
  (`--approve-for-me`, `-s`, `--add-dir`) come before `resume`, which does not accept them
  (incident 2eeaf7d5349a, Q-S5b-1).
- mailbox/watch: the reply hint says `orch mailbox reply <id> "..."` when `orch` is on PATH
  (PE-26).
- parity: the harness PATH holds only links to node and sqlite3, and destructive cleanup refuses
  pids below 2 (Sol S4/R10, S3/R13).
- install: `--no-reload` skips the final `opencode reload` and doctor (for hermetic tests and
  scripted installs).
- install: release downloads give up after 20 s without a connection, 60 s below 1 KiB/s or 15 min
  in total, instead of holding the operation lock forever (Sol S4/R8).
- go: PATH lookup keeps each entry as written (`link/../bin` is resolved by the kernel), as Node
  does (Sol S1/R6).
- worker: a lane name containing `/` is refused, and a detached runner gets the absolute project
  root as `--cwd` (both runtimes; Sol S2/R8, S2/R6).
- worker (Go): `stop` signals only a worker whose lane lock holds the state's pid; a lock left by
  a SIGKILLed worker still matches until post-S12 (PE-23; Sol S2/R1).
- worker (Go): `workers/` and `logs/` are created owner-only (PE-21).
- plugin: the orch binary and `ORCH_HOME`/`ORCH_REGISTRY` are resolved to absolute paths once, so
  every tool runs the same binary and registry from any directory; a bare `ORCH_BIN=orch` now means
  `./orch` in the plugin's working directory, with no `PATH` lookup. Incidents go to `orch` on stdin
  (`--request -`), so summaries never appear in process arguments; go mode therefore needs an `orch`
  from this release. `manifest_create` refuses a task that is not a single file name (Sol S4/R3,
  S4/R4, S4/R5, S4/R7).
- worker (Go): a failed run's errors and its blocked message no longer carry token-shaped text
  (PE-24; Sol S2/R10).
- doctor: plugin error lines are redacted before they are printed (both runtimes; Sol S3/R10).
- incident: `orch incident record --request -` reads the whole incident as JSON from stdin, so
  callers keep summaries and context out of argv (PE-25; Sol S4/R5).
- bridges and worker: child output is decoded as a UTF-8 stream in both runtimes, so a character
  split between two reads is no longer turned into U+FFFD by the JS (Sol S1/R7, S2/R15). The
  worker relays and counts as progress only stderr chunks that decode to text, as Node does.
- go: the bridges' CLIs and `orch`'s synchronous children run in a process group of their own, so a
  timeout or signal stops their tools too, and a cancelled child's leftover group members are
  killed once it is reaped (PE-22; Sol S1/R8).

- go: the supervisor's log and lock are owner-only (an existing log keeps its mode). `flip.json`
  and the conductor briefs are replaced atomically, so a supervisor never reads a half-written
  gate (PE-21; Sol S3/R9, S2/R5, S2/R4).
- flip: `gate` on an array `flip.json` now records the gate, and the `.git/info/exclude` check
  splits lines as Git does (both runtimes; Sol S2/R12, S2/R14).
- worker: a signal that arrives while `worker start` is still starting its bridge is passed to
  the bridge once it runs (Go; Sol S2/R7). Both runtimes read the brief before claiming the lane, so
  an unreadable brief no longer leaves a permanent lock (Sol S2/R11).
- plugin: delegated and orch calls are never started once cancelled. A child that ignores SIGTERM
  is killed 15 s later, the plugin stops waiting 10 s after a child exits even if a descendant holds
  its output, output is decoded as a UTF-8 stream, and `orch routing`/`budget gate` get 90 s
  (Sol S4/R1, S4/R2, S4/R6).
- go: `orch`'s synchronous children (the supervisor's channels, `flip run`'s launcher, kickoff,
  doctor probes, usage feeds) stop at Node's 1 MiB combined output limit instead of buffering
  without bound (PE-20; Sol S3/R3).
- docs: the root README now tells the story (why, benefits and costs, daily commands, how it works,
  using it on a project, the mailbox in plain words, away mode) with `orch` commands throughout.
  The technical reference moves to `orchestrator/README.md` (scripts, routing, budgets, flips,
  supervisor, mailbox, policy, state files, install internals, troubleshooting, security) and
  `go/README.md` (plugin runtime, `orch` install); new `claude/README.md` covers the Claude Code
  front door. Fixed `go install ...@v0.5.0` (was `@go/v0.5.0`), removed pre-release wording, and
  documented that `flip gate --clear` alone does not restart a gated conductor.
- testdata: the fixture redactor's scan also checks raw JSONL text and every escaping layer, so
  duplicate keys and nested escapes can no longer hide a credential; committed fixtures re-scanned
  clean (Sol S1/R3).
- go: `procs` marks a child reaped as soon as it exits and drains its pipes afterwards (at most the
  PE-10 grace), so no signal can reach a reused process group during the drain (Sol S2/R2).
- go: run records, worker state and the supervisor's state files are written owner-only (0600
  files, 0700 directories) through a unique temp file and an atomic rename, so planted symlinks are
  never followed and concurrent writers cannot mix content (PE-21; Sol S1/R1, S2/R4, S2/R9, S3/R4,
  S3/R5). Run-record names replace `/` in session ids in both runtimes (Sol S1/R4).

## 0.5.0 — 2026-10-19

The Go runtime release. `orch`, one static Go binary, ports every `orchestrator/bin/*.mjs` script,
and a parity harness diffs each command against the JS. The plugin gains `runtime: "go"|"js"`
(default `js`). `install.sh` builds `orch` with Go or downloads the release asset (`--skip-binary`
opts out), and the doctor gains an `orch binary` row. The JS runtime stays the default; its removal
is planned for 0.6.0 behind a human gate.

- plugin: an opt-in `go` runtime (S10, three parts). Selection and routing: `plugins/orchestrator.ts` selects its runtime from the
  plugin option `runtime`, then `ORCH_RUNTIME`, else `js` (only `js` and `go` are accepted); in
  `go` mode it resolves `orch` from the option `binary`, `ORCH_BIN`, `~/.local/bin/orch`, then
  `PATH`, and a bad configuration fails loudly (tools error, hooks degrade) instead of falling
  back. Routing, cached routing, effort, the codex_review budget gate and the model-sync registry
  go through a runtime adapter; `orch_usage_status` metadata gains `runtime` and `orch`; a spawn
  failure now resolves with code -1 instead of an uncaught error. `js` (the default) is
  unchanged. New `tests/plugin-routing.test.mjs` drives the plugin through a fake opencode
  context.
- plugin, go runtime, state files and spawns: go mode now runs on `orch` alone (no `lib/*.mjs` import, no `bin/*.mjs` run).
  mailbox_post/list, manifest_create/verify, the incident tool and the `execute.after` incident
  hook call `orch mailbox|manifest|incident` (bodies and details on stdin, the project root always
  by flag), and delegate, worker_start, worker_status, supervisor and the conductor flip spawn
  `orch <script>` with the argv the JS script gets. In go mode, free text beginning with `--`
  that would travel as an argument (including mailbox_list filters and the incident kind) is
  refused before spawning (D-S10-4), and `manifest_create` with no paths is
  orch's usage error (D-S10-1). Also: the runtime value is trimmed and case-insensitive, option
  `binary` expands a leading `~`, `ORCH_HOME`/`ORCH_REGISTRY` are resolved to absolute paths, an
  `orch` timeout says it timed out, and model sync and the usage-limit flip log a misconfigured
  runtime instead of swallowing it. New `tests/plugin-tools.test.mjs`. `js` (the default) is unchanged.
- plugin, go runtime, differential and docs: new `tests/plugin-orch-parity.test.mjs` runs the
  plugin in both runtimes against the real `orch` (built into a temp dir, or `ORCH_TEST_BIN`;
  skipped when neither exists) over byte-identical sandboxes and compares the tool and hook
  results: usage_status, native delegate, red codex_review, mailbox post/list, manifest
  create/verify (byte-equal manifests), incident (the log line differs only in `at` and the PE-4
  `version`), worker_status, supervisor status, the context hook and the red `execute.before`
  gate. README gains "Plugin runtime" (selection, binary resolution, fail-loudly policy,
  D-S10-1..7, rollback, how to tell which runtime answered); `go/README.md` and an
  `opencode.jsonc` comment above `"plugins"` point to it. The S10 live check confirmed
  `ORCH_RUNTIME` (including `opencode service set env`) as the supported switch; object-form
  plugin options in a project `opencode.jsonc` did not reach the plugin (known limitation,
  probably upstream).
- Go port: the `kickoff` parity group gains the launching cases: headless conductor lanes on
  claude and opencode (runners settled and everything they wrote compared), the interactive
  `claude` and `opencode` launches (exit code passed through; a signal exits 0) and a dry run that
  really starts and then stops a supervisor serve. The fake `opencode` gains a `--prompt` mode
  (`$HOME/fake-opencode-tui/`), and the fakes no longer call `basename`. `orch watch`'s routing
  error is matched with `errors.As` (`watch.IsRoutingError` removed). Every `bin/*.mjs` script is
  now ported.
- Go port: `orch kickoff --brief FILE [--brief FILE2 ...] [--door auto|claude|opencode] [--headless]
  [--unattended] [--cwd DIR] [--dry-run]` mirrors `bin/kickoff.mjs`: brief checks, refreshed
  routing, door choice, the both-red pause, the preflight (policy, gate, supervisor, git exclude),
  and the headless (`orch worker start --detach`) or interactive (`claude`/`opencode`) launch, with
  its three latent defects (dry-run side effects, unvalidated `--door`, both-red without a
  supervisor start) recorded as incidents. New `kickoff` parity group (non-launching cases), run
  in CI. `orch watch` now treats a pid of `0`, `false` or `""` as alive, as `watch.mjs`'s
  `alive()` does (S9b review finding).
- Go port: `orch watch [--cwd DIR] [--follow] [--interval 20]` mirrors `bin/watch.mjs` line for
  line, including its two latent defects (a `flip.json` holding only a human gate crashes the
  render; `## Next action (exact)` prints `next: (exact)`), both recorded as incidents. New
  `watch` parity group (single renders and `--follow`), run in CI. The Node timer clamp moves to
  `procs.TimerDelay`, shared by the bridges and watch.
- Go port: `orch doctor` mirrors `bin/doctor.mjs` row for row (binaries, logins, usage feeds and
  routing, opencode config, plugin, agents, plugin log) and exit code; PE-18 adds `orch binary`
  and `orch self-test` rows and runs `go test ./...` for the `unit tests` row. The parity fakes
  gain a canned-answer mode; new `doctor` parity group, run in CI. Registry read failures are
  now worded as Node's (`ENOENT: no such file or directory, open '<file>'`) in every `orch`
  command, and a role tier without a string model fails doctor's `usage/routing` row with the JS
  TypeError; new `doctor/registry-missing` parity case.
- Go port: `orch supervisor start|serve` replace `bin/supervisor.mjs start|serve`: `start` launches
  a detached, kept-awake `orch supervisor serve`; `serve` claims the same `supervisor.lock`, ticks
  every 5 s (lanes, events, wake channels, redispatch, conductor flip evaluation, all self-exec'd
  on `orch`) and stops through `supervisor.stop`. New `supervisor` parity cases run both daemons
  for one tick; PE-17 (a missing `opencode` does not stop `orch supervisor serve`) is asserted.
  Further serve cases cover the stop file at the first loop, live-pid lanes, a `null` config,
  running lanes (quiet and check), the conductor flip evaluation (each case now required to reach
  it, with `decide` over a seeded usage cache), conductor launches (paused relaunch, pending launch,
  `decide` flip, finished-early `--resume`, never-started) and a redispatch launch with their
  runners settled, the `claude-resume` channel, restarts across runtimes and an opt-in slow case
  (`ORCH_PARITY_SLOW=1`).
- Go port: `orch supervisor status|stop|ack` matches `bin/supervisor.mjs` byte for byte (stdout,
  stderr, exit codes, `.orchestrator/supervisor-acks.json`), every state and acks shape included
  (Object.prototype names acknowledged, `__proto__` included, as in the JS). The supervisor's lane
  tick (phases, the two-phase intent, events, redispatch of budget-deferred lanes) is ported and
  unit-tested with an injected clock; `start` and `serve` arrive in S8c and exit 2 until then.
  New PE-16 (hand-edited non-object shapes). New `supervisor` parity group, run in CI.
- Go port (`go/`; the first command ported): `orch usage [--refresh] [--json]`
  produces output byte-identical to `bin/usage.mjs` and shares its usage cache in both directions,
  including keeping the last good reading when a refresh fails within 30 minutes of the last
  cache write. The window rolls, as in the JS: repeated failures keep the reading, aged by
  `last_good_at`. Also adds the plugin-facing `orch routing [--refresh | --cached] [--json]`,
  `orch budget gate --vendor V [--force] [--frontier]` and `orch effort --role R [--size S]`.
  The `usage` parity group compares all four against the JS.
- Go port: `orch mailbox post|list|ack|reply|inbox` matches `bin/mailbox.mjs` byte for byte
  (stdout, exit codes, `.orchestrator/mailbox/*.jsonl`) and appends one write per line, so JS
  and Go writers can share a mailbox. `post --json` prints the whole message. Uncaught JS errors
  print only their error line. The new `mailbox` parity group runs in CI.
- Go port: `orch manifest create|verify|diff` matches `bin/manifest.mjs` byte for byte (stdout,
  exit codes, manifest files), including the walk's symlink and FIFO handling and UTF-16 key
  order, and `internal/runrecord` writes run records with the same names and bytes as
  `writeRunRecord`. The new `manifest` parity group runs in CI.
- usage (JS and Go): a cached reading is stale once a routing window it holds (Claude session,
  weekly or Fable weekly; Codex 5-hour or weekly) resets after the reading was taken, not only
  after the 5-minute TTL. A conductor therefore relaunches right at a reset. When the post-reset
  collection fails, the last good reading is kept and retried every 5 minutes, not on every read.
- Go port: `orch incident record|list|report|share` and `orch policy show|set|unattended` match
  `bin/incident.mjs` and `bin/policy.mjs` byte for byte (stdout, exit codes, incident log, share
  bundle, `policy.json`): the same redaction, fingerprints (JS `toLowerCase`, final sigma
  included) and report order (a port of Node 26's V8 sort; Node 22/24 may order malformed short
  logs differently, PE-5), the registry read exactly as `policy.mjs`
  reads it, and an explicitly empty `HOME` kept as Node keeps it. Entries carry `orch`'s version
  (PE-4). The new `incident` and `policy` parity groups run in CI.
- Go port: `orch claude-exec` matches `bin/claude-exec.mjs` byte for byte when both read the same
  registry (argv passed to `claude`, stdout JSON, `[claude]` progress lines, exit codes, run
  records, incident lines), lines split as Node's readline splits them (U+2028 and U+2029 too),
  including trailing bare value keys read as the JS `true` and Node's timer clamp. Documented
  differences: a bare `--prompt` is refused with exit 2 (PE-8), no Node warnings (PE-9), claude is
  killed 10 s after a signal it ignores (PE-10), SIGTERM/SIGINT are forwarded to claude and the run
  is still reported (PE-11), the post-run usage refresh runs as a detached, bounded `orch usage
  --refresh` that orch does not wait for, so it completes where the JS one exits first (PE-12;
  `ORCH_NO_POST_RUN_REFRESH=1` turns it off for the parity harness), and U+FFFD where a UTF-16 cut leaves a lone surrogate (PE-1). A `--prompt-file`
  that cannot be read prints Node's fs message (ENOENT, EISDIR, EACCES) and exits 2, as the JS does. The new `bridges`
  parity group replays the captured claude stream-json fixtures through a scripted fake and runs
  in CI.
- Contract: no sleep-waiting while independent work exists; re-read the mailbox between waits.
- Go port: `orch codex-exec` and `orch opencode-exec` match `bin/codex-exec.mjs` and
  `bin/opencode-exec.mjs` byte for byte (argv, the prompt on codex's stdin, environment, stdout
  JSON, `[codex]`/`[opencode]` progress lines, exit codes, run records, incident lines), latent
  JS defects included (`--approve-for-me` with `workspace-write`, opencode's ignored `--help` and
  `--effort`). PE-8 to PE-11 now cover all three bridges, PE-12 (the detached post-run refresh)
  claude and codex; new PE-13: codex exiting without reading its prompt is reported as a failed
  run where the JS may die of `write EPIPE`. A NUL byte in a bridge's argv fails as Node's spawn
  does. The `bridges` parity group gains `codex` and `opencode` subgroups over scripted fakes.
- Go port: `orch worker status|stop` matches `bin/worker.mjs` byte for byte (stdout, exit codes,
  lane state and lock files read the same way), with new PE-14: `stop` signals only a stored pid
  of at least 2, where the JS would SIGINT every process of the user for pid `1` or `true`.
  New `worker` parity group, run in CI.
- Go port: foreground `orch worker start` matches `bin/worker.mjs start` (stdout with the relayed
  bridge progress, stderr, exit codes, lane state and lock, mailbox, incidents) and runs
  `orch <harness>-exec` as its bridge. PE-10, PE-11 and PE-13 are extended to the worker: it never
  kills its bridge and waits at most 10 s for its pipes, records the Go bridge's report of an
  interrupted run, and survives a bridge that never reads its prompt where the JS dies of
  `write EPIPE`.
- Go port: `orch worker start --detach` matches `bin/worker.mjs`: the synchronous lane-locked
  refusal `flip.mjs` relies on (stderr, exit 3, stdout empty), stale-lock removal, the 0600 run log,
  the runner re-run without `--detach` in a session of its own, and keep-awake (`caffeinate` /
  `systemd-inhibit`, `runtime.keep_awake`), printing `{lane, runner_pid, log_path, run_id,
  keep_awake}`. The `worker` parity group gains multi-step detached cases (settle, a stopped
  running worker under PE-11) over new `which`, `caffeinate` and `systemd-inhibit` fakes.
- Go port: `orch flip decide|pause|gate|status` matches `bin/flip.mjs` byte for byte (stdout,
  stderr, exit codes, `.orchestrator/flip.json`, mailbox), every `flip.json` shape included
  (`gate` on a number, string or `true` dies of the JS TypeError; a bare `--reason` on `gate`
  posts no message, as in the JS). New PE-15: `decide` reads `flip.json`'s `at` in the ECMAScript
  date formats only, so a hand-written legacy date never holds back a hand-back. New `flip`
  parity group, run in CI.
- Go port: `orch flip run` matches `bin/flip.mjs run`: `.git/info/exclude` (lines split as the JS
  multiline regex splits them), the continue brief, `flip.json` and the handoff message, then
  the conductor launched as `orch worker start --detach` (so a Go-driven flip runs the conductor
  on the Go worker and bridges), or `pending_launch` when the lane is locked. PE-1, PE-2 and
  PE-10 are extended to its `launcher` text and wait; worktree `.git` files still crash, as in
  the JS. The `flip` parity group gains pending and full launches against the fakes.
- install (S11a): `install.sh` builds `orch` from the checkout (`CGO_ENABLED=0 go build -trimpath
  -ldflags="-s -w"`), checks that `orch version` matches `go/internal/version/VERSION`, and places
  it at `~/.local/bin/orch` (or `$ORCH_BIN_DIR/orch`) with a no-clobber hard link; the receipt
  records it as `binary`. An existing `orch` at the destination, a relative or overlapping
  `ORCH_BIN_DIR`, a different filesystem from the installer state, or no `go` on `PATH` is refused
  before any change. New `--skip-binary` installs exactly as before (`binary: null`). Uninstall
  archives the recorded binary as `removed-binary` in the private archive (never deletes it). It
  leaves in place a non-regular file, a file an interrupted install did not place (the receipt
  records the placed file's identity as `binaryIdentity`), and any file when `removed-binary`
  already exists. A receipt `binary` must be an absolute path named `orch`. 0.4.x receipts
  without `binary` still uninstall.
- install and doctor (S11b): without `go`, `install.sh` downloads `orch_<VERSION>_<os>_<arch>` and
  `checksums.txt` with `curl` from the GitHub release `v<VERSION>` (or `ORCH_RELEASE_BASE_URL`,
  https only) and places the asset only when its sha256 matches (integrity only; unsigned; needs the
  published 0.5.0 release). No `go` and no `curl`, an unmapped platform or a non-https base URL is
  refused before any change; a failed download or checksum leaves a recoverable `preparing`
  receipt. `doctor.mjs` gains the `orch binary` row after `node present` (`ORCH_BIN`, else
  `~/.local/bin/orch`, else `PATH`; WARN when none is found), byte-identical to `orch doctor`'s
  row; the doctor parity cases now compare it and add `no-binary`.
- bridges (JS): `claude-exec.mjs`, `codex-exec.mjs` and `opencode-exec.mjs` no longer fail with
  `EAGAIN ... read` when they read stdin before their worker runner has written the prompt to the
  pipe (load makes that more likely; it is not the cause): `readPrompt` tests stdin with
  `tty.isatty(0)` instead of `process.stdin.isTTY`, which made the pipe non-blocking (incident
  e80dd9b226b2). `mailbox.mjs reply` reads an answer piped on stdin the same way.

## 0.4.7 — 2026-10-19

- Contract: pipelining. A slice waiting for a review the other vendor cannot give yet no longer
  idles the conductor; it starts the next independent slice and queues the review.
- usage: keep the last good reading for 30 minutes when a refresh fails (a 429, say) and show its
  age.
- routing: skip preferences whose tier is missing from the registry (was a TypeError in
  balancing).
- worker: detach checks the lane lock synchronously; the supervisor launches a flip target that
  never started.

## 0.4.6 — 2026-10-18

- routing: the 5-hour window steers routing; a weekly cap blocks a vendor only when red; coding is
  balanced across vendors by 5-hour headroom.
- routing: role resolution uses frontier tiers only on a green weekly posture and routine planning
  falls to Opus 5.5; a `size=hard` planning delegation may still take the first frontier tier that
  is not red (amber or unknown included), Fable before GPT-6 Astra, which is reserved for it.
- codex-exec: a completed turn with no tool calls and a tool-host failure message is a failure
  (`tool_host_unavailable`).

## 0.4.5 — 2026-10-18

- README operations cheat sheet; keep-README-current rule in the contracts and in review.

## 0.4.4 — 2026-10-18

- Autonomy policy (`policy.mjs`; verified slices are pushed automatically), `mailbox.mjs
  reply|inbox`, and `watch.mjs` shows open questions.

## 0.4.3 — 2026-10-18

- Human-gate state (`flip.mjs gate`) that the supervisor never relaunches.
- flip: a pending launch when the lane is still locked; the supervisor performs it after release.
- Headless rule: subagents run in the foreground only; the supervisor resumes a conductor that
  exited without a handoff.
- mailbox: `ack` accepts unique id prefixes; claude-exec no longer records "success" as an error.

## 0.4.2 — 2026-10-18

- No reset grace when starting work (an imminent reset no longer makes a window greener); relaunch
  failed or deferred conductor lanes; session-limit text is a deferral.

## 0.4.1 — 2026-10-18

- `kickoff.mjs` (preflight + door selection + launch, pause when both red) and `watch.mjs` (single dashboard).

## 0.4.0 — 2026-10-18

- Incident log (`lib/incident.mjs`, `bin/incident.mjs`, `orch_incident` tool): redacted, fingerprinted, local; written automatically by bridges, worker, supervisor and plugin; read by `/retro` and the audit skill; `share` makes a sanitized bundle, never uploads.
- Keep-awake: detached workers and the supervisor run under `caffeinate -i -s` (macOS) / `systemd-inhibit` (Linux); `runtime.keep_awake` toggle.

## 0.3.1 — 2026-10-18

- `flip.mjs pause` + supervisor relaunch of a paused conductor as soon as either window is usable (home door preferred); previously only the other-door case relaunched automatically.

## 0.3.0 — 2026-10-18

- Conductor flip: `orchestrator/bin/flip.mjs` (decide/run/status) with a tested policy
  (`lib/flip.mjs`): forced flip when the active conductor's vendor is red, hand-back to the home
  door at brief boundaries once it is stably green, cooldown, pause when both are red.
- Plugin flips the opencode conductor to the Claude door on usage-limit errors; the supervisor
  evaluates flips once a minute when a project has a conductor lane or has flipped before.
- `opencode-exec.mjs` bridge and `worker.mjs --harness opencode --agent` so either conductor can
  run headless as the `conductor` worker lane.
- `.orchestrator/` is added to `.git/info/exclude` on first flip/use.

## 0.2.0 — 2026-10-18

- Effort policy by task size (`effort_policy` in `orchestrator/models.json`); `orch_delegate` takes
  `size`; static agent efforts lowered to each model's recommended default (escalation per task).
- Budget gate for native OpenAI subagents: refused with guidance when the OpenAI window is red;
  provider usage-limit errors trigger an immediate usage refresh.
- `orch_delegate` infers the harness from an explicit model name (fixes the "Codex bridge failed
  before starting" observed during the first release run).
- Native provider detection reads opencode v2's SQLite credential store.
- Claude Code front door: `claude/agents/*` (conductor, architect, coder, reviewer, verifier,
  researcher and their hard variants), `claude/skills/orchestrator`, `install-claude.sh`.
- Contract: size every delegation; a conductor whose own provider is exhausted records STATE.md and
  hands off instead of retrying.

## 0.1.0 — 2026-10-18

First release.

- Sanitized initial release of the two-vendor opencode v2 orchestration configuration, role
  prompts, usage readers, routing, CLI bridges, mailbox, manifests, workers and supervisor.
- Reversible managed-entry symlink installation, optional copy mode, private force backups,
  ownership checks and archive-preserving uninstall.
- Root setup, routing, security, recovery and contribution documentation; MIT license.
- Retained 14 fast runtime unit tests and added 29 isolated installer integration cases under
  separate root `tests/`, outside the doctor's unit-test timeout.
- Initial packaging observation: Codex exec CLI argument parsing failed before delegation in that
  environment; this is not a fresh reproduction on the latest live CLIs. The original runtime is
  retained, not represented as end-to-end verified.

See [orchestrator/CHANGELOG.md](orchestrator/CHANGELOG.md) for the original implementation notes.
