package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/cliargs"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/paths"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/ci"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/config"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/discover"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/keychain"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/snapshot"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/state"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/remote/wire"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/usage"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/version"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/worker"
)

// orch remote is Go-only: it has no bin/*.mjs twin, so it parses flags with the standard flag
// package and writes plain encoding/json, free of the JS parity rules.

const remoteUsage = `usage: orch remote init --url URL [--root DIR]... [--exclude NAME]... [--force]
       orch remote status [--json]
       orch remote push --dry-run [--json]
       orch remote once [--json]
       orch remote serve
       orch remote launchd [--label LABEL]
       orch remote token rotate | token revoke
       orch remote service-token set`

// workerSecretCommand stores the machine token's hash as the Worker secret.
const workerSecretCommand = "cd remote/worker && wrangler secret put MACHINE_TOKEN_SHA256 -c wrangler.deploy.jsonc"

// remoteUsageError is a bad command line: exit 2 with the usage text.
type remoteUsageError struct{ message string }

func (e *remoteUsageError) Error() string { return e.message }

// remoteEnv is where orch remote keeps its files, resolved once from the environment.
type remoteEnv struct {
	home       string
	configPath string
	stateFile  string
	paths      paths.Paths // the usage cache and registry the budget is read from
}

// Remote implements `orch remote init|status|push --dry-run|once|serve|launchd|token rotate|token revoke|service-token set`.
func Remote(args []string, stdio Stdio) int {
	err := runRemote(args, stdio)
	var usage *remoteUsageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprintln(stdio.Out, remoteUsage)
		return 0
	case errors.As(err, &usage):
		fmt.Fprintf(stdio.Err, "orch remote: %s\n%s\n", err, remoteUsage)
		return 2
	}
	fmt.Fprintf(stdio.Err, "orch remote: %s\n", err)
	return 1
}

func runRemote(args []string, stdio Stdio) error {
	if len(args) == 0 {
		return &remoteUsageError{"expected a command"}
	}
	command, rest := args[0], args[1:]
	if (command == "token" || command == "service-token") && len(rest) > 0 {
		command, rest = command+" "+rest[0], rest[1:]
	}
	env, err := resolveRemoteEnv()
	if err != nil {
		return err
	}
	switch command {
	case "init":
		return remoteInit(rest, env, stdio)
	case "status":
		return remoteStatus(rest, env, stdio)
	case "push":
		return remotePush(rest, env, stdio)
	case "once":
		return remoteOnce(rest, env, stdio)
	case "serve":
		return remoteServe(rest, env, stdio)
	case "launchd":
		return remoteLaunchd(rest, env, stdio)
	case "token rotate":
		return remoteTokenRotate(rest, stdio)
	case "token revoke":
		return remoteTokenRevoke(rest, stdio)
	case "service-token set":
		return remoteServiceTokenSet(rest, stdio)
	case "-h", "--help":
		return flag.ErrHelp
	}
	return &remoteUsageError{fmt.Sprintf("unknown command %q", command)}
}

func resolveRemoteEnv() (remoteEnv, error) {
	resolved, err := paths.Resolve(os.Getenv)
	if err != nil {
		return remoteEnv{}, err
	}
	configPath, err := config.Path(os.Getenv, resolved.Home)
	if err != nil {
		return remoteEnv{}, err
	}
	return remoteEnv{home: resolved.Home, configPath: configPath, stateFile: state.File(resolved.Home), paths: resolved}, nil
}

// stringList is a flag that may be repeated, collecting every value.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// parseRemoteFlags parses args into flags, allowing no positional arguments. A parse failure
// is a usage error; -h/--help is flag.ErrHelp.
func parseRemoteFlags(flags *flag.FlagSet, args []string) error {
	flags.SetOutput(io.Discard)
	err := flags.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return err
	case err != nil:
		return &remoteUsageError{err.Error()}
	case flags.NArg() > 0:
		return &remoteUsageError{fmt.Sprintf("unexpected argument %q", flags.Arg(0))}
	}
	return nil
}

func remoteInit(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	url := flags.String("url", "", "")
	force := flags.Bool("force", false, "")
	var roots, excludes stringList
	flags.Var(&roots, "root", "")
	flags.Var(&excludes, "exclude", "")
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	if *url == "" {
		return &remoteUsageError{"init needs --url URL"}
	}
	cfg := config.Default()
	cfg.URL = *url
	if len(roots) > 0 {
		cfg.Roots = roots
	}
	if len(excludes) > 0 {
		cfg.Exclude = excludes
	}
	if err := refuseOverwrite(env.configPath, *force); err != nil {
		return err
	}
	var invalid *config.InvalidError
	if err := config.Save(env.configPath, env.home, cfg); errors.As(err, &invalid) {
		return &remoteUsageError{invalid.Problem}
	} else if err != nil {
		return err
	}
	st, err := state.Ensure(env.stateFile)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "wrote %s\nmachine id: %s\nnext steps:\n", env.configPath, st.MachineID)
	fmt.Fprintln(stdio.Out, "  orch remote service-token set   # the Access service token's client id, then its secret")
	fmt.Fprintln(stdio.Out, "  orch remote token rotate        # then store the printed hash as the Worker's MACHINE_TOKEN_SHA256")
	fmt.Fprintln(stdio.Out, "  orch remote status")
	return nil
}

func refuseOverwrite(path string, force bool) error {
	if force {
		return nil
	}
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return fmt.Errorf("%s already exists; pass --force to replace it", path)
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	return err
}

// remoteStatusReport is what `status` shows. It never holds a secret: Keychain items are
// reported as present or not, never read.
type remoteStatusReport struct {
	ConfigPath    string                      `json:"config_path"`
	Config        *config.Config              `json:"config"`
	ConfigError   *string                     `json:"config_error"` // why the config was refused`
	MachineID     *string                     `json:"machine_id"`
	Keychain      map[keychain.Account]string `json:"keychain"` // "yes", "no" or "unknown"
	KeychainError *string                     `json:"keychain_error"`
	LastPushAt    *time.Time                  `json:"last_push_at"`
	LastPullAt    *time.Time                  `json:"last_pull_at"`
	LastError     *state.LastError            `json:"last_error"`
	// The discovered projects; null (with no names) without a usable config.
	Projects        *int     `json:"projects"`
	ProjectNames    []string `json:"project_names"`
	DiscoveryErrors []string `json:"discovery_errors"` // roots or directories that could not be examined
}

func remoteStatus(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	report, configErr, err := buildRemoteStatus(env)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(stdio.Out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		printRemoteStatus(stdio.Out, report)
	}
	return configErr
}

// buildRemoteStatus gathers the report. A refused config does not stop it: the refusal goes
// into the report and comes back as configErr, so status still shows everything else and then
// exits 1. Any other failure is err.
func buildRemoteStatus(env remoteEnv) (report remoteStatusReport, configErr, err error) {
	report = remoteStatusReport{ConfigPath: env.configPath}
	cfg, loadErr := config.Load(env.configPath, env.home)
	switch {
	case loadErr == nil:
		report.Config = &cfg
		report.reportDiscovery(discover.Projects(cfg))
	case !errors.Is(loadErr, fs.ErrNotExist):
		message := loadErr.Error()
		report.ConfigError, configErr = &message, loadErr
	}
	st, err := state.Load(env.stateFile)
	switch {
	case err == nil:
		report.MachineID = &st.MachineID
		report.LastPushAt, report.LastPullAt, report.LastError = st.LastPushAt, st.LastPullAt, st.LastError
	case !errors.Is(err, fs.ErrNotExist):
		return report, configErr, err
	}
	report.Keychain, report.KeychainError = keychainPresence()
	return report, configErr, nil
}

// keychainPresence reports whether each Work Trains item exists. A Keychain that cannot be
// asked (locked, or no security tool) is reported as "unknown" with the first error, so
// status still shows everything else.
func keychainPresence() (map[keychain.Account]string, *string) {
	presence := map[keychain.Account]string{}
	var firstError *string
	for _, account := range keychain.Accounts {
		exists, err := keychain.Exists(account)
		switch {
		case err != nil:
			presence[account] = "unknown"
			if firstError == nil {
				message := err.Error()
				firstError = &message
			}
		case exists:
			presence[account] = "yes"
		default:
			presence[account] = "no"
		}
	}
	return presence, firstError
}

func printRemoteStatus(out io.Writer, r remoteStatusReport) {
	switch {
	case r.ConfigError != nil:
		fmt.Fprintf(out, "config:      refused: %s\n", *r.ConfigError)
	case r.Config == nil:
		fmt.Fprintf(out, "config:      %s (missing; run orch remote init --url URL)\n", r.ConfigPath)
	default:
		fmt.Fprintf(out, "config:      %s\n", r.ConfigPath)
		fmt.Fprintf(out, "url:         %s\n", r.Config.URL)
		fmt.Fprintf(out, "roots:       %s\n", listOrNone(r.Config.Roots))
		fmt.Fprintf(out, "exclude:     %s\n", listOrNone(r.Config.Exclude))
		fmt.Fprintf(out, "worktrees:   %s\n", includedOrNot(r.Config.IncludeWorktrees))
		fmt.Fprintf(out, "ci:          %s\n", includedOrNot(r.Config.CI))
		fmt.Fprintf(out, "poll:        every %ds\n", r.Config.PollSeconds)
	}
	fmt.Fprintf(out, "machine id:  %s\n", stringOr(r.MachineID, "none (run orch remote init)"))
	items := make([]string, len(keychain.Accounts))
	for i, account := range keychain.Accounts {
		items[i] = fmt.Sprintf("%s %s", account, r.Keychain[account])
	}
	fmt.Fprintf(out, "keychain:    %s\n", strings.Join(items, ", "))
	if r.KeychainError != nil {
		fmt.Fprintf(out, "             %s\n", *r.KeychainError)
	}
	fmt.Fprintf(out, "last push:   %s\n", timeOrNever(r.LastPushAt))
	fmt.Fprintf(out, "last pull:   %s\n", timeOrNever(r.LastPullAt))
	if r.LastError == nil {
		fmt.Fprintln(out, "last error:  none")
	} else {
		fmt.Fprintf(out, "last error:  %s %s: %s\n", r.LastError.At.UTC().Format(time.RFC3339), r.LastError.Kind, r.LastError.Message)
	}
	switch {
	case r.Projects == nil:
		fmt.Fprintln(out, "projects:    unknown (no usable config)")
	case *r.Projects == 0:
		fmt.Fprintln(out, "projects:    none found under the roots")
	default:
		fmt.Fprintf(out, "projects:    %d (%s)\n", *r.Projects, strings.Join(r.ProjectNames, ", "))
	}
	for _, problem := range r.DiscoveryErrors {
		fmt.Fprintf(out, "             skipped %s\n", problem)
	}
}

func (r *remoteStatusReport) reportDiscovery(projects []discover.Project, errs []*discover.Error) {
	count := len(projects)
	r.Projects, r.ProjectNames, r.DiscoveryErrors = &count, []string{}, []string{}
	for _, project := range projects {
		r.ProjectNames = append(r.ProjectNames, project.Name)
	}
	for _, err := range errs {
		r.DiscoveryErrors = append(r.DiscoveryErrors, err.Error())
	}
}

// remotePush implements `push --dry-run`: it discovers the projects and prints the PushRequest
// a push would send, so the owner can audit exactly what leaves the Mac. It sends nothing and
// writes nothing. A project or budget that cannot be read is reported on stderr and left out.
func remotePush(args []string, env remoteEnv, stdio Stdio) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	dryRun := flags.Bool("dry-run", false, "")
	asJSON := flags.Bool("json", false, "")
	if err := parseRemoteFlags(flags, args); err != nil {
		return err
	}
	if !*dryRun {
		return &remoteUsageError{"push needs --dry-run; orch remote once sends"}
	}
	cfg, st, err := loadRemoteSetup(env)
	if err != nil {
		return err
	}
	now := time.Now()
	projects, scanErrs := discover.Projects(cfg)
	for _, scanErr := range scanErrs {
		fmt.Fprintf(stdio.Err, "orch remote: skipped %s\n", scanErr)
	}
	snapshotEnv := snapshot.Env{MachineID: st.MachineID, Home: env.home, Now: now, Alive: worker.Alive, CI: snapshot.NoCI{}}
	var ciCache *ci.Cache
	if cfg.CI {
		// The cached runs, else gh's; the refreshed entries are not saved (a dry run writes nothing).
		ciCache = ci.NewCache(context.Background(), st.MachineID, env.home, now, st.CI)
		snapshotEnv.CI = ciCache
	}
	snapshots := buildSnapshots(projects, snapshotEnv, stdio.Err)
	if ciCache != nil {
		for _, note := range ciCache.Notes() {
			fmt.Fprintf(stdio.Err, "orch remote: ci left out for %s\n", note)
		}
	}
	budget, err := snapshot.Budget(usage.NewConfig(env.paths))
	if err != nil {
		fmt.Fprintf(stdio.Err, "orch remote: budget left out: %s\n", err)
	}
	// The release number only: the build's VCS revision (and +dirty) is the owner's business,
	// and it would change the machine row on every local edit.
	request := snapshot.NewPushRequest(st.MachineID, version.Number(), now, budget, snapshots)
	if !*asJSON {
		fmt.Fprintf(stdio.Out, "dry run: nothing was sent to %s\n", cfg.URL)
		fmt.Fprintf(stdio.Out, "would push %d project(s); this is the whole request body:\n", len(snapshots))
	}
	encoder := json.NewEncoder(stdio.Out)
	encoder.SetEscapeHTML(false) // keep redaction placeholders such as <email> readable
	encoder.SetIndent("", "  ")
	return encoder.Encode(request)
}

// loadRemoteSetup reads the config and the state, naming `orch remote init` when either is missing.
func loadRemoteSetup(env remoteEnv) (config.Config, state.State, error) {
	cfg, err := config.Load(env.configPath, env.home)
	if errors.Is(err, fs.ErrNotExist) {
		return config.Config{}, state.State{}, fmt.Errorf("no config at %s; run orch remote init --url URL", env.configPath)
	} else if err != nil {
		return config.Config{}, state.State{}, err
	}
	st, err := state.Load(env.stateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return config.Config{}, state.State{}, errors.New("no machine id yet; run orch remote init --url URL")
	} else if err != nil {
		return config.Config{}, state.State{}, err
	}
	return cfg, st, nil
}

// buildSnapshots builds each project's snapshot; one that cannot be read is reported on errOut
// and left out, so the others still go.
func buildSnapshots(projects []discover.Project, env snapshot.Env, errOut io.Writer) []wire.ProjectSnapshot {
	snapshots := []wire.ProjectSnapshot{}
	for _, project := range projects {
		built, err := snapshot.Build(project, env)
		if err != nil {
			fmt.Fprintf(errOut, "orch remote: skipped %s: %s\n", project.Name, err)
			continue
		}
		snapshots = append(snapshots, built)
	}
	return snapshots
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func includedOrNot(on bool) string {
	if on {
		return "included"
	}
	return "excluded"
}

func stringOr(text *string, fallback string) string {
	if text == nil {
		return fallback
	}
	return *text
}

func timeOrNever(at *time.Time) string {
	if at == nil {
		return "never"
	}
	return at.UTC().Format(time.RFC3339)
}

// remoteTokenRotate stores a new machine token and prints only its SHA-256, which the Worker
// compares bearer tokens against. 32 random bytes need no slow KDF.
func remoteTokenRotate(args []string, stdio Stdio) error {
	if len(args) > 0 {
		return &remoteUsageError{fmt.Sprintf("unexpected argument %q", args[0])}
	}
	var raw [32]byte
	rand.Read(raw[:]) // crypto/rand.Read never returns an error and never short-reads
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := keychain.Store(keychain.MachineToken, token); err != nil {
		return err
	}
	fmt.Fprintf(stdio.Out, "%x\n", sha256.Sum256([]byte(token)))
	fmt.Fprintf(stdio.Out, "%s   # paste the hash above\n", workerSecretCommand)
	fmt.Fprintln(stdio.Out, "This Mac sends the new token from now on; the Worker accepts it only once MACHINE_TOKEN_SHA256 holds the hash above, so replace it before the next push or pushes get 403.")
	return nil
}

func remoteTokenRevoke(args []string, stdio Stdio) error {
	if len(args) > 0 {
		return &remoteUsageError{fmt.Sprintf("unexpected argument %q", args[0])}
	}
	for _, account := range keychain.Accounts {
		err := keychain.Delete(account)
		switch {
		case err == nil:
			fmt.Fprintf(stdio.Out, "%s: deleted\n", account)
		case errors.Is(err, keychain.ErrNotFound):
			fmt.Fprintf(stdio.Out, "%s: not present\n", account)
		default:
			return err
		}
	}
	fmt.Fprintf(stdio.Out, "Worker side: revoke the Access service token in Cloudflare Zero Trust and replace MACHINE_TOKEN_SHA256 (%s), so these credentials stop working there too.\n", workerSecretCommand)
	return nil
}

// remoteServiceTokenSet reads the Access service token from stdin, client id then client
// secret, one per line. On a terminal it prompts on stderr with echo off.
func remoteServiceTokenSet(args []string, stdio Stdio) error {
	if len(args) > 0 {
		return &remoteUsageError{fmt.Sprintf("unexpected argument %q", args[0])}
	}
	reader := bufio.NewReader(stdio.In)
	clientID, err := readSecretLine(stdio, reader, "Access client id")
	if err != nil {
		return err
	}
	clientSecret, err := readSecretLine(stdio, reader, "Access client secret")
	if err != nil {
		return err
	}
	if err := errors.Join(
		keychain.Validate(keychain.AccessClientID, clientID),
		keychain.Validate(keychain.AccessClientSecret, clientSecret),
	); err != nil {
		return err
	}
	if err := keychain.Store(keychain.AccessClientID, clientID); err != nil {
		return err
	}
	if err := keychain.Store(keychain.AccessClientSecret, clientSecret); err != nil {
		return fmt.Errorf("stored the new %s, but not the %s, so the two no longer match; run orch remote service-token set again: %w",
			keychain.AccessClientID, keychain.AccessClientSecret, err)
	}
	fmt.Fprintf(stdio.Out, "stored %s and %s in the login Keychain\n", keychain.AccessClientID, keychain.AccessClientSecret)
	return nil
}

func readSecretLine(stdio Stdio, reader *bufio.Reader, what string) (string, error) {
	if !cliargs.IsTerminal(stdio.In) {
		return readLine(reader, what)
	}
	fmt.Fprintf(stdio.Err, "%s: ", what)
	var line string
	err := cliargs.WithoutEcho(stdio.In, func() (readErr error) {
		line, readErr = readLine(reader, what)
		return readErr
	})
	fmt.Fprintln(stdio.Err)
	return line, err
}

// readLine reads one non-empty line; a last line without a newline counts.
func readLine(reader *bufio.Reader, what string) (string, error) {
	line, err := reader.ReadString('\n')
	switch {
	case errors.Is(err, io.EOF) && line == "":
		return "", fmt.Errorf("stdin ended before the %s", what)
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", fmt.Errorf("the %s is empty", what)
	}
	return line, nil
}
