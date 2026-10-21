# Work Trains: operator guide

Work Trains is a private status page for your orchestrator projects, reachable from your phone. What it is, what it
costs and what leaves your Mac are in the [root README](../README.md#remote-status-work-trains); this page is the
operator detail: the Cloudflare Access setup, the deploy (by hand or from GitHub Actions), the Mac side's files, and the
tests.

| Directory | What it is |
|---|---|
| [`protocol/`](protocol/README.md) | The versioned v1 wire protocol (JSON Schema, validator, examples) shared by the Worker and `orch` |
| [`worker/`](worker/) | The Cloudflare Worker and its page: the Access check, the D1 migrations, the machine and browser APIs, the email notifier |
| [`e2e/`](e2e/) | The end-to-end test: the Worker under `wrangler dev --local` behind a local Access emulator, driven by the real `orch remote once` |
| [`../go/README.md`](../go/README.md#orch-remote-go-only) | `orch remote`, the Mac side: every subcommand, the snapshot, the sync loop |

Every value below in angle brackets or under `example.com` is a placeholder. This repository is public: never commit
your domain, team name, AUD tag, account or database id, email address or any token. Keep them in a password manager.

## Before you start

- A Cloudflare account with your domain on it. Every product used here has a free plan.
- Node 26, for the deploy. `npm ci` in `remote/worker` installs the pinned `wrangler` (4.118.0, in its
  `package-lock.json`); run it as `npx wrangler` there, or put `remote/worker/node_modules/.bin` on your PATH.
- An `orch` that has `orch remote` (`orch remote --help` prints its usage): a release after v0.5.0, or one built from
  `main`.
- Setup order: Access (1) -> D1 and deploy (2) -> machine token and Worker secrets (2) -> `orch remote init` (3) ->
  LaunchAgent (4) -> first sync (5).

Cloudflare's dashboard labels change. The pages linked below were checked on 2026-10-20; if a label differs, follow
the linked page.

## 1. Cloudflare Access

Access stands in front of the whole hostname: you sign in with a one-time PIN sent to your email, and the Mac
presents a service token. The Worker checks the Access assertion on every request as well.

1. **Zero Trust** (Free plan, up to 50 users, $0: <https://www.cloudflare.com/plans/sase-zero-trust/>). In the
   Cloudflare dashboard open **Zero Trust**. If asked, pick a team name and the Free plan. Your team domain is
   `https://<team>.cloudflareaccess.com` (shown under Zero Trust **Settings**).
2. **One-time PIN login**: **Zero Trust > Integrations > Identity providers > Add new identity provider > One-time
   PIN** (<https://developers.cloudflare.com/cloudflare-one/identity/one-time-pin/>).
3. **Service token for the Mac**: **Zero Trust > Access controls > Service credentials > Service Tokens > Create
   Service Token**. Name `work-trains-mac`; duration 1 year (8760h) unless you prefer otherwise; put the expiry in
   your calendar (or add the **Expiring Access Service Token** notification, which warns a week ahead). The Client
   Secret is shown once. Immediately, in Terminal, run `orch remote service-token set` and paste the Client ID (first
   line) and the Client Secret (second line) at its prompts. Close the dialog only after `orch remote status` shows
   `access-client-id` and `access-client-secret` as `yes`.
   (<https://developers.cloudflare.com/cloudflare-one/identity/service-tokens/>: clients send `CF-Access-Client-Id`
   and `CF-Access-Client-Secret`; the policy action must be **Service Auth**.)
4. **Application**: **Zero Trust > Access controls > Applications > Create new application > Self-hosted and
   private > Add public hostname**: subdomain `trains`, domain `example.com` (yours), path empty
   (<https://developers.cloudflare.com/cloudflare-one/applications/configure-apps/self-hosted-public-app/>).
   - Policy 1 "owner": Action **Allow**; Include **Emails** = `owner@example.com` (yours; add each address you sign in
     with). Session duration 24 hours (longer means fewer PIN prompts).
   - Policy 2 "mac": Action **Service Auth**; Include **Service Token** = `work-trains-mac`.
   - Login methods: One-time PIN only. Optionally enable instant authentication when it is the only method.
   - Save. On the application's overview copy the **Application Audience (AUD) Tag**
     (<https://developers.cloudflare.com/cloudflare-one/identity/authorization-cookie/validating-json/>).
5. Keep privately: the team domain, the AUD tag, the service token's Client ID and your email. Step 2 sets them as
   Worker secrets.

If the dashboard refuses an application for a hostname that has no DNS record yet, do the first deploy in step 2
first: until its secrets are set the Worker answers 503 on every route, so nothing is exposed. Then come back here.

## 2. D1, deploy and Worker secrets

From a checkout of this repository:

```sh
cd remote/worker
npm ci
wrangler login                      # browser OAuth to your account
wrangler d1 create work-trains      # prints a database_id
cp wrangler.deploy.example.jsonc wrangler.deploy.jsonc   # gitignored
# edit wrangler.deploy.jsonc: set database_id, and routes[0].pattern = "trains.example.com" (yours)
git status --porcelain              # must NOT list wrangler.deploy.jsonc
wrangler d1 migrations apply work-trains --remote -c wrangler.deploy.jsonc
orch remote token rotate            # stores the machine token in your Keychain; prints only its SHA-256
wrangler deploy -c wrangler.deploy.jsonc   # 503 on every route until the secrets below exist
# Each command prompts for its value; paste it at the prompt, never as an argument:
wrangler secret put ACCESS_TEAM_DOMAIN       -c wrangler.deploy.jsonc   # https://<team>.cloudflareaccess.com
wrangler secret put ACCESS_AUD               -c wrangler.deploy.jsonc   # the AUD tag
wrangler secret put OWNER_EMAIL              -c wrangler.deploy.jsonc   # the policy's emails, comma-separated
wrangler secret put ACCESS_SERVICE_CLIENT_ID -c wrangler.deploy.jsonc   # the service token's Client ID
wrangler secret put PUBLIC_ORIGIN            -c wrangler.deploy.jsonc   # https://trains.example.com, no trailing slash
wrangler secret put MACHINE_TOKEN_SHA256     -c wrangler.deploy.jsonc   # the hash token rotate printed
```

The deploy config keeps `workers_dev: false` and `preview_urls: false`, so the Worker is reachable only on your
hostname, behind Access. `UNDO_SECONDS` (default 10) is how long the page offers Undo before the Mac may pull an
answer or action.

**Email notifications (optional).** After a push, a new blocking question sends one email (never the question's
text; at most 20 a day). If your domain already receives mail elsewhere, enable Email Routing only on a subdomain
(**your domain > Email > Email Routing > Settings > Subdomains**, for example `notify.example.com`); if the dashboard
wants to change your apex MX records, stop and skip notifications. Add your address under **Destination addresses**
and click the verification link
(<https://developers.cloudflare.com/email-routing/setup/email-routing-addresses/>). Then:

```sh
wrangler secret put NOTIFY_FROM -c wrangler.deploy.jsonc   # a sender address on the Email Routing subdomain
wrangler secret put NOTIFY_TO   -c wrangler.deploy.jsonc   # your verified address
```

When GitHub Actions deploys (below), also set the environment variable `WORK_TRAINS_SEND_EMAIL` to `true`, or the
rendered config leaves out the `send_email` binding and no email is sent.

**Check it.** Opening `https://trains.example.com/` in a private window redirects to
`https://<team>.cloudflareaccess.com/...`. On your phone, sign in with the PIN: the page loads and shows "Mac seen
never". `wrangler deployments list -c wrangler.deploy.jsonc` lists the deployment.

**Roll back** with `wrangler rollback -c wrangler.deploy.jsonc` (the previous version). Access keeps the hostname
closed either way.

To deploy every verified push to main without running wrangler yourself, set up
[Deploy from GitHub Actions](#deploy-from-github-actions) once.

## Deploy from GitHub Actions

Once set up, `.github/workflows/remote-deploy.yml` deploys the Worker whenever the Remote workflow passes on a push
to main, and on demand. The workflow names no instance: everything comes from a GitHub **environment** called
`work-trains-production`. In a repository without that environment, such as a fork, a push skips the deploy with
a notice and fails nothing; a run started by hand fails, because it asked for a deploy that cannot happen.

**What a run does.** It checks out the exact commit the Remote workflow tested (refusing one that is not on main),
runs `npm ci` (wrangler is pinned by `remote/worker/package-lock.json`), and renders `wrangler.deploy.jsonc` from
[`wrangler.deploy.example.jsonc`](worker/wrangler.deploy.example.jsonc) with `scripts/render-deploy-config.ts`.
The rendered file is never committed or printed, and the D1 id and hostname are masked in the log. Then it reads
which commit is live: every CI deploy stores its commit as the version's message (`wrangler deploy --message
<sha>`). A push that changed nothing under `remote/worker/` or `remote/protocol/` since then deploys nothing.
Otherwise the run applies D1 migrations (`wrangler d1 migrations apply --remote`), deploys, and runs a smoke
check that uses no credentials. The check passes when `GET /` redirects to `*.cloudflareaccess.com` and
`GET /api/v1/machine/pull` is refused. If the smoke check, or reading the live version after the deploy, fails,
the run rolls back to the version that was live before and fails. The run's summary has the commit and version ids.
A push whose commit the live one already contains (two runs finishing out of order) deploys nothing.

**What the smoke check does not catch.** Access answers both of its requests before the Worker runs, so a
Worker that throws on every request still passes. The check proves that the deploy completed and that Access
still fronts the hostname; it does not prove the Worker is healthy behind Access. After a deploy, open the page
or run `orch remote once`; if the Worker is broken, roll back by hand (below).

**Migrations are add-only.** The plan step fails the run if a migration file that is already live was edited:
D1 records applied migrations by name, so an edit would never run. Add a new numbered file instead. A rollback
does not undo migrations, so each one must keep the previous Worker version working (add tables and columns;
do not rename or drop them in the same release).

**What CI does not manage.** Worker secrets (`wrangler secret put`), the Access application and policies, the
service token, DNS, and Email Routing. They stay as you set them in Cloudflare, and changing them is still done by
hand as in steps 1 and 2. The custom domain is the exception: the deploy keeps it the way the hostname variable
says (see [Moving to another hostname](#moving-to-another-hostname)).

**API token.** Create a token limited to this account and zone. In the dashboard, open **My Profile > API Tokens >
Create Token > Custom token > Get started** and set:

| Permission | Why |
|---|---|
| Account / Workers Scripts / Edit | Upload and deploy the Worker, its versions and rollback, the custom domain, workers.dev on or off |
| Account / D1 / Edit | `wrangler d1 migrations apply --remote` |
| Zone / Zone / Read | Only when `workers_dev` is false: wrangler then finds the zone of each route, custom domain included (`GET /zones`) |
| Zone / Workers Routes / Read | The same case: wrangler lists the zone's Worker routes to refuse one another Worker holds |

Under **Account Resources** include only your account; under **Zone Resources** include only your domain's
zone. Account Settings, Memberships and User Details are not needed, because `CLOUDFLARE_ACCOUNT_ID` is set and
wrangler does not have to look the account up. Neither DNS nor Access permissions are needed: the custom domain
and its DNS record already exist. Sources:
[GitHub Actions for Workers](https://developers.cloudflare.com/workers/ci-cd/external-cicd/github-actions/),
[API token permissions](https://developers.cloudflare.com/fundamentals/api/reference/permissions/), and the API
reference for [Attach to Domain](https://developers.cloudflare.com/api/resources/workers/subresources/domains/methods/update/)
(Workers Scripts Write), [List Zones](https://developers.cloudflare.com/api/resources/zones/methods/list/) (Zone Read),
[List Routes](https://developers.cloudflare.com/api/resources/workers/subresources/routes/methods/list/) (Workers
Routes Read) and [D1 query](https://developers.cloudflare.com/api/resources/d1/subresources/database/methods/query/).
The zone lookup is in wrangler 4.118's own deploy code, not in Cloudflare's docs. If a deploy fails with an
authentication error at the "Custom domains" stage, add **Zone / Workers Routes / Edit** for the zone and say so in
an issue.

**Environment.** **GitHub > your repository > Settings > Environments > New environment** named
`work-trains-production`. Under **Deployment branches and tags**, choose **Selected branches and tags** and add
`main`. This main-only policy is **required**, not a preference: a run started by hand on another branch uses
that branch's copy of the workflow, which could be edited to print the secrets, and the branch policy is what keeps
the environment's secrets away from it. A required reviewer would hold every deploy until someone approves it, so
add one only if you want that.
Then add:

| Kind | Name | Value |
|---|---|---|
| Secret | `CLOUDFLARE_API_TOKEN` | The token above |
| Secret | `CLOUDFLARE_ACCOUNT_ID` | Your account id (**Workers & Pages** overview, right column) |
| Variable | `WORK_TRAINS_D1_ID` | The `database_id` that `wrangler d1 create` printed |
| Variable | `WORK_TRAINS_HOSTNAME` | `trains.example.com` (yours; no scheme or slash) |
| Variable | `WORK_TRAINS_WORKERS_DEV` | Optional: `true` keeps the workers.dev URL answering; unset means `false` |
| Variable | `WORK_TRAINS_SEND_EMAIL` | Optional: `true` declares the `send_email` binding email notifications need; unset means `false`: the binding is left out (as an account without Email Routing needs) and the Worker sends no email |

With `gh`, pass each value on stdin so it never appears in argv or shell history:
`gh secret set CLOUDFLARE_API_TOKEN --env work-trains-production` (it prompts) and
`printf '%s' "$value" | gh variable set WORK_TRAINS_D1_ID --env work-trains-production`. An environment that
exists but lacks a secret or variable fails the run instead of skipping it.

**First run.** **Actions > Remote deploy > Run workflow** on `main` (or `gh workflow run remote-deploy.yml --ref
main`, then `gh run watch`). Its plan warns that the live version names no commit, since it was deployed by hand,
and deploys the head of main. Compare `wrangler deployments status` before and after.

**Redeploy or roll back.** Run the workflow by hand with the `sha` input set to the full SHA of any commit on main.
An older commit rolls the code back, but not the migrations. For an immediate rollback without a build, run
`wrangler rollback -c wrangler.deploy.jsonc` by hand: the next CI deploy then finds that the live version names an
older commit and diffs against it.

## 3. Configure the Mac

```sh
orch remote init --url https://trains.example.com            # roots default to ~/Developer
orch remote init --url https://trains.example.com --root ~/Work --exclude scratch --force   # example: other roots
orch remote push --dry-run                                     # exactly what would be sent; sends nothing
orch remote status
```

A project is any root, or direct child of a root, that has a `.orchestrator/` directory.

## 4. Keep it running (LaunchAgent)

```sh
orch remote launchd > ~/Library/LaunchAgents/org.opencode-orchestrator.work-trains.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/org.opencode-orchestrator.work-trains.plist
```

`orch remote launchd` writes nothing itself; it prints the plist on stdout and these install and uninstall commands
on stderr. The agent runs `orch remote serve` at login and restarts it at most every 30 s. Rerun both commands after
moving or replacing the `orch` binary, since the plist holds its absolute path.

## 5. First sync

`orch remote once` runs one cycle and prints `pulled N, applied N, pushed N, deferred N`; it refuses to run while
`serve` holds the lock, and then `serve` syncs on its own. `orch remote status` should show a recent `last push` and
`last pull` and `last error: none`, and the page lists your projects.

## Moving to another hostname

The Worker serves the page on one origin, `PUBLIC_ORIGIN`: a browser write (Accept, a typed answer, Undo, pause,
resume, hide) from any other origin is refused, and notification emails link to it. Moving the page, say from the
`workers.dev` URL to `https://trains.example.com`, is therefore a one-step cut-over, in this order:

1. **Custom Domain**: **Workers & Pages > work-trains > Settings > Domains & Routes > Add > Custom Domain** =
   `trains.example.com` (<https://developers.cloudflare.com/workers/configuration/routing/custom-domains/>).
   Cloudflare creates the DNS record and the certificate.
2. **Access**: the Access application must cover the new hostname (**Zero Trust > Access controls > Applications >
   your app > Add public hostname**), so it keeps the same AUD tag. A second application would have another AUD, and
   the Worker would refuse every request it lets through.
3. **Config**: put the hostname in `routes` in `wrangler.deploy.jsonc`, even though step 1 added it in the
   dashboard: `"routes": [{ "pattern": "trains.example.com", "custom_domain": true }]`. The config is the source of
   truth: a `wrangler deploy` whose `routes` list custom domains replaces the Worker's custom domains with that
   list (wrangler 4.118; with no `routes` it leaves them alone). Keep `"workers_dev": true` while you move if the
   old URL should keep answering. If [GitHub Actions deploys](#deploy-from-github-actions) for you, set the
   environment's `WORK_TRAINS_HOSTNAME` (and `WORK_TRAINS_WORKERS_DEV`) instead, and in step 4 set the secret,
   then run the workflow rather than `wrangler deploy`.
4. **Worker**: point it at the new origin and deploy:

   ```sh
   cd remote/worker
   wrangler secret put PUBLIC_ORIGIN -c wrangler.deploy.jsonc   # https://trains.example.com
   wrangler deploy -c wrangler.deploy.jsonc
   ```

5. **Mac**: point `orch remote` at it and restart the LaunchAgent, which reads the config only at start:

   ```sh
   orch remote init --url https://trains.example.com --force    # keep your --root/--exclude flags
   launchctl kickstart -k gui/$(id -u)/org.opencode-orchestrator.work-trains
   orch remote status                                             # a new last push, last error: none
   ```

6. **Phone**: open the new address and add it to the home screen again. The old address now sends the page and its
   files to the same path on `PUBLIC_ORIGIN` (a 302, after Access, so keep the old hostname behind the same Access
   application until you retire it). A page still open there can read but not write: its banner links to the new
   address. The API (the Mac's machine routes included) still answers on the old hostname until you set
   `"workers_dev": false`.

**Sign-in works at Access but the page says "Access token from another team domain".** The Access application in
front of the hostname belongs to another Zero Trust team than the Worker's `ACCESS_TEAM_DOMAIN`, for example one
created under another Cloudflare account. The Worker answers 403 `wrong_issuer` with the team domain it expects
(`expected_issuer`; a team domain is public, the AUD tag and emails are never shown), and `orch remote status` shows
`HTTP 403 wrong_issuer (the Worker expects Access team https://<team>.cloudflareaccess.com)` once the Mac hits it.
Fix it on one side: protect the hostname with the application of the team the Worker expects, or set
`ACCESS_TEAM_DOMAIN` and `ACCESS_AUD` to the team and AUD tag of the application that does protect it (and use a
service token of that same team for the Mac). Then `wrangler deploy` is not needed: secrets apply at once.
In the Worker's logs these refusals are lines with `"auth":"jwt_wrong_issuer"`. They mean this misconfiguration,
not an attack: Access in front of the hostname issues the token, so a token from another team means that team's
application protects the hostname. Only an `iss` shaped like a team domain gets `wrong_issuer`; any other bad
token is refused with `forbidden`. On a page or browser API route its `reason` is the code of the failed check
(`jwt_invalid:<jose code>`, `browser_email_mismatch`, `browser_write_foreign_origin`, ...), never an email or a
token; on a machine route the body is the same for every failure, so read the Worker's log for the code.

## Reference

**Config** `~/.config/orch/remote.json` (override with `ORCH_REMOTE_CONFIG`; directory 0700, file 0600; no secrets):

| Field | Default | Meaning |
|---|---|---|
| `url` | (required) | The Worker's origin, `https://` |
| `roots` | `["~/Developer"]` | Directories scanned for projects (each root and its direct children) |
| `exclude` | `[]` | Directory names or absolute paths to skip |
| `include_worktrees` | `false` | Also list linked git worktrees |
| `ci` | `true` | Send each project's latest GitHub Actions runs, read with your own `gh` |
| `poll_seconds` | `30` | Pull interval (15..300) until the Worker sends its own |

orch refuses a config inside any git work tree or readable by group or others. `serve` reads it once at start:
restart the LaunchAgent after editing it (`launchctl kickstart -k gui/$(id -u)/org.opencode-orchestrator.work-trains`).

**Keychain** (login keychain, service `opencode-orchestrator.work-trains`): `machine-token` (written by `orch remote
token rotate`), `access-client-id` and `access-client-secret` (written by `orch remote service-token set`). They are
written without ever appearing in argv, and never printed.

**State** in `~/.local/state/opencode/orchestrator/remote/`: `state.json` (machine id, per-project push bookkeeping,
the last push, pull and error, cached CI), `serve.lock` (one sync loop at a time), `remote.log` (the LaunchAgent's
output; never rotated). Each project also keeps `.orchestrator/remote/actions.json`, the journal of pauses and
resumes applied from the page. Field-level detail is in [go/README.md](../go/README.md#orch-remote-go-only).

**Protocol**: [protocol/README.md](protocol/README.md).

**Worker secrets**: listed at the top of [`worker/wrangler.deploy.example.jsonc`](worker/wrangler.deploy.example.jsonc).

## Tests

```sh
node --test 'remote/protocol/test/*.test.ts'          # protocol: schema, examples, Go goldens
(cd remote/worker && npm ci && npm test && npm run typecheck)   # Worker and deploy scripts; includes the public-repo guard
node --test 'remote/e2e/*.test.ts'                    # end to end, about 90 s
```

Run them from the repository root. The end-to-end test needs Go, Node 26, and `npm ci` in `remote/worker` with
`remote/worker/node_modules/.bin` on PATH (the pinned wrangler, as CI does); it uses no Cloudflare account. Name the files
with a glob as above: Node 26 does not accept a directory there. The guard (`worker/test/guard.test.ts`) fails on an
email address, an Access team host, a UUID or long hex value, or a 32-hex Cloudflare id in config files, outside the
placeholders, in tracked files under `remote/`; run it before every commit that touches them.

**Screenshots** of the page, for design review, come from fixtures, not from a deployment:

```sh
node remote/e2e/screens.ts --out remote/docs/design/before [--chrome <path>]
```

It serves `worker/public/` and fixture summaries (`e2e/screens/fixtures.ts`, fake projects only, each checked
against the protocol's `Summary` first) on loopback with the Worker's security headers, drives headless Chrome
over the DevTools protocol, and writes `<state>-<phone|desktop>.png` for the states `needs-you`, `calm`,
`stale-mac`, `failed-ci-paused` and `budget-credits` (phone 390x844, desktop 1280x900, both at 2x, light mode). It fails if a
fixture is not a valid `Summary` or the page requests anything off loopback. Chrome is `--chrome`, else `$CHROME`,
else Google Chrome in `/Applications`, else Playwright's `chrome-headless-shell`. It is not a test, so CI never
needs Chrome; and Chrome is not iOS Safari (no Dynamic Type, no safe-area insets), so the last look is on a phone.
