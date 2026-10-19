# Contributing

Keep changes bounded and reviewable. Read `AGENTS.md` and nearby code first. Keep routing defaults
in `orchestrator/models.json`; cite dated official sources for model/CLI changes. Do not bundle
runtime fixes into packaging or prompt rewrites into unrelated work.

Run from the checkout:

```sh
node --test orchestrator/tests/*.test.mjs
node --test tests/*.test.mjs
bash -n install.sh uninstall.sh
node -e 'JSON.parse(require("node:fs").readFileSync("orchestrator/models.json", "utf8"))'
(cd go && go build ./... && go vet ./... && test -z "$(gofmt -l .)" && go test ./...)
(cd go && ./parity/run.sh)   # slow; builds go/bin/orch and diffs every command against the JS
```

The first suite is the doctor's fast runtime check; the second is the isolated installer matrix and
the plugin tests. Keep slow integration cases in root `tests/`. Use temporary HOME/XDG directories
and command stubs: never test installation against a real user config. The Go lines need Go 1.25+;
under a temporary HOME, export `GOCACHE` to one shared directory (e.g. `$TMPDIR/orch-gocache`).
The Node code has no configured build or semantic typecheck task; report its syntax checks
honestly rather than calling them typechecks.

Match local formatting. Prettier can format Markdown/JavaScript (including the installer's embedded
JavaScript); no runtime npm dependencies are needed. Run the doctor only when authorized to check
local CLI logins and usage feeds, and distinguish it from deterministic tests.

Include the outcome, changed paths, exact checks/results, omissions and known issues in your
change summary. Protect changed behavior with tests. Have another reviewer inspect a fingerprint
of the released files; prefer the other vendor, and disclose when that gate is unavailable.
Never approve your own work.

Do not include credentials, personal account/tier/catalog data, private paths, real session IDs,
emails, logs or backups in patches/issues. Keep `.orchestrator/` private in work projects, scan the
whole candidate tree before release, and sanitize diagnostic output. Publication, pushes,
deployments and real config replacement require explicit approval.
